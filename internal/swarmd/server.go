// Package swarmd provides the daemon scaffolding for the Swarm node service.
package swarmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/opencode-ai/swarm/gen/swarmd/v1"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// transcriptEntry represents a single transcript entry for an agent.
type transcriptEntry struct {
	id        int64 // monotonic ID for cursor-based streaming
	timestamp time.Time
	entryType swarmdv1.TranscriptEntryType
	content   string
	metadata  map[string]string
}

// agentInfo tracks a running agent's state.
type agentInfo struct {
	id          string
	workspaceID string
	paneID      string
	command     string
	adapter     string
	pid         int
	state       swarmdv1.AgentState
	spawnedAt   time.Time
	lastActive  time.Time
	contentHash string

	// Transcript storage
	transcript     []transcriptEntry
	transcriptNext int64 // next ID for new entries
}

// Server implements the SwarmdService gRPC interface.
type Server struct {
	swarmdv1.UnimplementedSwarmdServiceServer

	logger    zerolog.Logger
	tmux      *tmux.Client
	startedAt time.Time
	hostname  string
	version   string

	mu     sync.RWMutex
	agents map[string]*agentInfo // keyed by agent ID
}

// ServerOption configures the Server.
type ServerOption func(*Server)

// WithVersion sets the daemon version.
func WithVersion(version string) ServerOption {
	return func(s *Server) {
		s.version = version
	}
}

// NewServer creates a new gRPC server for the swarmd service.
func NewServer(logger zerolog.Logger, opts ...ServerOption) *Server {
	hostname, _ := os.Hostname()

	s := &Server{
		logger:    logger,
		tmux:      tmux.NewLocalClient(),
		startedAt: time.Now(),
		hostname:  hostname,
		version:   "dev",
		agents:    make(map[string]*agentInfo),
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// =============================================================================
// Agent Control
// =============================================================================

// SpawnAgent creates a new agent in a tmux pane.
func (s *Server) SpawnAgent(ctx context.Context, req *swarmdv1.SpawnAgentRequest) (*swarmdv1.SpawnAgentResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.Command == "" {
		return nil, status.Error(codes.InvalidArgument, "command is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if agent already exists
	if _, exists := s.agents[req.AgentId]; exists {
		return nil, status.Errorf(codes.AlreadyExists, "agent %q already exists", req.AgentId)
	}

	// Determine session and window names
	sessionName := req.SessionName
	if sessionName == "" {
		sessionName = fmt.Sprintf("swarm-%s", req.WorkspaceId)
	}

	workDir := req.WorkingDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	// Ensure session exists
	hasSession, err := s.tmux.HasSession(ctx, sessionName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check session: %v", err)
	}
	if !hasSession {
		if err := s.tmux.NewSession(ctx, sessionName, workDir); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
		}
	}

	// Create a new pane by splitting the window
	paneID, err := s.tmux.SplitWindow(ctx, sessionName, true, workDir)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create pane: %v", err)
	}

	// Build the command with args
	cmdLine := req.Command
	for _, arg := range req.Args {
		cmdLine += " " + arg
	}

	// Set environment variables and run the command
	for k, v := range req.Env {
		envCmd := fmt.Sprintf("export %s=%q", k, v)
		if err := s.tmux.SendKeys(ctx, paneID, envCmd, true, true); err != nil {
			s.logger.Warn().Err(err).Str("pane", paneID).Msg("failed to set env var")
		}
	}

	// Send the command to the pane
	if err := s.tmux.SendKeys(ctx, paneID, cmdLine, true, true); err != nil {
		// Try to clean up the pane
		_ = s.tmux.KillPane(ctx, paneID)
		return nil, status.Errorf(codes.Internal, "failed to send command: %v", err)
	}

	now := time.Now()
	info := &agentInfo{
		id:          req.AgentId,
		workspaceID: req.WorkspaceId,
		paneID:      paneID,
		command:     req.Command,
		adapter:     req.Adapter,
		pid:         0, // We don't know the PID yet
		state:       swarmdv1.AgentState_AGENT_STATE_STARTING,
		spawnedAt:   now,
		lastActive:  now,
		transcript:  make([]transcriptEntry, 0, 100), // Pre-allocate for efficiency
	}
	s.agents[req.AgentId] = info

	// Record spawn event in transcript
	s.addTranscriptEntryLocked(info, swarmdv1.TranscriptEntryType_TRANSCRIPT_ENTRY_TYPE_COMMAND, cmdLine, map[string]string{
		"event":     "spawn",
		"adapter":   req.Adapter,
		"workspace": req.WorkspaceId,
	})

	s.logger.Info().
		Str("agent_id", req.AgentId).
		Str("pane_id", paneID).
		Str("command", cmdLine).
		Msg("agent spawned")

	return &swarmdv1.SpawnAgentResponse{
		Agent:  s.agentToProto(info),
		PaneId: paneID,
	}, nil
}

// KillAgent terminates an agent's process.
func (s *Server) KillAgent(ctx context.Context, req *swarmdv1.KillAgentRequest) (*swarmdv1.KillAgentResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	info, exists := s.agents[req.AgentId]
	if !exists {
		return nil, status.Errorf(codes.NotFound, "agent %q not found", req.AgentId)
	}

	// Send interrupt first (Ctrl+C) unless force is set
	if !req.Force {
		if err := s.tmux.SendInterrupt(ctx, info.paneID); err != nil {
			s.logger.Warn().Err(err).Str("agent_id", req.AgentId).Msg("failed to send interrupt")
		}

		// Wait for grace period if specified
		if req.GracePeriod != nil && req.GracePeriod.AsDuration() > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(req.GracePeriod.AsDuration()):
			}
		}
	}

	// Record state change in transcript before killing
	s.addTranscriptEntryLocked(info, swarmdv1.TranscriptEntryType_TRANSCRIPT_ENTRY_TYPE_STATE_CHANGE, "stopped", map[string]string{
		"event":    "kill",
		"force":    fmt.Sprintf("%v", req.Force),
		"previous": info.state.String(),
	})

	// Kill the pane
	if err := s.tmux.KillPane(ctx, info.paneID); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", req.AgentId).Msg("failed to kill pane")
	}

	info.state = swarmdv1.AgentState_AGENT_STATE_STOPPED
	delete(s.agents, req.AgentId)

	s.logger.Info().
		Str("agent_id", req.AgentId).
		Bool("force", req.Force).
		Msg("agent killed")

	return &swarmdv1.KillAgentResponse{Success: true}, nil
}

// SendInput sends keystrokes or text to an agent's pane.
func (s *Server) SendInput(ctx context.Context, req *swarmdv1.SendInputRequest) (*swarmdv1.SendInputResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	s.mu.RLock()
	info, exists := s.agents[req.AgentId]
	s.mu.RUnlock()

	if !exists {
		return nil, status.Errorf(codes.NotFound, "agent %q not found", req.AgentId)
	}

	// Send special keys first
	for _, key := range req.Keys {
		keyCmd := fmt.Sprintf("tmux send-keys -t %s %s", info.paneID, key)
		cmd := exec.CommandContext(ctx, "sh", "-c", keyCmd)
		if err := cmd.Run(); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to send key %q: %v", key, err)
		}
	}

	// Send text if provided
	if req.Text != "" {
		if err := s.tmux.SendKeys(ctx, info.paneID, req.Text, true, req.SendEnter); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to send text: %v", err)
		}
	}

	// Update last active time and record transcript entry
	s.mu.Lock()
	if agent, ok := s.agents[req.AgentId]; ok {
		agent.lastActive = time.Now()

		// Record user input in transcript
		inputContent := req.Text
		if len(req.Keys) > 0 {
			inputContent = fmt.Sprintf("[keys: %v] %s", req.Keys, req.Text)
		}
		if inputContent != "" {
			s.addTranscriptEntryLocked(agent, swarmdv1.TranscriptEntryType_TRANSCRIPT_ENTRY_TYPE_USER_INPUT, inputContent, nil)
		}
	}
	s.mu.Unlock()

	return &swarmdv1.SendInputResponse{Success: true}, nil
}

// ListAgents returns all agents managed by this daemon.
func (s *Server) ListAgents(ctx context.Context, req *swarmdv1.ListAgentsRequest) (*swarmdv1.ListAgentsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var agents []*swarmdv1.Agent
	for _, info := range s.agents {
		// Apply workspace filter
		if req.WorkspaceId != "" && info.workspaceID != req.WorkspaceId {
			continue
		}

		// Apply state filter
		if len(req.States) > 0 {
			matched := false
			for _, state := range req.States {
				if info.state == state {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		agents = append(agents, s.agentToProto(info))
	}

	return &swarmdv1.ListAgentsResponse{Agents: agents}, nil
}

// GetAgent returns details for a specific agent.
func (s *Server) GetAgent(ctx context.Context, req *swarmdv1.GetAgentRequest) (*swarmdv1.GetAgentResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	s.mu.RLock()
	info, exists := s.agents[req.AgentId]
	s.mu.RUnlock()

	if !exists {
		return nil, status.Errorf(codes.NotFound, "agent %q not found", req.AgentId)
	}

	return &swarmdv1.GetAgentResponse{Agent: s.agentToProto(info)}, nil
}

// =============================================================================
// Screen Capture
// =============================================================================

// Default polling interval for StreamPaneUpdates.
const defaultPollInterval = 500 * time.Millisecond

// StreamPaneUpdates streams pane content changes in real-time.
func (s *Server) StreamPaneUpdates(req *swarmdv1.StreamPaneUpdatesRequest, stream swarmdv1.SwarmdService_StreamPaneUpdatesServer) error {
	if req.AgentId == "" {
		return status.Error(codes.InvalidArgument, "agent_id is required")
	}

	s.mu.RLock()
	info, exists := s.agents[req.AgentId]
	s.mu.RUnlock()

	if !exists {
		return status.Errorf(codes.NotFound, "agent %q not found", req.AgentId)
	}

	// Determine polling interval
	pollInterval := defaultPollInterval
	if req.MinInterval != nil && req.MinInterval.AsDuration() > 0 {
		pollInterval = req.MinInterval.AsDuration()
	}

	// Track last known hash for change detection
	lastHash := req.LastKnownHash

	ctx := stream.Context()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	s.logger.Debug().
		Str("agent_id", req.AgentId).
		Dur("poll_interval", pollInterval).
		Msg("starting pane update stream")

	for {
		select {
		case <-ctx.Done():
			s.logger.Debug().
				Str("agent_id", req.AgentId).
				Msg("pane update stream ended (context done)")
			return ctx.Err()
		case <-ticker.C:
			// Check if agent still exists
			s.mu.RLock()
			info, exists = s.agents[req.AgentId]
			s.mu.RUnlock()

			if !exists {
				return status.Errorf(codes.NotFound, "agent %q no longer exists", req.AgentId)
			}

			// Capture pane content
			content, err := s.tmux.CapturePane(ctx, info.paneID, false)
			if err != nil {
				s.logger.Warn().Err(err).Str("agent_id", req.AgentId).Msg("failed to capture pane")
				continue
			}

			currentHash := tmux.HashSnapshot(content)
			changed := currentHash != lastHash

			// Only send if changed, or if this is the first response
			if changed || lastHash == "" {
				resp := &swarmdv1.StreamPaneUpdatesResponse{
					AgentId:     req.AgentId,
					ContentHash: currentHash,
					Changed:     changed,
					Timestamp:   timestamppb.Now(),
				}

				// Include content if requested
				if req.IncludeContent {
					resp.Content = content
				}

				// Detect state from content
				resp.DetectedState = s.detectAgentState(content, info.adapter)

				// Update agent's content hash, last active time, and record output
				s.mu.Lock()
				if agent, ok := s.agents[req.AgentId]; ok {
					agent.contentHash = currentHash
					agent.lastActive = time.Now()

					// Record content change in transcript (truncate if very long)
					outputContent := content
					if len(outputContent) > 4096 {
						outputContent = outputContent[len(outputContent)-4096:]
					}
					s.addTranscriptEntryLocked(agent, swarmdv1.TranscriptEntryType_TRANSCRIPT_ENTRY_TYPE_OUTPUT, outputContent, map[string]string{
						"content_hash": currentHash,
					})

					if resp.DetectedState != swarmdv1.AgentState_AGENT_STATE_UNSPECIFIED {
						// Record state change if different
						if agent.state != resp.DetectedState {
							s.addTranscriptEntryLocked(agent, swarmdv1.TranscriptEntryType_TRANSCRIPT_ENTRY_TYPE_STATE_CHANGE, resp.DetectedState.String(), map[string]string{
								"previous": agent.state.String(),
							})
						}
						agent.state = resp.DetectedState
					}
				}
				s.mu.Unlock()

				if err := stream.Send(resp); err != nil {
					s.logger.Debug().Err(err).Str("agent_id", req.AgentId).Msg("failed to send pane update")
					return err
				}

				lastHash = currentHash
			}
		}
	}
}

// detectAgentState analyzes pane content to determine agent state.
// This is a simplified version - full adapters have more sophisticated detection.
func (s *Server) detectAgentState(content, adapter string) swarmdv1.AgentState {
	// Look for common patterns indicating different states
	// These patterns are simplified - real adapters have more detailed detection

	// Check for approval/confirmation prompts
	if containsAny(content,
		"Do you want to",
		"Proceed?",
		"[y/n]",
		"[Y/n]",
		"approve",
		"confirm",
		"Allow?") {
		return swarmdv1.AgentState_AGENT_STATE_WAITING_APPROVAL
	}

	// Check for idle prompts (command line ready)
	if containsAny(content,
		"$",
		"❯",
		"→",
		">",
		"claude>",
		"opencode>") {
		// If we see a prompt at the end, it's likely idle
		lines := splitLines(content)
		if len(lines) > 0 {
			lastLine := lines[len(lines)-1]
			if containsAny(lastLine, "$", "❯", "→", ">") {
				return swarmdv1.AgentState_AGENT_STATE_IDLE
			}
		}
	}

	// Check for running indicators
	if containsAny(content,
		"Thinking...",
		"Working...",
		"Processing...",
		"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏") {
		return swarmdv1.AgentState_AGENT_STATE_RUNNING
	}

	// Check for error indicators
	if containsAny(content,
		"error:",
		"Error:",
		"ERROR",
		"fatal:",
		"Fatal:",
		"panic:",
		"Panic:") {
		return swarmdv1.AgentState_AGENT_STATE_FAILED
	}

	// Default to running if we can't determine
	return swarmdv1.AgentState_AGENT_STATE_RUNNING
}

// containsAny checks if s contains any of the substrings.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// splitLines splits content into lines.
func splitLines(content string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

// CapturePane returns the current content of an agent's pane.
func (s *Server) CapturePane(ctx context.Context, req *swarmdv1.CapturePaneRequest) (*swarmdv1.CapturePaneResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	s.mu.RLock()
	info, exists := s.agents[req.AgentId]
	s.mu.RUnlock()

	if !exists {
		return nil, status.Errorf(codes.NotFound, "agent %q not found", req.AgentId)
	}

	// Capture with or without history based on lines parameter
	includeHistory := req.Lines < 0
	content, err := s.tmux.CapturePane(ctx, info.paneID, includeHistory)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to capture pane: %v", err)
	}

	hash := tmux.HashSnapshot(content)

	// Update content hash
	s.mu.Lock()
	if agent, ok := s.agents[req.AgentId]; ok {
		agent.contentHash = hash
		agent.lastActive = time.Now()
	}
	s.mu.Unlock()

	return &swarmdv1.CapturePaneResponse{
		Content:     content,
		ContentHash: hash,
		CapturedAt:  timestamppb.Now(),
	}, nil
}

// =============================================================================
// Health & Status
// =============================================================================

// GetStatus returns daemon health and resource usage.
func (s *Server) GetStatus(ctx context.Context, req *swarmdv1.GetStatusRequest) (*swarmdv1.GetStatusResponse, error) {
	s.mu.RLock()
	agentCount := len(s.agents)
	s.mu.RUnlock()

	uptime := time.Since(s.startedAt)

	return &swarmdv1.GetStatusResponse{
		Status: &swarmdv1.DaemonStatus{
			Version:    s.version,
			Hostname:   s.hostname,
			StartedAt:  timestamppb.New(s.startedAt),
			Uptime:     durationpb.New(uptime),
			AgentCount: int32(agentCount),
			Resources:  s.getResourceUsage(),
			Health:     s.getHealthStatus(),
		},
	}, nil
}

// Ping is a simple health check.
func (s *Server) Ping(ctx context.Context, req *swarmdv1.PingRequest) (*swarmdv1.PingResponse, error) {
	return &swarmdv1.PingResponse{
		Timestamp: timestamppb.Now(),
		Version:   s.version,
	}, nil
}

// =============================================================================
// Helpers
// =============================================================================

func (s *Server) agentToProto(info *agentInfo) *swarmdv1.Agent {
	return &swarmdv1.Agent{
		Id:             info.id,
		WorkspaceId:    info.workspaceID,
		State:          info.state,
		PaneId:         info.paneID,
		Pid:            int32(info.pid),
		Command:        info.command,
		Adapter:        info.adapter,
		SpawnedAt:      timestamppb.New(info.spawnedAt),
		LastActivityAt: timestamppb.New(info.lastActive),
		ContentHash:    info.contentHash,
	}
}

func (s *Server) getResourceUsage() *swarmdv1.ResourceUsage {
	var rusage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &rusage); err != nil {
		return &swarmdv1.ResourceUsage{}
	}

	return &swarmdv1.ResourceUsage{
		MemoryBytes: rusage.Maxrss * 1024, // maxrss is in KB on Linux
	}
}

func (s *Server) getHealthStatus() *swarmdv1.HealthStatus {
	checks := []*swarmdv1.HealthCheck{
		{
			Name:      "tmux",
			Health:    swarmdv1.Health_HEALTH_HEALTHY,
			Message:   "tmux available",
			LastCheck: timestamppb.Now(),
		},
	}

	// Check if tmux is available
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.tmux.ListSessions(ctx)
	if err != nil {
		checks[0].Health = swarmdv1.Health_HEALTH_UNHEALTHY
		checks[0].Message = fmt.Sprintf("tmux error: %v", err)
	}

	// Determine overall health
	overallHealth := swarmdv1.Health_HEALTH_HEALTHY
	for _, check := range checks {
		if check.Health == swarmdv1.Health_HEALTH_UNHEALTHY {
			overallHealth = swarmdv1.Health_HEALTH_UNHEALTHY
			break
		}
		if check.Health == swarmdv1.Health_HEALTH_DEGRADED && overallHealth == swarmdv1.Health_HEALTH_HEALTHY {
			overallHealth = swarmdv1.Health_HEALTH_DEGRADED
		}
	}

	return &swarmdv1.HealthStatus{
		Health: overallHealth,
		Checks: checks,
	}
}

// =============================================================================
// Transcript Collection
// =============================================================================

// addTranscriptEntryLocked adds a transcript entry to an agent's transcript.
// The caller must hold the write lock.
func (s *Server) addTranscriptEntryLocked(info *agentInfo, entryType swarmdv1.TranscriptEntryType, content string, metadata map[string]string) {
	entry := transcriptEntry{
		id:        info.transcriptNext,
		timestamp: time.Now(),
		entryType: entryType,
		content:   content,
		metadata:  metadata,
	}
	info.transcript = append(info.transcript, entry)
	info.transcriptNext++
}

// addTranscriptEntry adds a transcript entry (acquires lock).
func (s *Server) addTranscriptEntry(agentID string, entryType swarmdv1.TranscriptEntryType, content string, metadata map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, exists := s.agents[agentID]
	if !exists {
		return
	}
	s.addTranscriptEntryLocked(info, entryType, content, metadata)
}

// GetTranscript retrieves the full transcript for an agent.
func (s *Server) GetTranscript(ctx context.Context, req *swarmdv1.GetTranscriptRequest) (*swarmdv1.GetTranscriptResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	s.mu.RLock()
	info, exists := s.agents[req.AgentId]
	if !exists {
		s.mu.RUnlock()
		return nil, status.Errorf(codes.NotFound, "agent %q not found", req.AgentId)
	}

	// Copy transcript entries while holding lock
	entries := make([]transcriptEntry, len(info.transcript))
	copy(entries, info.transcript)
	s.mu.RUnlock()

	// Apply time filters
	var filtered []transcriptEntry
	for _, e := range entries {
		if req.StartTime != nil && e.timestamp.Before(req.StartTime.AsTime()) {
			continue
		}
		if req.EndTime != nil && e.timestamp.After(req.EndTime.AsTime()) {
			continue
		}
		filtered = append(filtered, e)
	}

	// Apply limit
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 1000 // Default limit
	}
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}

	// Convert to proto
	protoEntries := make([]*swarmdv1.TranscriptEntry, len(filtered))
	for i, e := range filtered {
		protoEntries[i] = s.transcriptEntryToProto(&e)
	}

	var nextCursor string
	if hasMore && len(filtered) > 0 {
		nextCursor = fmt.Sprintf("%d", filtered[len(filtered)-1].id+1)
	}

	return &swarmdv1.GetTranscriptResponse{
		AgentId:    req.AgentId,
		Entries:    protoEntries,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, nil
}

// StreamTranscript streams transcript updates in real-time.
func (s *Server) StreamTranscript(req *swarmdv1.StreamTranscriptRequest, stream swarmdv1.SwarmdService_StreamTranscriptServer) error {
	if req.AgentId == "" {
		return status.Error(codes.InvalidArgument, "agent_id is required")
	}

	// Parse cursor if provided
	var cursor int64
	if req.Cursor != "" {
		var err error
		cursor, err = parseInt64(req.Cursor)
		if err != nil {
			return status.Errorf(codes.InvalidArgument, "invalid cursor: %v", err)
		}
	}

	ctx := stream.Context()
	ticker := time.NewTicker(100 * time.Millisecond) // Poll for new entries
	defer ticker.Stop()

	s.logger.Debug().
		Str("agent_id", req.AgentId).
		Int64("cursor", cursor).
		Msg("starting transcript stream")

	for {
		select {
		case <-ctx.Done():
			s.logger.Debug().
				Str("agent_id", req.AgentId).
				Msg("transcript stream ended (context done)")
			return ctx.Err()
		case <-ticker.C:
			s.mu.RLock()
			info, exists := s.agents[req.AgentId]
			if !exists {
				s.mu.RUnlock()
				return status.Errorf(codes.NotFound, "agent %q no longer exists", req.AgentId)
			}

			// Find new entries since cursor
			var newEntries []transcriptEntry
			for _, e := range info.transcript {
				if e.id >= cursor {
					newEntries = append(newEntries, e)
				}
			}
			s.mu.RUnlock()

			if len(newEntries) > 0 {
				// Convert to proto
				protoEntries := make([]*swarmdv1.TranscriptEntry, len(newEntries))
				for i, e := range newEntries {
					protoEntries[i] = s.transcriptEntryToProto(&e)
				}

				// Update cursor for next iteration
				cursor = newEntries[len(newEntries)-1].id + 1

				resp := &swarmdv1.StreamTranscriptResponse{
					Entries: protoEntries,
					Cursor:  fmt.Sprintf("%d", cursor),
				}

				if err := stream.Send(resp); err != nil {
					s.logger.Debug().Err(err).Str("agent_id", req.AgentId).Msg("failed to send transcript update")
					return err
				}
			}
		}
	}
}

// transcriptEntryToProto converts a transcriptEntry to proto format.
func (s *Server) transcriptEntryToProto(e *transcriptEntry) *swarmdv1.TranscriptEntry {
	return &swarmdv1.TranscriptEntry{
		Timestamp: timestamppb.New(e.timestamp),
		Type:      e.entryType,
		Content:   e.content,
		Metadata:  e.metadata,
	}
}

// parseInt64 parses a string to int64.
func parseInt64(s string) (int64, error) {
	var result int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid character: %c", c)
		}
		result = result*10 + int64(c-'0')
	}
	return result, nil
}

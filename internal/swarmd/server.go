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
	}
	s.agents[req.AgentId] = info

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

	// Update last active time
	s.mu.Lock()
	if agent, ok := s.agents[req.AgentId]; ok {
		agent.lastActive = time.Now()
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

// Package agent provides agent lifecycle management for Swarm.
package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/adapters"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/rs/zerolog"
)

// Service errors.
var (
	ErrServiceAgentNotFound = errors.New("agent not found")
	ErrAgentAlreadyExists   = errors.New("agent already exists")
	ErrWorkspaceNotFound    = errors.New("workspace not found")
	ErrSpawnFailed          = errors.New("failed to spawn agent")
	ErrInterruptFailed      = errors.New("failed to interrupt agent")
	ErrTerminateFailed      = errors.New("failed to terminate agent")
	ErrSendFailed           = errors.New("failed to send message to agent")
	ErrAgentNotIdle         = errors.New("agent is not idle")
)

// Service manages agent lifecycle operations.
type Service struct {
	repo             *db.AgentRepository
	queueRepo        *db.QueueRepository
	workspaceService *workspace.Service
	tmuxClient       *tmux.Client
	paneMap          *PaneMap
	logger           zerolog.Logger
}

// NewService creates a new AgentService.
func NewService(
	repo *db.AgentRepository,
	queueRepo *db.QueueRepository,
	workspaceService *workspace.Service,
	tmuxClient *tmux.Client,
) *Service {
	return &Service{
		repo:             repo,
		queueRepo:        queueRepo,
		workspaceService: workspaceService,
		tmuxClient:       tmuxClient,
		paneMap:          NewPaneMap(),
		logger:           logging.Component("agent"),
	}
}

// SpawnOptions contains options for spawning a new agent.
type SpawnOptions struct {
	// WorkspaceID is the workspace where the agent will run.
	WorkspaceID string

	// Type is the agent type (opencode, claude-code, etc.).
	Type models.AgentType

	// AccountID is an optional account profile to use.
	AccountID string

	// InitialPrompt is an optional prompt to send after spawning.
	InitialPrompt string

	// Environment contains optional environment variable overrides.
	Environment map[string]string

	// WorkingDir is an optional working directory override.
	// If empty, uses the workspace's repo path.
	WorkingDir string

	// ReadyTimeout is how long to wait for the agent to reach a ready state.
	// If zero, defaults to 30 seconds.
	ReadyTimeout time.Duration

	// ReadyPollInterval controls how often to poll for readiness.
	// If zero, defaults to 250 milliseconds.
	ReadyPollInterval time.Duration
}

// SpawnAgent creates a new agent in a workspace.
func (s *Service) SpawnAgent(ctx context.Context, opts SpawnOptions) (*models.Agent, error) {
	s.logger.Debug().
		Str("workspace_id", opts.WorkspaceID).
		Str("type", string(opts.Type)).
		Msg("spawning agent")

	// Validate workspace exists
	ws, err := s.workspaceService.GetWorkspace(ctx, opts.WorkspaceID)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			return nil, ErrWorkspaceNotFound
		}
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}

	// Determine working directory
	workDir := opts.WorkingDir
	if workDir == "" {
		workDir = ws.RepoPath
	}

	// Create a new pane in the workspace's tmux session
	paneID, err := s.tmuxClient.SplitWindow(ctx, ws.TmuxSession, false, workDir)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create pane: %v", ErrSpawnFailed, err)
	}

	// Build pane target (session:window.pane format)
	paneTarget := fmt.Sprintf("%s:%s", ws.TmuxSession, paneID)

	// Create agent record
	agent := &models.Agent{
		WorkspaceID: opts.WorkspaceID,
		Type:        opts.Type,
		TmuxPane:    paneTarget,
		AccountID:   opts.AccountID,
		State:       models.AgentStateStarting,
		StateInfo: models.StateInfo{
			State:      models.AgentStateStarting,
			Confidence: models.StateConfidenceHigh,
			Reason:     "Agent spawned, awaiting startup",
			DetectedAt: time.Now().UTC(),
		},
		Metadata: models.AgentMetadata{
			Environment: opts.Environment,
		},
	}

	// Persist agent to database
	if err := s.repo.Create(ctx, agent); err != nil {
		// Clean up pane on failure
		_ = s.tmuxClient.KillPane(ctx, paneTarget)
		if errors.Is(err, db.ErrAgentAlreadyExists) {
			return nil, ErrAgentAlreadyExists
		}
		return nil, fmt.Errorf("failed to create agent: %w", err)
	}

	// Register pane mapping
	if err := s.paneMap.Register(agent.ID, paneID, paneTarget); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to register pane mapping")
	}

	// Start the agent CLI in the pane
	startCmd := s.buildStartCommand(opts)
	if startCmd != "" {
		if err := s.tmuxClient.SendKeys(ctx, paneTarget, startCmd, true, true); err != nil {
			spawnErr := fmt.Errorf("failed to send start command: %w", err)
			s.logger.Warn().Err(spawnErr).Str("agent_id", agent.ID).Msg("agent spawn failed")
			s.markAgentError(ctx, agent, spawnErr.Error(), models.StateConfidenceLow, nil)
			s.cleanupSpawnFailure(ctx, agent)
			return nil, fmt.Errorf("%w: %v", ErrSpawnFailed, spawnErr)
		}
		agent.Metadata.StartCommand = startCmd
	}

	// Send initial prompt if provided
	if opts.InitialPrompt != "" {
		// Wait a bit for the agent to start
		time.Sleep(500 * time.Millisecond)
		if err := s.tmuxClient.SendKeys(ctx, paneTarget, opts.InitialPrompt, true, true); err != nil {
			s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to send initial prompt")
		}
	}

	// Wait for agent to reach ready/idle state
	if err := s.waitForReady(ctx, agent, opts); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("agent failed to reach ready state")
		s.markAgentError(ctx, agent, err.Error(), models.StateConfidenceLow, nil)
		s.cleanupSpawnFailure(ctx, agent)
		return nil, fmt.Errorf("%w: %v", ErrSpawnFailed, err)
	}

	s.logger.Info().
		Str("agent_id", agent.ID).
		Str("workspace_id", opts.WorkspaceID).
		Str("type", string(opts.Type)).
		Str("pane", paneTarget).
		Msg("agent spawned")

	return agent, nil
}

func (s *Service) waitForReady(ctx context.Context, agent *models.Agent, opts SpawnOptions) error {
	if agent == nil {
		return fmt.Errorf("agent is nil")
	}
	if agent.TmuxPane == "" {
		return fmt.Errorf("agent has no tmux pane")
	}

	adapter := adapters.GetByAgentType(agent.Type)
	if adapter == nil {
		adapter = adapters.GenericFallbackAdapter()
	}

	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	interval := opts.ReadyPollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}

	var lastOutput string
	var lastCaptureErr error
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(interval)
	defer deadline.Stop()
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if lastLine := lastNonEmptyLine(lastOutput); lastLine != "" {
				return fmt.Errorf("timeout waiting for agent readiness; last output: %s", lastLine)
			}
			if lastCaptureErr != nil {
				return fmt.Errorf("timeout waiting for agent readiness; last capture error: %v", lastCaptureErr)
			}
			return fmt.Errorf("timeout waiting for agent readiness")
		case <-ticker.C:
		}

		output, err := s.tmuxClient.CapturePane(ctx, agent.TmuxPane, false)
		if err != nil {
			lastCaptureErr = err
			exists, checkErr := s.paneExists(ctx, agent.TmuxPane)
			if checkErr != nil {
				s.logger.Debug().Err(checkErr).Str("agent_id", agent.ID).Msg("failed to confirm pane existence while waiting for ready")
			} else if !exists {
				return fmt.Errorf("agent pane %s missing; agent likely exited before ready", agent.TmuxPane)
			}
			s.logger.Debug().Err(err).Str("agent_id", agent.ID).Msg("failed to capture pane while waiting for ready")
			continue
		}
		lastOutput = output

		ready, err := adapter.DetectReady(output)
		if err != nil {
			s.logger.Debug().Err(err).Str("agent_id", agent.ID).Msg("ready detection failed")
		}
		if ready {
			s.markAgentState(ctx, agent, models.AgentStateIdle, "Agent ready", models.StateConfidenceMedium, nil)
			return nil
		}

		state, reason, err := adapter.DetectState(output, nil)
		if err != nil {
			continue
		}
		if state == models.AgentStateError {
			s.markAgentState(ctx, agent, state, reason.Reason, reason.Confidence, reason.Evidence)
			return fmt.Errorf("agent reported error before ready: %s", reason.Reason)
		}
	}
}

func (s *Service) markAgentError(ctx context.Context, agent *models.Agent, reason string, confidence models.StateConfidence, evidence []string) {
	s.markAgentState(ctx, agent, models.AgentStateError, reason, confidence, evidence)
}

func (s *Service) markAgentState(ctx context.Context, agent *models.Agent, state models.AgentState, reason string, confidence models.StateConfidence, evidence []string) {
	if agent == nil {
		return
	}

	now := time.Now().UTC()
	agent.State = state
	agent.StateInfo = models.StateInfo{
		State:      state,
		Confidence: confidence,
		Reason:     reason,
		Evidence:   evidence,
		DetectedAt: now,
	}
	agent.LastActivity = &now

	if err := s.repo.Update(ctx, agent); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to update agent state")
	}
}

func (s *Service) cleanupSpawnFailure(ctx context.Context, agent *models.Agent) {
	if agent == nil {
		return
	}

	if agent.TmuxPane != "" {
		if err := s.tmuxClient.KillPane(ctx, agent.TmuxPane); err != nil {
			s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to kill pane after spawn failure")
		}
	}

	if s.queueRepo != nil {
		if _, err := s.queueRepo.Clear(ctx, agent.ID); err != nil {
			s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to clear queue after spawn failure")
		}
	}

	if err := s.paneMap.UnregisterAgent(agent.ID); err != nil && !errors.Is(err, ErrAgentNotFound) {
		s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to unregister pane mapping after spawn failure")
	}
}

func (s *Service) paneExists(ctx context.Context, target string) (bool, error) {
	session, paneID, ok := splitPaneTarget(target)
	if !ok {
		return false, fmt.Errorf("invalid pane target: %q", target)
	}

	hasSession, err := s.tmuxClient.HasSession(ctx, session)
	if err != nil {
		return false, err
	}
	if !hasSession {
		return false, nil
	}

	panes, err := s.tmuxClient.ListPanes(ctx, session)
	if err != nil {
		return false, err
	}

	for _, pane := range panes {
		if pane.ID == paneID {
			return true, nil
		}
	}

	return false, nil
}

func splitPaneTarget(target string) (session, paneID string, ok bool) {
	parts := strings.SplitN(target, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	session = strings.TrimSpace(parts[0])
	paneID = strings.TrimSpace(parts[1])
	if session == "" || paneID == "" {
		return "", "", false
	}

	return session, paneID, true
}

func lastNonEmptyLine(output string) string {
	if output == "" {
		return ""
	}

	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if len(line) > 200 {
			line = line[len(line)-200:]
		}
		return line
	}

	return ""
}

// ListAgentsOptions contains options for listing agents.
type ListAgentsOptions struct {
	// WorkspaceID filters by workspace.
	WorkspaceID string

	// State filters by state.
	State *models.AgentState

	// IncludeQueueLength includes queue length in results.
	IncludeQueueLength bool
}

// ListAgents returns agents matching the options.
func (s *Service) ListAgents(ctx context.Context, opts ListAgentsOptions) ([]*models.Agent, error) {
	var agents []*models.Agent
	var err error

	if opts.WorkspaceID != "" {
		agents, err = s.repo.ListByWorkspace(ctx, opts.WorkspaceID)
	} else if opts.State != nil {
		agents, err = s.repo.ListByState(ctx, *opts.State)
	} else if opts.IncludeQueueLength {
		agents, err = s.repo.ListWithQueueLength(ctx)
	} else {
		agents, err = s.repo.List(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}

	return agents, nil
}

// GetAgent retrieves an agent by ID.
func (s *Service) GetAgent(ctx context.Context, id string) (*models.Agent, error) {
	agent, err := s.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrAgentNotFound) {
			return nil, ErrServiceAgentNotFound
		}
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}

	// Get queue length
	if s.queueRepo != nil {
		count, err := s.queueRepo.Count(ctx, agent.ID)
		if err == nil {
			agent.QueueLength = count
		}
	}

	return agent, nil
}

// AgentStateResult contains comprehensive state information.
type AgentStateResult struct {
	// Agent is the base agent info.
	Agent *models.Agent

	// PaneActive indicates if the tmux pane is active.
	PaneActive bool

	// LastOutput is recent output from the pane (if captured).
	LastOutput string

	// QueueLength is the number of pending queue items.
	QueueLength int
}

// GetAgentState retrieves comprehensive state for an agent.
func (s *Service) GetAgentState(ctx context.Context, id string) (*AgentStateResult, error) {
	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return nil, err
	}

	result := &AgentStateResult{
		Agent:       agent,
		QueueLength: agent.QueueLength,
	}

	// Check if pane is active
	if agent.TmuxPane != "" {
		exists, err := s.tmuxClient.HasSession(ctx, agent.TmuxPane)
		if err == nil {
			result.PaneActive = exists
		}

		// Capture recent output
		output, err := s.tmuxClient.CapturePane(ctx, agent.TmuxPane, false)
		if err == nil {
			result.LastOutput = output
		}
	}

	return result, nil
}

// InterruptAgent sends an interrupt signal to an agent.
func (s *Service) InterruptAgent(ctx context.Context, id string) error {
	s.logger.Debug().Str("agent_id", id).Msg("interrupting agent")

	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	if agent.TmuxPane == "" {
		return fmt.Errorf("%w: agent has no tmux pane", ErrInterruptFailed)
	}

	// Send Ctrl+C to the pane
	if err := s.tmuxClient.SendInterrupt(ctx, agent.TmuxPane); err != nil {
		return fmt.Errorf("%w: %v", ErrInterruptFailed, err)
	}

	// Update agent state
	now := time.Now().UTC()
	agent.State = models.AgentStateIdle
	agent.StateInfo = models.StateInfo{
		State:      models.AgentStateIdle,
		Confidence: models.StateConfidenceMedium,
		Reason:     "Interrupted by user",
		DetectedAt: now,
	}
	agent.LastActivity = &now

	if err := s.repo.Update(ctx, agent); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", id).Msg("failed to update agent state after interrupt")
	}

	s.logger.Info().Str("agent_id", id).Msg("agent interrupted")
	return nil
}

// RestartAgent restarts an agent by terminating and respawning it.
func (s *Service) RestartAgent(ctx context.Context, id string) (*models.Agent, error) {
	s.logger.Debug().Str("agent_id", id).Msg("restarting agent")

	// Get current agent
	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return nil, err
	}

	// Remember spawn options
	opts := SpawnOptions{
		WorkspaceID: agent.WorkspaceID,
		Type:        agent.Type,
		AccountID:   agent.AccountID,
		Environment: agent.Metadata.Environment,
	}

	// Terminate the existing agent
	if err := s.TerminateAgent(ctx, id); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", id).Msg("failed to terminate agent during restart")
	}

	// Spawn a new agent with the same options
	newAgent, err := s.SpawnAgent(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to respawn agent: %w", err)
	}

	s.logger.Info().
		Str("old_agent_id", id).
		Str("new_agent_id", newAgent.ID).
		Msg("agent restarted")

	return newAgent, nil
}

// TerminateAgent stops and removes an agent.
func (s *Service) TerminateAgent(ctx context.Context, id string) error {
	s.logger.Debug().Str("agent_id", id).Msg("terminating agent")

	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	// Send interrupt first to gracefully stop
	if agent.TmuxPane != "" {
		_ = s.tmuxClient.SendInterrupt(ctx, agent.TmuxPane)
		time.Sleep(100 * time.Millisecond)

		// Kill the pane
		if err := s.tmuxClient.KillPane(ctx, agent.TmuxPane); err != nil {
			s.logger.Warn().Err(err).Str("agent_id", id).Msg("failed to kill pane")
		}
	}

	// Clear the agent's queue
	if s.queueRepo != nil {
		cleared, err := s.queueRepo.Clear(ctx, id)
		if err != nil {
			s.logger.Warn().Err(err).Str("agent_id", id).Msg("failed to clear queue")
		} else if cleared > 0 {
			s.logger.Debug().Int("cleared", cleared).Str("agent_id", id).Msg("cleared queue items")
		}
	}

	// Unregister pane mapping
	if err := s.paneMap.UnregisterAgent(id); err != nil && !errors.Is(err, ErrAgentNotFound) {
		s.logger.Warn().Err(err).Str("agent_id", id).Msg("failed to unregister pane mapping")
	}

	// Delete agent from database
	if err := s.repo.Delete(ctx, id); err != nil {
		if errors.Is(err, db.ErrAgentNotFound) {
			return ErrServiceAgentNotFound
		}
		return fmt.Errorf("%w: %v", ErrTerminateFailed, err)
	}

	s.logger.Info().Str("agent_id", id).Msg("agent terminated")
	return nil
}

// UpdateAgentState updates an agent's state.
func (s *Service) UpdateAgentState(ctx context.Context, id string, state models.AgentState, reason string, confidence models.StateConfidence) error {
	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	agent.State = state
	agent.StateInfo = models.StateInfo{
		State:      state,
		Confidence: confidence,
		Reason:     reason,
		DetectedAt: now,
	}
	agent.LastActivity = &now

	return s.repo.Update(ctx, agent)
}

// PauseAgent pauses an agent for a duration.
func (s *Service) PauseAgent(ctx context.Context, id string, duration time.Duration) error {
	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	pausedUntil := now.Add(duration)

	agent.State = models.AgentStatePaused
	agent.StateInfo = models.StateInfo{
		State:      models.AgentStatePaused,
		Confidence: models.StateConfidenceHigh,
		Reason:     fmt.Sprintf("Paused until %s", pausedUntil.Format(time.RFC3339)),
		DetectedAt: now,
	}
	agent.PausedUntil = &pausedUntil

	return s.repo.Update(ctx, agent)
}

// ResumeAgent resumes a paused agent.
func (s *Service) ResumeAgent(ctx context.Context, id string) error {
	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	agent.State = models.AgentStateIdle
	agent.StateInfo = models.StateInfo{
		State:      models.AgentStateIdle,
		Confidence: models.StateConfidenceMedium,
		Reason:     "Resumed from pause",
		DetectedAt: now,
	}
	agent.PausedUntil = nil
	agent.LastActivity = &now

	return s.repo.Update(ctx, agent)
}

// buildStartCommand builds the command to start an agent CLI.
func (s *Service) buildStartCommand(opts SpawnOptions) string {
	adapter := adapters.GetByAgentType(opts.Type)
	if adapter == nil {
		adapter = adapters.GenericFallbackAdapter()
	}
	if adapter == nil {
		return ""
	}

	cmd, args := adapter.SpawnCommand(adapters.SpawnOptions{
		AgentType:     opts.Type,
		AccountID:     opts.AccountID,
		InitialPrompt: opts.InitialPrompt,
		Environment:   opts.Environment,
	})
	if cmd == "" {
		return ""
	}

	envPrefix := formatEnvPrefix(opts.Environment)
	return envPrefix + joinCommand(cmd, args)
}

func formatEnvPrefix(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(shellEscape(env[key]))
		builder.WriteString(" ")
	}
	return builder.String()
}

func joinCommand(cmd string, args []string) string {
	if cmd == "" {
		return ""
	}

	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellEscape(cmd))
	for _, arg := range args {
		parts = append(parts, shellEscape(arg))
	}
	return strings.Join(parts, " ")
}

func shellEscape(value string) string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(value, "'", "'\\''"))
}

// GetPaneMap returns the pane mapping registry.
func (s *Service) GetPaneMap() *PaneMap {
	return s.paneMap
}

// SendMessageOptions contains options for sending a message to an agent.
type SendMessageOptions struct {
	// SkipIdleCheck bypasses the idle state verification.
	SkipIdleCheck bool

	// WaitForStable waits for the screen to stabilize after sending.
	WaitForStable bool

	// StableRounds is the number of unchanged polls needed to consider stable.
	StableRounds int

	// MaxRetries is the maximum number of retry attempts on failure.
	// 0 means no retries (default).
	MaxRetries int

	// RetryBackoff is the initial backoff duration between retries.
	// Doubles on each retry. Defaults to 100ms.
	RetryBackoff time.Duration
}

// SendMessage sends a message to an agent.
// By default, it verifies the agent is in an idle state before sending.
// The message is sent via the adapter for proper formatting.
func (s *Service) SendMessage(ctx context.Context, id, message string, opts *SendMessageOptions) error {
	if opts == nil {
		opts = &SendMessageOptions{}
	}

	s.logger.Debug().
		Str("agent_id", id).
		Str("message", message).
		Msg("sending message to agent")

	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	// Verify agent is idle (unless skipped)
	if !opts.SkipIdleCheck && agent.State != models.AgentStateIdle {
		return fmt.Errorf("%w: current state is %s", ErrAgentNotIdle, agent.State)
	}

	if agent.TmuxPane == "" {
		return fmt.Errorf("%w: agent has no tmux pane", ErrSendFailed)
	}

	// Update state to working
	now := time.Now().UTC()
	agent.State = models.AgentStateWorking
	agent.StateInfo = models.StateInfo{
		State:      models.AgentStateWorking,
		Confidence: models.StateConfidenceMedium,
		Reason:     "Message sent, awaiting response",
		DetectedAt: now,
	}
	agent.LastActivity = &now

	if err := s.repo.Update(ctx, agent); err != nil {
		s.logger.Warn().Err(err).Str("agent_id", id).Msg("failed to update agent state before send")
	}

	// Send the message via tmux with optional retry
	backoff := opts.RetryBackoff
	if backoff <= 0 {
		backoff = 100 * time.Millisecond
	}

	var lastErr error
	maxAttempts := opts.MaxRetries + 1 // +1 for initial attempt
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check context cancellation
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}

		// Check if pane is still active
		paneActive, err := s.tmuxClient.HasSession(ctx, agent.TmuxPane)
		if err != nil || !paneActive {
			lastErr = fmt.Errorf("pane is dead or inaccessible")
			break // Don't retry if pane is dead
		}

		// Send the message
		if opts.WaitForStable {
			stableRounds := opts.StableRounds
			if stableRounds <= 0 {
				stableRounds = 3
			}
			_, lastErr = s.tmuxClient.SendAndWait(ctx, agent.TmuxPane, message, true, true, stableRounds)
		} else {
			lastErr = s.tmuxClient.SendKeys(ctx, agent.TmuxPane, message, true, true)
		}

		if lastErr == nil {
			break // Success
		}

		// Log retry attempt
		if attempt < maxAttempts {
			s.logger.Warn().
				Err(lastErr).
				Str("agent_id", id).
				Int("attempt", attempt).
				Int("max_attempts", maxAttempts).
				Dur("backoff", backoff).
				Msg("send failed, retrying")

			time.Sleep(backoff)
			backoff *= 2 // Exponential backoff
		}
	}

	if lastErr != nil {
		// Revert state on failure
		agent.State = models.AgentStateError
		agent.StateInfo = models.StateInfo{
			State:      models.AgentStateError,
			Confidence: models.StateConfidenceHigh,
			Reason:     fmt.Sprintf("Send failed after %d attempts: %v", maxAttempts, lastErr),
			DetectedAt: time.Now().UTC(),
		}
		_ = s.repo.Update(ctx, agent)
		return fmt.Errorf("%w: %v", ErrSendFailed, lastErr)
	}

	s.logger.Info().
		Str("agent_id", id).
		Msg("message sent to agent")

	return nil
}

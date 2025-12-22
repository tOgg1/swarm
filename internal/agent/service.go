// Package agent provides agent lifecycle management for Swarm.
package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	startCmd := s.buildStartCommand(opts.Type, opts.AccountID, opts.Environment)
	if startCmd != "" {
		if err := s.tmuxClient.SendKeys(ctx, paneTarget, startCmd, true, true); err != nil {
			s.logger.Warn().Err(err).Str("agent_id", agent.ID).Msg("failed to send start command")
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

	s.logger.Info().
		Str("agent_id", agent.ID).
		Str("workspace_id", opts.WorkspaceID).
		Str("type", string(opts.Type)).
		Str("pane", paneTarget).
		Msg("agent spawned")

	return agent, nil
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
			return ErrAgentNotFound
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
func (s *Service) buildStartCommand(agentType models.AgentType, accountID string, env map[string]string) string {
	// Build environment prefix
	envPrefix := ""
	for k, v := range env {
		envPrefix += fmt.Sprintf("%s=%q ", k, v)
	}

	switch agentType {
	case models.AgentTypeOpenCode:
		return envPrefix + "opencode"
	case models.AgentTypeClaudeCode:
		return envPrefix + "claude"
	case models.AgentTypeCodex:
		return envPrefix + "codex"
	case models.AgentTypeGemini:
		return envPrefix + "gemini"
	case models.AgentTypeGeneric:
		// Generic agents don't auto-start a CLI
		return ""
	default:
		return ""
	}
}

// GetPaneMap returns the pane mapping registry.
func (s *Service) GetPaneMap() *PaneMap {
	return s.paneMap
}

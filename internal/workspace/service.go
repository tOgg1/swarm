// Package workspace provides helpers for workspace lifecycle management.
package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/rs/zerolog"
)

// Service errors.
var (
	ErrWorkspaceNotFound      = errors.New("workspace not found")
	ErrWorkspaceAlreadyExists = errors.New("workspace already exists")
	ErrNodeNotFound           = errors.New("node not found")
	ErrTmuxSessionFailed      = errors.New("failed to create tmux session")
	ErrRepoValidationFailed   = errors.New("repository validation failed")
)

// Service manages workspace operations.
type Service struct {
	repo        *db.WorkspaceRepository
	nodeService *node.Service
	agentRepo   *db.AgentRepository
	logger      zerolog.Logger
}

// NewService creates a new WorkspaceService.
func NewService(repo *db.WorkspaceRepository, nodeService *node.Service, agentRepo *db.AgentRepository) *Service {
	return &Service{
		repo:        repo,
		nodeService: nodeService,
		agentRepo:   agentRepo,
		logger:      logging.Component("workspace"),
	}
}

// CreateWorkspaceInput contains the parameters for creating a workspace.
type CreateWorkspaceInput struct {
	// NodeID is the node where the workspace will be created.
	// If empty, uses the local node.
	NodeID string

	// RepoPath is the absolute path to the repository.
	RepoPath string

	// Name is an optional human-friendly name.
	// If empty, derived from the repo path.
	Name string

	// TmuxSession is an optional session name.
	// If empty, auto-generated from repo path.
	TmuxSession string

	// CreateTmuxSession indicates whether to create a new tmux session.
	CreateTmuxSession bool
}

// CreateWorkspace creates a new workspace for a repository.
func (s *Service) CreateWorkspace(ctx context.Context, input CreateWorkspaceInput) (*models.Workspace, error) {
	s.logger.Debug().
		Str("node_id", input.NodeID).
		Str("repo_path", input.RepoPath).
		Msg("creating workspace")

	// Validate repo path exists
	if err := ValidateRepoPath(input.RepoPath); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRepoValidationFailed, err)
	}

	// Get or default node
	nodeID := input.NodeID
	if nodeID == "" {
		// Use local node - it must exist
		nodes, err := s.nodeService.ListNodes(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to list nodes: %w", err)
		}
		for _, n := range nodes {
			if n.IsLocal {
				nodeID = n.ID
				break
			}
		}
		if nodeID == "" {
			return nil, fmt.Errorf("no local node found")
		}
	}

	// Verify node exists
	_, err := s.nodeService.GetNode(ctx, nodeID)
	if err != nil {
		if errors.Is(err, node.ErrNodeNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	// Generate tmux session name if not provided
	tmuxSession := input.TmuxSession
	if tmuxSession == "" {
		tmuxSession, err = GenerateTmuxSessionName("swarm", input.RepoPath)
		if err != nil {
			return nil, fmt.Errorf("failed to generate tmux session name: %w", err)
		}
	}

	// Detect git info
	gitInfo, err := DetectGitInfo(input.RepoPath)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to detect git info")
		// Not fatal - continue without git info
	}

	// Create workspace record
	workspace := &models.Workspace{
		Name:        input.Name,
		NodeID:      nodeID,
		RepoPath:    input.RepoPath,
		TmuxSession: tmuxSession,
		Status:      models.WorkspaceStatusActive,
		GitInfo:     gitInfo,
	}

	if workspace.Name == "" {
		workspace.Name = tmuxSession
	}

	// Create tmux session if requested
	if input.CreateTmuxSession {
		if err := s.createTmuxSession(ctx, workspace); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTmuxSessionFailed, err)
		}
	}

	// Persist to database
	if err := s.repo.Create(ctx, workspace); err != nil {
		if errors.Is(err, db.ErrWorkspaceAlreadyExists) {
			return nil, ErrWorkspaceAlreadyExists
		}
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	s.logger.Info().
		Str("workspace_id", workspace.ID).
		Str("name", workspace.Name).
		Str("tmux_session", workspace.TmuxSession).
		Msg("workspace created")

	return workspace, nil
}

// ImportWorkspaceInput contains the parameters for importing an existing tmux session.
type ImportWorkspaceInput struct {
	// NodeID is the node where the session exists.
	NodeID string

	// TmuxSession is the name of the existing tmux session.
	TmuxSession string

	// Name is an optional human-friendly name.
	Name string
}

// ImportWorkspace imports an existing tmux session as a workspace.
func (s *Service) ImportWorkspace(ctx context.Context, input ImportWorkspaceInput) (*models.Workspace, error) {
	s.logger.Debug().
		Str("node_id", input.NodeID).
		Str("tmux_session", input.TmuxSession).
		Msg("importing workspace")

	// Verify node exists
	nodeObj, err := s.nodeService.GetNode(ctx, input.NodeID)
	if err != nil {
		if errors.Is(err, node.ErrNodeNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	// Get working directory from tmux session
	repoPath, err := s.getTmuxSessionWorkingDir(ctx, nodeObj, input.TmuxSession)
	if err != nil {
		return nil, fmt.Errorf("failed to get tmux session working directory: %w", err)
	}

	// Detect git info
	gitInfo, err := DetectGitInfo(repoPath)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to detect git info")
	}

	// Create workspace record
	workspace := &models.Workspace{
		Name:        input.Name,
		NodeID:      input.NodeID,
		RepoPath:    repoPath,
		TmuxSession: input.TmuxSession,
		Status:      models.WorkspaceStatusActive,
		GitInfo:     gitInfo,
	}

	if workspace.Name == "" {
		workspace.Name = input.TmuxSession
	}

	// Persist to database
	if err := s.repo.Create(ctx, workspace); err != nil {
		if errors.Is(err, db.ErrWorkspaceAlreadyExists) {
			return nil, ErrWorkspaceAlreadyExists
		}
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	s.logger.Info().
		Str("workspace_id", workspace.ID).
		Str("name", workspace.Name).
		Str("tmux_session", workspace.TmuxSession).
		Msg("workspace imported")

	return workspace, nil
}

// ListWorkspacesOptions contains options for listing workspaces.
type ListWorkspacesOptions struct {
	// NodeID filters by node.
	NodeID string

	// Status filters by status.
	Status *models.WorkspaceStatus

	// IncludeAgentCounts includes agent counts in results.
	IncludeAgentCounts bool
}

// ListWorkspaces returns all workspaces matching the options.
func (s *Service) ListWorkspaces(ctx context.Context, opts ListWorkspacesOptions) ([]*models.Workspace, error) {
	var workspaces []*models.Workspace
	var err error

	if opts.NodeID != "" {
		workspaces, err = s.repo.ListByNode(ctx, opts.NodeID)
	} else if opts.Status != nil {
		workspaces, err = s.repo.ListByStatus(ctx, *opts.Status)
	} else if opts.IncludeAgentCounts {
		workspaces, err = s.repo.ListWithAgentCounts(ctx)
	} else {
		workspaces, err = s.repo.List(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}

	// Populate agent counts if not already included
	if !opts.IncludeAgentCounts {
		for _, ws := range workspaces {
			count, err := s.repo.GetAgentCount(ctx, ws.ID)
			if err != nil {
				s.logger.Warn().Err(err).Str("workspace_id", ws.ID).Msg("failed to get agent count")
				continue
			}
			ws.AgentCount = count
		}
	}

	return workspaces, nil
}

// GetWorkspace retrieves a workspace by ID.
func (s *Service) GetWorkspace(ctx context.Context, id string) (*models.Workspace, error) {
	workspace, err := s.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrWorkspaceNotFound) {
			return nil, ErrWorkspaceNotFound
		}
		return nil, fmt.Errorf("failed to get workspace: %w", err)
	}

	// Populate agent count
	count, err := s.repo.GetAgentCount(ctx, workspace.ID)
	if err != nil {
		s.logger.Warn().Err(err).Str("workspace_id", workspace.ID).Msg("failed to get agent count")
	} else {
		workspace.AgentCount = count
	}

	return workspace, nil
}

// WorkspaceStatusResult contains comprehensive status information.
type WorkspaceStatusResult struct {
	// Workspace is the base workspace info.
	Workspace *models.Workspace

	// TmuxActive indicates if the tmux session is active.
	TmuxActive bool

	// NodeOnline indicates if the node is reachable.
	NodeOnline bool

	// GitInfo is the current git repository state.
	GitInfo *models.GitInfo

	// ActiveAgents is the number of active agents.
	ActiveAgents int

	// IdleAgents is the number of idle agents.
	IdleAgents int

	// BlockedAgents is the number of blocked agents.
	BlockedAgents int

	// Alerts contains current alerts.
	Alerts []models.Alert
}

// GetWorkspaceStatus retrieves comprehensive status for a workspace.
func (s *Service) GetWorkspaceStatus(ctx context.Context, id string) (*WorkspaceStatusResult, error) {
	workspace, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return nil, err
	}

	result := &WorkspaceStatusResult{
		Workspace: workspace,
		GitInfo:   workspace.GitInfo,
	}

	// Check if node is online
	nodeObj, err := s.nodeService.GetNode(ctx, workspace.NodeID)
	if err == nil {
		result.NodeOnline = nodeObj.Status == models.NodeStatusOnline
	}

	// Check if tmux session is active
	if result.NodeOnline {
		result.TmuxActive = s.isTmuxSessionActive(ctx, nodeObj, workspace.TmuxSession)
	}

	// Refresh git info
	if workspace.RepoPath != "" {
		if gitInfo, err := DetectGitInfo(workspace.RepoPath); err == nil {
			result.GitInfo = gitInfo
			// Update stored git info
			if err := s.repo.UpdateGitInfo(ctx, workspace.ID, gitInfo); err != nil {
				s.logger.Warn().Err(err).Msg("failed to update git info")
			}
		}
	}

	// Populate alerts from agent states when available.
	if s.agentRepo != nil {
		agents, err := s.agentRepo.ListByWorkspace(ctx, workspace.ID)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to list agents for alerts")
		} else {
			result.Alerts = append(result.Alerts, BuildAlerts(agents)...)

			for _, agent := range agents {
				switch agent.State {
				case models.AgentStateWorking:
					result.ActiveAgents++
				case models.AgentStateIdle:
					result.IdleAgents++
				case models.AgentStateAwaitingApproval, models.AgentStateRateLimited, models.AgentStateError:
					result.BlockedAgents++
				}
			}
		}
	} else {
		// Fallback when agent repository isn't wired.
		result.ActiveAgents = workspace.AgentCount
	}

	return result, nil
}

// DeleteWorkspace removes a workspace from Swarm.
// It does not terminate the tmux session or delete files.
func (s *Service) DeleteWorkspace(ctx context.Context, id string) error {
	s.logger.Debug().Str("workspace_id", id).Msg("deleting workspace")

	if err := s.repo.Delete(ctx, id); err != nil {
		if errors.Is(err, db.ErrWorkspaceNotFound) {
			return ErrWorkspaceNotFound
		}
		return fmt.Errorf("failed to delete workspace: %w", err)
	}

	s.logger.Info().Str("workspace_id", id).Msg("workspace deleted")
	return nil
}

// UpdateWorkspace updates a workspace's configuration.
func (s *Service) UpdateWorkspace(ctx context.Context, workspace *models.Workspace) error {
	s.logger.Debug().Str("workspace_id", workspace.ID).Msg("updating workspace")

	if err := s.repo.Update(ctx, workspace); err != nil {
		if errors.Is(err, db.ErrWorkspaceNotFound) {
			return ErrWorkspaceNotFound
		}
		return fmt.Errorf("failed to update workspace: %w", err)
	}

	s.logger.Info().Str("workspace_id", workspace.ID).Msg("workspace updated")
	return nil
}

// RefreshGitInfo updates the git information for a workspace.
func (s *Service) RefreshGitInfo(ctx context.Context, id string) (*models.GitInfo, error) {
	workspace, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return nil, err
	}

	gitInfo, err := DetectGitInfo(workspace.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to detect git info: %w", err)
	}

	if err := s.repo.UpdateGitInfo(ctx, id, gitInfo); err != nil {
		return nil, fmt.Errorf("failed to update git info: %w", err)
	}

	return gitInfo, nil
}

// AttachWorkspace returns the tmux attach command for a workspace.
func (s *Service) AttachWorkspace(ctx context.Context, id string) (string, error) {
	workspace, err := s.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrWorkspaceNotFound) {
			return "", ErrWorkspaceNotFound
		}
		return "", fmt.Errorf("failed to get workspace: %w", err)
	}

	if workspace.TmuxSession == "" {
		return "", fmt.Errorf("workspace %s has no tmux session", workspace.ID)
	}

	return fmt.Sprintf("tmux attach -t %s", workspace.TmuxSession), nil
}

// UnmanageWorkspace removes a workspace record while leaving tmux intact.
func (s *Service) UnmanageWorkspace(ctx context.Context, id string) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		if errors.Is(err, db.ErrWorkspaceNotFound) {
			return ErrWorkspaceNotFound
		}
		return fmt.Errorf("failed to unmanage workspace: %w", err)
	}

	s.logger.Info().Str("workspace_id", id).Msg("workspace unmanaged")
	return nil
}

// DestroyWorkspace kills the tmux session (if local) and removes the workspace record.
func (s *Service) DestroyWorkspace(ctx context.Context, id string) error {
	workspace, err := s.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrWorkspaceNotFound) {
			return ErrWorkspaceNotFound
		}
		return fmt.Errorf("failed to get workspace: %w", err)
	}

	nodeObj, err := s.nodeService.GetNode(ctx, workspace.NodeID)
	if err != nil {
		if errors.Is(err, node.ErrNodeNotFound) {
			return ErrNodeNotFound
		}
		return fmt.Errorf("failed to get node: %w", err)
	}

	if !nodeObj.IsLocal {
		return fmt.Errorf("remote workspace destroy not yet implemented")
	}

	if workspace.TmuxSession != "" {
		client := tmux.NewLocalClient()
		exists, err := client.HasSession(ctx, workspace.TmuxSession)
		if err != nil {
			return fmt.Errorf("failed to check tmux session: %w", err)
		}
		if exists {
			if err := client.KillSession(ctx, workspace.TmuxSession); err != nil {
				return fmt.Errorf("failed to kill tmux session %s: %w", workspace.TmuxSession, err)
			}
		}
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		if errors.Is(err, db.ErrWorkspaceNotFound) {
			return ErrWorkspaceNotFound
		}
		return fmt.Errorf("failed to delete workspace: %w", err)
	}

	s.logger.Info().
		Str("workspace_id", id).
		Str("tmux_session", workspace.TmuxSession).
		Msg("workspace destroyed")

	return nil
}

// createTmuxSession creates a new tmux session for a workspace.
func (s *Service) createTmuxSession(ctx context.Context, workspace *models.Workspace) error {
	// For now, only support local node
	// TODO: Support remote nodes via SSH

	client := tmux.NewLocalClient()
	return client.NewSession(ctx, workspace.TmuxSession, workspace.RepoPath)
}

// getTmuxSessionWorkingDir gets the working directory of a tmux session.
func (s *Service) getTmuxSessionWorkingDir(ctx context.Context, nodeObj *models.Node, sessionName string) (string, error) {
	if !nodeObj.IsLocal {
		return "", fmt.Errorf("remote node tmux inspection not yet implemented")
	}

	client := tmux.NewLocalClient()

	// Use ListPanePaths to get the working directories for the session
	paths, err := client.ListPanePaths(ctx, sessionName)
	if err != nil {
		return "", err
	}

	if len(paths) == 0 {
		return "", fmt.Errorf("no panes found in tmux session %q", sessionName)
	}

	// Return the first path as the main working directory
	return paths[0], nil
}

// isTmuxSessionActive checks if a tmux session is running.
func (s *Service) isTmuxSessionActive(ctx context.Context, nodeObj *models.Node, sessionName string) bool {
	if !nodeObj.IsLocal {
		// TODO: Check remote via SSH
		return false
	}

	client := tmux.NewLocalClient()
	exists, err := client.HasSession(ctx, sessionName)
	if err != nil {
		return false
	}

	return exists
}

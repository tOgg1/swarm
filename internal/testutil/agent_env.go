package testutil

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/adapters"
	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/node"
	"github.com/opencode-ai/swarm/internal/state"
	"github.com/opencode-ai/swarm/internal/tmux"
	"github.com/opencode-ai/swarm/internal/workspace"
	"github.com/stretchr/testify/require"
)

// AgentTestEnv provides a complete environment for testing agent operations.
// It combines TmuxTestEnv and TestDBEnv with agent/state services.
type AgentTestEnv struct {
	// Tmux provides tmux operations.
	Tmux *TmuxTestEnv

	// DB provides database access.
	DB *TestDBEnv

	// Services
	NodeService      *node.Service
	WorkspaceService *workspace.Service
	AgentService     *agent.Service
	StateEngine      *state.Engine

	// Registry is the adapter registry.
	Registry *adapters.Registry

	// TestNode is the default node for testing.
	TestNode *models.Node

	// TestWorkspace is the default workspace for testing.
	TestWorkspace *models.Workspace

	t       *testing.T
	cleanup []func()
}

// NewAgentTestEnv creates a complete agent testing environment.
// It sets up tmux, database, node, workspace, and all services needed
// for integration testing.
func NewAgentTestEnv(t *testing.T) *AgentTestEnv {
	t.Helper()

	env := &AgentTestEnv{
		t:       t,
		cleanup: make([]func(), 0),
	}

	// Set up tmux environment
	env.Tmux = NewTmuxTestEnv(t)
	env.cleanup = append(env.cleanup, env.Tmux.Close)

	// Set up database environment
	env.DB = NewTestDBEnv(t)
	env.cleanup = append(env.cleanup, env.DB.Close)

	// Create adapter registry with generic adapter
	env.Registry = adapters.NewRegistry()
	genericAdapter := adapters.NewGenericAdapter("generic", "bash",
		adapters.WithIdleIndicators("$", ">", "%", "#"), // Include # for root prompt
		adapters.WithBusyIndicators("..."),
	)
	require.NoError(t, env.Registry.Register(genericAdapter), "failed to register generic adapter")

	// Create node service and test node
	env.NodeService = node.NewService(env.DB.NodeRepo)
	ctx := context.Background()

	env.TestNode = &models.Node{
		Name:       "test-node",
		IsLocal:    true,
		SSHBackend: models.SSHBackendAuto,
		Status:     models.NodeStatusOnline,
	}
	require.NoError(t, env.DB.NodeRepo.Create(ctx, env.TestNode), "failed to create test node")

	// Create workspace service with tmux factory
	env.WorkspaceService = workspace.NewService(
		env.DB.WorkspaceRepo,
		env.NodeService,
		env.DB.AgentRepo,
		workspace.WithTmuxClientFactory(func() *tmux.Client {
			return env.Tmux.Client
		}),
	)

	// Create test workspace
	env.TestWorkspace = &models.Workspace{
		NodeID:      env.TestNode.ID,
		RepoPath:    env.Tmux.WorkDir,
		Name:        "test-workspace",
		TmuxSession: env.Tmux.Session,
	}
	require.NoError(t, env.DB.WorkspaceRepo.Create(ctx, env.TestWorkspace), "failed to create test workspace")

	// Create agent service
	env.AgentService = agent.NewService(
		env.DB.AgentRepo,
		env.DB.QueueRepo,
		env.WorkspaceService,
		nil, // no account service for tests
		env.Tmux.Client,
		agent.WithEventRepository(env.DB.EventRepo),
	)

	// Create state engine
	env.StateEngine = state.NewEngine(
		env.DB.AgentRepo,
		env.DB.EventRepo,
		env.Tmux.Client,
		env.Registry,
	)

	return env
}

// Close cleans up all test resources.
func (e *AgentTestEnv) Close() {
	// Run cleanup in reverse order
	for i := len(e.cleanup) - 1; i >= 0; i-- {
		e.cleanup[i]()
	}
}

// SpawnTestAgent spawns a test agent in the test workspace.
// It uses the generic adapter with a simple bash shell.
func (e *AgentTestEnv) SpawnTestAgent(ctx context.Context, opts *agent.SpawnOptions) (*models.Agent, error) {
	e.t.Helper()

	if opts == nil {
		opts = &agent.SpawnOptions{}
	}
	if opts.WorkspaceID == "" {
		opts.WorkspaceID = e.TestWorkspace.ID
	}
	if opts.Type == "" {
		opts.Type = models.AgentType("generic")
	}
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 10 * time.Second
	}
	if opts.ReadyPollInterval == 0 {
		opts.ReadyPollInterval = 100 * time.Millisecond
	}

	return e.AgentService.SpawnAgent(ctx, *opts)
}

// SendKeys sends keys to a specific agent's pane.
func (e *AgentTestEnv) SendKeys(ctx context.Context, agentModel *models.Agent, keys string, enter bool) error {
	e.t.Helper()

	return e.Tmux.Client.SendKeys(ctx, agentModel.TmuxPane, keys, true, enter)
}

// CapturePane captures the content of an agent's pane.
func (e *AgentTestEnv) CapturePane(ctx context.Context, agentModel *models.Agent, withHistory bool) (string, error) {
	e.t.Helper()

	return e.Tmux.Client.CapturePane(ctx, agentModel.TmuxPane, withHistory)
}

// DetectState detects the current state of an agent.
func (e *AgentTestEnv) DetectState(ctx context.Context, agentID string) (*state.DetectionResult, error) {
	e.t.Helper()

	return e.StateEngine.DetectState(ctx, agentID)
}

// DetectAndUpdate detects state and updates the agent record.
func (e *AgentTestEnv) DetectAndUpdate(ctx context.Context, agentID string) (*state.DetectionResult, error) {
	e.t.Helper()

	return e.StateEngine.DetectAndUpdate(ctx, agentID)
}

// WaitForContent waits for specific content to appear in an agent's pane.
func (e *AgentTestEnv) WaitForContent(ctx context.Context, agentModel *models.Agent, substring string, timeout time.Duration) bool {
	e.t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		content, err := e.CapturePane(ctx, agentModel, false)
		if err == nil && strings.Contains(content, substring) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// WaitForState waits for an agent to reach a specific state.
func (e *AgentTestEnv) WaitForState(ctx context.Context, agentID string, targetState models.AgentState, timeout time.Duration) bool {
	e.t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		result, err := e.DetectState(ctx, agentID)
		if err == nil && result.State == targetState {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// GetAgent retrieves an agent from the database.
func (e *AgentTestEnv) GetAgent(ctx context.Context, agentID string) (*models.Agent, error) {
	e.t.Helper()

	return e.DB.AgentRepo.Get(ctx, agentID)
}

// CreateTestAgent creates an agent record directly in the database without spawning.
// Useful for unit testing state detection on existing agents.
func (e *AgentTestEnv) CreateTestAgent(ctx context.Context, pane string) (*models.Agent, error) {
	e.t.Helper()

	agentModel := &models.Agent{
		WorkspaceID: e.TestWorkspace.ID,
		TmuxPane:    pane,
		Type:        models.AgentType("generic"),
		State:       models.AgentStateIdle,
		StateInfo: models.StateInfo{
			State:      models.AgentStateIdle,
			Confidence: models.StateConfidenceHigh,
			Reason:     "Test agent created",
			DetectedAt: time.Now().UTC(),
		},
	}

	if err := e.DB.AgentRepo.Create(ctx, agentModel); err != nil {
		return nil, err
	}

	return agentModel, nil
}

// SubscribeStateChanges subscribes to state changes for testing.
// Returns a channel that receives state changes and an unsubscribe function.
func (e *AgentTestEnv) SubscribeStateChanges(id string) (<-chan state.StateChange, func()) {
	e.t.Helper()

	ch := make(chan state.StateChange, 10)

	err := e.StateEngine.SubscribeFunc(id, func(change state.StateChange) {
		select {
		case ch <- change:
		default:
			// Channel full, drop oldest
			select {
			case <-ch:
				ch <- change
			default:
			}
		}
	})
	require.NoError(e.t, err, "failed to subscribe to state changes")

	unsubscribe := func() {
		_ = e.StateEngine.Unsubscribe(id)
		close(ch)
	}

	return ch, unsubscribe
}

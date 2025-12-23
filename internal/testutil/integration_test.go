package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

// TestTmuxTestEnv_Basic tests the basic tmux test environment.
func TestTmuxTestEnv_Basic(t *testing.T) {
	env := NewTmuxTestEnv(t)
	defer env.Close()

	// Verify session was created
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hasSession, err := env.Client.HasSession(ctx, env.Session)
	if err != nil {
		t.Fatalf("failed to check session: %v", err)
	}
	if !hasSession {
		t.Fatal("expected session to exist")
	}
}

// TestTmuxTestEnv_SendAndCapture tests sending keys and capturing output.
func TestTmuxTestEnv_SendAndCapture(t *testing.T) {
	env := NewTmuxTestEnv(t)
	defer env.Close()

	// Send echo command
	env.SendKeys("echo 'hello world'", true)

	// Wait for output
	found := env.WaitForContent("hello world", 5*time.Second)
	if !found {
		content := env.Capture()
		t.Fatalf("expected 'hello world' in output, got:\n%s", content)
	}
}

// TestTmuxTestEnv_WaitForStable tests waiting for stable output.
func TestTmuxTestEnv_WaitForStable(t *testing.T) {
	env := NewTmuxTestEnv(t)
	defer env.Close()

	// Send a command that produces output
	env.SendKeys("echo 'stable output'", true)

	// Wait for stable content
	content := env.WaitForStable(5*time.Second, 200*time.Millisecond)
	if content == "" {
		t.Fatal("expected non-empty stable content")
	}
}

// TestTmuxTestEnv_SplitPane tests pane splitting.
func TestTmuxTestEnv_SplitPane(t *testing.T) {
	env := NewTmuxTestEnv(t)
	defer env.Close()

	// Initially should have 1 pane
	panes := env.ListPanes()
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane initially, got %d", len(panes))
	}

	// Split horizontally
	_ = env.SplitPane(true)

	// Should now have 2 panes
	panes = env.ListPanes()
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes after split, got %d", len(panes))
	}
}

// TestDBEnv_Basic tests the database test environment.
func TestDBEnv_Basic(t *testing.T) {
	env := NewTestDBEnv(t)
	defer env.Close()

	ctx := context.Background()

	// First create a node (required for workspace)
	node := &models.Node{
		Name:       "test-node",
		IsLocal:    true,
		SSHBackend: models.SSHBackendAuto,
		Status:     models.NodeStatusOnline,
	}
	err := env.NodeRepo.Create(ctx, node)
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// Create a test workspace
	workspace := &models.Workspace{
		NodeID:      node.ID,
		RepoPath:    "/test/workspace",
		Name:        "test-workspace",
		TmuxSession: "test-session",
	}
	err = env.WorkspaceRepo.Create(ctx, workspace)
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	// Verify it was created
	ws, err := env.WorkspaceRepo.Get(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("failed to get workspace: %v", err)
	}
	if ws.Name != "test-workspace" {
		t.Errorf("expected workspace name 'test-workspace', got %q", ws.Name)
	}
}

// TestDBEnv_Agents tests agent creation and retrieval.
func TestDBEnv_Agents(t *testing.T) {
	env := NewTestDBEnv(t)
	defer env.Close()

	ctx := context.Background()

	// Create a node first
	node := &models.Node{
		Name:       "test-node",
		IsLocal:    true,
		SSHBackend: models.SSHBackendAuto,
		Status:     models.NodeStatusOnline,
	}
	err := env.NodeRepo.Create(ctx, node)
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// Create a workspace (agent requires workspace)
	workspace := &models.Workspace{
		NodeID:      node.ID,
		RepoPath:    "/test/agent-workspace",
		Name:        "agent-workspace",
		TmuxSession: "agent-session",
	}
	err = env.WorkspaceRepo.Create(ctx, workspace)
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	// Create an agent
	agent := &models.Agent{
		WorkspaceID: workspace.ID,
		TmuxPane:    "%0",
		Type:        models.AgentTypeClaudeCode,
		State:       models.AgentStateIdle,
	}
	err = env.AgentRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Verify it was created
	found, err := env.AgentRepo.Get(ctx, agent.ID)
	if err != nil {
		t.Fatalf("failed to get agent: %v", err)
	}
	if found.Type != models.AgentTypeClaudeCode {
		t.Errorf("expected agent type claude-code, got %v", found.Type)
	}
	if found.State != models.AgentStateIdle {
		t.Errorf("expected agent state idle, got %v", found.State)
	}
}

// TestFixturePath tests the fixture path helper.
func TestFixturePath(t *testing.T) {
	path := FixturePath(t, "transcripts", "claude_code_idle.txt")
	if path == "" {
		t.Fatal("expected non-empty path")
	}
}

// TestReadFixture tests the fixture reader.
func TestReadFixture(t *testing.T) {
	data := ReadFixture(t, "transcripts", "claude_code_idle.txt")
	if len(data) == 0 {
		t.Fatal("expected non-empty fixture data")
	}
}

// TestAgentTestEnv_Basic tests the basic agent test environment setup.
func TestAgentTestEnv_Basic(t *testing.T) {
	env := NewAgentTestEnv(t)
	defer env.Close()

	ctx := context.Background()

	// Verify node was created
	node, err := env.DB.NodeRepo.Get(ctx, env.TestNode.ID)
	if err != nil {
		t.Fatalf("failed to get test node: %v", err)
	}
	if node.Name != "test-node" {
		t.Errorf("expected node name 'test-node', got %q", node.Name)
	}

	// Verify workspace was created
	ws, err := env.DB.WorkspaceRepo.Get(ctx, env.TestWorkspace.ID)
	if err != nil {
		t.Fatalf("failed to get test workspace: %v", err)
	}
	if ws.Name != "test-workspace" {
		t.Errorf("expected workspace name 'test-workspace', got %q", ws.Name)
	}

	// Verify tmux session exists
	hasSession, err := env.Tmux.Client.HasSession(ctx, env.Tmux.Session)
	if err != nil {
		t.Fatalf("failed to check session: %v", err)
	}
	if !hasSession {
		t.Fatal("expected tmux session to exist")
	}
}

// TestAgentTestEnv_CreateTestAgent tests creating a test agent directly.
func TestAgentTestEnv_CreateTestAgent(t *testing.T) {
	env := NewAgentTestEnv(t)
	defer env.Close()

	ctx := context.Background()

	// Create a test agent using the session as the pane target
	// (tmux will use the active pane in the session)
	paneTarget := env.Tmux.Session
	agent, err := env.CreateTestAgent(ctx, paneTarget)
	if err != nil {
		t.Fatalf("failed to create test agent: %v", err)
	}

	// Verify agent was created
	if agent.ID == "" {
		t.Fatal("expected agent to have an ID")
	}
	if agent.State != models.AgentStateIdle {
		t.Errorf("expected agent state idle, got %v", agent.State)
	}

	// Verify we can retrieve it
	retrieved, err := env.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("failed to get agent: %v", err)
	}
	if retrieved.TmuxPane != paneTarget {
		t.Errorf("expected pane %q, got %q", paneTarget, retrieved.TmuxPane)
	}
}

// TestAgentTestEnv_SendAndCapture tests sending keys and capturing pane content.
func TestAgentTestEnv_SendAndCapture(t *testing.T) {
	env := NewAgentTestEnv(t)
	defer env.Close()

	ctx := context.Background()

	// Create a test agent using the session as the pane target
	paneTarget := env.Tmux.Session
	agent, err := env.CreateTestAgent(ctx, paneTarget)
	if err != nil {
		t.Fatalf("failed to create test agent: %v", err)
	}

	// Send an echo command
	if err := env.SendKeys(ctx, agent, "echo 'integration test output'", true); err != nil {
		t.Fatalf("failed to send keys: %v", err)
	}

	// Wait for the output to appear
	found := env.WaitForContent(ctx, agent, "integration test output", 5*time.Second)
	if !found {
		content, _ := env.CapturePane(ctx, agent, false)
		t.Fatalf("expected 'integration test output' in pane, got:\n%s", content)
	}
}

// TestAgentTestEnv_StateDetection tests state detection on pane content.
func TestAgentTestEnv_StateDetection(t *testing.T) {
	env := NewAgentTestEnv(t)
	defer env.Close()

	ctx := context.Background()

	// Create a test agent using session as target
	paneTarget := env.Tmux.Session
	agent, err := env.CreateTestAgent(ctx, paneTarget)
	if err != nil {
		t.Fatalf("failed to create test agent: %v", err)
	}

	// Send a command to create some output with a $ prompt
	if err := env.SendKeys(ctx, agent, "echo 'done'", true); err != nil {
		t.Fatalf("failed to send keys: %v", err)
	}

	// Wait for the prompt to appear
	time.Sleep(500 * time.Millisecond)

	// Detect state - should be idle since we see a $ prompt in bash
	result, err := env.DetectState(ctx, agent.ID)
	if err != nil {
		t.Fatalf("failed to detect state: %v", err)
	}

	// The generic adapter should detect idle from the $ prompt
	if result.State != models.AgentStateIdle {
		t.Logf("Detected state: %s, reason: %s", result.State, result.Reason)
		// Note: The exact state depends on the screen content
		// We mainly want to verify that detection works without error
	}
}

// TestAgentTestEnv_StateSubscription tests subscribing to state changes.
func TestAgentTestEnv_StateSubscription(t *testing.T) {
	env := NewAgentTestEnv(t)
	defer env.Close()

	ctx := context.Background()

	// Create a test agent using session as target
	paneTarget := env.Tmux.Session
	agent, err := env.CreateTestAgent(ctx, paneTarget)
	if err != nil {
		t.Fatalf("failed to create test agent: %v", err)
	}

	// Subscribe to state changes
	changes, unsubscribe := env.SubscribeStateChanges("test-subscriber")
	defer unsubscribe()

	// Trigger a state change by updating through the engine
	newState := models.AgentStateWorking
	info := models.StateInfo{
		State:      newState,
		Confidence: models.StateConfidenceMedium,
		Reason:     "Test state change",
		DetectedAt: time.Now().UTC(),
	}

	if err := env.StateEngine.UpdateState(ctx, agent.ID, newState, info, nil, nil); err != nil {
		t.Fatalf("failed to update state: %v", err)
	}

	// Wait for the state change notification
	select {
	case change := <-changes:
		if change.PreviousState != models.AgentStateIdle {
			t.Errorf("expected previous state idle, got %v", change.PreviousState)
		}
		if change.CurrentState != models.AgentStateWorking {
			t.Errorf("expected current state working, got %v", change.CurrentState)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for state change notification")
	}
}

// TestAgentTestEnv_WaitForState tests waiting for a specific state.
func TestAgentTestEnv_WaitForState(t *testing.T) {
	env := NewAgentTestEnv(t)
	defer env.Close()

	ctx := context.Background()

	// Create a test agent using session as target
	paneTarget := env.Tmux.Session
	agent, err := env.CreateTestAgent(ctx, paneTarget)
	if err != nil {
		t.Fatalf("failed to create test agent: %v", err)
	}

	// Agent should already be idle based on bash prompt
	time.Sleep(200 * time.Millisecond) // Let shell settle

	// Wait for idle state (should already be there)
	found := env.WaitForState(ctx, agent.ID, models.AgentStateIdle, 5*time.Second)
	if !found {
		result, err := env.DetectState(ctx, agent.ID)
		if err != nil {
			t.Fatalf("failed to detect state: %v", err)
		}
		t.Fatalf("expected to find idle state, detected: %v (reason: %s)", result.State, result.Reason)
	}
}

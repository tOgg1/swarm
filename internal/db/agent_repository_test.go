package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/swarm/internal/models"
)

func createTestWorkspace(t *testing.T, db *DB) *models.Workspace {
	t.Helper()

	nodeRepo := NewNodeRepository(db)
	wsRepo := NewWorkspaceRepository(db)

	node := &models.Node{
		Name:       "test-node",
		SSHBackend: models.SSHBackendAuto,
		Status:     models.NodeStatusUnknown,
		IsLocal:    true,
	}
	if err := nodeRepo.Create(context.Background(), node); err != nil {
		t.Fatalf("create node: %v", err)
	}

	ws := &models.Workspace{
		NodeID:      node.ID,
		RepoPath:    "/tmp/swarm-test",
		TmuxSession: "swarm-test",
	}
	if err := wsRepo.Create(context.Background(), ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	return ws
}

func insertQueueItem(t *testing.T, db *DB, agentID string, status models.QueueItemStatus, position int) {
	t.Helper()

	payload, err := json.Marshal(models.MessagePayload{Text: "hello"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)
	var completedAt *string
	if status == models.QueueItemStatusCompleted {
		value := time.Now().UTC().Format(time.RFC3339)
		completedAt = &value
	}

	_, err = db.ExecContext(context.Background(), `
		INSERT INTO queue_items (
			id, agent_id, type, position, status, payload_json, error_message,
			created_at, dispatched_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		uuid.New().String(),
		agentID,
		string(models.QueueItemTypeMessage),
		position,
		string(status),
		string(payload),
		nil,
		createdAt,
		nil,
		completedAt,
	)
	if err != nil {
		t.Fatalf("insert queue item: %v", err)
	}
}

func TestAgentRepository_CreateAndGet(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ws := createTestWorkspace(t, db)
	repo := NewAgentRepository(db)

	agent := &models.Agent{
		WorkspaceID: ws.ID,
		Type:        models.AgentTypeOpenCode,
		TmuxPane:    "swarm-test:0.1",
		State:       models.AgentStateIdle,
		StateInfo: models.StateInfo{
			Confidence: models.StateConfidenceMedium,
			Reason:     "ready",
			DetectedAt: time.Now().UTC(),
		},
		Metadata: models.AgentMetadata{
			Model: "gpt-5",
		},
	}

	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	retrieved, err := repo.Get(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved.State != models.AgentStateIdle {
		t.Errorf("expected state %q, got %q", models.AgentStateIdle, retrieved.State)
	}
	if retrieved.StateInfo.Confidence != models.StateConfidenceMedium {
		t.Errorf("expected confidence %q, got %q", models.StateConfidenceMedium, retrieved.StateInfo.Confidence)
	}
	if retrieved.Metadata.Model != "gpt-5" {
		t.Errorf("expected metadata model gpt-5, got %q", retrieved.Metadata.Model)
	}
}

func TestAgentRepository_ListByWorkspaceAndState(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ws := createTestWorkspace(t, db)
	repo := NewAgentRepository(db)

	agent1 := &models.Agent{
		WorkspaceID: ws.ID,
		Type:        models.AgentTypeOpenCode,
		TmuxPane:    "swarm-test:0.1",
		State:       models.AgentStateWorking,
	}
	agent2 := &models.Agent{
		WorkspaceID: ws.ID,
		Type:        models.AgentTypeCodex,
		TmuxPane:    "swarm-test:0.2",
		State:       models.AgentStateIdle,
	}

	if err := repo.Create(context.Background(), agent1); err != nil {
		t.Fatalf("Create agent1 failed: %v", err)
	}
	if err := repo.Create(context.Background(), agent2); err != nil {
		t.Fatalf("Create agent2 failed: %v", err)
	}

	byWorkspace, err := repo.ListByWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("ListByWorkspace failed: %v", err)
	}
	if len(byWorkspace) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(byWorkspace))
	}

	working, err := repo.ListByState(context.Background(), models.AgentStateWorking)
	if err != nil {
		t.Fatalf("ListByState failed: %v", err)
	}
	if len(working) != 1 {
		t.Fatalf("expected 1 working agent, got %d", len(working))
	}
}

func TestAgentRepository_ListWithQueueLength(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ws := createTestWorkspace(t, db)
	repo := NewAgentRepository(db)

	agent := &models.Agent{
		WorkspaceID: ws.ID,
		Type:        models.AgentTypeOpenCode,
		TmuxPane:    "swarm-test:0.1",
		State:       models.AgentStateIdle,
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	insertQueueItem(t, db, agent.ID, models.QueueItemStatusPending, 1)
	insertQueueItem(t, db, agent.ID, models.QueueItemStatusCompleted, 2)
	insertQueueItem(t, db, agent.ID, models.QueueItemStatusDispatched, 3)

	agents, err := repo.ListWithQueueLength(context.Background())
	if err != nil {
		t.Fatalf("ListWithQueueLength failed: %v", err)
	}

	var found *models.Agent
	for _, a := range agents {
		if a.ID == agent.ID {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatalf("expected agent in list")
	}
	if found.QueueLength != 1 {
		t.Errorf("expected queue length 1, got %d", found.QueueLength)
	}
}

package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
)

func setupTestService(t *testing.T) (*Service, *db.DB, func()) {
	t.Helper()

	testDB, err := db.OpenInMemory()
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}

	if err := testDB.Migrate(context.Background()); err != nil {
		testDB.Close()
		t.Fatalf("failed to migrate database: %v", err)
	}

	repo := db.NewQueueRepository(testDB)
	service := NewService(repo)

	cleanup := func() {
		testDB.Close()
	}

	return service, testDB, cleanup
}

func createTestWorkspace(t *testing.T, testDB *db.DB) *models.Workspace {
	t.Helper()

	nodeRepo := db.NewNodeRepository(testDB)
	wsRepo := db.NewWorkspaceRepository(testDB)

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

func createTestAgent(t *testing.T, testDB *db.DB, ws *models.Workspace) *models.Agent {
	t.Helper()

	repo := db.NewAgentRepository(testDB)
	agent := &models.Agent{
		WorkspaceID: ws.ID,
		Type:        models.AgentTypeOpenCode,
		TmuxPane:    "swarm-test:0.1",
		State:       models.AgentStateIdle,
	}

	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	return agent
}

func newMessageItem(t *testing.T, text string) *models.QueueItem {
	t.Helper()

	payload, err := json.Marshal(models.MessagePayload{Text: text})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	return &models.QueueItem{
		Type:    models.QueueItemTypeMessage,
		Payload: payload,
	}
}

func TestService_EnqueuePeekDequeue(t *testing.T) {
	service, testDB, cleanup := setupTestService(t)
	defer cleanup()

	ws := createTestWorkspace(t, testDB)
	agent := createTestAgent(t, testDB, ws)
	ctx := context.Background()

	item := newMessageItem(t, "hello")
	if err := service.Enqueue(ctx, agent.ID, item); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	peeked, err := service.Peek(ctx, agent.ID)
	if err != nil {
		t.Fatalf("Peek failed: %v", err)
	}
	if peeked.ID == "" {
		t.Fatalf("expected peeked item ID to be set")
	}

	dequeued, err := service.Dequeue(ctx, agent.ID)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}
	if dequeued.Status != models.QueueItemStatusDispatched {
		t.Fatalf("expected dispatched status, got %q", dequeued.Status)
	}

	_, err = service.Peek(ctx, agent.ID)
	if err == nil {
		t.Fatal("expected empty queue error")
	}
	if !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("expected ErrQueueEmpty, got %v", err)
	}
}

func TestService_InsertReorderClearRemove(t *testing.T) {
	service, testDB, cleanup := setupTestService(t)
	defer cleanup()

	ws := createTestWorkspace(t, testDB)
	agent := createTestAgent(t, testDB, ws)
	ctx := context.Background()

	item1 := newMessageItem(t, "one")
	item2 := newMessageItem(t, "two")
	if err := service.Enqueue(ctx, agent.ID, item1, item2); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	inserted := newMessageItem(t, "insert")
	if err := service.InsertAt(ctx, agent.ID, 1, inserted); err != nil {
		t.Fatalf("InsertAt failed: %v", err)
	}

	items, err := service.List(ctx, agent.ID)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].ID != inserted.ID {
		t.Fatalf("expected inserted item first")
	}

	if err := service.Reorder(ctx, agent.ID, []string{item2.ID, inserted.ID, item1.ID}); err != nil {
		t.Fatalf("Reorder failed: %v", err)
	}

	removed, err := service.Clear(ctx, agent.ID)
	if err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	if removed != 3 {
		t.Fatalf("expected 3 items removed, got %d", removed)
	}

	item3 := newMessageItem(t, "three")
	if err := service.Enqueue(ctx, agent.ID, item3); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if err := service.Remove(ctx, item3.ID); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	if err := service.Remove(ctx, item3.ID); err == nil || !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("expected ErrQueueItemNotFound, got %v", err)
	}
}

func TestService_UpdateStatus(t *testing.T) {
	service, testDB, cleanup := setupTestService(t)
	defer cleanup()

	ws := createTestWorkspace(t, testDB)
	agent := createTestAgent(t, testDB, ws)
	ctx := context.Background()

	item := newMessageItem(t, "status test")
	if err := service.Enqueue(ctx, agent.ID, item); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Update status to failed with error message
	if err := service.UpdateStatus(ctx, item.ID, models.QueueItemStatusFailed, "dispatch error"); err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}

	// Verify status was updated
	items, err := service.List(ctx, agent.ID)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Status != models.QueueItemStatusFailed {
		t.Errorf("expected status=failed, got %s", items[0].Status)
	}
	if items[0].Error != "dispatch error" {
		t.Errorf("expected error='dispatch error', got %s", items[0].Error)
	}
}

func TestService_UpdateStatus_NotFound(t *testing.T) {
	service, _, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	err := service.UpdateStatus(ctx, "nonexistent-id", models.QueueItemStatusFailed, "error")
	if err == nil {
		t.Fatal("expected error for nonexistent item")
	}
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Errorf("expected ErrQueueItemNotFound, got %v", err)
	}
}

func TestService_UpdateAttempts(t *testing.T) {
	service, testDB, cleanup := setupTestService(t)
	defer cleanup()

	ws := createTestWorkspace(t, testDB)
	agent := createTestAgent(t, testDB, ws)
	ctx := context.Background()

	item := newMessageItem(t, "attempts test")
	if err := service.Enqueue(ctx, agent.ID, item); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Update attempts
	if err := service.UpdateAttempts(ctx, item.ID, 3); err != nil {
		t.Fatalf("UpdateAttempts failed: %v", err)
	}

	// Verify attempts was updated
	items, err := service.List(ctx, agent.ID)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Attempts != 3 {
		t.Errorf("expected attempts=3, got %d", items[0].Attempts)
	}
}

func TestService_UpdateAttempts_NotFound(t *testing.T) {
	service, _, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	err := service.UpdateAttempts(ctx, "nonexistent-id", 5)
	if err == nil {
		t.Fatal("expected error for nonexistent item")
	}
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Errorf("expected ErrQueueItemNotFound, got %v", err)
	}
}

func TestService_Dequeue_Empty(t *testing.T) {
	service, testDB, cleanup := setupTestService(t)
	defer cleanup()

	ws := createTestWorkspace(t, testDB)
	agent := createTestAgent(t, testDB, ws)
	ctx := context.Background()

	// Dequeue from empty queue
	_, err := service.Dequeue(ctx, agent.ID)
	if err == nil {
		t.Fatal("expected error for empty queue")
	}
	if !errors.Is(err, ErrQueueEmpty) {
		t.Errorf("expected ErrQueueEmpty, got %v", err)
	}
}

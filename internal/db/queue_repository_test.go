package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/opencode-ai/swarm/internal/models"
)

func createTestAgent(t *testing.T, db *DB, ws *models.Workspace) *models.Agent {
	t.Helper()

	repo := NewAgentRepository(db)
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
		ID:      uuid.New().String(),
		Type:    models.QueueItemTypeMessage,
		Payload: payload,
	}
}

func TestQueueRepository_EnqueuePeekDequeue(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ws := createTestWorkspace(t, db)
	agent := createTestAgent(t, db, ws)
	repo := NewQueueRepository(db)
	ctx := context.Background()

	item1 := newMessageItem(t, "first")
	item2 := newMessageItem(t, "second")

	if err := repo.Enqueue(ctx, agent.ID, item1, item2); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	count, err := repo.Count(ctx, agent.ID)
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 pending items, got %d", count)
	}

	peeked, err := repo.Peek(ctx, agent.ID)
	if err != nil {
		t.Fatalf("Peek failed: %v", err)
	}
	if peeked.Position != 1 {
		t.Fatalf("expected position 1, got %d", peeked.Position)
	}

	var payload models.MessagePayload
	if err := json.Unmarshal(peeked.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Text != "first" {
		t.Fatalf("expected first payload, got %q", payload.Text)
	}

	dequeued, err := repo.Dequeue(ctx, agent.ID)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}
	if dequeued.Status != models.QueueItemStatusDispatched {
		t.Fatalf("expected dispatched status, got %q", dequeued.Status)
	}
	if dequeued.DispatchedAt == nil {
		t.Fatalf("expected dispatched_at set")
	}

	count, err = repo.Count(ctx, agent.ID)
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 pending item, got %d", count)
	}
}

func TestQueueRepository_Reorder(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ws := createTestWorkspace(t, db)
	agent := createTestAgent(t, db, ws)
	repo := NewQueueRepository(db)
	ctx := context.Background()

	item1 := newMessageItem(t, "one")
	item2 := newMessageItem(t, "two")
	item3 := newMessageItem(t, "three")

	if err := repo.Enqueue(ctx, agent.ID, item1, item2, item3); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if err := repo.Reorder(ctx, agent.ID, []string{item2.ID, item3.ID, item1.ID}); err != nil {
		t.Fatalf("Reorder failed: %v", err)
	}

	items, err := repo.List(ctx, agent.ID)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].ID != item2.ID || items[1].ID != item3.ID || items[2].ID != item1.ID {
		t.Fatalf("unexpected order after reorder")
	}
}

func TestQueueRepository_InsertAt(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ws := createTestWorkspace(t, db)
	agent := createTestAgent(t, db, ws)
	repo := NewQueueRepository(db)
	ctx := context.Background()

	item1 := newMessageItem(t, "one")
	item2 := newMessageItem(t, "two")

	if err := repo.Enqueue(ctx, agent.ID, item1, item2); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	inserted := newMessageItem(t, "insert")
	if err := repo.InsertAt(ctx, agent.ID, 1, inserted); err != nil {
		t.Fatalf("InsertAt failed: %v", err)
	}

	items, err := repo.List(ctx, agent.ID)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].ID != inserted.ID {
		t.Fatalf("expected inserted item first")
	}
	if items[0].Position != 1 || items[1].Position != 2 || items[2].Position != 3 {
		t.Fatalf("unexpected positions after insert")
	}
}

func TestQueueRepository_ClearUpdateStatusAndRemove(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ws := createTestWorkspace(t, db)
	agent := createTestAgent(t, db, ws)
	repo := NewQueueRepository(db)
	ctx := context.Background()

	item := newMessageItem(t, "one")
	if err := repo.Enqueue(ctx, agent.ID, item); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if err := repo.UpdateStatus(ctx, item.ID, models.QueueItemStatusCompleted, ""); err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}

	updated, err := repo.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if updated.Status != models.QueueItemStatusCompleted {
		t.Fatalf("expected completed status, got %q", updated.Status)
	}
	if updated.CompletedAt == nil {
		t.Fatalf("expected completed_at set")
	}

	pending := newMessageItem(t, "two")
	if err := repo.Enqueue(ctx, agent.ID, pending); err != nil {
		t.Fatalf("Enqueue pending failed: %v", err)
	}

	removed, err := repo.Clear(ctx, agent.ID)
	if err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 pending item removed, got %d", removed)
	}

	if err := repo.Remove(ctx, item.ID); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	_, err = repo.Get(ctx, item.ID)
	if err == nil {
		t.Fatalf("expected error after removal")
	}
}

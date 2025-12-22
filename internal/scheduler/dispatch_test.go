package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/agent"
	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/queue"
	"github.com/opencode-ai/swarm/internal/tmux"
)

type dispatchStatusUpdate struct {
	itemID   string
	status   models.QueueItemStatus
	errorMsg string
}

type dispatchInsertCall struct {
	agentID  string
	position int
	itemID   string
}

type trackingQueueService struct {
	mu            sync.Mutex
	queues        map[string][]*models.QueueItem
	dequeueCalls  int
	insertCalls   []dispatchInsertCall
	statusUpdates []dispatchStatusUpdate
}

func newTrackingQueueService() *trackingQueueService {
	return &trackingQueueService{
		queues: make(map[string][]*models.QueueItem),
	}
}

func (m *trackingQueueService) Enqueue(ctx context.Context, agentID string, items ...*models.QueueItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queues[agentID] = append(m.queues[agentID], items...)
	return nil
}

func (m *trackingQueueService) Dequeue(ctx context.Context, agentID string) (*models.QueueItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dequeueCalls++
	items := m.queues[agentID]
	if len(items) == 0 {
		return nil, queue.ErrQueueEmpty
	}
	item := items[0]
	m.queues[agentID] = items[1:]
	return item, nil
}

func (m *trackingQueueService) Peek(ctx context.Context, agentID string) (*models.QueueItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.queues[agentID]
	if len(items) == 0 {
		return nil, queue.ErrQueueEmpty
	}
	return items[0], nil
}

func (m *trackingQueueService) List(ctx context.Context, agentID string) ([]*models.QueueItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*models.QueueItem(nil), m.queues[agentID]...), nil
}

func (m *trackingQueueService) Reorder(ctx context.Context, agentID string, ordering []string) error {
	return nil
}

func (m *trackingQueueService) Clear(ctx context.Context, agentID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := len(m.queues[agentID])
	m.queues[agentID] = nil
	return count, nil
}

func (m *trackingQueueService) InsertAt(ctx context.Context, agentID string, position int, item *models.QueueItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertCalls = append(m.insertCalls, dispatchInsertCall{
		agentID:  agentID,
		position: position,
		itemID:   item.ID,
	})
	items := m.queues[agentID]
	if position >= len(items) {
		m.queues[agentID] = append(items, item)
	} else {
		m.queues[agentID] = append(items[:position], append([]*models.QueueItem{item}, items[position:]...)...)
	}
	return nil
}

func (m *trackingQueueService) Remove(ctx context.Context, itemID string) error {
	return nil
}

func (m *trackingQueueService) UpdateStatus(ctx context.Context, itemID string, status models.QueueItemStatus, errorMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusUpdates = append(m.statusUpdates, dispatchStatusUpdate{
		itemID:   itemID,
		status:   status,
		errorMsg: errorMsg,
	})
	for _, items := range m.queues {
		for _, item := range items {
			if item != nil && item.ID == itemID {
				item.Status = status
				item.Error = errorMsg
				return nil
			}
		}
	}
	return nil
}

func (m *trackingQueueService) UpdateAttempts(ctx context.Context, itemID string, attempts int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, items := range m.queues {
		for _, item := range items {
			if item != nil && item.ID == itemID {
				item.Attempts = attempts
				return nil
			}
		}
	}
	return nil
}

func (m *trackingQueueService) queueLength(agentID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queues[agentID])
}

type dispatchExecutor struct {
	mu       sync.Mutex
	commands []string
}

func (d *dispatchExecutor) Exec(ctx context.Context, cmd string) ([]byte, []byte, error) {
	d.mu.Lock()
	d.commands = append(d.commands, cmd)
	d.mu.Unlock()

	switch {
	case strings.Contains(cmd, "has-session"):
		return nil, nil, nil
	case strings.Contains(cmd, "list-panes"):
		return []byte("%1|0|0|/tmp|1|bash\n"), nil, nil
	default:
		return nil, nil, nil
	}
}

func (d *dispatchExecutor) Commands() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.commands...)
}

func makeMessageItem(id, text string) *models.QueueItem {
	payload, _ := json.Marshal(models.MessagePayload{Text: text})
	return &models.QueueItem{
		ID:        id,
		Type:      models.QueueItemTypeMessage,
		Status:    models.QueueItemStatusPending,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
}

func makePauseItem(id string, durationSec int, reason string) *models.QueueItem {
	payload, _ := json.Marshal(models.PausePayload{
		DurationSeconds: durationSec,
		Reason:          reason,
	})
	return &models.QueueItem{
		ID:        id,
		Type:      models.QueueItemTypePause,
		Status:    models.QueueItemStatusPending,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
}

func makeConditionalItem(id string, payload models.ConditionalPayload) *models.QueueItem {
	data, _ := json.Marshal(payload)
	return &models.QueueItem{
		ID:        id,
		Type:      models.QueueItemTypeConditional,
		Status:    models.QueueItemStatusPending,
		Payload:   data,
		CreatedAt: time.Now(),
	}
}

func setupAgentServiceForDispatch(t *testing.T, state models.AgentState, queueLength int, tmuxClient *tmux.Client) (*agent.Service, string, func()) {
	t.Helper()

	ctx := context.Background()
	database, err := db.OpenInMemory()
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}

	if err := database.Migrate(ctx); err != nil {
		_ = database.Close()
		t.Fatalf("failed to migrate db: %v", err)
	}

	nodeRepo := db.NewNodeRepository(database)
	workspaceRepo := db.NewWorkspaceRepository(database)
	agentRepo := db.NewAgentRepository(database)

	node := &models.Node{
		Name:       "local",
		IsLocal:    true,
		Status:     models.NodeStatusOnline,
		SSHBackend: models.SSHBackendAuto,
	}
	if err := nodeRepo.Create(ctx, node); err != nil {
		_ = database.Close()
		t.Fatalf("failed to create node: %v", err)
	}

	workspace := &models.Workspace{
		NodeID:      node.ID,
		RepoPath:    "/tmp/repo",
		TmuxSession: "session",
	}
	if err := workspaceRepo.Create(ctx, workspace); err != nil {
		_ = database.Close()
		t.Fatalf("failed to create workspace: %v", err)
	}

	now := time.Now().UTC()
	agentModel := &models.Agent{
		WorkspaceID: workspace.ID,
		Type:        models.AgentTypeOpenCode,
		TmuxPane:    "session:0.0",
		State:       state,
		QueueLength: queueLength,
		StateInfo: models.StateInfo{
			State:      state,
			Confidence: models.StateConfidenceHigh,
			Reason:     "test setup",
			DetectedAt: now,
		},
	}
	if err := agentRepo.Create(ctx, agentModel); err != nil {
		_ = database.Close()
		t.Fatalf("failed to create agent: %v", err)
	}

	cleanup := func() {
		_ = database.Close()
	}

	return agent.NewService(agentRepo, nil, nil, nil, tmuxClient), agentModel.ID, cleanup
}

func TestScheduler_DispatchToAgent_MessageItemSends(t *testing.T) {
	exec := &dispatchExecutor{}
	tmuxClient := tmux.NewClient(exec)

	agentSvc, agentID, cleanup := setupAgentServiceForDispatch(t, models.AgentStateIdle, 1, tmuxClient)
	defer cleanup()

	queueSvc := newTrackingQueueService()
	if err := queueSvc.Enqueue(context.Background(), agentID, makeMessageItem("item-1", "hello")); err != nil {
		t.Fatalf("failed to enqueue item: %v", err)
	}

	sched := New(DefaultConfig(), agentSvc, queueSvc, nil, nil)
	sched.ctx = context.Background()

	sched.dispatchToAgent(agentID)

	if got := queueSvc.queueLength(agentID); got != 0 {
		t.Fatalf("expected queue to be empty, got %d", got)
	}

	commands := exec.Commands()
	found := false
	for _, cmd := range commands {
		if strings.Contains(cmd, "send-keys") && strings.Contains(cmd, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected send-keys command with message, got %v", commands)
	}
}

func TestScheduler_DispatchToAgent_PauseItemPausesAgent(t *testing.T) {
	agentSvc, agentID, cleanup := setupAgentServiceForDispatch(t, models.AgentStateIdle, 1, nil)
	defer cleanup()

	queueSvc := newTrackingQueueService()
	if err := queueSvc.Enqueue(context.Background(), agentID, makePauseItem("pause-1", 30, "cooldown")); err != nil {
		t.Fatalf("failed to enqueue pause item: %v", err)
	}

	sched := New(DefaultConfig(), agentSvc, queueSvc, nil, nil)
	sched.ctx = context.Background()

	sched.dispatchToAgent(agentID)

	agentModel, err := agentSvc.GetAgent(context.Background(), agentID)
	if err != nil {
		t.Fatalf("failed to fetch agent: %v", err)
	}
	if agentModel.State != models.AgentStatePaused {
		t.Fatalf("expected agent state paused, got %s", agentModel.State)
	}
	if agentModel.PausedUntil == nil {
		t.Fatal("expected PausedUntil to be set")
	}
	if !sched.IsAgentPaused(agentID) {
		t.Fatal("expected scheduler to mark agent as paused")
	}
}

func TestScheduler_DispatchConditional_RequeuesWhenNotMet(t *testing.T) {
	agentSvc, agentID, cleanup := setupAgentServiceForDispatch(t, models.AgentStateIdle, 0, nil)
	defer cleanup()

	queueSvc := newTrackingQueueService()
	payload := models.ConditionalPayload{
		ConditionType: models.ConditionTypeCustomExpression,
		Expression:    "queue_length > 0",
		Message:       "hello",
	}
	if err := queueSvc.Enqueue(context.Background(), agentID, makeConditionalItem("cond-1", payload)); err != nil {
		t.Fatalf("failed to enqueue conditional item: %v", err)
	}

	sched := New(DefaultConfig(), agentSvc, queueSvc, nil, nil)
	sched.ctx = context.Background()

	sched.dispatchToAgent(agentID)

	if got := queueSvc.queueLength(agentID); got != 1 {
		t.Fatalf("expected conditional item to be re-queued, got %d", got)
	}

	items, err := queueSvc.List(context.Background(), agentID)
	if err != nil {
		t.Fatalf("failed to list queue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 queue item, got %d", len(items))
	}
	if items[0].EvaluationCount != 1 {
		t.Fatalf("expected evaluation count 1, got %d", items[0].EvaluationCount)
	}
}

func TestScheduler_DispatchConditional_SkipsAfterMaxEvaluations(t *testing.T) {
	agentSvc, agentID, cleanup := setupAgentServiceForDispatch(t, models.AgentStateIdle, 0, nil)
	defer cleanup()

	queueSvc := newTrackingQueueService()
	payload := models.ConditionalPayload{
		ConditionType: models.ConditionTypeWhenIdle,
		Message:       "hello",
	}
	item := makeConditionalItem("cond-max", payload)
	item.EvaluationCount = MaxConditionalEvaluations

	if err := queueSvc.Enqueue(context.Background(), agentID, item); err != nil {
		t.Fatalf("failed to enqueue conditional item: %v", err)
	}

	sched := New(DefaultConfig(), agentSvc, queueSvc, nil, nil)
	sched.ctx = context.Background()

	sched.dispatchToAgent(agentID)

	if got := queueSvc.queueLength(agentID); got != 0 {
		t.Fatalf("expected queue to be empty, got %d", got)
	}
	if len(queueSvc.statusUpdates) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(queueSvc.statusUpdates))
	}
	update := queueSvc.statusUpdates[0]
	if update.itemID != item.ID {
		t.Fatalf("expected status update for %s, got %s", item.ID, update.itemID)
	}
	if update.status != models.QueueItemStatusSkipped {
		t.Fatalf("expected status skipped, got %s", update.status)
	}
	if update.errorMsg != "max evaluations exceeded" {
		t.Fatalf("expected max evaluations error, got %q", update.errorMsg)
	}
}

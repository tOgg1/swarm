package scheduler

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/queue"
)

// mockAgentService implements a minimal agent service for testing.
type mockAgentService struct {
	mu            sync.Mutex
	agents        map[string]*models.Agent
	messages      map[string][]string
	pausedAgents  map[string]time.Duration
	resumedAgents map[string]bool
	sendError     error
	pauseError    error
	listError     error
}

func newMockAgentService() *mockAgentService {
	return &mockAgentService{
		agents:        make(map[string]*models.Agent),
		messages:      make(map[string][]string),
		pausedAgents:  make(map[string]time.Duration),
		resumedAgents: make(map[string]bool),
	}
}

func (m *mockAgentService) addAgent(a *models.Agent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[a.ID] = a
}

func (m *mockAgentService) getMessages(agentID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.messages[agentID]
}

// mockQueueService implements queue.QueueService for testing.
type mockQueueService struct {
	mu     sync.Mutex
	queues map[string][]*models.QueueItem
}

func newMockQueueService() *mockQueueService {
	return &mockQueueService{
		queues: make(map[string][]*models.QueueItem),
	}
}

func (m *mockQueueService) Enqueue(ctx context.Context, agentID string, items ...*models.QueueItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queues[agentID] = append(m.queues[agentID], items...)
	return nil
}

func (m *mockQueueService) Dequeue(ctx context.Context, agentID string) (*models.QueueItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.queues[agentID]
	if len(items) == 0 {
		return nil, queue.ErrQueueEmpty
	}
	item := items[0]
	m.queues[agentID] = items[1:]
	return item, nil
}

func (m *mockQueueService) Peek(ctx context.Context, agentID string) (*models.QueueItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.queues[agentID]
	if len(items) == 0 {
		return nil, queue.ErrQueueEmpty
	}
	return items[0], nil
}

func (m *mockQueueService) List(ctx context.Context, agentID string) ([]*models.QueueItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queues[agentID], nil
}

func (m *mockQueueService) Reorder(ctx context.Context, agentID string, ordering []string) error {
	return nil
}

func (m *mockQueueService) Clear(ctx context.Context, agentID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := len(m.queues[agentID])
	m.queues[agentID] = nil
	return count, nil
}

func (m *mockQueueService) InsertAt(ctx context.Context, agentID string, position int, item *models.QueueItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.queues[agentID]
	if position >= len(items) {
		m.queues[agentID] = append(items, item)
	} else {
		// Insert at position
		m.queues[agentID] = append(items[:position], append([]*models.QueueItem{item}, items[position:]...)...)
	}
	return nil
}

func (m *mockQueueService) Remove(ctx context.Context, itemID string) error {
	return nil
}

func (m *mockQueueService) queueLength(agentID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queues[agentID])
}

// Helper to create a message queue item
func createMessageItem(id, text string) *models.QueueItem {
	payload, _ := json.Marshal(models.MessagePayload{Text: text})
	return &models.QueueItem{
		ID:        id,
		Type:      models.QueueItemTypeMessage,
		Status:    models.QueueItemStatusPending,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
}

// Helper to create a pause queue item
func createPauseItem(id string, durationSec int, reason string) *models.QueueItem {
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

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.TickInterval != 1*time.Second {
		t.Errorf("expected TickInterval 1s, got %v", cfg.TickInterval)
	}
	if cfg.DispatchTimeout != 30*time.Second {
		t.Errorf("expected DispatchTimeout 30s, got %v", cfg.DispatchTimeout)
	}
	if cfg.MaxConcurrentDispatches != 10 {
		t.Errorf("expected MaxConcurrentDispatches 10, got %d", cfg.MaxConcurrentDispatches)
	}
	if !cfg.IdleStateRequired {
		t.Error("expected IdleStateRequired true")
	}
	if !cfg.AutoResumeEnabled {
		t.Error("expected AutoResumeEnabled true")
	}
}

func TestNew(t *testing.T) {
	cfg := DefaultConfig()
	queueSvc := newMockQueueService()

	// Test with nil services (scheduler creation should still work)
	sched := New(cfg, nil, queueSvc, nil)

	if sched == nil {
		t.Fatal("expected scheduler to be created")
	}
	if sched.config.TickInterval != cfg.TickInterval {
		t.Error("config not applied correctly")
	}
}

func TestNew_DefaultsApplied(t *testing.T) {
	// Test with zero config values
	cfg := Config{
		TickInterval:            0,
		DispatchTimeout:         0,
		MaxConcurrentDispatches: 0,
	}

	sched := New(cfg, nil, nil, nil)

	if sched.config.TickInterval != DefaultConfig().TickInterval {
		t.Errorf("expected default TickInterval, got %v", sched.config.TickInterval)
	}
	if sched.config.DispatchTimeout != DefaultConfig().DispatchTimeout {
		t.Errorf("expected default DispatchTimeout, got %v", sched.config.DispatchTimeout)
	}
	if sched.config.MaxConcurrentDispatches != DefaultConfig().MaxConcurrentDispatches {
		t.Errorf("expected default MaxConcurrentDispatches, got %d", sched.config.MaxConcurrentDispatches)
	}
}

func TestScheduler_StartStop(t *testing.T) {
	cfg := Config{
		TickInterval:            10 * time.Millisecond,
		DispatchTimeout:         100 * time.Millisecond,
		MaxConcurrentDispatches: 5,
	}

	sched := New(cfg, nil, nil, nil)
	ctx := context.Background()

	// Start scheduler
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}

	stats := sched.Stats()
	if !stats.Running {
		t.Error("expected scheduler to be running")
	}
	if stats.Paused {
		t.Error("expected scheduler not to be paused")
	}
	if stats.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}

	// Double start should fail
	if err := sched.Start(ctx); err != ErrSchedulerAlreadyRunning {
		t.Errorf("expected ErrSchedulerAlreadyRunning, got %v", err)
	}

	// Stop scheduler
	if err := sched.Stop(); err != nil {
		t.Fatalf("failed to stop scheduler: %v", err)
	}

	stats = sched.Stats()
	if stats.Running {
		t.Error("expected scheduler to be stopped")
	}

	// Double stop should fail
	if err := sched.Stop(); err != ErrSchedulerNotRunning {
		t.Errorf("expected ErrSchedulerNotRunning, got %v", err)
	}
}

func TestScheduler_PauseResume(t *testing.T) {
	cfg := Config{
		TickInterval:            10 * time.Millisecond,
		DispatchTimeout:         100 * time.Millisecond,
		MaxConcurrentDispatches: 5,
	}

	sched := New(cfg, nil, nil, nil)
	ctx := context.Background()

	// Pause before start should fail
	if err := sched.Pause(); err != ErrSchedulerNotRunning {
		t.Errorf("expected ErrSchedulerNotRunning, got %v", err)
	}

	// Resume before start should fail
	if err := sched.Resume(); err != ErrSchedulerNotRunning {
		t.Errorf("expected ErrSchedulerNotRunning, got %v", err)
	}

	// Start scheduler
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Pause scheduler
	if err := sched.Pause(); err != nil {
		t.Fatalf("failed to pause scheduler: %v", err)
	}

	stats := sched.Stats()
	if !stats.Paused {
		t.Error("expected scheduler to be paused")
	}
	if !stats.Running {
		t.Error("expected scheduler to still be running (but paused)")
	}

	// Double pause should be idempotent
	if err := sched.Pause(); err != nil {
		t.Errorf("expected pause to be idempotent, got %v", err)
	}

	// Resume scheduler
	if err := sched.Resume(); err != nil {
		t.Fatalf("failed to resume scheduler: %v", err)
	}

	stats = sched.Stats()
	if stats.Paused {
		t.Error("expected scheduler not to be paused")
	}

	// Double resume should be idempotent
	if err := sched.Resume(); err != nil {
		t.Errorf("expected resume to be idempotent, got %v", err)
	}
}

func TestScheduler_ScheduleNow_NotRunning(t *testing.T) {
	sched := New(DefaultConfig(), nil, nil, nil)

	err := sched.ScheduleNow("agent-1")
	if err != ErrSchedulerNotRunning {
		t.Errorf("expected ErrSchedulerNotRunning, got %v", err)
	}
}

func TestScheduler_ScheduleNow_Paused(t *testing.T) {
	cfg := Config{
		TickInterval:            100 * time.Millisecond,
		DispatchTimeout:         100 * time.Millisecond,
		MaxConcurrentDispatches: 5,
	}
	sched := New(cfg, nil, nil, nil)
	ctx := context.Background()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer sched.Stop()

	if err := sched.Pause(); err != nil {
		t.Fatalf("failed to pause: %v", err)
	}

	err := sched.ScheduleNow("agent-1")
	if err != ErrSchedulerNotRunning {
		t.Errorf("expected ErrSchedulerNotRunning when paused, got %v", err)
	}
}

func TestScheduler_AgentPauseResume(t *testing.T) {
	sched := New(DefaultConfig(), nil, nil, nil)

	// Pause agent
	if err := sched.PauseAgent("agent-1"); err != nil {
		t.Fatalf("failed to pause agent: %v", err)
	}

	if !sched.IsAgentPaused("agent-1") {
		t.Error("expected agent to be paused")
	}

	stats := sched.Stats()
	if stats.PausedAgents != 1 {
		t.Errorf("expected 1 paused agent, got %d", stats.PausedAgents)
	}

	// Pause another agent
	if err := sched.PauseAgent("agent-2"); err != nil {
		t.Fatalf("failed to pause agent-2: %v", err)
	}

	stats = sched.Stats()
	if stats.PausedAgents != 2 {
		t.Errorf("expected 2 paused agents, got %d", stats.PausedAgents)
	}

	// Resume agent
	if err := sched.ResumeAgent("agent-1"); err != nil {
		t.Fatalf("failed to resume agent: %v", err)
	}

	if sched.IsAgentPaused("agent-1") {
		t.Error("expected agent-1 not to be paused")
	}
	if !sched.IsAgentPaused("agent-2") {
		t.Error("expected agent-2 to still be paused")
	}

	stats = sched.Stats()
	if stats.PausedAgents != 1 {
		t.Errorf("expected 1 paused agent, got %d", stats.PausedAgents)
	}
}

func TestScheduler_IsEligibleForDispatch(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IdleStateRequired = true
	sched := New(cfg, nil, nil, nil)

	tests := []struct {
		name     string
		agent    *models.Agent
		paused   bool
		eligible bool
	}{
		{
			name: "idle agent with queue",
			agent: &models.Agent{
				ID:          "agent-1",
				State:       models.AgentStateIdle,
				QueueLength: 5,
			},
			eligible: true,
		},
		{
			name: "idle agent empty queue",
			agent: &models.Agent{
				ID:          "agent-2",
				State:       models.AgentStateIdle,
				QueueLength: 0,
			},
			eligible: false,
		},
		{
			name: "working agent",
			agent: &models.Agent{
				ID:          "agent-3",
				State:       models.AgentStateWorking,
				QueueLength: 5,
			},
			eligible: false,
		},
		{
			name: "paused agent state",
			agent: &models.Agent{
				ID:          "agent-4",
				State:       models.AgentStatePaused,
				QueueLength: 5,
			},
			eligible: false,
		},
		{
			name: "stopped agent",
			agent: &models.Agent{
				ID:          "agent-5",
				State:       models.AgentStateStopped,
				QueueLength: 5,
			},
			eligible: false,
		},
		{
			name: "idle agent paused in scheduler",
			agent: &models.Agent{
				ID:          "agent-6",
				State:       models.AgentStateIdle,
				QueueLength: 5,
			},
			paused:   true,
			eligible: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.paused {
				sched.PauseAgent(tt.agent.ID)
				defer sched.ResumeAgent(tt.agent.ID)
			}

			got := sched.isEligibleForDispatch(tt.agent)
			if got != tt.eligible {
				t.Errorf("isEligibleForDispatch() = %v, want %v", got, tt.eligible)
			}
		})
	}
}

func TestScheduler_IsEligibleForDispatch_IdleNotRequired(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IdleStateRequired = false
	sched := New(cfg, nil, nil, nil)

	// Working agent with queue should be eligible when idle not required
	agent := &models.Agent{
		ID:          "agent-1",
		State:       models.AgentStateWorking,
		QueueLength: 5,
	}

	if !sched.isEligibleForDispatch(agent) {
		t.Error("expected working agent to be eligible when IdleStateRequired is false")
	}
}

func TestScheduler_Stats(t *testing.T) {
	sched := New(DefaultConfig(), nil, nil, nil)

	stats := sched.Stats()
	if stats.Running {
		t.Error("expected not running initially")
	}
	if stats.Paused {
		t.Error("expected not paused initially")
	}
	if stats.TotalDispatches != 0 {
		t.Error("expected 0 dispatches initially")
	}
	if stats.SuccessfulDispatches != 0 {
		t.Error("expected 0 successful dispatches initially")
	}
	if stats.FailedDispatches != 0 {
		t.Error("expected 0 failed dispatches initially")
	}
}

func TestScheduler_DispatchEvents(t *testing.T) {
	sched := New(DefaultConfig(), nil, nil, nil)

	ch := sched.DispatchEvents()
	if ch == nil {
		t.Error("expected dispatch events channel")
	}
}

func TestScheduler_RecordDispatch(t *testing.T) {
	sched := New(DefaultConfig(), nil, nil, nil)

	// Record a successful dispatch
	event := DispatchEvent{
		AgentID:   "agent-1",
		ItemID:    "item-1",
		ItemType:  models.QueueItemTypeMessage,
		Success:   true,
		Timestamp: time.Now(),
		Duration:  100 * time.Millisecond,
	}
	sched.recordDispatch(event)

	stats := sched.Stats()
	if stats.TotalDispatches != 1 {
		t.Errorf("expected 1 total dispatch, got %d", stats.TotalDispatches)
	}
	if stats.SuccessfulDispatches != 1 {
		t.Errorf("expected 1 successful dispatch, got %d", stats.SuccessfulDispatches)
	}
	if stats.FailedDispatches != 0 {
		t.Errorf("expected 0 failed dispatches, got %d", stats.FailedDispatches)
	}
	if stats.LastDispatchAt == nil {
		t.Error("expected LastDispatchAt to be set")
	}

	// Record a failed dispatch
	event.Success = false
	event.Error = "test error"
	sched.recordDispatch(event)

	stats = sched.Stats()
	if stats.TotalDispatches != 2 {
		t.Errorf("expected 2 total dispatches, got %d", stats.TotalDispatches)
	}
	if stats.FailedDispatches != 1 {
		t.Errorf("expected 1 failed dispatch, got %d", stats.FailedDispatches)
	}
}

func TestScheduler_ScheduleNow_Running(t *testing.T) {
	cfg := Config{
		TickInterval:            100 * time.Millisecond,
		DispatchTimeout:         100 * time.Millisecond,
		MaxConcurrentDispatches: 5,
	}
	sched := New(cfg, nil, nil, nil)
	ctx := context.Background()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer sched.Stop()

	// Should succeed even with nil agent service (dispatch will just fail gracefully)
	if err := sched.ScheduleNow("agent-1"); err != nil {
		t.Errorf("expected ScheduleNow to succeed, got %v", err)
	}
}

func TestDispatchEvent_Fields(t *testing.T) {
	now := time.Now()
	event := DispatchEvent{
		AgentID:   "agent-1",
		ItemID:    "item-1",
		ItemType:  models.QueueItemTypeMessage,
		Success:   true,
		Error:     "",
		Timestamp: now,
		Duration:  500 * time.Millisecond,
	}

	if event.AgentID != "agent-1" {
		t.Error("AgentID mismatch")
	}
	if event.ItemID != "item-1" {
		t.Error("ItemID mismatch")
	}
	if event.ItemType != models.QueueItemTypeMessage {
		t.Error("ItemType mismatch")
	}
	if !event.Success {
		t.Error("Success mismatch")
	}
	if event.Error != "" {
		t.Error("Error should be empty")
	}
	if event.Timestamp != now {
		t.Error("Timestamp mismatch")
	}
	if event.Duration != 500*time.Millisecond {
		t.Error("Duration mismatch")
	}
}

func TestSchedulerStats_Fields(t *testing.T) {
	now := time.Now()
	stats := SchedulerStats{
		Running:              true,
		Paused:               false,
		StartedAt:            &now,
		TotalDispatches:      100,
		SuccessfulDispatches: 95,
		FailedDispatches:     5,
		LastDispatchAt:       &now,
		PausedAgents:         2,
	}

	if !stats.Running {
		t.Error("Running mismatch")
	}
	if stats.Paused {
		t.Error("Paused mismatch")
	}
	if stats.StartedAt == nil || *stats.StartedAt != now {
		t.Error("StartedAt mismatch")
	}
	if stats.TotalDispatches != 100 {
		t.Error("TotalDispatches mismatch")
	}
	if stats.SuccessfulDispatches != 95 {
		t.Error("SuccessfulDispatches mismatch")
	}
	if stats.FailedDispatches != 5 {
		t.Error("FailedDispatches mismatch")
	}
	if stats.PausedAgents != 2 {
		t.Error("PausedAgents mismatch")
	}
}

func TestConfig_Fields(t *testing.T) {
	cfg := Config{
		TickInterval:            2 * time.Second,
		DispatchTimeout:         1 * time.Minute,
		MaxConcurrentDispatches: 20,
		IdleStateRequired:       false,
		AutoResumeEnabled:       false,
	}

	if cfg.TickInterval != 2*time.Second {
		t.Error("TickInterval mismatch")
	}
	if cfg.DispatchTimeout != 1*time.Minute {
		t.Error("DispatchTimeout mismatch")
	}
	if cfg.MaxConcurrentDispatches != 20 {
		t.Error("MaxConcurrentDispatches mismatch")
	}
	if cfg.IdleStateRequired {
		t.Error("IdleStateRequired mismatch")
	}
	if cfg.AutoResumeEnabled {
		t.Error("AutoResumeEnabled mismatch")
	}
}

func TestScheduler_EvaluateCondition_WhenIdle(t *testing.T) {
	// This is a unit test for evaluateCondition
	// Without agent service, this will panic (nil pointer), which is expected
	// In production, the scheduler guards against this in the dispatch flow
	sched := New(DefaultConfig(), nil, nil, nil)

	payload := models.ConditionalPayload{
		ConditionType: models.ConditionTypeWhenIdle,
		Message:       "test",
	}

	// Test should recover from panic when agent service is nil
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic without agent service")
		}
	}()

	_, _ = sched.evaluateCondition(context.Background(), "agent-1", payload)
}

func TestScheduler_EvaluateCondition_AfterPrevious(t *testing.T) {
	sched := New(DefaultConfig(), nil, nil, nil)

	payload := models.ConditionalPayload{
		ConditionType: models.ConditionTypeAfterPrevious,
		Message:       "test",
	}

	// AfterPrevious should always return true
	met, err := sched.evaluateCondition(context.Background(), "agent-1", payload)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !met {
		t.Error("expected condition to be met")
	}
}

func TestScheduler_EvaluateCondition_Custom(t *testing.T) {
	sched := New(DefaultConfig(), nil, nil, nil)

	payload := models.ConditionalPayload{
		ConditionType: models.ConditionTypeCustomExpression,
		Expression:    "some expression",
		Message:       "test",
	}

	// Custom expressions are not implemented
	_, err := sched.evaluateCondition(context.Background(), "agent-1", payload)
	if err == nil {
		t.Error("expected error for custom expression")
	}
}

func TestScheduler_EvaluateCondition_UnknownType(t *testing.T) {
	sched := New(DefaultConfig(), nil, nil, nil)

	payload := models.ConditionalPayload{
		ConditionType: "unknown",
		Message:       "test",
	}

	_, err := sched.evaluateCondition(context.Background(), "agent-1", payload)
	if err == nil {
		t.Error("expected error for unknown condition type")
	}
}

package scheduler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

func TestTick_DispatchOnlyWhenIdle(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name         string
		agentState   models.AgentState
		wantDispatch bool
		wantBlocked  BlockReason
	}{
		{
			name:         "idle agent receives dispatch",
			agentState:   models.AgentStateIdle,
			wantDispatch: true,
			wantBlocked:  BlockReasonNone,
		},
		{
			name:         "working agent blocked",
			agentState:   models.AgentStateWorking,
			wantDispatch: false,
			wantBlocked:  BlockReasonNotIdle,
		},
		{
			name:         "paused agent blocked",
			agentState:   models.AgentStatePaused,
			wantDispatch: false,
			wantBlocked:  BlockReasonPaused,
		},
		{
			name:         "stopped agent blocked",
			agentState:   models.AgentStateStopped,
			wantDispatch: false,
			wantBlocked:  BlockReasonStopped,
		},
		{
			name:         "awaiting approval blocked",
			agentState:   models.AgentStateAwaitingApproval,
			wantDispatch: false,
			wantBlocked:  BlockReasonAwaitingApproval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := TickInput{
				Agents: []AgentSnapshot{{
					ID:          "agent-1",
					State:       tt.agentState,
					QueueLength: 1,
				}},
				QueueItems: []QueueItemSnapshot{{
					ID:      "item-1",
					AgentID: "agent-1",
					Type:    models.QueueItemTypeMessage,
					Status:  models.QueueItemStatusPending,
					Payload: mustMarshal(models.MessagePayload{Text: "hello"}),
				}},
				Now:    now,
				Config: DefaultTickConfig(),
			}

			result := Tick(input)

			hasDispatch := false
			for _, action := range result.Actions {
				if action.Type == ActionTypeDispatch && action.AgentID == "agent-1" {
					hasDispatch = true
					break
				}
			}

			if hasDispatch != tt.wantDispatch {
				t.Errorf("dispatch = %v, want %v", hasDispatch, tt.wantDispatch)
			}

			if tt.wantBlocked != BlockReasonNone {
				if blocked, ok := result.Blocked["agent-1"]; !ok || blocked != tt.wantBlocked {
					t.Errorf("blocked = %v, want %v", blocked, tt.wantBlocked)
				}
			}
		})
	}
}

func TestTick_AccountCooldown(t *testing.T) {
	now := time.Now().UTC()
	futureTime := now.Add(5 * time.Minute)
	pastTime := now.Add(-5 * time.Minute)

	tests := []struct {
		name          string
		cooldownUntil *time.Time
		wantDispatch  bool
		wantBlocked   BlockReason
	}{
		{
			name:          "no cooldown - dispatch allowed",
			cooldownUntil: nil,
			wantDispatch:  true,
			wantBlocked:   BlockReasonNone,
		},
		{
			name:          "active cooldown - blocked",
			cooldownUntil: &futureTime,
			wantDispatch:  false,
			wantBlocked:   BlockReasonAccountCooldown,
		},
		{
			name:          "expired cooldown - dispatch allowed",
			cooldownUntil: &pastTime,
			wantDispatch:  true,
			wantBlocked:   BlockReasonNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := TickInput{
				Agents: []AgentSnapshot{{
					ID:          "agent-1",
					State:       models.AgentStateIdle,
					AccountID:   "account-1",
					QueueLength: 1,
				}},
				QueueItems: []QueueItemSnapshot{{
					ID:      "item-1",
					AgentID: "agent-1",
					Type:    models.QueueItemTypeMessage,
					Status:  models.QueueItemStatusPending,
					Payload: mustMarshal(models.MessagePayload{Text: "hello"}),
				}},
				Accounts: []AccountSnapshot{{
					ID:            "account-1",
					IsActive:      true,
					CooldownUntil: tt.cooldownUntil,
				}},
				Now:    now,
				Config: DefaultTickConfig(),
			}

			result := Tick(input)

			hasDispatch := false
			for _, action := range result.Actions {
				if action.Type == ActionTypeDispatch {
					hasDispatch = true
					break
				}
			}

			if hasDispatch != tt.wantDispatch {
				t.Errorf("dispatch = %v, want %v", hasDispatch, tt.wantDispatch)
			}

			if tt.wantBlocked != BlockReasonNone {
				if blocked, ok := result.Blocked["agent-1"]; !ok || blocked != tt.wantBlocked {
					t.Errorf("blocked = %v, want %v", blocked, tt.wantBlocked)
				}
			}
		})
	}
}

func TestTick_ConditionalItems(t *testing.T) {
	now := time.Now().UTC()
	recentActivity := now.Add(-10 * time.Second)
	oldActivity := now.Add(-2 * time.Minute)

	tests := []struct {
		name          string
		conditionType models.ConditionType
		expression    string
		agentState    models.AgentState
		lastActivity  *time.Time
		wantDispatch  bool
		wantRequeue   bool
		wantSkip      bool
	}{
		{
			name:          "when_idle with idle agent - dispatch",
			conditionType: models.ConditionTypeWhenIdle,
			agentState:    models.AgentStateIdle,
			wantDispatch:  true,
		},
		{
			name:          "after_cooldown with recent activity - requeue",
			conditionType: models.ConditionTypeAfterCooldown,
			expression:    "60s",
			agentState:    models.AgentStateIdle,
			lastActivity:  &recentActivity,
			wantRequeue:   true,
		},
		{
			name:          "after_cooldown with old activity - dispatch",
			conditionType: models.ConditionTypeAfterCooldown,
			expression:    "30s",
			agentState:    models.AgentStateIdle,
			lastActivity:  &oldActivity,
			wantDispatch:  true,
		},
		{
			name:          "custom expression queue_length == 0 - requeue",
			conditionType: models.ConditionTypeCustomExpression,
			expression:    "queue_length == 0",
			agentState:    models.AgentStateIdle,
			wantRequeue:   true, // Queue length is 1 (the item itself)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := TickInput{
				Agents: []AgentSnapshot{{
					ID:           "agent-1",
					State:        tt.agentState,
					QueueLength:  1,
					LastActivity: tt.lastActivity,
				}},
				QueueItems: []QueueItemSnapshot{{
					ID:      "item-1",
					AgentID: "agent-1",
					Type:    models.QueueItemTypeConditional,
					Status:  models.QueueItemStatusPending,
					Payload: mustMarshal(models.ConditionalPayload{
						ConditionType: tt.conditionType,
						Expression:    tt.expression,
						Message:       "conditional message",
					}),
				}},
				Now:    now,
				Config: DefaultTickConfig(),
			}

			result := Tick(input)

			var hasDispatch, hasRequeue, hasSkip bool
			for _, action := range result.Actions {
				switch action.Type {
				case ActionTypeDispatch:
					hasDispatch = true
				case ActionTypeRequeue:
					hasRequeue = true
				case ActionTypeSkip:
					hasSkip = true
				}
			}

			if hasDispatch != tt.wantDispatch {
				t.Errorf("dispatch = %v, want %v", hasDispatch, tt.wantDispatch)
			}
			if hasRequeue != tt.wantRequeue {
				t.Errorf("requeue = %v, want %v", hasRequeue, tt.wantRequeue)
			}
			if hasSkip != tt.wantSkip {
				t.Errorf("skip = %v, want %v", hasSkip, tt.wantSkip)
			}
		})
	}
}

func TestTick_MaxConditionalEvaluations(t *testing.T) {
	now := time.Now().UTC()

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:          "agent-1",
			State:       models.AgentStateIdle,
			QueueLength: 1,
		}},
		QueueItems: []QueueItemSnapshot{{
			ID:              "item-1",
			AgentID:         "agent-1",
			Type:            models.QueueItemTypeConditional,
			Status:          models.QueueItemStatusPending,
			EvaluationCount: 100, // At max
			Payload: mustMarshal(models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    "false", // Would never be met
				Message:       "test",
			}),
		}},
		Now:    now,
		Config: DefaultTickConfig(),
	}

	result := Tick(input)

	var hasSkip bool
	for _, action := range result.Actions {
		if action.Type == ActionTypeSkip {
			hasSkip = true
			if action.Reason != "max evaluations exceeded" {
				t.Errorf("skip reason = %q, want 'max evaluations exceeded'", action.Reason)
			}
		}
	}

	if !hasSkip {
		t.Error("expected skip action for max evaluations")
	}
}

func TestTick_PauseItem(t *testing.T) {
	now := time.Now().UTC()

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:          "agent-1",
			State:       models.AgentStateIdle,
			QueueLength: 1,
		}},
		QueueItems: []QueueItemSnapshot{{
			ID:      "item-1",
			AgentID: "agent-1",
			Type:    models.QueueItemTypePause,
			Status:  models.QueueItemStatusPending,
			Payload: mustMarshal(models.PausePayload{
				DurationSeconds: 60,
				Reason:          "test pause",
			}),
		}},
		Now:    now,
		Config: DefaultTickConfig(),
	}

	result := Tick(input)

	var hasPause bool
	for _, action := range result.Actions {
		if action.Type == ActionTypePause {
			hasPause = true
			if action.Duration != 60*time.Second {
				t.Errorf("pause duration = %v, want 60s", action.Duration)
			}
			if action.Reason != "test pause" {
				t.Errorf("pause reason = %q, want 'test pause'", action.Reason)
			}
		}
	}

	if !hasPause {
		t.Error("expected pause action")
	}
}

func TestTick_AutoResume(t *testing.T) {
	now := time.Now().UTC()
	pastTime := now.Add(-5 * time.Minute)

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:          "agent-1",
			State:       models.AgentStatePaused,
			PausedUntil: &pastTime, // Already expired
			QueueLength: 1,
		}},
		QueueItems: []QueueItemSnapshot{{
			ID:      "item-1",
			AgentID: "agent-1",
			Type:    models.QueueItemTypeMessage,
			Status:  models.QueueItemStatusPending,
			Payload: mustMarshal(models.MessagePayload{Text: "hello"}),
		}},
		Now:    now,
		Config: DefaultTickConfig(),
	}

	result := Tick(input)

	var hasResume bool
	for _, action := range result.Actions {
		if action.Type == ActionTypeResume && action.AgentID == "agent-1" {
			hasResume = true
			break
		}
	}

	if !hasResume {
		t.Error("expected resume action for expired pause")
	}
}

func TestTick_SchedulerPaused(t *testing.T) {
	now := time.Now().UTC()

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:              "agent-1",
			State:           models.AgentStateIdle,
			SchedulerPaused: true,
			QueueLength:     1,
		}},
		QueueItems: []QueueItemSnapshot{{
			ID:      "item-1",
			AgentID: "agent-1",
			Type:    models.QueueItemTypeMessage,
			Status:  models.QueueItemStatusPending,
			Payload: mustMarshal(models.MessagePayload{Text: "hello"}),
		}},
		Now:    now,
		Config: DefaultTickConfig(),
	}

	result := Tick(input)

	if blocked, ok := result.Blocked["agent-1"]; !ok || blocked != BlockReasonSchedulerPaused {
		t.Errorf("blocked = %v, want %v", blocked, BlockReasonSchedulerPaused)
	}

	for _, action := range result.Actions {
		if action.Type == ActionTypeDispatch {
			t.Error("should not dispatch when scheduler is paused")
		}
	}
}

func TestTick_RetryBackoff(t *testing.T) {
	now := time.Now().UTC()
	futureRetry := now.Add(30 * time.Second)

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:          "agent-1",
			State:       models.AgentStateIdle,
			RetryAfter:  &futureRetry,
			QueueLength: 1,
		}},
		QueueItems: []QueueItemSnapshot{{
			ID:      "item-1",
			AgentID: "agent-1",
			Type:    models.QueueItemTypeMessage,
			Status:  models.QueueItemStatusPending,
			Payload: mustMarshal(models.MessagePayload{Text: "hello"}),
		}},
		Now:    now,
		Config: DefaultTickConfig(),
	}

	result := Tick(input)

	if blocked, ok := result.Blocked["agent-1"]; !ok || blocked != BlockReasonRetryBackoff {
		t.Errorf("blocked = %v, want %v", blocked, BlockReasonRetryBackoff)
	}
}

func TestTick_EmptyQueue(t *testing.T) {
	now := time.Now().UTC()

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:          "agent-1",
			State:       models.AgentStateIdle,
			QueueLength: 0,
		}},
		QueueItems: []QueueItemSnapshot{}, // No items
		Now:        now,
		Config:     DefaultTickConfig(),
	}

	result := Tick(input)

	if blocked, ok := result.Blocked["agent-1"]; !ok || blocked != BlockReasonQueueEmpty {
		t.Errorf("blocked = %v, want %v", blocked, BlockReasonQueueEmpty)
	}

	if len(result.Actions) != 0 {
		t.Errorf("expected no actions for empty queue, got %d", len(result.Actions))
	}
}

func TestTick_MultipleAgents(t *testing.T) {
	now := time.Now().UTC()

	input := TickInput{
		Agents: []AgentSnapshot{
			{ID: "agent-1", State: models.AgentStateIdle, QueueLength: 1},
			{ID: "agent-2", State: models.AgentStateWorking, QueueLength: 1},
			{ID: "agent-3", State: models.AgentStateIdle, QueueLength: 1},
		},
		QueueItems: []QueueItemSnapshot{
			{ID: "item-1", AgentID: "agent-1", Type: models.QueueItemTypeMessage, Status: models.QueueItemStatusPending, Payload: mustMarshal(models.MessagePayload{Text: "hello 1"})},
			{ID: "item-2", AgentID: "agent-2", Type: models.QueueItemTypeMessage, Status: models.QueueItemStatusPending, Payload: mustMarshal(models.MessagePayload{Text: "hello 2"})},
			{ID: "item-3", AgentID: "agent-3", Type: models.QueueItemTypeMessage, Status: models.QueueItemStatusPending, Payload: mustMarshal(models.MessagePayload{Text: "hello 3"})},
		},
		Now:    now,
		Config: DefaultTickConfig(),
	}

	result := Tick(input)

	// Count dispatches
	dispatches := make(map[string]bool)
	for _, action := range result.Actions {
		if action.Type == ActionTypeDispatch {
			dispatches[action.AgentID] = true
		}
	}

	// Agent 1 and 3 should have dispatches (idle)
	if !dispatches["agent-1"] {
		t.Error("agent-1 should have dispatch")
	}
	if dispatches["agent-2"] {
		t.Error("agent-2 should not have dispatch (working)")
	}
	if !dispatches["agent-3"] {
		t.Error("agent-3 should have dispatch")
	}

	// Agent 2 should be blocked
	if blocked, ok := result.Blocked["agent-2"]; !ok || blocked != BlockReasonNotIdle {
		t.Errorf("agent-2 blocked = %v, want %v", blocked, BlockReasonNotIdle)
	}
}

func TestTick_QueuePriority(t *testing.T) {
	now := time.Now().UTC()

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:          "agent-1",
			State:       models.AgentStateIdle,
			QueueLength: 2,
		}},
		QueueItems: []QueueItemSnapshot{
			{ID: "item-2", AgentID: "agent-1", Type: models.QueueItemTypeMessage, Position: 1, Status: models.QueueItemStatusPending, Payload: mustMarshal(models.MessagePayload{Text: "second"})},
			{ID: "item-1", AgentID: "agent-1", Type: models.QueueItemTypeMessage, Position: 0, Status: models.QueueItemStatusPending, Payload: mustMarshal(models.MessagePayload{Text: "first"})},
		},
		Now:    now,
		Config: DefaultTickConfig(),
	}

	result := Tick(input)

	// Should dispatch item-1 (position 0) not item-2 (position 1)
	for _, action := range result.Actions {
		if action.Type == ActionTypeDispatch {
			if action.ItemID != "item-1" {
				t.Errorf("dispatched item = %q, want 'item-1'", action.ItemID)
			}
			if action.Message != "first" {
				t.Errorf("message = %q, want 'first'", action.Message)
			}
		}
	}
}

func TestTick_IdleStateNotRequired(t *testing.T) {
	now := time.Now().UTC()

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:          "agent-1",
			State:       models.AgentStateWorking, // Not idle
			QueueLength: 1,
		}},
		QueueItems: []QueueItemSnapshot{{
			ID:      "item-1",
			AgentID: "agent-1",
			Type:    models.QueueItemTypeMessage,
			Status:  models.QueueItemStatusPending,
			Payload: mustMarshal(models.MessagePayload{Text: "hello"}),
		}},
		Now: now,
		Config: TickConfig{
			IdleStateRequired: false, // Allow dispatch to non-idle agents
		},
	}

	result := Tick(input)

	var hasDispatch bool
	for _, action := range result.Actions {
		if action.Type == ActionTypeDispatch {
			hasDispatch = true
		}
	}

	if !hasDispatch {
		t.Error("expected dispatch when IdleStateRequired=false")
	}
}

func TestTick_AwaitingApproval(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name                 string
		isPermissionResponse bool
		wantDispatch         bool
		wantBlocked          BlockReason
	}{
		{
			name:                 "regular message blocked when awaiting approval",
			isPermissionResponse: false,
			wantDispatch:         false,
			wantBlocked:          BlockReasonAwaitingApproval,
		},
		{
			name:                 "permission response allowed when awaiting approval",
			isPermissionResponse: true,
			wantDispatch:         true,
			wantBlocked:          BlockReasonNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := TickInput{
				Agents: []AgentSnapshot{{
					ID:          "agent-1",
					State:       models.AgentStateAwaitingApproval,
					QueueLength: 1,
				}},
				QueueItems: []QueueItemSnapshot{{
					ID:      "item-1",
					AgentID: "agent-1",
					Type:    models.QueueItemTypeMessage,
					Status:  models.QueueItemStatusPending,
					Payload: mustMarshal(models.MessagePayload{
						Text:                 "y",
						IsPermissionResponse: tt.isPermissionResponse,
					}),
				}},
				Now:    now,
				Config: DefaultTickConfig(),
			}

			result := Tick(input)

			hasDispatch := false
			for _, action := range result.Actions {
				if action.Type == ActionTypeDispatch {
					hasDispatch = true
					break
				}
			}

			if hasDispatch != tt.wantDispatch {
				t.Errorf("dispatch = %v, want %v", hasDispatch, tt.wantDispatch)
			}

			if tt.wantBlocked != BlockReasonNone {
				if blocked, ok := result.Blocked["agent-1"]; !ok || blocked != tt.wantBlocked {
					t.Errorf("blocked = %v, want %v", blocked, tt.wantBlocked)
				}
			}
		})
	}
}

func TestTick_AwaitingApproval_NoItem(t *testing.T) {
	now := time.Now().UTC()

	input := TickInput{
		Agents: []AgentSnapshot{{
			ID:          "agent-1",
			State:       models.AgentStateAwaitingApproval,
			QueueLength: 0, // No items
		}},
		QueueItems: []QueueItemSnapshot{},
		Now:        now,
		Config:     DefaultTickConfig(),
	}

	result := Tick(input)

	// Should be blocked with AwaitingApproval reason (no items to check)
	if blocked, ok := result.Blocked["agent-1"]; !ok {
		t.Error("expected agent to be blocked")
	} else if blocked != BlockReasonAwaitingApproval && blocked != BlockReasonQueueEmpty {
		// Either reason is acceptable - AwaitingApproval checked first, then QueueEmpty
		t.Errorf("blocked = %v, want %v or %v", blocked, BlockReasonAwaitingApproval, BlockReasonQueueEmpty)
	}
}

// mustMarshal marshals a value to JSON or panics.
func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

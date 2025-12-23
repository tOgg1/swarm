package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

func TestConditionEvaluator_WhenIdle(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()

	tests := []struct {
		name    string
		state   models.AgentState
		wantMet bool
	}{
		{"idle agent", models.AgentStateIdle, true},
		{"working agent", models.AgentStateWorking, false},
		{"paused agent", models.AgentStatePaused, false},
		{"rate limited agent", models.AgentStateRateLimited, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{State: tt.state},
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeWhenIdle,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v", result.Met, tt.wantMet)
			}
		})
	}
}

func TestConditionEvaluator_AfterCooldown(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()
	now := time.Now().UTC()

	tests := []struct {
		name         string
		lastActivity *time.Time
		expression   string
		wantMet      bool
	}{
		{"no prior activity", nil, "", true},
		{"cooldown elapsed", timePtr(now.Add(-60 * time.Second)), "30s", true},
		{"cooldown not elapsed", timePtr(now.Add(-10 * time.Second)), "30s", false},
		{"custom cooldown elapsed", timePtr(now.Add(-2 * time.Minute)), "1m", true},
		{"custom cooldown not elapsed", timePtr(now.Add(-30 * time.Second)), "1m", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{
					State:        models.AgentStateIdle,
					LastActivity: tt.lastActivity,
				},
				Now: now,
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeAfterCooldown,
				Expression:    tt.expression,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v (reason: %s)", result.Met, tt.wantMet, result.Reason)
			}
		})
	}
}

func TestConditionEvaluator_AfterPrevious(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()

	condCtx := ConditionContext{
		Agent: &models.Agent{State: models.AgentStateIdle},
	}
	payload := models.ConditionalPayload{
		ConditionType: models.ConditionTypeAfterPrevious,
		Message:       "test",
	}

	result, err := evaluator.Evaluate(ctx, condCtx, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Met {
		t.Error("after_previous should always be met")
	}
}

func TestConditionEvaluator_CustomExpression_StateComparison(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()

	tests := []struct {
		name       string
		expression string
		state      models.AgentState
		wantMet    bool
		wantErr    bool
	}{
		{"state equals idle", "state == idle", models.AgentStateIdle, true, false},
		{"agent_state equals idle", "agent_state == idle", models.AgentStateIdle, true, false},
		{"state equals working", "state == working", models.AgentStateWorking, true, false},
		{"agent_state not equals working", "agent_state != working", models.AgentStateIdle, true, false},
		{"state not equals paused", "state != paused", models.AgentStateIdle, true, false},
		{"state equals but different", "state == working", models.AgentStateIdle, false, false},
		{"state not equals but same", "state != idle", models.AgentStateIdle, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{State: tt.state},
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    tt.expression,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if tt.wantErr && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.wantErr && result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v (reason: %s)", result.Met, tt.wantMet, result.Reason)
			}
		})
	}
}

func TestConditionEvaluator_CustomExpression_QueueLength(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()

	tests := []struct {
		name        string
		expression  string
		queueLength int
		wantMet     bool
	}{
		{"queue equals zero", "queue_length == 0", 0, true},
		{"queue greater than zero", "queue_length > 0", 5, true},
		{"queue less than ten", "queue_length < 10", 5, true},
		{"queue greater or equal", "queue_length >= 5", 5, true},
		{"queue less or equal", "queue_length <= 5", 5, true},
		{"queue not equal", "queue != 0", 5, true},
		{"queue equals but different", "queue_length == 0", 5, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent:       &models.Agent{State: models.AgentStateIdle},
				QueueLength: tt.queueLength,
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    tt.expression,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v (reason: %s)", result.Met, tt.wantMet, result.Reason)
			}
		})
	}
}

func TestConditionEvaluator_CustomExpression_IdleFor(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()
	now := time.Now().UTC()

	tests := []struct {
		name         string
		expression   string
		state        models.AgentState
		lastActivity *time.Time
		wantMet      bool
	}{
		{"idle for 1 minute", "idle_for >= 1m", models.AgentStateIdle, timePtr(now.Add(-2 * time.Minute)), true},
		{"idle for less than required", "idle_for >= 5m", models.AgentStateIdle, timePtr(now.Add(-2 * time.Minute)), false},
		{"not idle", "idle_for >= 1m", models.AgentStateWorking, timePtr(now.Add(-2 * time.Minute)), false},
		{"idle less than threshold", "idle < 1m", models.AgentStateIdle, timePtr(now.Add(-30 * time.Second)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{
					State:        tt.state,
					LastActivity: tt.lastActivity,
				},
				Now: now,
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    tt.expression,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v (reason: %s)", result.Met, tt.wantMet, result.Reason)
			}
		})
	}
}

func TestConditionEvaluator_CustomExpression_TimeSinceLast(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()
	now := time.Now().UTC()

	tests := []struct {
		name         string
		expression   string
		lastActivity *time.Time
		wantMet      bool
	}{
		{"since last meets", "time_since_last >= 1m", timePtr(now.Add(-2 * time.Minute)), true},
		{"since last not met", "time_since_last < 30s", timePtr(now.Add(-2 * time.Minute)), false},
		{"since last with no activity", "time_since_last == 0s", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{
					State:        models.AgentStateIdle,
					LastActivity: tt.lastActivity,
				},
				Now: now,
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    tt.expression,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v (reason: %s)", result.Met, tt.wantMet, result.Reason)
			}
		})
	}
}

func TestConditionEvaluator_CustomExpression_CooldownRemaining(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()
	now := time.Now().UTC()
	activeCooldown := now.Add(2 * time.Minute)
	expiredCooldown := now.Add(-2 * time.Minute)

	tests := []struct {
		name        string
		expression  string
		pausedUntil *time.Time
		wantMet     bool
	}{
		{"cooldown remaining greater than", "cooldown_remaining > 1m", &activeCooldown, true},
		{"cooldown remaining less or equal", "cooldown_remaining <= 1m", &activeCooldown, false},
		{"cooldown expired", "cooldown_remaining == 0s", &expiredCooldown, true},
		{"cooldown missing", "cooldown_remaining == 0s", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{
					State:       models.AgentStatePaused,
					PausedUntil: tt.pausedUntil,
				},
				Now: now,
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    tt.expression,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v (reason: %s)", result.Met, tt.wantMet, result.Reason)
			}
		})
	}
}

func TestConditionEvaluator_CustomExpression_TimeOfDay(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()

	// Set a fixed time for testing: 14:30 UTC
	now := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name       string
		expression string
		wantMet    bool
	}{
		{"after business hours start", "time_of_day >= 09:00", true},
		{"before business hours end", "time <= 17:00", true},
		{"exact time match", "time == 14:30", true},
		{"before current time", "time < 15:00", true},
		{"after current time", "time > 14:00", true},
		{"not yet reached", "time >= 16:00", false},
		{"already passed", "time <= 10:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{State: models.AgentStateIdle},
				Now:   now,
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    tt.expression,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v (reason: %s)", result.Met, tt.wantMet, result.Reason)
			}
		})
	}
}

func TestConditionEvaluator_CustomExpression_BooleanLiterals(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()

	tests := []struct {
		expression string
		wantMet    bool
	}{
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"false", false},
		{"FALSE", false},
		{"False", false},
	}

	for _, tt := range tests {
		t.Run(tt.expression, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{State: models.AgentStateIdle},
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    tt.expression,
				Message:       "test",
			}

			result, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Met != tt.wantMet {
				t.Errorf("got Met=%v, want %v", result.Met, tt.wantMet)
			}
		})
	}
}

func TestConditionEvaluator_CustomExpression_Errors(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()

	tests := []struct {
		name       string
		expression string
	}{
		{"empty expression", ""},
		{"unknown field", "unknown_field == 1"},
		{"invalid queue value", "queue_length == abc"},
		{"invalid time format", "time >= 25:00"},
		{"invalid duration", "idle_for >= xyz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condCtx := ConditionContext{
				Agent: &models.Agent{State: models.AgentStateIdle},
			}
			payload := models.ConditionalPayload{
				ConditionType: models.ConditionTypeCustomExpression,
				Expression:    tt.expression,
				Message:       "test",
			}

			_, err := evaluator.Evaluate(ctx, condCtx, payload)
			if err == nil {
				t.Error("expected error but got none")
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"30s", 30 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"0.5d", 12 * time.Hour, false},
		{"", 0, true},
		{"invalid", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseDuration(tt.input)
			if tt.wantErr && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestParseTimeOfDay(t *testing.T) {
	tests := []struct {
		input   string
		hour    int
		minute  int
		wantErr bool
	}{
		{"09:00", 9, 0, false},
		{"17:30", 17, 30, false},
		{"00:00", 0, 0, false},
		{"23:59", 23, 59, false},
		{"25:00", 0, 0, true},
		{"12:60", 0, 0, true},
		{"invalid", 0, 0, true},
		{"12", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseTimeOfDay(tt.input)
			if tt.wantErr && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.wantErr && (result.hour != tt.hour || result.minute != tt.minute) {
				t.Errorf("got %d:%d, want %d:%d", result.hour, result.minute, tt.hour, tt.minute)
			}
		})
	}
}

func TestTokenizeExpression(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"state == idle", []string{"state", "==", "idle"}},
		{"queue_length >= 5", []string{"queue_length", ">=", "5"}},
		{"time_of_day <= 17:30", []string{"time_of_day", "<=", "17:30"}},
		{"idle_for > 1h30m", []string{"idle_for", ">", "1h30m"}},
		{"state != working", []string{"state", "!=", "working"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := tokenizeExpression(tt.input)
			if len(result) != len(tt.expected) {
				t.Fatalf("got %d tokens, want %d: %v", len(result), len(tt.expected), result)
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("token[%d] got %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestConditionResult_RetryAfter(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := context.Background()
	now := time.Now().UTC()

	// Test that RetryAfter is populated when condition not met
	condCtx := ConditionContext{
		Agent: &models.Agent{
			State:        models.AgentStateIdle,
			LastActivity: timePtr(now.Add(-10 * time.Second)),
		},
		Now: now,
	}
	payload := models.ConditionalPayload{
		ConditionType: models.ConditionTypeAfterCooldown,
		Expression:    "30s",
		Message:       "test",
	}

	result, err := evaluator.Evaluate(ctx, condCtx, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Met {
		t.Error("expected condition to not be met")
	}
	if result.RetryAfter == nil {
		t.Error("expected RetryAfter to be set")
	}
	if *result.RetryAfter < 19*time.Second || *result.RetryAfter > 21*time.Second {
		t.Errorf("expected RetryAfter ~20s, got %v", *result.RetryAfter)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// Package scheduler provides the message dispatch scheduler for Swarm agents.
package scheduler

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

// ConditionContext provides context for evaluating conditions.
type ConditionContext struct {
	// Agent is the agent being evaluated.
	Agent *models.Agent

	// QueueLength is the current queue length.
	QueueLength int

	// Now is the current time (for testing).
	Now time.Time
}

// ConditionResult contains the result of a condition evaluation.
type ConditionResult struct {
	// Met indicates if the condition was satisfied.
	Met bool

	// Reason explains why the condition was or wasn't met.
	Reason string

	// RetryAfter suggests when to retry if not met (optional).
	RetryAfter *time.Duration
}

// ConditionEvaluator evaluates conditional gate expressions.
type ConditionEvaluator struct{}

// NewConditionEvaluator creates a new condition evaluator.
func NewConditionEvaluator() *ConditionEvaluator {
	return &ConditionEvaluator{}
}

// Evaluate evaluates a condition against the given context.
func (e *ConditionEvaluator) Evaluate(ctx context.Context, condCtx ConditionContext, payload models.ConditionalPayload) (ConditionResult, error) {
	switch payload.ConditionType {
	case models.ConditionTypeWhenIdle:
		return e.evaluateWhenIdle(condCtx)

	case models.ConditionTypeAfterCooldown:
		return e.evaluateAfterCooldown(condCtx, payload)

	case models.ConditionTypeAfterPrevious:
		return e.evaluateAfterPrevious(condCtx)

	case models.ConditionTypeCustomExpression:
		return e.evaluateCustomExpression(condCtx, payload.Expression)

	default:
		return ConditionResult{Met: false, Reason: "unknown condition type"}, fmt.Errorf("unknown condition type: %s", payload.ConditionType)
	}
}

// evaluateWhenIdle checks if the agent is idle.
func (e *ConditionEvaluator) evaluateWhenIdle(ctx ConditionContext) (ConditionResult, error) {
	if ctx.Agent == nil {
		return ConditionResult{Met: false, Reason: "agent not available"}, nil
	}

	if ctx.Agent.State == models.AgentStateIdle {
		return ConditionResult{Met: true, Reason: "agent is idle"}, nil
	}

	return ConditionResult{
		Met:    false,
		Reason: fmt.Sprintf("agent is %s, waiting for idle", ctx.Agent.State),
	}, nil
}

// evaluateAfterCooldown checks if enough time has passed since last activity.
func (e *ConditionEvaluator) evaluateAfterCooldown(ctx ConditionContext, payload models.ConditionalPayload) (ConditionResult, error) {
	if ctx.Agent == nil {
		return ConditionResult{Met: false, Reason: "agent not available"}, nil
	}

	// Parse cooldown duration from expression if provided, otherwise use default
	cooldown := 30 * time.Second
	if payload.Expression != "" {
		parsed, err := parseDuration(payload.Expression)
		if err == nil && parsed > 0 {
			cooldown = parsed
		}
	}

	if ctx.Agent.LastActivity == nil {
		return ConditionResult{Met: true, Reason: "no prior activity recorded"}, nil
	}

	now := ctx.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	elapsed := now.Sub(*ctx.Agent.LastActivity)
	if elapsed >= cooldown {
		return ConditionResult{
			Met:    true,
			Reason: fmt.Sprintf("cooldown elapsed (%.0fs >= %.0fs)", elapsed.Seconds(), cooldown.Seconds()),
		}, nil
	}

	remaining := cooldown - elapsed
	return ConditionResult{
		Met:        false,
		Reason:     fmt.Sprintf("cooldown not elapsed (%.0fs < %.0fs)", elapsed.Seconds(), cooldown.Seconds()),
		RetryAfter: &remaining,
	}, nil
}

// evaluateAfterPrevious always returns true since items are processed in order.
func (e *ConditionEvaluator) evaluateAfterPrevious(ctx ConditionContext) (ConditionResult, error) {
	return ConditionResult{Met: true, Reason: "previous item completed"}, nil
}

// evaluateCustomExpression evaluates a custom expression.
// Supported expressions:
//   - state == idle | working | paused | ...
//   - agent_state != <state>
//   - queue_length == 0 | > 0 | < 5 | >= 3 | <= 10
//   - idle_for >= 30s | > 1m | < 5m
//   - time_since_last >= 30s | < 5m
//   - cooldown_remaining <= 30s | == 0s
//   - time_of_day >= 09:00 | <= 17:00
//   - true (always satisfied)
//   - false (never satisfied)
func (e *ConditionEvaluator) evaluateCustomExpression(ctx ConditionContext, expr string) (ConditionResult, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ConditionResult{Met: false, Reason: "empty expression"}, fmt.Errorf("empty expression")
	}

	// Handle boolean literals
	switch strings.ToLower(expr) {
	case "true":
		return ConditionResult{Met: true, Reason: "literal true"}, nil
	case "false":
		return ConditionResult{Met: false, Reason: "literal false"}, nil
	}

	// Parse expression
	parts := tokenizeExpression(expr)
	if len(parts) < 3 {
		return ConditionResult{Met: false, Reason: "invalid expression format"}, fmt.Errorf("invalid expression: %s", expr)
	}

	field := strings.ToLower(parts[0])
	operator := parts[1]
	value := strings.Join(parts[2:], " ")

	switch field {
	case "state", "agent_state":
		return e.evaluateStateExpr(ctx, operator, value)
	case "queue_length", "queue":
		return e.evaluateQueueLengthExpr(ctx, operator, value)
	case "idle_for", "idle":
		return e.evaluateIdleForExpr(ctx, operator, value)
	case "time_since_last":
		return e.evaluateTimeSinceLastExpr(ctx, operator, value)
	case "cooldown_remaining":
		return e.evaluateCooldownRemainingExpr(ctx, operator, value)
	case "time_of_day", "time":
		return e.evaluateTimeOfDayExpr(ctx, operator, value)
	default:
		return ConditionResult{Met: false, Reason: fmt.Sprintf("unknown field: %s", field)}, fmt.Errorf("unknown field: %s", field)
	}
}

// evaluateStateExpr evaluates state comparisons.
func (e *ConditionEvaluator) evaluateStateExpr(ctx ConditionContext, operator, value string) (ConditionResult, error) {
	if ctx.Agent == nil {
		return ConditionResult{Met: false, Reason: "agent not available"}, nil
	}

	currentState := string(ctx.Agent.State)
	targetState := strings.ToLower(strings.TrimSpace(value))

	switch operator {
	case "==", "=":
		if strings.ToLower(currentState) == targetState {
			return ConditionResult{Met: true, Reason: fmt.Sprintf("state is %s", currentState)}, nil
		}
		return ConditionResult{Met: false, Reason: fmt.Sprintf("state is %s, expected %s", currentState, targetState)}, nil

	case "!=", "<>":
		if strings.ToLower(currentState) != targetState {
			return ConditionResult{Met: true, Reason: fmt.Sprintf("state is %s (not %s)", currentState, targetState)}, nil
		}
		return ConditionResult{Met: false, Reason: fmt.Sprintf("state is %s", currentState)}, nil

	default:
		return ConditionResult{Met: false, Reason: fmt.Sprintf("invalid operator for state: %s", operator)}, fmt.Errorf("invalid operator: %s", operator)
	}
}

// evaluateQueueLengthExpr evaluates queue length comparisons.
func (e *ConditionEvaluator) evaluateQueueLengthExpr(ctx ConditionContext, operator, value string) (ConditionResult, error) {
	targetLen, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return ConditionResult{Met: false, Reason: "invalid queue length value"}, fmt.Errorf("invalid queue length: %s", value)
	}

	currentLen := ctx.QueueLength

	var met bool
	switch operator {
	case "==", "=":
		met = currentLen == targetLen
	case "!=", "<>":
		met = currentLen != targetLen
	case ">":
		met = currentLen > targetLen
	case ">=":
		met = currentLen >= targetLen
	case "<":
		met = currentLen < targetLen
	case "<=":
		met = currentLen <= targetLen
	default:
		return ConditionResult{Met: false, Reason: fmt.Sprintf("invalid operator: %s", operator)}, fmt.Errorf("invalid operator: %s", operator)
	}

	reason := fmt.Sprintf("queue_length %d %s %d", currentLen, operator, targetLen)
	if met {
		return ConditionResult{Met: true, Reason: reason}, nil
	}
	return ConditionResult{Met: false, Reason: reason}, nil
}

// evaluateIdleForExpr evaluates idle duration comparisons.
func (e *ConditionEvaluator) evaluateIdleForExpr(ctx ConditionContext, operator, value string) (ConditionResult, error) {
	if ctx.Agent == nil {
		return ConditionResult{Met: false, Reason: "agent not available"}, nil
	}

	targetDuration, err := parseDuration(strings.TrimSpace(value))
	if err != nil {
		return ConditionResult{Met: false, Reason: "invalid duration"}, fmt.Errorf("invalid duration: %s", value)
	}

	// If not idle, the condition is not met (unless checking < or <=)
	if ctx.Agent.State != models.AgentStateIdle {
		switch operator {
		case "<", "<=":
			return ConditionResult{Met: true, Reason: "agent is not idle"}, nil
		default:
			return ConditionResult{Met: false, Reason: fmt.Sprintf("agent is %s, not idle", ctx.Agent.State)}, nil
		}
	}

	now := ctx.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var idleFor time.Duration
	if ctx.Agent.LastActivity != nil {
		idleFor = now.Sub(*ctx.Agent.LastActivity)
	}

	var met bool
	switch operator {
	case ">=":
		met = idleFor >= targetDuration
	case ">":
		met = idleFor > targetDuration
	case "<=":
		met = idleFor <= targetDuration
	case "<":
		met = idleFor < targetDuration
	case "==", "=":
		// Allow 1 second tolerance for equality
		diff := idleFor - targetDuration
		if diff < 0 {
			diff = -diff
		}
		met = diff <= time.Second
	default:
		return ConditionResult{Met: false, Reason: fmt.Sprintf("invalid operator: %s", operator)}, fmt.Errorf("invalid operator: %s", operator)
	}

	reason := fmt.Sprintf("idle_for %s %s %s", idleFor.Round(time.Second), operator, targetDuration)
	if met {
		return ConditionResult{Met: true, Reason: reason}, nil
	}

	if operator == ">=" || operator == ">" {
		remaining := targetDuration - idleFor
		if remaining > 0 {
			return ConditionResult{Met: false, Reason: reason, RetryAfter: &remaining}, nil
		}
	}
	return ConditionResult{Met: false, Reason: reason}, nil
}

// evaluateTimeSinceLastExpr evaluates time since last activity comparisons.
func (e *ConditionEvaluator) evaluateTimeSinceLastExpr(ctx ConditionContext, operator, value string) (ConditionResult, error) {
	if ctx.Agent == nil {
		return ConditionResult{Met: false, Reason: "agent not available"}, nil
	}

	targetDuration, err := parseDuration(strings.TrimSpace(value))
	if err != nil {
		return ConditionResult{Met: false, Reason: "invalid duration"}, fmt.Errorf("invalid duration: %s", value)
	}

	now := ctx.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var since time.Duration
	if ctx.Agent.LastActivity != nil {
		since = now.Sub(*ctx.Agent.LastActivity)
	}

	return evaluateDurationExpr("time_since_last", since, operator, targetDuration)
}

// evaluateCooldownRemainingExpr evaluates cooldown remaining comparisons.
func (e *ConditionEvaluator) evaluateCooldownRemainingExpr(ctx ConditionContext, operator, value string) (ConditionResult, error) {
	targetDuration, err := parseDuration(strings.TrimSpace(value))
	if err != nil {
		return ConditionResult{Met: false, Reason: "invalid duration"}, fmt.Errorf("invalid duration: %s", value)
	}

	now := ctx.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var remaining time.Duration
	if ctx.Agent != nil && ctx.Agent.PausedUntil != nil && now.Before(*ctx.Agent.PausedUntil) {
		remaining = ctx.Agent.PausedUntil.Sub(now)
	}

	return evaluateDurationExpr("cooldown_remaining", remaining, operator, targetDuration)
}

// evaluateTimeOfDayExpr evaluates time-of-day comparisons.
func (e *ConditionEvaluator) evaluateTimeOfDayExpr(ctx ConditionContext, operator, value string) (ConditionResult, error) {
	targetTime, err := parseTimeOfDay(strings.TrimSpace(value))
	if err != nil {
		return ConditionResult{Met: false, Reason: "invalid time format"}, fmt.Errorf("invalid time: %s", value)
	}

	now := ctx.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	currentMinutes := now.Hour()*60 + now.Minute()
	targetMinutes := targetTime.hour*60 + targetTime.minute

	var met bool
	switch operator {
	case ">=":
		met = currentMinutes >= targetMinutes
	case ">":
		met = currentMinutes > targetMinutes
	case "<=":
		met = currentMinutes <= targetMinutes
	case "<":
		met = currentMinutes < targetMinutes
	case "==", "=":
		met = currentMinutes == targetMinutes
	default:
		return ConditionResult{Met: false, Reason: fmt.Sprintf("invalid operator: %s", operator)}, fmt.Errorf("invalid operator: %s", operator)
	}

	currentTimeStr := fmt.Sprintf("%02d:%02d", now.Hour(), now.Minute())
	targetTimeStr := fmt.Sprintf("%02d:%02d", targetTime.hour, targetTime.minute)
	reason := fmt.Sprintf("time %s %s %s", currentTimeStr, operator, targetTimeStr)

	if met {
		return ConditionResult{Met: true, Reason: reason}, nil
	}
	return ConditionResult{Met: false, Reason: reason}, nil
}

// Helper types and functions

type timeOfDay struct {
	hour   int
	minute int
}

// parseTimeOfDay parses a time string like "09:00" or "17:30".
func parseTimeOfDay(s string) (timeOfDay, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return timeOfDay{}, fmt.Errorf("invalid time format: %s", s)
	}

	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || hour < 0 || hour > 23 {
		return timeOfDay{}, fmt.Errorf("invalid hour: %s", parts[0])
	}

	minute, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || minute < 0 || minute > 59 {
		return timeOfDay{}, fmt.Errorf("invalid minute: %s", parts[1])
	}

	return timeOfDay{hour: hour, minute: minute}, nil
}

// parseDuration parses a duration string with support for days (d).
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Handle 'd' suffix for days
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.ParseFloat(daysStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid days: %s", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}

	return time.ParseDuration(s)
}

func evaluateDurationExpr(label string, current time.Duration, operator string, target time.Duration) (ConditionResult, error) {
	var met bool
	switch operator {
	case ">=":
		met = current >= target
	case ">":
		met = current > target
	case "<=":
		met = current <= target
	case "<":
		met = current < target
	case "==", "=":
		diff := current - target
		if diff < 0 {
			diff = -diff
		}
		met = diff <= time.Second
	default:
		return ConditionResult{Met: false, Reason: fmt.Sprintf("invalid operator: %s", operator)}, fmt.Errorf("invalid operator: %s", operator)
	}

	reason := fmt.Sprintf("%s %s %s %s", label, current.Round(time.Second), operator, target)
	if met {
		return ConditionResult{Met: true, Reason: reason}, nil
	}

	result := ConditionResult{Met: false, Reason: reason}
	switch operator {
	case ">=", ">":
		if remaining := target - current; remaining > 0 {
			result.RetryAfter = &remaining
		}
	case "<=", "<":
		if remaining := current - target; remaining > 0 {
			result.RetryAfter = &remaining
		}
	}
	return result, nil
}

// tokenizeExpression splits an expression into tokens.
var operatorRegex = regexp.MustCompile(`^(.+?)\s*(==|!=|<>|>=|<=|>|<|=)\s*(.+)$`)

func tokenizeExpression(expr string) []string {
	matches := operatorRegex.FindStringSubmatch(expr)
	if matches == nil || len(matches) < 4 {
		// Fallback to simple split
		return strings.Fields(expr)
	}
	return []string{matches[1], matches[2], matches[3]}
}

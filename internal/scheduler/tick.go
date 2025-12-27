// Package scheduler provides deterministic scheduling for Swarm agents.
package scheduler

import (
	"encoding/json"
	"time"

	"github.com/opencode-ai/swarm/internal/models"
)

// ActionType specifies the type of scheduler action.
type ActionType string

const (
	// ActionTypeDispatch indicates a message should be dispatched.
	ActionTypeDispatch ActionType = "dispatch"

	// ActionTypePause indicates an agent should be paused.
	ActionTypePause ActionType = "pause"

	// ActionTypeResume indicates an agent should be resumed.
	ActionTypeResume ActionType = "resume"

	// ActionTypeRequeue indicates a conditional item should be re-queued.
	ActionTypeRequeue ActionType = "requeue"

	// ActionTypeSkip indicates an item should be skipped.
	ActionTypeSkip ActionType = "skip"

	// ActionTypeRotateAccount indicates an account rotation is needed.
	ActionTypeRotateAccount ActionType = "rotate_account"

	// ActionTypeCooldown indicates an account cooldown should be set.
	ActionTypeCooldown ActionType = "cooldown"
)

// BlockReason specifies why an agent cannot receive dispatches.
type BlockReason string

const (
	BlockReasonNone             BlockReason = ""
	BlockReasonNotIdle          BlockReason = "agent_not_idle"
	BlockReasonPaused           BlockReason = "agent_paused"
	BlockReasonStopped          BlockReason = "agent_stopped"
	BlockReasonSchedulerPaused  BlockReason = "scheduler_paused"
	BlockReasonRetryBackoff     BlockReason = "retry_backoff"
	BlockReasonAccountCooldown  BlockReason = "account_cooldown"
	BlockReasonQueueEmpty       BlockReason = "queue_empty"
	BlockReasonConditionNotMet  BlockReason = "condition_not_met"
	BlockReasonAwaitingApproval BlockReason = "awaiting_approval"
)

// AgentSnapshot represents the state of an agent at a point in time.
type AgentSnapshot struct {
	ID              string            `json:"id"`
	State           models.AgentState `json:"state"`
	AccountID       string            `json:"account_id,omitempty"`
	QueueLength     int               `json:"queue_length"`
	LastActivity    *time.Time        `json:"last_activity,omitempty"`
	PausedUntil     *time.Time        `json:"paused_until,omitempty"`
	SchedulerPaused bool              `json:"scheduler_paused"`
	RetryAfter      *time.Time        `json:"retry_after,omitempty"`
}

// QueueItemSnapshot represents a queue item at a point in time.
type QueueItemSnapshot struct {
	ID              string                 `json:"id"`
	AgentID         string                 `json:"agent_id"`
	Type            models.QueueItemType   `json:"type"`
	Position        int                    `json:"position"`
	Status          models.QueueItemStatus `json:"status"`
	Attempts        int                    `json:"attempts"`
	EvaluationCount int                    `json:"evaluation_count"`
	Payload         json.RawMessage        `json:"payload"`
}

// AccountSnapshot represents an account at a point in time.
type AccountSnapshot struct {
	ID            string     `json:"id"`
	IsActive      bool       `json:"is_active"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

// TickInput contains all state needed for a scheduling tick.
type TickInput struct {
	// Agents is the list of all agents.
	Agents []AgentSnapshot `json:"agents"`

	// QueueItems is the list of pending queue items (first item per agent).
	QueueItems []QueueItemSnapshot `json:"queue_items"`

	// Accounts is the list of all accounts.
	Accounts []AccountSnapshot `json:"accounts"`

	// Now is the current time.
	Now time.Time `json:"now"`

	// Config contains scheduler configuration.
	Config TickConfig `json:"config"`
}

// TickConfig contains configuration for the tick function.
type TickConfig struct {
	// IdleStateRequired requires agents to be idle before dispatch.
	IdleStateRequired bool `json:"idle_state_required"`

	// MaxRetries is the maximum number of dispatch retries.
	MaxRetries int `json:"max_retries"`

	// MaxConditionalEvaluations is the maximum condition evaluation count.
	MaxConditionalEvaluations int `json:"max_conditional_evaluations"`
}

// DefaultTickConfig returns sensible default configuration.
func DefaultTickConfig() TickConfig {
	return TickConfig{
		IdleStateRequired:         true,
		MaxRetries:                3,
		MaxConditionalEvaluations: 100,
	}
}

// TickAction represents an action to be taken by the scheduler.
type TickAction struct {
	// Type specifies the action type.
	Type ActionType `json:"type"`

	// AgentID is the target agent.
	AgentID string `json:"agent_id"`

	// ItemID is the queue item ID (for dispatch/requeue/skip actions).
	ItemID string `json:"item_id,omitempty"`

	// ItemType is the type of queue item.
	ItemType models.QueueItemType `json:"item_type,omitempty"`

	// Reason explains why this action was taken.
	Reason string `json:"reason"`

	// Duration is used for pause actions.
	Duration time.Duration `json:"duration,omitempty"`

	// Message is the text to send (for dispatch actions).
	Message string `json:"message,omitempty"`

	// AccountID is used for account rotation actions.
	AccountID string `json:"account_id,omitempty"`
}

// TickResult contains the result of a scheduling tick.
type TickResult struct {
	// Actions is the list of actions to execute.
	Actions []TickAction `json:"actions"`

	// Blocked contains agents that were blocked from dispatch with reasons.
	Blocked map[string]BlockReason `json:"blocked"`
}

// Tick performs a single scheduling cycle and returns actions to execute.
// This is a pure function with no side effects - it only examines input
// and produces output, making it easy to unit test.
func Tick(input TickInput) TickResult {
	result := TickResult{
		Actions: make([]TickAction, 0),
		Blocked: make(map[string]BlockReason),
	}

	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}

	config := input.Config
	if config.MaxConditionalEvaluations <= 0 {
		config.MaxConditionalEvaluations = DefaultTickConfig().MaxConditionalEvaluations
	}

	// Build lookup maps for efficient access
	accountMap := buildAccountMap(input.Accounts)
	queueMap := buildQueueMap(input.QueueItems)

	// Check each agent for potential dispatch
	for _, agent := range input.Agents {
		// Check for auto-resume first
		if agent.State == models.AgentStatePaused && agent.PausedUntil != nil {
			if input.Now.After(*agent.PausedUntil) {
				result.Actions = append(result.Actions, TickAction{
					Type:    ActionTypeResume,
					AgentID: agent.ID,
					Reason:  "pause duration expired",
				})
				// After resume, agent will be eligible on next tick
				continue
			}
		}

		// Check if there's a queue item first (needed for permission response check)
		item, hasItem := queueMap[agent.ID]

		// Check eligibility (passing item for AwaitingApproval special case)
		blocked, reason := checkEligibilityWithItem(agent, accountMap, input.Now, config, hasItem, item)
		if blocked {
			result.Blocked[agent.ID] = reason
			continue
		}

		if !hasItem {
			result.Blocked[agent.ID] = BlockReasonQueueEmpty
			continue
		}

		// Process the queue item
		actions := processQueueItem(agent, item, accountMap, input.Now, config)
		result.Actions = append(result.Actions, actions...)
	}

	return result
}

// buildAccountMap creates a lookup map from account ID to account snapshot.
func buildAccountMap(accounts []AccountSnapshot) map[string]AccountSnapshot {
	m := make(map[string]AccountSnapshot, len(accounts))
	for _, acc := range accounts {
		m[acc.ID] = acc
	}
	return m
}

// buildQueueMap creates a lookup map from agent ID to their first queue item.
func buildQueueMap(items []QueueItemSnapshot) map[string]QueueItemSnapshot {
	m := make(map[string]QueueItemSnapshot)
	for _, item := range items {
		if item.Status != models.QueueItemStatusPending {
			continue
		}
		// Only keep the first item per agent (lowest position)
		existing, exists := m[item.AgentID]
		if !exists || item.Position < existing.Position {
			m[item.AgentID] = item
		}
	}
	return m
}

// checkEligibility determines if an agent can receive dispatches.
// Deprecated: Use checkEligibilityWithItem for proper AwaitingApproval handling.
func checkEligibility(
	agent AgentSnapshot,
	accounts map[string]AccountSnapshot,
	now time.Time,
	config TickConfig,
) (blocked bool, reason BlockReason) {
	return checkEligibilityWithItem(agent, accounts, now, config, false, QueueItemSnapshot{})
}

// checkEligibilityWithItem determines if an agent can receive dispatches,
// considering the queue item for special cases like permission responses.
func checkEligibilityWithItem(
	agent AgentSnapshot,
	accounts map[string]AccountSnapshot,
	now time.Time,
	config TickConfig,
	hasItem bool,
	item QueueItemSnapshot,
) (blocked bool, reason BlockReason) {
	// Check scheduler-level pause
	if agent.SchedulerPaused {
		return true, BlockReasonSchedulerPaused
	}

	// Check retry backoff
	if agent.RetryAfter != nil && now.Before(*agent.RetryAfter) {
		return true, BlockReasonRetryBackoff
	}

	// Check agent state
	if agent.State == models.AgentStatePaused {
		return true, BlockReasonPaused
	}
	if agent.State == models.AgentStateStopped {
		return true, BlockReasonStopped
	}

	// Special handling for AwaitingApproval state:
	// Only allow dispatch if the message is a permission response
	if agent.State == models.AgentStateAwaitingApproval {
		if !hasItem {
			return true, BlockReasonAwaitingApproval
		}
		if item.Type != models.QueueItemTypeMessage {
			return true, BlockReasonAwaitingApproval
		}
		// Check if this is a permission response message
		if !isPermissionResponse(item.Payload) {
			return true, BlockReasonAwaitingApproval
		}
		// Permission response can be dispatched - skip idle check
		goto checkAccountCooldown
	}

	// Check idle requirement (skip for permission responses handled above)
	if config.IdleStateRequired && agent.State != models.AgentStateIdle {
		return true, BlockReasonNotIdle
	}

checkAccountCooldown:
	// Check account cooldown
	if agent.AccountID != "" {
		if acc, ok := accounts[agent.AccountID]; ok {
			if acc.CooldownUntil != nil && now.Before(*acc.CooldownUntil) {
				return true, BlockReasonAccountCooldown
			}
		}
	}

	// Check queue length
	if agent.QueueLength <= 0 {
		return true, BlockReasonQueueEmpty
	}

	return false, BlockReasonNone
}

// isPermissionResponse checks if a message payload is a permission response.
func isPermissionResponse(payload json.RawMessage) bool {
	var msg models.MessagePayload
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	return msg.IsPermissionResponse
}

// processQueueItem determines actions for a specific queue item.
func processQueueItem(
	agent AgentSnapshot,
	item QueueItemSnapshot,
	accounts map[string]AccountSnapshot,
	now time.Time,
	config TickConfig,
) []TickAction {
	actions := make([]TickAction, 0, 2)

	switch item.Type {
	case models.QueueItemTypeMessage:
		actions = append(actions, processMessageItem(agent, item))

	case models.QueueItemTypePause:
		actions = append(actions, processPauseItem(agent, item))

	case models.QueueItemTypeConditional:
		actions = append(actions, processConditionalItem(agent, item, now, config)...)
	}

	return actions
}

// processMessageItem creates a dispatch action for a message.
func processMessageItem(agent AgentSnapshot, item QueueItemSnapshot) TickAction {
	var payload models.MessagePayload
	_ = json.Unmarshal(item.Payload, &payload)

	return TickAction{
		Type:     ActionTypeDispatch,
		AgentID:  agent.ID,
		ItemID:   item.ID,
		ItemType: models.QueueItemTypeMessage,
		Message:  payload.Text,
		Reason:   "message ready for dispatch",
	}
}

// processPauseItem creates a pause action.
func processPauseItem(agent AgentSnapshot, item QueueItemSnapshot) TickAction {
	var payload models.PausePayload
	_ = json.Unmarshal(item.Payload, &payload)

	return TickAction{
		Type:     ActionTypePause,
		AgentID:  agent.ID,
		ItemID:   item.ID,
		ItemType: models.QueueItemTypePause,
		Duration: time.Duration(payload.DurationSeconds) * time.Second,
		Reason:   payload.Reason,
	}
}

// processConditionalItem evaluates a condition and returns appropriate actions.
func processConditionalItem(
	agent AgentSnapshot,
	item QueueItemSnapshot,
	now time.Time,
	config TickConfig,
) []TickAction {
	var payload models.ConditionalPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return []TickAction{{
			Type:     ActionTypeSkip,
			AgentID:  agent.ID,
			ItemID:   item.ID,
			ItemType: models.QueueItemTypeConditional,
			Reason:   "invalid conditional payload",
		}}
	}

	// Check evaluation count limit
	if item.EvaluationCount >= config.MaxConditionalEvaluations {
		return []TickAction{{
			Type:     ActionTypeSkip,
			AgentID:  agent.ID,
			ItemID:   item.ID,
			ItemType: models.QueueItemTypeConditional,
			Reason:   "max evaluations exceeded",
		}}
	}

	// Evaluate the condition
	condCtx := ConditionContext{
		Agent: &models.Agent{
			ID:           agent.ID,
			State:        agent.State,
			QueueLength:  agent.QueueLength,
			LastActivity: agent.LastActivity,
			PausedUntil:  agent.PausedUntil,
		},
		QueueLength: agent.QueueLength,
		Now:         now,
	}

	evaluator := NewConditionEvaluator()
	result, err := evaluator.Evaluate(nil, condCtx, payload)
	if err != nil {
		return []TickAction{{
			Type:     ActionTypeSkip,
			AgentID:  agent.ID,
			ItemID:   item.ID,
			ItemType: models.QueueItemTypeConditional,
			Reason:   "condition evaluation error: " + err.Error(),
		}}
	}

	if !result.Met {
		return []TickAction{{
			Type:     ActionTypeRequeue,
			AgentID:  agent.ID,
			ItemID:   item.ID,
			ItemType: models.QueueItemTypeConditional,
			Reason:   result.Reason,
		}}
	}

	// Condition met - dispatch the message
	return []TickAction{{
		Type:     ActionTypeDispatch,
		AgentID:  agent.ID,
		ItemID:   item.ID,
		ItemType: models.QueueItemTypeConditional,
		Message:  payload.Message,
		Reason:   "condition met: " + result.Reason,
	}}
}

package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// QueueItemType specifies the type of queue item.
type QueueItemType string

const (
	QueueItemTypeMessage     QueueItemType = "message"
	QueueItemTypePause       QueueItemType = "pause"
	QueueItemTypeConditional QueueItemType = "conditional"
)

// QueueItemStatus represents the status of a queue item.
type QueueItemStatus string

const (
	QueueItemStatusPending    QueueItemStatus = "pending"
	QueueItemStatusDispatched QueueItemStatus = "dispatched"
	QueueItemStatusCompleted  QueueItemStatus = "completed"
	QueueItemStatusFailed     QueueItemStatus = "failed"
	QueueItemStatusSkipped    QueueItemStatus = "skipped"
)

// QueueItem represents an item in an agent's message queue.
type QueueItem struct {
	// ID is the unique identifier for the queue item.
	ID string `json:"id"`

	// AgentID references the agent this item belongs to.
	AgentID string `json:"agent_id"`

	// Type specifies the item type.
	Type QueueItemType `json:"type"`

	// Position is the order in the queue (lower = earlier).
	Position int `json:"position"`

	// Status is the current item status.
	Status QueueItemStatus `json:"status"`

	// Attempts is the number of dispatch attempts recorded.
	Attempts int `json:"attempts"`

	// EvaluationCount tracks how many times a conditional item has been evaluated.
	// Used to prevent infinite re-queue loops.
	EvaluationCount int `json:"evaluation_count,omitempty"`

	// Payload contains the item data (type-specific).
	Payload json.RawMessage `json:"payload"`

	// CreatedAt is when the item was queued.
	CreatedAt time.Time `json:"created_at"`

	// DispatchedAt is when the item was sent (if dispatched).
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`

	// CompletedAt is when the item completed (if finished).
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// Error contains error details (if failed).
	Error string `json:"error,omitempty"`
}

// MessagePayload is the payload for message queue items.
type MessagePayload struct {
	// Text is the message content to send.
	Text string `json:"text"`

	// IsPermissionResponse indicates this message is a response to a permission prompt.
	// When true, this message can be dispatched to agents in AwaitingApproval state.
	IsPermissionResponse bool `json:"is_permission_response,omitempty"`
}

// Validate checks if the message payload is valid.
func (p MessagePayload) Validate() error {
	validation := &ValidationErrors{}
	if strings.TrimSpace(p.Text) == "" {
		validation.AddMessage("text", "message text is required")
	}
	return validation.Err()
}

// PausePayload is the payload for pause queue items.
type PausePayload struct {
	// Duration is how long to pause in seconds.
	DurationSeconds int `json:"duration_seconds"`

	// Reason explains why the pause was inserted.
	Reason string `json:"reason,omitempty"`
}

// Validate checks if the pause payload is valid.
func (p PausePayload) Validate() error {
	validation := &ValidationErrors{}
	if p.DurationSeconds <= 0 {
		validation.AddMessage("duration_seconds", "duration_seconds must be greater than 0")
	}
	return validation.Err()
}

// ConditionType specifies the type of condition gate.
type ConditionType string

const (
	ConditionTypeWhenIdle         ConditionType = "when_idle"
	ConditionTypeAfterCooldown    ConditionType = "after_cooldown"
	ConditionTypeAfterPrevious    ConditionType = "after_previous"
	ConditionTypeCustomExpression ConditionType = "custom"
)

// ConditionalPayload is the payload for conditional queue items.
type ConditionalPayload struct {
	// ConditionType specifies the gate type.
	ConditionType ConditionType `json:"condition_type"`

	// Expression is a custom condition expression (for custom type).
	Expression string `json:"expression,omitempty"`

	// Message is the message to send when condition is satisfied.
	Message string `json:"message"`
}

// Validate checks if the conditional payload is valid.
func (p ConditionalPayload) Validate() error {
	validation := &ValidationErrors{}
	if p.ConditionType == "" {
		validation.AddMessage("condition_type", "condition_type is required")
	}
	if strings.TrimSpace(p.Message) == "" {
		validation.AddMessage("message", "message is required")
	}
	if p.ConditionType == ConditionTypeCustomExpression && strings.TrimSpace(p.Expression) == "" {
		validation.AddMessage("expression", "expression is required for custom condition type")
	}
	return validation.Err()
}

// Validate checks if the queue item is valid.
func (q *QueueItem) Validate() error {
	validation := &ValidationErrors{}
	if q.Type == "" {
		validation.AddMessage("type", "queue item type is required")
	}
	if q.Attempts < 0 {
		validation.AddMessage("attempts", "attempts must be greater than or equal to 0")
	}
	if len(q.Payload) == 0 {
		validation.Add("payload", ErrInvalidQueueItem)
	}

	if q.Type != "" && len(q.Payload) > 0 {
		switch q.Type {
		case QueueItemTypeMessage:
			var payload MessagePayload
			if err := json.Unmarshal(q.Payload, &payload); err != nil {
				validation.AddMessage("payload", fmt.Sprintf("invalid message payload: %v", err))
				break
			}
			validation.Add("payload", payload.Validate())
		case QueueItemTypePause:
			var payload PausePayload
			if err := json.Unmarshal(q.Payload, &payload); err != nil {
				validation.AddMessage("payload", fmt.Sprintf("invalid pause payload: %v", err))
				break
			}
			validation.Add("payload", payload.Validate())
		case QueueItemTypeConditional:
			var payload ConditionalPayload
			if err := json.Unmarshal(q.Payload, &payload); err != nil {
				validation.AddMessage("payload", fmt.Sprintf("invalid conditional payload: %v", err))
				break
			}
			validation.Add("payload", payload.Validate())
		default:
			validation.AddMessage("type", fmt.Sprintf("unknown queue item type %q", q.Type))
		}
	}

	return validation.Err()
}

// GetMessagePayload extracts the message payload.
func (q *QueueItem) GetMessagePayload() (*MessagePayload, error) {
	if q.Type != QueueItemTypeMessage {
		return nil, ErrInvalidQueueItem
	}
	var payload MessagePayload
	if err := json.Unmarshal(q.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// GetPausePayload extracts the pause payload.
func (q *QueueItem) GetPausePayload() (*PausePayload, error) {
	if q.Type != QueueItemTypePause {
		return nil, ErrInvalidQueueItem
	}
	var payload PausePayload
	if err := json.Unmarshal(q.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// GetConditionalPayload extracts the conditional payload.
func (q *QueueItem) GetConditionalPayload() (*ConditionalPayload, error) {
	if q.Type != QueueItemTypeConditional {
		return nil, ErrInvalidQueueItem
	}
	var payload ConditionalPayload
	if err := json.Unmarshal(q.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

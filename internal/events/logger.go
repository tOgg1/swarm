// Package events provides helper functions for logging Swarm events.
package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/opencode-ai/swarm/internal/models"
)

// Repository is the minimal interface needed to write events.
type Repository interface {
	Create(ctx context.Context, event *models.Event) error
}

// LogMessageDispatched records a message dispatch event for an agent.
func LogMessageDispatched(ctx context.Context, repo Repository, agentID, queueItemID, message string) error {
	if repo == nil {
		return fmt.Errorf("event repository is required")
	}
	if agentID == "" {
		return fmt.Errorf("agent id is required")
	}

	payload, err := json.Marshal(models.MessageDispatchedPayload{
		QueueItemID: queueItemID,
		Message:     message,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal dispatch payload: %w", err)
	}

	event := &models.Event{
		Type:       models.EventTypeMessageDispatched,
		EntityType: models.EntityTypeAgent,
		EntityID:   agentID,
		Payload:    payload,
	}

	return repo.Create(ctx, event)
}

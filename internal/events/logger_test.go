package events

import (
	"context"
	"testing"

	"github.com/opencode-ai/swarm/internal/models"
)

type fakeRepo struct {
	last *models.Event
}

func (r *fakeRepo) Create(ctx context.Context, event *models.Event) error {
	r.last = event
	return nil
}

func TestLogMessageDispatched(t *testing.T) {
	repo := &fakeRepo{}

	if err := LogMessageDispatched(context.Background(), repo, "agent-1", "queue-1", "hello"); err != nil {
		t.Fatalf("LogMessageDispatched failed: %v", err)
	}

	if repo.last == nil {
		t.Fatal("expected event to be created")
	}
	if repo.last.Type != models.EventTypeMessageDispatched {
		t.Fatalf("unexpected event type: %q", repo.last.Type)
	}
	if repo.last.EntityID != "agent-1" {
		t.Fatalf("unexpected entity id: %q", repo.last.EntityID)
	}
}

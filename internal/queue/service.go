// Package queue provides queue management for agent messages.
package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/logging"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/rs/zerolog"
)

// Service errors.
var (
	ErrQueueItemNotFound = errors.New("queue item not found")
	ErrQueueEmpty        = errors.New("queue is empty")
)

// QueueService defines the queue operations for agents.
type QueueService interface {
	Enqueue(ctx context.Context, agentID string, items ...*models.QueueItem) error
	Dequeue(ctx context.Context, agentID string) (*models.QueueItem, error)
	Peek(ctx context.Context, agentID string) (*models.QueueItem, error)
	List(ctx context.Context, agentID string) ([]*models.QueueItem, error)
	Reorder(ctx context.Context, agentID string, ordering []string) error
	Clear(ctx context.Context, agentID string) (int, error)
	InsertAt(ctx context.Context, agentID string, position int, item *models.QueueItem) error
	Remove(ctx context.Context, itemID string) error
}

// Service implements QueueService using a QueueRepository.
type Service struct {
	repo   *db.QueueRepository
	logger zerolog.Logger
}

// NewService creates a new QueueService.
func NewService(repo *db.QueueRepository) *Service {
	return &Service{
		repo:   repo,
		logger: logging.Component("queue"),
	}
}

// Enqueue adds items to the agent queue.
func (s *Service) Enqueue(ctx context.Context, agentID string, items ...*models.QueueItem) error {
	if err := s.repo.Enqueue(ctx, agentID, items...); err != nil {
		return fmt.Errorf("failed to enqueue items: %w", err)
	}
	return nil
}

// Dequeue removes and returns the next pending item.
func (s *Service) Dequeue(ctx context.Context, agentID string) (*models.QueueItem, error) {
	item, err := s.repo.Dequeue(ctx, agentID)
	if err != nil {
		if errors.Is(err, db.ErrQueueEmpty) {
			return nil, ErrQueueEmpty
		}
		return nil, fmt.Errorf("failed to dequeue item: %w", err)
	}
	return item, nil
}

// Peek returns the next pending item without removing it.
func (s *Service) Peek(ctx context.Context, agentID string) (*models.QueueItem, error) {
	item, err := s.repo.Peek(ctx, agentID)
	if err != nil {
		if errors.Is(err, db.ErrQueueEmpty) {
			return nil, ErrQueueEmpty
		}
		return nil, fmt.Errorf("failed to peek queue: %w", err)
	}
	return item, nil
}

// List returns all queue items for an agent.
func (s *Service) List(ctx context.Context, agentID string) ([]*models.QueueItem, error) {
	items, err := s.repo.List(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to list queue: %w", err)
	}
	return items, nil
}

// Reorder updates queue ordering based on the provided item IDs.
func (s *Service) Reorder(ctx context.Context, agentID string, ordering []string) error {
	if err := s.repo.Reorder(ctx, agentID, ordering); err != nil {
		return fmt.Errorf("failed to reorder queue: %w", err)
	}
	return nil
}

// Clear removes all pending items from the queue.
func (s *Service) Clear(ctx context.Context, agentID string) (int, error) {
	removed, err := s.repo.Clear(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("failed to clear queue: %w", err)
	}
	return removed, nil
}

// InsertAt inserts an item at a specific position.
func (s *Service) InsertAt(ctx context.Context, agentID string, position int, item *models.QueueItem) error {
	if err := s.repo.InsertAt(ctx, agentID, position, item); err != nil {
		return fmt.Errorf("failed to insert queue item: %w", err)
	}
	return nil
}

// Remove deletes an item by ID.
func (s *Service) Remove(ctx context.Context, itemID string) error {
	if err := s.repo.Remove(ctx, itemID); err != nil {
		if errors.Is(err, db.ErrQueueItemNotFound) {
			return ErrQueueItemNotFound
		}
		return fmt.Errorf("failed to remove queue item: %w", err)
	}
	return nil
}

var _ QueueService = (*Service)(nil)

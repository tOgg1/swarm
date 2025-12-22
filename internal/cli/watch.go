// Package cli provides the watch/streaming functionality for CLI commands.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
)

// ConnectionStatus represents the current state of the event stream connection.
type ConnectionStatus string

const (
	// ConnectionStatusConnected indicates the stream is actively receiving events.
	ConnectionStatusConnected ConnectionStatus = "connected"
	// ConnectionStatusReconnecting indicates the stream is attempting to reconnect.
	ConnectionStatusReconnecting ConnectionStatus = "reconnecting"
	// ConnectionStatusDisconnected indicates the stream has permanently disconnected.
	ConnectionStatusDisconnected ConnectionStatus = "disconnected"
)

// StatusCallback is called when the connection status changes.
type StatusCallback func(status ConnectionStatus, attempt int, nextRetry time.Duration, err error)

// ReconnectConfig configures reconnection behavior for event streams.
type ReconnectConfig struct {
	// Enabled enables automatic reconnection on errors.
	Enabled bool

	// MaxAttempts is the maximum number of reconnection attempts (0 = unlimited).
	MaxAttempts int

	// InitialBackoff is the initial delay before first retry.
	InitialBackoff time.Duration

	// MaxBackoff is the maximum delay between retries.
	MaxBackoff time.Duration

	// BackoffMultiplier is the factor by which backoff increases each retry.
	BackoffMultiplier float64

	// OnStatusChange is called when connection status changes.
	OnStatusChange StatusCallback
}

// DefaultReconnectConfig returns sensible defaults for reconnection.
func DefaultReconnectConfig() ReconnectConfig {
	return ReconnectConfig{
		Enabled:           true,
		MaxAttempts:       0, // unlimited
		InitialBackoff:    1 * time.Second,
		MaxBackoff:        30 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// StreamConfig configures event streaming behavior.
type StreamConfig struct {
	// PollInterval is how often to check for new events.
	PollInterval time.Duration

	// EventTypes filters to specific event types (nil = all).
	EventTypes []models.EventType

	// EntityTypes filters to specific entity types (nil = all).
	EntityTypes []models.EntityType

	// EntityID filters to a specific entity.
	EntityID string

	// Since streams events after this timestamp.
	Since *time.Time

	// IncludeExisting includes events before streaming starts.
	IncludeExisting bool

	// BatchSize is the max events per poll.
	BatchSize int

	// Reconnect configures automatic reconnection behavior.
	Reconnect ReconnectConfig
}

// DefaultStreamConfig returns sensible defaults for streaming.
func DefaultStreamConfig() StreamConfig {
	return StreamConfig{
		PollInterval:    500 * time.Millisecond,
		IncludeExisting: false,
		BatchSize:       100,
		Reconnect:       DefaultReconnectConfig(),
	}
}

// EventStreamer streams events to an output writer in JSONL format.
type EventStreamer struct {
	repo   *db.EventRepository
	out    io.Writer
	config StreamConfig
	logger func(string, ...any)
}

// NewEventStreamer creates a new event streamer.
func NewEventStreamer(repo *db.EventRepository, out io.Writer, config StreamConfig) *EventStreamer {
	if config.PollInterval == 0 {
		config.PollInterval = 500 * time.Millisecond
	}
	if config.BatchSize == 0 {
		config.BatchSize = 100
	}
	return &EventStreamer{
		repo:   repo,
		out:    out,
		config: config,
		logger: func(format string, args ...any) {
			if IsVerbose() {
				fmt.Fprintf(os.Stderr, format+"\n", args...)
			}
		},
	}
}

// Stream starts streaming events until the context is cancelled.
// Returns nil on graceful shutdown (Ctrl+C), error otherwise.
// If reconnection is enabled, temporary errors will trigger automatic retries.
func (s *EventStreamer) Stream(ctx context.Context) error {
	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigChan:
			s.logger("Received interrupt, shutting down...")
			cancel()
		case <-ctx.Done():
		}
	}()

	// Initialize cursor
	var cursor string
	var since *time.Time

	if s.config.IncludeExisting {
		// Start from the beginning or specified time
		since = s.config.Since
	} else {
		// Start from now
		now := time.Now().UTC()
		since = &now
	}

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	s.logger("Starting event stream (poll interval: %v)", s.config.PollInterval)
	s.notifyStatus(ConnectionStatusConnected, 0, 0, nil)

	// Track reconnection state
	var consecutiveErrors int
	var currentBackoff time.Duration

	for {
		select {
		case <-ctx.Done():
			s.notifyStatus(ConnectionStatusDisconnected, 0, 0, nil)
			return nil
		case <-ticker.C:
			events, nextCursor, err := s.poll(ctx, cursor, since)
			if err != nil {
				if ctx.Err() != nil {
					s.notifyStatus(ConnectionStatusDisconnected, 0, 0, nil)
					return nil // Context cancelled, graceful shutdown
				}

				// Handle error with reconnection logic
				if !s.config.Reconnect.Enabled {
					s.notifyStatus(ConnectionStatusDisconnected, 0, 0, err)
					return fmt.Errorf("failed to poll events: %w", err)
				}

				consecutiveErrors++

				// Check max attempts
				if s.config.Reconnect.MaxAttempts > 0 && consecutiveErrors > s.config.Reconnect.MaxAttempts {
					s.notifyStatus(ConnectionStatusDisconnected, consecutiveErrors, 0, err)
					return fmt.Errorf("max reconnection attempts (%d) exceeded: %w", s.config.Reconnect.MaxAttempts, err)
				}

				// Calculate backoff
				currentBackoff = s.calculateBackoff(consecutiveErrors, currentBackoff)
				s.notifyStatus(ConnectionStatusReconnecting, consecutiveErrors, currentBackoff, err)
				s.logger("Poll failed (attempt %d), retrying in %v: %v", consecutiveErrors, currentBackoff, err)

				// Wait before retrying
				if err := s.sleepWithContext(ctx, currentBackoff); err != nil {
					s.notifyStatus(ConnectionStatusDisconnected, consecutiveErrors, 0, nil)
					return nil // Context cancelled during backoff
				}

				continue
			}

			// Reset error state on successful poll
			if consecutiveErrors > 0 {
				s.logger("Reconnected successfully after %d attempts", consecutiveErrors)
				s.notifyStatus(ConnectionStatusConnected, 0, 0, nil)
				consecutiveErrors = 0
				currentBackoff = 0
			}

			for _, event := range events {
				if err := s.writeEvent(event); err != nil {
					return fmt.Errorf("failed to write event: %w", err)
				}
			}

			if nextCursor != "" {
				cursor = nextCursor
				since = nil // Use cursor-based pagination after first batch
			}
		}
	}
}

// calculateBackoff computes the next backoff duration using exponential backoff.
func (s *EventStreamer) calculateBackoff(attempt int, current time.Duration) time.Duration {
	rc := s.config.Reconnect

	if current == 0 {
		return rc.InitialBackoff
	}

	next := time.Duration(float64(current) * rc.BackoffMultiplier)
	if next > rc.MaxBackoff {
		next = rc.MaxBackoff
	}

	return next
}

// sleepWithContext sleeps for the given duration or until context is cancelled.
func (s *EventStreamer) sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// notifyStatus calls the status callback if configured.
func (s *EventStreamer) notifyStatus(status ConnectionStatus, attempt int, nextRetry time.Duration, err error) {
	if s.config.Reconnect.OnStatusChange != nil {
		s.config.Reconnect.OnStatusChange(status, attempt, nextRetry, err)
	}
}

// poll fetches the next batch of events.
func (s *EventStreamer) poll(ctx context.Context, cursor string, since *time.Time) ([]*models.Event, string, error) {
	query := db.EventQuery{
		Cursor: cursor,
		Since:  since,
		Limit:  s.config.BatchSize,
	}

	// Apply filters
	if len(s.config.EventTypes) == 1 {
		query.Type = &s.config.EventTypes[0]
	}
	if len(s.config.EntityTypes) == 1 {
		query.EntityType = &s.config.EntityTypes[0]
	}
	if s.config.EntityID != "" {
		query.EntityID = &s.config.EntityID
	}

	page, err := s.repo.Query(ctx, query)
	if err != nil {
		return nil, "", err
	}

	// Filter by multiple event types if specified
	var filtered []*models.Event
	if len(s.config.EventTypes) > 1 {
		typeSet := make(map[models.EventType]bool)
		for _, t := range s.config.EventTypes {
			typeSet[t] = true
		}
		for _, e := range page.Events {
			if typeSet[e.Type] {
				filtered = append(filtered, e)
			}
		}
	} else {
		filtered = page.Events
	}

	// Filter by multiple entity types if specified
	if len(s.config.EntityTypes) > 1 {
		typeSet := make(map[models.EntityType]bool)
		for _, t := range s.config.EntityTypes {
			typeSet[t] = true
		}
		var refiltered []*models.Event
		for _, e := range filtered {
			if typeSet[e.EntityType] {
				refiltered = append(refiltered, e)
			}
		}
		filtered = refiltered
	}

	return filtered, page.NextCursor, nil
}

// writeEvent writes a single event as JSONL.
func (s *EventStreamer) writeEvent(event *models.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(s.out, string(data))
	return err
}

// StreamEvents is a convenience function to stream events with default config.
// It blocks until Ctrl+C or context cancellation.
func StreamEvents(ctx context.Context, repo *db.EventRepository, out io.Writer) error {
	streamer := NewEventStreamer(repo, out, DefaultStreamConfig())
	return streamer.Stream(ctx)
}

// StreamEventsWithFilter streams events matching the given filters.
func StreamEventsWithFilter(
	ctx context.Context,
	repo *db.EventRepository,
	out io.Writer,
	eventTypes []models.EventType,
	entityTypes []models.EntityType,
	entityID string,
) error {
	config := DefaultStreamConfig()
	config.EventTypes = eventTypes
	config.EntityTypes = entityTypes
	config.EntityID = entityID

	streamer := NewEventStreamer(repo, out, config)
	return streamer.Stream(ctx)
}

// WatchHelper provides a standard way for commands to implement --watch mode.
// It returns true if watch mode is active and the command should stream.
func WatchHelper(ctx context.Context, repo *db.EventRepository, entityType models.EntityType, entityID string) error {
	if !IsWatchMode() {
		return nil
	}

	config := DefaultStreamConfig()
	config.EntityTypes = []models.EntityType{entityType}
	if entityID != "" {
		config.EntityID = entityID
	}

	streamer := NewEventStreamer(repo, os.Stdout, config)
	return streamer.Stream(ctx)
}

// MustBeJSONLForWatch ensures JSONL mode is used with --watch.
// Returns an error if --watch is used without --jsonl.
func MustBeJSONLForWatch() error {
	if IsWatchMode() && !IsJSONLOutput() {
		return fmt.Errorf("--watch requires --jsonl output format")
	}
	return nil
}

// ParseSince parses a duration string or timestamp and returns the corresponding time.
// Supports:
//   - Relative durations: "1h", "30m", "24h", "7d" (days are 24h)
//   - ISO 8601 timestamps: "2024-01-15T10:30:00Z"
//   - RFC3339 timestamps: "2024-01-15T10:30:00-05:00"
//   - Simple date: "2024-01-15"
//
// Returns nil if the input is empty.
func ParseSince(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}

	s = strings.TrimSpace(s)

	// Try parsing as a duration with optional 'd' for days
	if dur, err := parseDurationWithDays(s); err == nil {
		t := time.Now().UTC().Add(-dur)
		return &t, nil
	}

	// Try RFC3339 (includes ISO 8601 with timezone)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		utc := t.UTC()
		return &utc, nil
	}

	// Try RFC3339Nano for high precision timestamps
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		utc := t.UTC()
		return &utc, nil
	}

	// Try simple date format
	if t, err := time.Parse("2006-01-02", s); err == nil {
		utc := t.UTC()
		return &utc, nil
	}

	// Try date with time (no timezone - assume UTC)
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return &t, nil
	}

	return nil, fmt.Errorf("invalid time format: %q (use duration like '1h' or timestamp like '2024-01-15T10:30:00Z')", s)
}

// parseDurationWithDays parses a duration string, supporting 'd' suffix for days.
func parseDurationWithDays(s string) (time.Duration, error) {
	// Handle 'd' suffix for days
	if strings.HasSuffix(s, "d") {
		dayStr := strings.TrimSuffix(s, "d")
		var days float64
		if _, err := fmt.Sscanf(dayStr, "%f", &days); err != nil {
			return 0, err
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}

	// Standard Go duration parsing
	return time.ParseDuration(s)
}

// GetSinceTime parses the --since flag and returns the corresponding time.
// Returns nil if --since was not specified.
func GetSinceTime() (*time.Time, error) {
	return ParseSince(GetSinceFlag())
}

// StreamEventsWithReplay streams events starting from a specific timestamp.
// It first replays historical events since the given time, then continues
// streaming live events.
func StreamEventsWithReplay(
	ctx context.Context,
	repo *db.EventRepository,
	out io.Writer,
	since *time.Time,
	eventTypes []models.EventType,
	entityTypes []models.EntityType,
	entityID string,
) error {
	config := DefaultStreamConfig()
	config.EventTypes = eventTypes
	config.EntityTypes = entityTypes
	config.EntityID = entityID

	if since != nil {
		config.Since = since
		config.IncludeExisting = true
	}

	streamer := NewEventStreamer(repo, out, config)
	return streamer.Stream(ctx)
}

// WatchHelperWithSince provides a standard way for commands to implement --watch mode
// with support for replay from a timestamp via the --since flag.
func WatchHelperWithSince(ctx context.Context, repo *db.EventRepository, entityType models.EntityType, entityID string) error {
	if !IsWatchMode() {
		return nil
	}

	since, err := GetSinceTime()
	if err != nil {
		return fmt.Errorf("invalid --since value: %w", err)
	}

	config := DefaultStreamConfig()
	config.EntityTypes = []models.EntityType{entityType}
	if entityID != "" {
		config.EntityID = entityID
	}

	if since != nil {
		config.Since = since
		config.IncludeExisting = true
	}

	streamer := NewEventStreamer(repo, os.Stdout, config)
	return streamer.Stream(ctx)
}

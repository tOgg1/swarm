// Package db provides SQLite database access for Swarm.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/swarm/internal/models"
)

// Event repository errors.
var (
	ErrEventNotFound = errors.New("event not found")
	ErrInvalidEvent  = errors.New("invalid event")
)

// EventRepository handles event persistence.
type EventRepository struct {
	db *DB
}

type eventExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// NewEventRepository creates a new EventRepository.
func NewEventRepository(db *DB) *EventRepository {
	return &EventRepository{db: db}
}

// EventQuery defines filters for querying events.
type EventQuery struct {
	Type       *models.EventType  // Filter by event type
	EntityType *models.EntityType // Filter by entity type
	EntityID   *string            // Filter by entity ID
	Since      *time.Time         // Events at or after this time (inclusive)
	Until      *time.Time         // Events before this time (exclusive)
	Cursor     string             // Pagination cursor (event ID)
	Limit      int                // Max results to return
}

// EventPage represents a page of query results.
type EventPage struct {
	Events     []*models.Event
	NextCursor string
}

// Append adds a new event to the event log.
// Returns ErrInvalidEvent if required fields are missing.
func (r *EventRepository) Append(ctx context.Context, event *models.Event) error {
	if event.Type == "" || event.EntityType == "" || event.EntityID == "" {
		return ErrInvalidEvent
	}
	return r.Create(ctx, event)
}

// Create appends a new event to the event log.
func (r *EventRepository) Create(ctx context.Context, event *models.Event) error {
	return r.createWithExecutor(ctx, r.db, event)
}

// CreateWithTx appends a new event using an existing transaction.
func (r *EventRepository) CreateWithTx(ctx context.Context, tx *sql.Tx, event *models.Event) error {
	if tx == nil {
		return fmt.Errorf("transaction is required")
	}
	return r.createWithExecutor(ctx, tx, event)
}

func (r *EventRepository) createWithExecutor(ctx context.Context, execer eventExecer, event *models.Event) error {
	if event.Type == "" {
		return fmt.Errorf("event type is required")
	}
	if event.EntityType == "" {
		return fmt.Errorf("event entity type is required")
	}
	if event.EntityID == "" {
		return fmt.Errorf("event entity id is required")
	}

	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}

	var payloadJSON *string
	if len(event.Payload) > 0 {
		s := string(event.Payload)
		payloadJSON = &s
	}

	var metadataJSON *string
	if event.Metadata != nil {
		data, err := json.Marshal(event.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
		s := string(data)
		metadataJSON = &s
	}

	_, err := execer.ExecContext(ctx, `
		INSERT INTO events (
			id, timestamp, type, entity_type, entity_id, payload_json, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		event.ID,
		event.Timestamp.Format(time.RFC3339),
		string(event.Type),
		string(event.EntityType),
		event.EntityID,
		payloadJSON,
		metadataJSON,
	)
	if err != nil {
		return fmt.Errorf("failed to insert event: %w", err)
	}

	return nil
}

// Get retrieves an event by ID.
func (r *EventRepository) Get(ctx context.Context, id string) (*models.Event, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, timestamp, type, entity_type, entity_id, payload_json, metadata_json
		FROM events WHERE id = ?
	`, id)

	return r.scanEvent(row)
}

// Query retrieves events matching the given filters with cursor-based pagination.
func (r *EventRepository) Query(ctx context.Context, q EventQuery) (*EventPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}

	// Build query dynamically
	query := `SELECT id, timestamp, type, entity_type, entity_id, payload_json, metadata_json FROM events WHERE 1=1`
	args := []any{}

	if q.Type != nil {
		query += ` AND type = ?`
		args = append(args, string(*q.Type))
	}
	if q.EntityType != nil {
		query += ` AND entity_type = ?`
		args = append(args, string(*q.EntityType))
	}
	if q.EntityID != nil {
		query += ` AND entity_id = ?`
		args = append(args, *q.EntityID)
	}
	if q.Since != nil {
		query += ` AND timestamp >= ?`
		args = append(args, q.Since.UTC().Format(time.RFC3339))
	}
	if q.Until != nil {
		query += ` AND timestamp < ?`
		args = append(args, q.Until.UTC().Format(time.RFC3339))
	}
	if q.Cursor != "" {
		// Cursor is the last event ID; fetch events with timestamp >= cursor's timestamp
		// but exclude events with same timestamp and id <= cursor
		query += ` AND (timestamp, id) > (SELECT timestamp, id FROM events WHERE id = ?)`
		args = append(args, q.Cursor)
	}

	query += ` ORDER BY timestamp, id LIMIT ?`
	args = append(args, limit+1) // Fetch one extra to determine if there's a next page

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	var events []*models.Event
	for rows.Next() {
		event, err := r.scanEventFromRows(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating events: %w", err)
	}

	page := &EventPage{}
	if len(events) > limit {
		// There's a next page
		page.Events = events[:limit]
		page.NextCursor = events[limit-1].ID
	} else {
		page.Events = events
	}

	return page, nil
}

// ListByEntity retrieves events for an entity, ordered by timestamp.
func (r *EventRepository) ListByEntity(ctx context.Context, entityType models.EntityType, entityID string, limit int) ([]*models.Event, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, timestamp, type, entity_type, entity_id, payload_json, metadata_json
		FROM events
		WHERE entity_type = ? AND entity_id = ?
		ORDER BY timestamp
		LIMIT ?
	`, string(entityType), entityID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	var events []*models.Event
	for rows.Next() {
		event, err := r.scanEventFromRows(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating events: %w", err)
	}

	return events, nil
}

func (r *EventRepository) scanEvent(row *sql.Row) (*models.Event, error) {
	var event models.Event
	var timestamp, eventType, entityType string
	var payloadJSON sql.NullString
	var metadataJSON sql.NullString

	err := row.Scan(
		&event.ID,
		&timestamp,
		&eventType,
		&entityType,
		&event.EntityID,
		&payloadJSON,
		&metadataJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrEventNotFound
		}
		return nil, fmt.Errorf("failed to scan event: %w", err)
	}

	event.Type = models.EventType(eventType)
	event.EntityType = models.EntityType(entityType)

	if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
		event.Timestamp = t
	}

	if payloadJSON.Valid {
		event.Payload = json.RawMessage(payloadJSON.String)
	}
	if metadataJSON.Valid {
		if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
			r.db.logger.Warn().Err(err).Str("event_id", event.ID).Msg("failed to parse event metadata")
		}
	}

	return &event, nil
}

func (r *EventRepository) scanEventFromRows(rows *sql.Rows) (*models.Event, error) {
	var event models.Event
	var timestamp, eventType, entityType string
	var payloadJSON sql.NullString
	var metadataJSON sql.NullString

	if err := rows.Scan(
		&event.ID,
		&timestamp,
		&eventType,
		&entityType,
		&event.EntityID,
		&payloadJSON,
		&metadataJSON,
	); err != nil {
		return nil, fmt.Errorf("failed to scan event: %w", err)
	}

	event.Type = models.EventType(eventType)
	event.EntityType = models.EntityType(entityType)

	if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
		event.Timestamp = t
	}

	if payloadJSON.Valid {
		event.Payload = json.RawMessage(payloadJSON.String)
	}
	if metadataJSON.Valid {
		if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
			r.db.logger.Warn().Err(err).Str("event_id", event.ID).Msg("failed to parse event metadata")
		}
	}

	return &event, nil
}

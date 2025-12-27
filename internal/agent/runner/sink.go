package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/swarm/internal/db"
	"github.com/opencode-ai/swarm/internal/models"
)

// EventSink receives runner events.
type EventSink interface {
	Emit(ctx context.Context, event RunnerEvent) error
	Close() error
}

// NoopSink drops all events.
type NoopSink struct{}

// Emit ignores events.
func (NoopSink) Emit(ctx context.Context, event RunnerEvent) error {
	return nil
}

// Close is a no-op.
func (NoopSink) Close() error {
	return nil
}

// SocketEventSink streams events over a unix socket as JSON lines.
type SocketEventSink struct {
	mu      sync.Mutex
	conn    net.Conn
	encoder *json.Encoder
	closed  bool
}

// NewSocketEventSink connects to the unix socket at the given path.
func NewSocketEventSink(path string) (*SocketEventSink, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("event socket path is required")
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}

	return &SocketEventSink{
		conn:    conn,
		encoder: json.NewEncoder(conn),
	}, nil
}

// Emit writes an event to the socket.
func (s *SocketEventSink) Emit(ctx context.Context, event RunnerEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("event socket closed")
	}
	return s.encoder.Encode(event)
}

// Close closes the socket connection.
func (s *SocketEventSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

// DatabaseEventSink writes events to the SQLite event log.
type DatabaseEventSink struct {
	mu          sync.Mutex
	repo        *db.EventRepository
	database    *db.DB
	workspaceID string
	agentID     string
}

// NewDatabaseEventSink creates a database-backed event sink.
func NewDatabaseEventSink(database *db.DB, workspaceID, agentID string) *DatabaseEventSink {
	var repo *db.EventRepository
	if database != nil {
		repo = db.NewEventRepository(database)
	}

	return &DatabaseEventSink{
		repo:        repo,
		database:    database,
		workspaceID: workspaceID,
		agentID:     agentID,
	}
}

// Emit persists an event to the event repository.
func (s *DatabaseEventSink) Emit(ctx context.Context, event RunnerEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.repo == nil {
		return errors.New("event repository is required")
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	payload, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}

	metadata := map[string]string{}
	if s.workspaceID != "" {
		metadata["workspace_id"] = s.workspaceID
	}

	typeValue := runnerEventType(event.Type)

	modelEvent := &models.Event{
		Timestamp:  event.Timestamp,
		Type:       typeValue,
		EntityType: models.EntityTypeAgent,
		EntityID:   s.agentID,
		Payload:    payload,
		Metadata:   metadata,
	}

	return s.repo.Create(ctx, modelEvent)
}

// Close closes the underlying database connection if present.
func (s *DatabaseEventSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.database != nil {
		return s.database.Close()
	}
	return nil
}

func runnerEventType(eventType string) models.EventType {
	trimmed := strings.TrimSpace(eventType)
	if strings.HasPrefix(trimmed, "runner.") {
		return models.EventType(trimmed)
	}
	if trimmed == "" {
		return models.EventType("runner.unknown")
	}
	return models.EventType("runner." + trimmed)
}

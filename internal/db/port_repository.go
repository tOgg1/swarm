// Package db provides SQLite database access for Swarm.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Port allocation constants.
const (
	// DefaultPortRangeStart is the start of the default port range for OpenCode servers.
	DefaultPortRangeStart = 17000
	// DefaultPortRangeEnd is the end of the default port range for OpenCode servers.
	DefaultPortRangeEnd = 17999
)

// Port repository errors.
var (
	ErrNoAvailablePorts     = errors.New("no available ports in range")
	ErrPortAlreadyAllocated = errors.New("port already allocated")
	ErrPortNotAllocated     = errors.New("port not allocated")
)

// PortAllocation represents a port allocation record.
type PortAllocation struct {
	ID          int64
	Port        int
	NodeID      string
	AgentID     *string
	Reason      string
	AllocatedAt time.Time
}

// PortRepository handles port allocation persistence.
type PortRepository struct {
	db         *DB
	rangeStart int
	rangeEnd   int
}

// NewPortRepository creates a new PortRepository with default port range.
func NewPortRepository(db *DB) *PortRepository {
	return &PortRepository{
		db:         db,
		rangeStart: DefaultPortRangeStart,
		rangeEnd:   DefaultPortRangeEnd,
	}
}

// NewPortRepositoryWithRange creates a new PortRepository with custom port range.
func NewPortRepositoryWithRange(db *DB, start, end int) *PortRepository {
	return &PortRepository{
		db:         db,
		rangeStart: start,
		rangeEnd:   end,
	}
}

// Allocate finds an available port for the given node and allocates it.
// Returns the allocated port number.
func (r *PortRepository) Allocate(ctx context.Context, nodeID, agentID, reason string) (int, error) {
	var port int
	var err error

	// Use a transaction to ensure atomicity
	err = r.db.Transaction(ctx, func(tx *sql.Tx) error {
		// Find the first available port in range
		// A port is available if it has no allocation record or its allocation is released
		port, err = r.findAvailablePort(ctx, tx, nodeID)
		if err != nil {
			return err
		}

		// Create the allocation
		now := time.Now().UTC().Format(time.RFC3339)
		var agentIDPtr *string
		if agentID != "" {
			agentIDPtr = &agentID
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO port_allocations (port, node_id, agent_id, reason, allocated_at)
			VALUES (?, ?, ?, ?, ?)
		`, port, nodeID, agentIDPtr, reason, now)
		if err != nil {
			return fmt.Errorf("failed to insert port allocation: %w", err)
		}

		return nil
	})

	if err != nil {
		return 0, err
	}

	return port, nil
}

// AllocateSpecific allocates a specific port for the given node.
// Returns an error if the port is already in use.
func (r *PortRepository) AllocateSpecific(ctx context.Context, nodeID string, port int, agentID, reason string) error {
	if port < r.rangeStart || port > r.rangeEnd {
		return fmt.Errorf("port %d is outside valid range %d-%d", port, r.rangeStart, r.rangeEnd)
	}

	// Check if port is available
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM port_allocations 
		WHERE node_id = ? AND port = ? AND released_at IS NULL
	`, nodeID, port).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check port availability: %w", err)
	}

	if count > 0 {
		return ErrPortAlreadyAllocated
	}

	// Create the allocation
	now := time.Now().UTC().Format(time.RFC3339)
	var agentIDPtr *string
	if agentID != "" {
		agentIDPtr = &agentID
	}

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO port_allocations (port, node_id, agent_id, reason, allocated_at)
		VALUES (?, ?, ?, ?, ?)
	`, port, nodeID, agentIDPtr, reason, now)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrPortAlreadyAllocated
		}
		return fmt.Errorf("failed to insert port allocation: %w", err)
	}

	return nil
}

// Release releases a port allocation by deleting it.
func (r *PortRepository) Release(ctx context.Context, nodeID string, port int) error {
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM port_allocations 
		WHERE node_id = ? AND port = ?
	`, nodeID, port)
	if err != nil {
		return fmt.Errorf("failed to release port: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrPortNotAllocated
	}

	return nil
}

// ReleaseByAgent releases all ports allocated to a specific agent.
func (r *PortRepository) ReleaseByAgent(ctx context.Context, agentID string) (int, error) {
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM port_allocations 
		WHERE agent_id = ?
	`, agentID)
	if err != nil {
		return 0, fmt.Errorf("failed to release agent ports: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return int(rowsAffected), nil
}

// GetByAgent retrieves the active port allocation for an agent.
func (r *PortRepository) GetByAgent(ctx context.Context, agentID string) (*PortAllocation, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, port, node_id, agent_id, reason, allocated_at
		FROM port_allocations
		WHERE agent_id = ?
		ORDER BY allocated_at DESC
		LIMIT 1
	`, agentID)

	return r.scanAllocation(row)
}

// GetByNodeAndPort retrieves an active port allocation by node and port.
func (r *PortRepository) GetByNodeAndPort(ctx context.Context, nodeID string, port int) (*PortAllocation, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, port, node_id, agent_id, reason, allocated_at
		FROM port_allocations
		WHERE node_id = ? AND port = ?
	`, nodeID, port)

	return r.scanAllocation(row)
}

// ListActiveByNode retrieves all active port allocations for a node.
func (r *PortRepository) ListActiveByNode(ctx context.Context, nodeID string) ([]*PortAllocation, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, port, node_id, agent_id, reason, allocated_at, released_at
		FROM port_allocations
		WHERE node_id = ? AND released_at IS NULL
		ORDER BY port
	`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query port allocations: %w", err)
	}
	defer rows.Close()

	return r.scanAllocations(rows)
}

// CountActiveByNode returns the number of active port allocations for a node.
func (r *PortRepository) CountActiveByNode(ctx context.Context, nodeID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM port_allocations
		WHERE node_id = ? AND released_at IS NULL
	`, nodeID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count port allocations: %w", err)
	}
	return count, nil
}

// IsPortAvailable checks if a specific port is available on a node.
func (r *PortRepository) IsPortAvailable(ctx context.Context, nodeID string, port int) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM port_allocations
		WHERE node_id = ? AND port = ? AND released_at IS NULL
	`, nodeID, port).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check port availability: %w", err)
	}
	return count == 0, nil
}

// CleanupExpired releases allocations for agents that no longer exist.
// This handles orphaned allocations from crashed agents.
func (r *PortRepository) CleanupExpired(ctx context.Context) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	// Release allocations where the agent no longer exists
	result, err := r.db.ExecContext(ctx, `
		UPDATE port_allocations 
		SET released_at = ?
		WHERE agent_id IS NOT NULL 
		AND released_at IS NULL
		AND agent_id NOT IN (SELECT id FROM agents)
	`, now)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup expired allocations: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return int(rowsAffected), nil
}

// findAvailablePort finds the first available port in the configured range.
func (r *PortRepository) findAvailablePort(ctx context.Context, tx *sql.Tx, nodeID string) (int, error) {
	// Get all allocated (non-released) ports for this node
	rows, err := tx.QueryContext(ctx, `
		SELECT port FROM port_allocations
		WHERE node_id = ? AND released_at IS NULL
		ORDER BY port
	`, nodeID)
	if err != nil {
		return 0, fmt.Errorf("failed to query allocated ports: %w", err)
	}
	defer rows.Close()

	// Build a set of allocated ports
	allocated := make(map[int]bool)
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			return 0, fmt.Errorf("failed to scan port: %w", err)
		}
		allocated[port] = true
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("error iterating ports: %w", err)
	}

	// Find the first available port in range
	for port := r.rangeStart; port <= r.rangeEnd; port++ {
		if !allocated[port] {
			return port, nil
		}
	}

	return 0, ErrNoAvailablePorts
}

func (r *PortRepository) scanAllocation(row *sql.Row) (*PortAllocation, error) {
	var alloc PortAllocation
	var agentID sql.NullString
	var reason sql.NullString
	var allocatedAt string
	var releasedAt sql.NullString

	err := row.Scan(
		&alloc.ID,
		&alloc.Port,
		&alloc.NodeID,
		&agentID,
		&reason,
		&allocatedAt,
		&releasedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPortNotAllocated
		}
		return nil, fmt.Errorf("failed to scan port allocation: %w", err)
	}

	if agentID.Valid {
		alloc.AgentID = &agentID.String
	}
	if reason.Valid {
		alloc.Reason = reason.String
	}
	if t, err := time.Parse(time.RFC3339, allocatedAt); err == nil {
		alloc.AllocatedAt = t
	}
	if releasedAt.Valid {
		if t, err := time.Parse(time.RFC3339, releasedAt.String); err == nil {
			alloc.ReleasedAt = &t
		}
	}

	return &alloc, nil
}

func (r *PortRepository) scanAllocations(rows *sql.Rows) ([]*PortAllocation, error) {
	var allocations []*PortAllocation

	for rows.Next() {
		var alloc PortAllocation
		var agentID sql.NullString
		var reason sql.NullString
		var allocatedAt string
		var releasedAt sql.NullString

		err := rows.Scan(
			&alloc.ID,
			&alloc.Port,
			&alloc.NodeID,
			&agentID,
			&reason,
			&allocatedAt,
			&releasedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan port allocation: %w", err)
		}

		if agentID.Valid {
			alloc.AgentID = &agentID.String
		}
		if reason.Valid {
			alloc.Reason = reason.String
		}
		if t, err := time.Parse(time.RFC3339, allocatedAt); err == nil {
			alloc.AllocatedAt = t
		}
		if releasedAt.Valid {
			if t, err := time.Parse(time.RFC3339, releasedAt.String); err == nil {
				alloc.ReleasedAt = &t
			}
		}

		allocations = append(allocations, &alloc)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating port allocations: %w", err)
	}

	return allocations, nil
}

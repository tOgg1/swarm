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

// Agent repository errors.
var (
	ErrAgentNotFound      = errors.New("agent not found")
	ErrAgentAlreadyExists = errors.New("agent with this workspace and pane already exists")
)

// AgentRepository handles agent persistence.
type AgentRepository struct {
	db *DB
}

type agentExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// NewAgentRepository creates a new AgentRepository.
func NewAgentRepository(db *DB) *AgentRepository {
	return &AgentRepository{db: db}
}

// Create adds a new agent to the database.
func (r *AgentRepository) Create(ctx context.Context, agent *models.Agent) error {
	if err := agent.Validate(); err != nil {
		return fmt.Errorf("invalid agent: %w", err)
	}

	if agent.ID == "" {
		agent.ID = uuid.New().String()
	}

	now := time.Now().UTC()
	agent.CreatedAt = now
	agent.UpdatedAt = now

	state, confidence := normalizeAgentState(agent)
	agent.State = state
	agent.StateInfo.State = state
	agent.StateInfo.Confidence = confidence

	metadataJSON, err := json.Marshal(agent.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	var accountID *string
	if agent.AccountID != "" {
		accountID = &agent.AccountID
	}

	stateDetectedAt := timePtrFrom(agent.StateInfo.DetectedAt)
	pausedUntil := stringTimePtr(agent.PausedUntil)
	lastActivity := stringTimePtr(agent.LastActivity)

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO agents (
			id, workspace_id, type, tmux_pane, account_id,
			state, state_confidence, state_reason, state_detected_at,
			paused_until, last_activity_at, metadata_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		agent.ID,
		agent.WorkspaceID,
		string(agent.Type),
		agent.TmuxPane,
		accountID,
		string(agent.State),
		string(agent.StateInfo.Confidence),
		agent.StateInfo.Reason,
		stateDetectedAt,
		pausedUntil,
		lastActivity,
		string(metadataJSON),
		agent.CreatedAt.Format(time.RFC3339),
		agent.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrAgentAlreadyExists
		}
		return fmt.Errorf("failed to insert agent: %w", err)
	}

	return nil
}

// Get retrieves an agent by ID.
func (r *AgentRepository) Get(ctx context.Context, id string) (*models.Agent, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT
			id, workspace_id, type, tmux_pane, account_id,
			state, state_confidence, state_reason, state_detected_at,
			paused_until, last_activity_at, metadata_json,
			created_at, updated_at
		FROM agents WHERE id = ?
	`, id)

	return r.scanAgent(row)
}

// List retrieves all agents.
func (r *AgentRepository) List(ctx context.Context) ([]*models.Agent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			id, workspace_id, type, tmux_pane, account_id,
			state, state_confidence, state_reason, state_detected_at,
			paused_until, last_activity_at, metadata_json,
			created_at, updated_at
		FROM agents ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query agents: %w", err)
	}
	defer rows.Close()

	return r.scanAgents(rows)
}

// ListByWorkspace retrieves agents for a specific workspace.
func (r *AgentRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]*models.Agent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			id, workspace_id, type, tmux_pane, account_id,
			state, state_confidence, state_reason, state_detected_at,
			paused_until, last_activity_at, metadata_json,
			created_at, updated_at
		FROM agents WHERE workspace_id = ?
		ORDER BY created_at
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query agents by workspace: %w", err)
	}
	defer rows.Close()

	return r.scanAgents(rows)
}

// ListByState retrieves agents with a specific state.
func (r *AgentRepository) ListByState(ctx context.Context, state models.AgentState) ([]*models.Agent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			id, workspace_id, type, tmux_pane, account_id,
			state, state_confidence, state_reason, state_detected_at,
			paused_until, last_activity_at, metadata_json,
			created_at, updated_at
		FROM agents WHERE state = ?
		ORDER BY created_at
	`, string(state))
	if err != nil {
		return nil, fmt.Errorf("failed to query agents by state: %w", err)
	}
	defer rows.Close()

	return r.scanAgents(rows)
}

// ListWithQueueLength retrieves all agents with queue length counts.
func (r *AgentRepository) ListWithQueueLength(ctx context.Context) ([]*models.Agent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			a.id, a.workspace_id, a.type, a.tmux_pane, a.account_id,
			a.state, a.state_confidence, a.state_reason, a.state_detected_at,
			a.paused_until, a.last_activity_at, a.metadata_json,
			a.created_at, a.updated_at,
			COUNT(q.id) AS queue_length
		FROM agents a
		LEFT JOIN queue_items q
			ON q.agent_id = a.id
			AND q.status = 'pending'
		GROUP BY a.id
		ORDER BY a.created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query agents with queue length: %w", err)
	}
	defer rows.Close()

	return r.scanAgentsWithQueueLength(rows)
}

// Update updates an existing agent.
func (r *AgentRepository) Update(ctx context.Context, agent *models.Agent) error {
	return r.updateWithExecutor(ctx, r.db, agent)
}

// UpdateWithTx updates an agent using an existing transaction.
func (r *AgentRepository) UpdateWithTx(ctx context.Context, tx *sql.Tx, agent *models.Agent) error {
	if tx == nil {
		return fmt.Errorf("transaction is required")
	}
	return r.updateWithExecutor(ctx, tx, agent)
}

// UpdateWithEvent updates an agent and creates an event atomically.
func (r *AgentRepository) UpdateWithEvent(ctx context.Context, agent *models.Agent, event *models.Event, eventRepo *EventRepository) error {
	if event == nil || eventRepo == nil {
		return r.Update(ctx, agent)
	}

	return r.db.Transaction(ctx, func(tx *sql.Tx) error {
		if err := r.updateWithExecutor(ctx, tx, agent); err != nil {
			return err
		}
		if err := eventRepo.CreateWithTx(ctx, tx, event); err != nil {
			return err
		}
		return nil
	})
}

func (r *AgentRepository) updateWithExecutor(ctx context.Context, execer agentExecer, agent *models.Agent) error {
	if err := agent.Validate(); err != nil {
		return fmt.Errorf("invalid agent: %w", err)
	}

	agent.UpdatedAt = time.Now().UTC()
	state, confidence := normalizeAgentState(agent)
	agent.State = state
	agent.StateInfo.State = state
	agent.StateInfo.Confidence = confidence

	metadataJSON, err := json.Marshal(agent.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	var accountID *string
	if agent.AccountID != "" {
		accountID = &agent.AccountID
	}

	stateDetectedAt := timePtrFrom(agent.StateInfo.DetectedAt)
	pausedUntil := stringTimePtr(agent.PausedUntil)
	lastActivity := stringTimePtr(agent.LastActivity)

	result, err := execer.ExecContext(ctx, `
		UPDATE agents SET
			workspace_id = ?,
			type = ?,
			tmux_pane = ?,
			account_id = ?,
			state = ?,
			state_confidence = ?,
			state_reason = ?,
			state_detected_at = ?,
			paused_until = ?,
			last_activity_at = ?,
			metadata_json = ?,
			updated_at = ?
		WHERE id = ?
	`,
		agent.WorkspaceID,
		string(agent.Type),
		agent.TmuxPane,
		accountID,
		string(agent.State),
		string(agent.StateInfo.Confidence),
		agent.StateInfo.Reason,
		stateDetectedAt,
		pausedUntil,
		lastActivity,
		string(metadataJSON),
		agent.UpdatedAt.Format(time.RFC3339),
		agent.ID,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrAgentAlreadyExists
		}
		return fmt.Errorf("failed to update agent: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrAgentNotFound
	}

	return nil
}

// Delete removes an agent by ID.
func (r *AgentRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete agent: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrAgentNotFound
	}

	return nil
}

func (r *AgentRepository) scanAgent(row *sql.Row) (*models.Agent, error) {
	var agent models.Agent
	var agentType, state, confidence string
	var accountID, stateReason, stateDetectedAt sql.NullString
	var pausedUntil, lastActivity sql.NullString
	var metadataJSON sql.NullString
	var createdAt, updatedAt string

	err := row.Scan(
		&agent.ID,
		&agent.WorkspaceID,
		&agentType,
		&agent.TmuxPane,
		&accountID,
		&state,
		&confidence,
		&stateReason,
		&stateDetectedAt,
		&pausedUntil,
		&lastActivity,
		&metadataJSON,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAgentNotFound
		}
		return nil, fmt.Errorf("failed to scan agent: %w", err)
	}

	populateAgentFields(&agent, agentType, state, confidence, accountID, stateReason, stateDetectedAt, pausedUntil, lastActivity, metadataJSON, createdAt, updatedAt)
	return &agent, nil
}

func (r *AgentRepository) scanAgents(rows *sql.Rows) ([]*models.Agent, error) {
	var agents []*models.Agent

	for rows.Next() {
		var agent models.Agent
		var agentType, state, confidence string
		var accountID, stateReason, stateDetectedAt sql.NullString
		var pausedUntil, lastActivity sql.NullString
		var metadataJSON sql.NullString
		var createdAt, updatedAt string

		err := rows.Scan(
			&agent.ID,
			&agent.WorkspaceID,
			&agentType,
			&agent.TmuxPane,
			&accountID,
			&state,
			&confidence,
			&stateReason,
			&stateDetectedAt,
			&pausedUntil,
			&lastActivity,
			&metadataJSON,
			&createdAt,
			&updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan agent: %w", err)
		}

		populateAgentFields(&agent, agentType, state, confidence, accountID, stateReason, stateDetectedAt, pausedUntil, lastActivity, metadataJSON, createdAt, updatedAt)
		agents = append(agents, &agent)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agents: %w", err)
	}

	return agents, nil
}

func (r *AgentRepository) scanAgentsWithQueueLength(rows *sql.Rows) ([]*models.Agent, error) {
	var agents []*models.Agent

	for rows.Next() {
		var agent models.Agent
		var agentType, state, confidence string
		var accountID, stateReason, stateDetectedAt sql.NullString
		var pausedUntil, lastActivity sql.NullString
		var metadataJSON sql.NullString
		var createdAt, updatedAt string
		var queueLength int

		err := rows.Scan(
			&agent.ID,
			&agent.WorkspaceID,
			&agentType,
			&agent.TmuxPane,
			&accountID,
			&state,
			&confidence,
			&stateReason,
			&stateDetectedAt,
			&pausedUntil,
			&lastActivity,
			&metadataJSON,
			&createdAt,
			&updatedAt,
			&queueLength,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan agent with queue length: %w", err)
		}

		populateAgentFields(&agent, agentType, state, confidence, accountID, stateReason, stateDetectedAt, pausedUntil, lastActivity, metadataJSON, createdAt, updatedAt)
		agent.QueueLength = queueLength
		agents = append(agents, &agent)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agents: %w", err)
	}

	return agents, nil
}

func populateAgentFields(agent *models.Agent, agentType, state, confidence string, accountID, stateReason, stateDetectedAt, pausedUntil, lastActivity, metadataJSON sql.NullString, createdAt, updatedAt string) {
	agent.Type = models.AgentType(agentType)
	agent.State = models.AgentState(state)
	if accountID.Valid {
		agent.AccountID = accountID.String
	}

	agent.StateInfo.State = models.AgentState(state)
	agent.StateInfo.Confidence = models.StateConfidence(confidence)
	if stateReason.Valid {
		agent.StateInfo.Reason = stateReason.String
	}
	if stateDetectedAt.Valid {
		if t, err := time.Parse(time.RFC3339, stateDetectedAt.String); err == nil {
			agent.StateInfo.DetectedAt = t
		}
	}

	if pausedUntil.Valid {
		if t, err := time.Parse(time.RFC3339, pausedUntil.String); err == nil {
			agent.PausedUntil = &t
		}
	}
	if lastActivity.Valid {
		if t, err := time.Parse(time.RFC3339, lastActivity.String); err == nil {
			agent.LastActivity = &t
		}
	}

	if metadataJSON.Valid && metadataJSON.String != "" {
		if err := json.Unmarshal([]byte(metadataJSON.String), &agent.Metadata); err != nil {
			// If metadata is malformed, keep agent.Metadata empty.
		}
	}

	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		agent.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
		agent.UpdatedAt = t
	}
}

func normalizeAgentState(agent *models.Agent) (models.AgentState, models.StateConfidence) {
	state := agent.State
	if state == "" {
		state = models.AgentStateStarting
	}

	confidence := agent.StateInfo.Confidence
	if confidence == "" {
		confidence = models.StateConfidenceLow
	}

	return state, confidence
}

func stringTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.UTC().Format(time.RFC3339)
	return &formatted
}

func timePtrFrom(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	formatted := t.UTC().Format(time.RFC3339)
	return &formatted
}

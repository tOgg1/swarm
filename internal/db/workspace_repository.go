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

// Workspace repository errors
var (
	ErrWorkspaceNotFound      = errors.New("workspace not found")
	ErrWorkspaceAlreadyExists = errors.New("workspace with this path or session already exists")
)

// WorkspaceRepository handles workspace persistence.
type WorkspaceRepository struct {
	db *DB
}

// NewWorkspaceRepository creates a new WorkspaceRepository.
func NewWorkspaceRepository(db *DB) *WorkspaceRepository {
	return &WorkspaceRepository{db: db}
}

// Create adds a new workspace to the database.
func (r *WorkspaceRepository) Create(ctx context.Context, workspace *models.Workspace) error {
	if err := workspace.Validate(); err != nil {
		return fmt.Errorf("invalid workspace: %w", err)
	}

	if workspace.ID == "" {
		workspace.ID = uuid.New().String()
	}

	now := time.Now().UTC()
	workspace.CreatedAt = now
	workspace.UpdatedAt = now

	// Set defaults
	if workspace.Status == "" {
		workspace.Status = models.WorkspaceStatusActive
	}
	if workspace.Name == "" {
		workspace.Name = workspace.TmuxSession
	}

	var gitInfoJSON *string
	if workspace.GitInfo != nil {
		data, err := json.Marshal(workspace.GitInfo)
		if err != nil {
			return fmt.Errorf("failed to marshal git info: %w", err)
		}
		s := string(data)
		gitInfoJSON = &s
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO workspaces (
			id, name, node_id, repo_path, tmux_session, status,
			git_info_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		workspace.ID,
		workspace.Name,
		workspace.NodeID,
		workspace.RepoPath,
		workspace.TmuxSession,
		string(workspace.Status),
		gitInfoJSON,
		workspace.CreatedAt.Format(time.RFC3339),
		workspace.UpdatedAt.Format(time.RFC3339),
	)

	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrWorkspaceAlreadyExists
		}
		return fmt.Errorf("failed to insert workspace: %w", err)
	}

	return nil
}

// Get retrieves a workspace by ID.
func (r *WorkspaceRepository) Get(ctx context.Context, id string) (*models.Workspace, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT 
			id, name, node_id, repo_path, tmux_session, status,
			git_info_json, created_at, updated_at
		FROM workspaces WHERE id = ?
	`, id)

	return r.scanWorkspace(row)
}

// GetByNodeAndPath retrieves a workspace by node ID and repo path.
func (r *WorkspaceRepository) GetByNodeAndPath(ctx context.Context, nodeID, repoPath string) (*models.Workspace, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT 
			id, name, node_id, repo_path, tmux_session, status,
			git_info_json, created_at, updated_at
		FROM workspaces WHERE node_id = ? AND repo_path = ?
	`, nodeID, repoPath)

	return r.scanWorkspace(row)
}

// GetByTmuxSession retrieves a workspace by node ID and tmux session name.
func (r *WorkspaceRepository) GetByTmuxSession(ctx context.Context, nodeID, sessionName string) (*models.Workspace, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT 
			id, name, node_id, repo_path, tmux_session, status,
			git_info_json, created_at, updated_at
		FROM workspaces WHERE node_id = ? AND tmux_session = ?
	`, nodeID, sessionName)

	return r.scanWorkspace(row)
}

// GetByName retrieves a workspace by name.
func (r *WorkspaceRepository) GetByName(ctx context.Context, name string) (*models.Workspace, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT 
			id, name, node_id, repo_path, tmux_session, status,
			git_info_json, created_at, updated_at
		FROM workspaces WHERE name = ?
	`, name)

	return r.scanWorkspace(row)
}

// List retrieves all workspaces.
func (r *WorkspaceRepository) List(ctx context.Context) ([]*models.Workspace, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 
			id, name, node_id, repo_path, tmux_session, status,
			git_info_json, created_at, updated_at
		FROM workspaces ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query workspaces: %w", err)
	}
	defer rows.Close()

	return r.scanWorkspaces(rows)
}

// ListByNode retrieves all workspaces for a specific node.
func (r *WorkspaceRepository) ListByNode(ctx context.Context, nodeID string) ([]*models.Workspace, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 
			id, name, node_id, repo_path, tmux_session, status,
			git_info_json, created_at, updated_at
		FROM workspaces WHERE node_id = ? ORDER BY name
	`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query workspaces by node: %w", err)
	}
	defer rows.Close()

	return r.scanWorkspaces(rows)
}

// ListByStatus retrieves workspaces with a specific status.
func (r *WorkspaceRepository) ListByStatus(ctx context.Context, status models.WorkspaceStatus) ([]*models.Workspace, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 
			id, name, node_id, repo_path, tmux_session, status,
			git_info_json, created_at, updated_at
		FROM workspaces WHERE status = ? ORDER BY name
	`, string(status))
	if err != nil {
		return nil, fmt.Errorf("failed to query workspaces by status: %w", err)
	}
	defer rows.Close()

	return r.scanWorkspaces(rows)
}

// ListWithAgentCounts retrieves all workspaces with their agent counts.
func (r *WorkspaceRepository) ListWithAgentCounts(ctx context.Context) ([]*models.Workspace, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 
			w.id, w.name, w.node_id, w.repo_path, w.tmux_session, w.status,
			w.git_info_json, w.created_at, w.updated_at,
			COUNT(a.id) as agent_count
		FROM workspaces w
		LEFT JOIN agents a ON w.id = a.workspace_id
		GROUP BY w.id
		ORDER BY w.name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query workspaces with agent counts: %w", err)
	}
	defer rows.Close()

	return r.scanWorkspacesWithCounts(rows)
}

// Update updates an existing workspace.
func (r *WorkspaceRepository) Update(ctx context.Context, workspace *models.Workspace) error {
	if err := workspace.Validate(); err != nil {
		return fmt.Errorf("invalid workspace: %w", err)
	}

	workspace.UpdatedAt = time.Now().UTC()

	var gitInfoJSON *string
	if workspace.GitInfo != nil {
		data, err := json.Marshal(workspace.GitInfo)
		if err != nil {
			return fmt.Errorf("failed to marshal git info: %w", err)
		}
		s := string(data)
		gitInfoJSON = &s
	}

	result, err := r.db.ExecContext(ctx, `
		UPDATE workspaces SET
			name = ?,
			node_id = ?,
			repo_path = ?,
			tmux_session = ?,
			status = ?,
			git_info_json = ?,
			updated_at = ?
		WHERE id = ?
	`,
		workspace.Name,
		workspace.NodeID,
		workspace.RepoPath,
		workspace.TmuxSession,
		string(workspace.Status),
		gitInfoJSON,
		workspace.UpdatedAt.Format(time.RFC3339),
		workspace.ID,
	)

	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrWorkspaceAlreadyExists
		}
		return fmt.Errorf("failed to update workspace: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrWorkspaceNotFound
	}

	return nil
}

// UpdateStatus updates just the status of a workspace.
func (r *WorkspaceRepository) UpdateStatus(ctx context.Context, id string, status models.WorkspaceStatus) error {
	now := time.Now().UTC().Format(time.RFC3339)

	result, err := r.db.ExecContext(ctx, `
		UPDATE workspaces SET status = ?, updated_at = ?
		WHERE id = ?
	`, string(status), now, id)

	if err != nil {
		return fmt.Errorf("failed to update workspace status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrWorkspaceNotFound
	}

	return nil
}

// UpdateGitInfo updates just the git info of a workspace.
func (r *WorkspaceRepository) UpdateGitInfo(ctx context.Context, id string, gitInfo *models.GitInfo) error {
	now := time.Now().UTC().Format(time.RFC3339)

	var gitInfoJSON *string
	if gitInfo != nil {
		data, err := json.Marshal(gitInfo)
		if err != nil {
			return fmt.Errorf("failed to marshal git info: %w", err)
		}
		s := string(data)
		gitInfoJSON = &s
	}

	result, err := r.db.ExecContext(ctx, `
		UPDATE workspaces SET git_info_json = ?, updated_at = ?
		WHERE id = ?
	`, gitInfoJSON, now, id)

	if err != nil {
		return fmt.Errorf("failed to update workspace git info: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrWorkspaceNotFound
	}

	return nil
}

// Delete removes a workspace by ID.
func (r *WorkspaceRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete workspace: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return ErrWorkspaceNotFound
	}

	return nil
}

// GetAgentCount returns the number of agents in a workspace.
func (r *WorkspaceRepository) GetAgentCount(ctx context.Context, workspaceID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agents WHERE workspace_id = ?
	`, workspaceID).Scan(&count)

	if err != nil {
		return 0, fmt.Errorf("failed to count agents: %w", err)
	}

	return count, nil
}

// Count returns the total number of workspaces.
func (r *WorkspaceRepository) Count(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count workspaces: %w", err)
	}
	return count, nil
}

// scanWorkspace scans a single workspace from a row.
func (r *WorkspaceRepository) scanWorkspace(row *sql.Row) (*models.Workspace, error) {
	var workspace models.Workspace
	var status string
	var gitInfoJSON sql.NullString
	var createdAt, updatedAt string

	err := row.Scan(
		&workspace.ID,
		&workspace.Name,
		&workspace.NodeID,
		&workspace.RepoPath,
		&workspace.TmuxSession,
		&status,
		&gitInfoJSON,
		&createdAt,
		&updatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWorkspaceNotFound
		}
		return nil, fmt.Errorf("failed to scan workspace: %w", err)
	}

	workspace.Status = models.WorkspaceStatus(status)

	if gitInfoJSON.Valid && gitInfoJSON.String != "" {
		var gitInfo models.GitInfo
		if err := json.Unmarshal([]byte(gitInfoJSON.String), &gitInfo); err != nil {
			r.db.logger.Warn().Err(err).Str("workspace_id", workspace.ID).Msg("failed to parse git info")
		} else {
			workspace.GitInfo = &gitInfo
		}
	}

	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		workspace.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
		workspace.UpdatedAt = t
	}

	return &workspace, nil
}

// scanWorkspaces scans multiple workspaces from rows.
func (r *WorkspaceRepository) scanWorkspaces(rows *sql.Rows) ([]*models.Workspace, error) {
	var workspaces []*models.Workspace

	for rows.Next() {
		var workspace models.Workspace
		var status string
		var gitInfoJSON sql.NullString
		var createdAt, updatedAt string

		err := rows.Scan(
			&workspace.ID,
			&workspace.Name,
			&workspace.NodeID,
			&workspace.RepoPath,
			&workspace.TmuxSession,
			&status,
			&gitInfoJSON,
			&createdAt,
			&updatedAt,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan workspace: %w", err)
		}

		workspace.Status = models.WorkspaceStatus(status)

		if gitInfoJSON.Valid && gitInfoJSON.String != "" {
			var gitInfo models.GitInfo
			if err := json.Unmarshal([]byte(gitInfoJSON.String), &gitInfo); err != nil {
				r.db.logger.Warn().Err(err).Str("workspace_id", workspace.ID).Msg("failed to parse git info")
			} else {
				workspace.GitInfo = &gitInfo
			}
		}

		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			workspace.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
			workspace.UpdatedAt = t
		}

		workspaces = append(workspaces, &workspace)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating workspaces: %w", err)
	}

	return workspaces, nil
}

// scanWorkspacesWithCounts scans workspaces with agent counts from rows.
func (r *WorkspaceRepository) scanWorkspacesWithCounts(rows *sql.Rows) ([]*models.Workspace, error) {
	var workspaces []*models.Workspace

	for rows.Next() {
		var workspace models.Workspace
		var status string
		var gitInfoJSON sql.NullString
		var createdAt, updatedAt string

		err := rows.Scan(
			&workspace.ID,
			&workspace.Name,
			&workspace.NodeID,
			&workspace.RepoPath,
			&workspace.TmuxSession,
			&status,
			&gitInfoJSON,
			&createdAt,
			&updatedAt,
			&workspace.AgentCount,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan workspace with count: %w", err)
		}

		workspace.Status = models.WorkspaceStatus(status)

		if gitInfoJSON.Valid && gitInfoJSON.String != "" {
			var gitInfo models.GitInfo
			if err := json.Unmarshal([]byte(gitInfoJSON.String), &gitInfo); err != nil {
				r.db.logger.Warn().Err(err).Str("workspace_id", workspace.ID).Msg("failed to parse git info")
			} else {
				workspace.GitInfo = &gitInfo
			}
		}

		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			workspace.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
			workspace.UpdatedAt = t
		}

		workspaces = append(workspaces, &workspace)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating workspaces: %w", err)
	}

	return workspaces, nil
}

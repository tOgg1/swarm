package models

import (
	"time"
)

// WorkspaceStatus represents the current status of a workspace.
type WorkspaceStatus string

const (
	WorkspaceStatusActive   WorkspaceStatus = "active"
	WorkspaceStatusInactive WorkspaceStatus = "inactive"
	WorkspaceStatusError    WorkspaceStatus = "error"
)

// Workspace represents a managed unit binding a node, repo, and tmux session.
type Workspace struct {
	// ID is the unique identifier for the workspace.
	ID string `json:"id"`

	// Name is the human-friendly name for the workspace.
	Name string `json:"name"`

	// NodeID references the node where this workspace lives.
	NodeID string `json:"node_id"`

	// RepoPath is the absolute path to the repository on the node.
	RepoPath string `json:"repo_path"`

	// TmuxSession is the name of the tmux session for this workspace.
	TmuxSession string `json:"tmux_session"`

	// Status is the current workspace status.
	Status WorkspaceStatus `json:"status"`

	// GitInfo contains git repository information.
	GitInfo *GitInfo `json:"git_info,omitempty"`

	// AgentCount is the number of agents in this workspace.
	AgentCount int `json:"agent_count"`

	// AgentStats contains the breakdown of agents by state.
	AgentStats AgentStats `json:"agent_stats"`

	// Alerts contains current alerts for this workspace.
	Alerts []Alert `json:"alerts,omitempty"`

	// CreatedAt is when the workspace was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the workspace was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentStats contains the breakdown of agents by state.
type AgentStats struct {
	Working int `json:"working"`
	Idle    int `json:"idle"`
	Blocked int `json:"blocked"`
	Error   int `json:"error"`
}

// GitInfo contains information about the git repository.
type GitInfo struct {
	// IsRepo indicates if the path is a git repository.
	IsRepo bool `json:"is_repo"`

	// Branch is the current branch name.
	Branch string `json:"branch,omitempty"`

	// IsDirty indicates if there are uncommitted changes.
	IsDirty bool `json:"is_dirty"`

	// Ahead is the number of commits ahead of remote.
	Ahead int `json:"ahead"`

	// Behind is the number of commits behind remote.
	Behind int `json:"behind"`

	// RemoteURL is the remote repository URL.
	RemoteURL string `json:"remote_url,omitempty"`

	// LastCommit is the hash of the last commit.
	LastCommit string `json:"last_commit,omitempty"`
}

// AlertSeverity indicates the severity of an alert.
type AlertSeverity string

const (
	AlertSeverityInfo     AlertSeverity = "info"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityError    AlertSeverity = "error"
	AlertSeverityCritical AlertSeverity = "critical"
)

// AlertType categorizes alerts.
type AlertType string

const (
	AlertTypeApprovalNeeded AlertType = "approval_needed"
	AlertTypeCooldown       AlertType = "cooldown"
	AlertTypeError          AlertType = "error"
	AlertTypeRateLimit      AlertType = "rate_limit"
)

// Alert represents a notification requiring attention.
type Alert struct {
	// Type categorizes the alert.
	Type AlertType `json:"type"`

	// Severity indicates urgency.
	Severity AlertSeverity `json:"severity"`

	// Message is the alert description.
	Message string `json:"message"`

	// AgentID is the related agent (if applicable).
	AgentID string `json:"agent_id,omitempty"`

	// CreatedAt is when the alert was raised.
	CreatedAt time.Time `json:"created_at"`
}

// Validate checks if the workspace configuration is valid.
func (w *Workspace) Validate() error {
	if w.NodeID == "" {
		return ErrInvalidWorkspaceNode
	}
	if w.RepoPath == "" {
		return ErrInvalidRepoPath
	}
	if w.TmuxSession == "" {
		return ErrInvalidTmuxSession
	}
	return nil
}

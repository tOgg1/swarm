package models

import (
	"time"
)

// AgentState represents the current state of an agent.
type AgentState string

const (
	AgentStateWorking          AgentState = "working"
	AgentStateIdle             AgentState = "idle"
	AgentStateAwaitingApproval AgentState = "awaiting_approval"
	AgentStateRateLimited      AgentState = "rate_limited"
	AgentStateError            AgentState = "error"
	AgentStatePaused           AgentState = "paused"
	AgentStateStarting         AgentState = "starting"
	AgentStateStopped          AgentState = "stopped"
)

// StateConfidence indicates how confident Swarm is about the detected state.
type StateConfidence string

const (
	StateConfidenceHigh   StateConfidence = "high"
	StateConfidenceMedium StateConfidence = "medium"
	StateConfidenceLow    StateConfidence = "low"
)

// AgentType identifies the agent CLI being used.
type AgentType string

const (
	AgentTypeOpenCode   AgentType = "opencode"
	AgentTypeClaudeCode AgentType = "claude-code"
	AgentTypeCodex      AgentType = "codex"
	AgentTypeGemini     AgentType = "gemini"
	AgentTypeGeneric    AgentType = "generic"
)

// AdapterTier indicates the level of integration support for an agent type.
type AdapterTier int

const (
	// AdapterTierGeneric provides basic tmux-only integration (heuristic state).
	AdapterTierGeneric AdapterTier = 1

	// AdapterTierTelemetry provides log/telemetry-based integration.
	AdapterTierTelemetry AdapterTier = 2

	// AdapterTierNative provides full native integration (structured events).
	AdapterTierNative AdapterTier = 3
)

// Agent represents a running agent process in a tmux pane.
type Agent struct {
	// ID is the unique identifier for the agent.
	ID string `json:"id"`

	// WorkspaceID references the workspace this agent belongs to.
	WorkspaceID string `json:"workspace_id"`

	// Type identifies the agent CLI being used.
	Type AgentType `json:"type"`

	// TmuxPane is the tmux pane target (session:window.pane).
	TmuxPane string `json:"tmux_pane"`

	// AccountID references the account profile being used.
	AccountID string `json:"account_id,omitempty"`

	// State is the current detected state.
	State AgentState `json:"state"`

	// StateInfo contains detailed state information.
	StateInfo StateInfo `json:"state_info"`

	// QueueLength is the number of items in the agent's queue.
	QueueLength int `json:"queue_length"`

	// LastActivity is the timestamp of the last detected activity.
	LastActivity *time.Time `json:"last_activity,omitempty"`

	// PausedUntil is when the agent will auto-resume (if paused).
	PausedUntil *time.Time `json:"paused_until,omitempty"`

	// Metadata contains additional agent information.
	Metadata AgentMetadata `json:"metadata,omitempty"`

	// CreatedAt is when the agent was spawned.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the agent was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}

// StateInfo contains detailed information about the current state.
type StateInfo struct {
	// State is the detected state value.
	State AgentState `json:"state"`

	// Confidence indicates how certain we are about the state.
	Confidence StateConfidence `json:"confidence"`

	// Reason is a human-readable explanation of why we detected this state.
	Reason string `json:"reason"`

	// Evidence contains supporting evidence for the state detection.
	Evidence []string `json:"evidence,omitempty"`

	// DetectedAt is when this state was detected.
	DetectedAt time.Time `json:"detected_at"`
}

// AgentMetadata contains additional agent information.
type AgentMetadata struct {
	// Model is the AI model being used (if known).
	Model string `json:"model,omitempty"`

	// PID is the process ID of the agent (if known).
	PID int `json:"pid,omitempty"`

	// StartCommand is the command used to spawn the agent.
	StartCommand string `json:"start_command,omitempty"`

	// Environment contains environment variable overrides.
	Environment map[string]string `json:"environment,omitempty"`

	// ApprovalPolicy captures the effective approval policy for the agent.
	ApprovalPolicy string `json:"approval_policy,omitempty"`

	// UsageMetrics captures best-effort usage data from adapters.
	UsageMetrics *UsageMetrics `json:"usage_metrics,omitempty"`

	// DiffMetadata captures best-effort diff data from adapters.
	DiffMetadata *DiffMetadata `json:"diff_metadata,omitempty"`
}

// UsageMetrics contains usage metrics captured from an agent runtime.
type UsageMetrics struct {
	// Sessions is the number of sessions in the usage window.
	Sessions int64 `json:"sessions,omitempty"`

	// Messages is the number of messages in the usage window.
	Messages int64 `json:"messages,omitempty"`

	// Days is the number of days in the usage window.
	Days int64 `json:"days,omitempty"`

	// TotalCostCents is the total cost in cents.
	TotalCostCents int64 `json:"total_cost_cents,omitempty"`

	// AvgCostPerDayCents is the average cost per day in cents.
	AvgCostPerDayCents int64 `json:"avg_cost_per_day_cents,omitempty"`

	// AvgTokensPerSession is the average tokens per session.
	AvgTokensPerSession int64 `json:"avg_tokens_per_session,omitempty"`

	// MedianTokensPerSession is the median tokens per session.
	MedianTokensPerSession int64 `json:"median_tokens_per_session,omitempty"`

	// InputTokens is the number of input tokens in the usage window.
	InputTokens int64 `json:"input_tokens,omitempty"`

	// OutputTokens is the number of output tokens in the usage window.
	OutputTokens int64 `json:"output_tokens,omitempty"`

	// CacheReadTokens is the number of cache read tokens in the usage window.
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`

	// CacheWriteTokens is the number of cache write tokens in the usage window.
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`

	// TotalTokens is the total tokens in the usage window.
	TotalTokens int64 `json:"total_tokens,omitempty"`

	// Source indicates where the metrics were derived from.
	Source string `json:"source,omitempty"`

	// UpdatedAt is when the metrics were captured.
	UpdatedAt time.Time `json:"updated_at"`
}

// DiffMetadata contains diff metadata captured from an agent runtime.
type DiffMetadata struct {
	// Files lists modified file paths.
	Files []string `json:"files,omitempty"`

	// FilesChanged is the number of files changed.
	FilesChanged int64 `json:"files_changed,omitempty"`

	// Insertions is the number of inserted lines.
	Insertions int64 `json:"insertions,omitempty"`

	// Deletions is the number of deleted lines.
	Deletions int64 `json:"deletions,omitempty"`

	// Commits lists related commit references.
	Commits []string `json:"commits,omitempty"`

	// Source indicates where the metadata was derived from.
	Source string `json:"source,omitempty"`

	// UpdatedAt is when the metadata was captured.
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate checks if the agent configuration is valid.
func (a *Agent) Validate() error {
	validation := &ValidationErrors{}
	if a.WorkspaceID == "" {
		validation.Add("workspace_id", ErrInvalidAgentWorkspace)
	}
	if a.Type == "" {
		validation.Add("type", ErrInvalidAgentType)
	}
	if a.TmuxPane == "" {
		validation.Add("tmux_pane", ErrInvalidTmuxPane)
	}
	return validation.Err()
}

// IsActive returns true if the agent is in an active state.
func (a *Agent) IsActive() bool {
	switch a.State {
	case AgentStateWorking, AgentStateIdle, AgentStateAwaitingApproval:
		return true
	default:
		return false
	}
}

// IsBlocked returns true if the agent is blocked and needs attention.
func (a *Agent) IsBlocked() bool {
	switch a.State {
	case AgentStateAwaitingApproval, AgentStateRateLimited, AgentStateError:
		return true
	default:
		return false
	}
}

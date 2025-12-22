package models

import (
	"time"
)

// UsageRecord represents a single usage record for an account.
type UsageRecord struct {
	// ID is the unique identifier for the record.
	ID string `json:"id"`

	// AccountID is the account this usage belongs to.
	AccountID string `json:"account_id"`

	// AgentID is the agent that generated this usage (optional).
	AgentID string `json:"agent_id,omitempty"`

	// SessionID groups usage within a session (optional).
	SessionID string `json:"session_id,omitempty"`

	// Provider is the AI provider for this usage.
	Provider Provider `json:"provider"`

	// Model is the model used (e.g., "claude-3-opus", "gpt-4").
	Model string `json:"model,omitempty"`

	// InputTokens is the number of input tokens used.
	InputTokens int64 `json:"input_tokens"`

	// OutputTokens is the number of output tokens generated.
	OutputTokens int64 `json:"output_tokens"`

	// TotalTokens is the total tokens (input + output).
	TotalTokens int64 `json:"total_tokens"`

	// CostCents is the estimated cost in cents (USD).
	CostCents int64 `json:"cost_cents"`

	// RequestCount is the number of API requests in this record.
	RequestCount int64 `json:"request_count"`

	// RecordedAt is when this usage was recorded.
	RecordedAt time.Time `json:"recorded_at"`

	// Metadata contains additional context.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// UsageSummary represents aggregated usage data.
type UsageSummary struct {
	// AccountID is the account this summary is for.
	AccountID string `json:"account_id,omitempty"`

	// Provider is the provider this summary is for.
	Provider Provider `json:"provider,omitempty"`

	// Period is the time period (e.g., "day", "week", "month", "all").
	Period string `json:"period"`

	// PeriodStart is the start of the period.
	PeriodStart time.Time `json:"period_start,omitempty"`

	// PeriodEnd is the end of the period.
	PeriodEnd time.Time `json:"period_end,omitempty"`

	// InputTokens is the total input tokens in this period.
	InputTokens int64 `json:"input_tokens"`

	// OutputTokens is the total output tokens in this period.
	OutputTokens int64 `json:"output_tokens"`

	// TotalTokens is the total tokens in this period.
	TotalTokens int64 `json:"total_tokens"`

	// TotalCostCents is the total estimated cost in cents.
	TotalCostCents int64 `json:"total_cost_cents"`

	// RequestCount is the total API requests in this period.
	RequestCount int64 `json:"request_count"`

	// RecordCount is the number of usage records in this summary.
	RecordCount int64 `json:"record_count"`
}

// DailyUsage represents usage for a specific day.
type DailyUsage struct {
	// Date is the day (YYYY-MM-DD).
	Date string `json:"date"`

	// AccountID is the account this usage is for.
	AccountID string `json:"account_id"`

	// Provider is the provider.
	Provider Provider `json:"provider"`

	// InputTokens for the day.
	InputTokens int64 `json:"input_tokens"`

	// OutputTokens for the day.
	OutputTokens int64 `json:"output_tokens"`

	// TotalTokens for the day.
	TotalTokens int64 `json:"total_tokens"`

	// CostCents for the day.
	CostCents int64 `json:"cost_cents"`

	// RequestCount for the day.
	RequestCount int64 `json:"request_count"`
}

// UsageQuery defines filters for querying usage.
type UsageQuery struct {
	// AccountID filters by account.
	AccountID *string

	// AgentID filters by agent.
	AgentID *string

	// SessionID filters by session.
	SessionID *string

	// Provider filters by provider.
	Provider *Provider

	// Model filters by model.
	Model *string

	// Since filters to records after this time (inclusive).
	Since *time.Time

	// Until filters to records before this time (exclusive).
	Until *time.Time

	// Limit is the maximum records to return.
	Limit int
}

// Validate checks if the usage record is valid.
func (r *UsageRecord) Validate() error {
	validation := &ValidationErrors{}
	if r.AccountID == "" {
		validation.AddMessage("account_id", "account_id is required")
	}
	if r.Provider == "" {
		validation.AddMessage("provider", "provider is required")
	}
	if r.TotalTokens < 0 {
		validation.AddMessage("total_tokens", "total_tokens must be non-negative")
	}
	if r.CostCents < 0 {
		validation.AddMessage("cost_cents", "cost_cents must be non-negative")
	}
	return validation.Err()
}

// CalculateTotalTokens calculates total from input and output.
func (r *UsageRecord) CalculateTotalTokens() {
	r.TotalTokens = r.InputTokens + r.OutputTokens
}

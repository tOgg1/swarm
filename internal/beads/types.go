// Package beads provides helpers for parsing beads issue data.
package beads

import "time"

// Dependency describes an issue dependency edge.
type Dependency struct {
	IssueID     string    `json:"issue_id"`
	DependsOnID string    `json:"depends_on_id"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
}

// Issue represents a single beads issue record.
type Issue struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Description  string       `json:"description"`
	Status       string       `json:"status"`
	Priority     int          `json:"priority"`
	IssueType    string       `json:"issue_type"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	ClosedAt     *time.Time   `json:"closed_at,omitempty"`
	CloseReason  string       `json:"close_reason,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
}

// TaskSummary is a compact view suitable for UI/CLI displays.
type TaskSummary struct {
	ID        string
	Title     string
	Status    string
	Priority  int
	IssueType string
	UpdatedAt time.Time
}

// Summary returns a compact view of the issue for display.
func (i Issue) Summary() TaskSummary {
	return TaskSummary{
		ID:        i.ID,
		Title:     i.Title,
		Status:    i.Status,
		Priority:  i.Priority,
		IssueType: i.IssueType,
		UpdatedAt: i.UpdatedAt,
	}
}

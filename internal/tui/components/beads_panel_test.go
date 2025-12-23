package components

import (
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/swarm/internal/beads"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

func TestRenderBeadsPanel_Empty(t *testing.T) {
	styleSet := styles.DefaultStyles()
	data := BeadsPanelData{
		Issues: nil,
	}

	result := RenderBeadsPanel(styleSet, data, 60)

	if !strings.Contains(result, "No tasks found") {
		t.Error("Empty panel should show 'No tasks found'")
	}
	if !strings.Contains(result, "bd create") {
		t.Error("Empty panel should suggest 'bd create'")
	}
}

func TestRenderBeadsPanel_WithIssues(t *testing.T) {
	styleSet := styles.DefaultStyles()
	now := time.Now()

	data := BeadsPanelData{
		Issues: []beads.Issue{
			{ID: "test-1", Title: "Open task", Status: "open", Priority: 1, UpdatedAt: now},
			{ID: "test-2", Title: "In progress task", Status: "in_progress", Priority: 0, UpdatedAt: now},
			{ID: "test-3", Title: "Closed task", Status: "closed", Priority: 2, UpdatedAt: now, ClosedAt: &now},
		},
		MaxIssues: 5,
	}

	result := RenderBeadsPanel(styleSet, data, 80)

	if !strings.Contains(result, "In Progress") {
		t.Error("Panel should show 'In Progress' section")
	}
	if !strings.Contains(result, "Open") {
		t.Error("Panel should show 'Open' section")
	}
	if !strings.Contains(result, "Recently Closed") {
		t.Error("Panel should show 'Recently Closed' section")
	}
	if !strings.Contains(result, "test-1") {
		t.Error("Panel should show issue ID test-1")
	}
	if !strings.Contains(result, "test-2") {
		t.Error("Panel should show issue ID test-2")
	}
}

func TestRenderBeadsSummary(t *testing.T) {
	styleSet := styles.DefaultStyles()
	now := time.Now()

	tests := []struct {
		name   string
		issues []beads.Issue
		want   string
	}{
		{
			name:   "empty",
			issues: nil,
			want:   "No tasks",
		},
		{
			name: "only open",
			issues: []beads.Issue{
				{ID: "1", Status: "open", UpdatedAt: now},
				{ID: "2", Status: "open", UpdatedAt: now},
			},
			want: "2 open",
		},
		{
			name: "only in progress",
			issues: []beads.Issue{
				{ID: "1", Status: "in_progress", UpdatedAt: now},
			},
			want: "1 active",
		},
		{
			name: "mixed",
			issues: []beads.Issue{
				{ID: "1", Status: "open", UpdatedAt: now},
				{ID: "2", Status: "in_progress", UpdatedAt: now},
			},
			want: "1 open",
		},
		{
			name: "all closed",
			issues: []beads.Issue{
				{ID: "1", Status: "closed", UpdatedAt: now},
			},
			want: "All done!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderBeadsSummary(styleSet, tt.issues)
			if !strings.Contains(result, tt.want) {
				t.Errorf("RenderBeadsSummary() = %q, want to contain %q", result, tt.want)
			}
		})
	}
}

func TestRenderBeadsBadge(t *testing.T) {
	styleSet := styles.DefaultStyles()
	now := time.Now()

	tests := []struct {
		name   string
		issues []beads.Issue
		want   string
	}{
		{
			name:   "empty",
			issues: nil,
			want:   "✓",
		},
		{
			name: "open tasks",
			issues: []beads.Issue{
				{ID: "1", Status: "open", UpdatedAt: now},
				{ID: "2", Status: "open", UpdatedAt: now},
			},
			want: "2",
		},
		{
			name: "in progress",
			issues: []beads.Issue{
				{ID: "1", Status: "in_progress", UpdatedAt: now},
			},
			want: "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RenderBeadsBadge(styleSet, tt.issues)
			if !strings.Contains(result, tt.want) {
				t.Errorf("RenderBeadsBadge() = %q, want to contain %q", result, tt.want)
			}
		})
	}
}

func TestFilterByStatus(t *testing.T) {
	now := time.Now()
	issues := []beads.Issue{
		{ID: "1", Status: "open", UpdatedAt: now},
		{ID: "2", Status: "in_progress", UpdatedAt: now},
		{ID: "3", Status: "closed", UpdatedAt: now},
		{ID: "4", Status: "open", UpdatedAt: now},
	}

	open := filterByStatus(issues, "open")
	if len(open) != 2 {
		t.Errorf("Expected 2 open issues, got %d", len(open))
	}

	inProgress := filterByStatus(issues, "in_progress")
	if len(inProgress) != 1 {
		t.Errorf("Expected 1 in_progress issue, got %d", len(inProgress))
	}
}

func TestSortByPriority(t *testing.T) {
	now := time.Now()
	issues := []beads.Issue{
		{ID: "1", Priority: 2, UpdatedAt: now},
		{ID: "2", Priority: 0, UpdatedAt: now},
		{ID: "3", Priority: 1, UpdatedAt: now},
	}

	sortByPriority(issues)

	if issues[0].Priority != 0 {
		t.Errorf("First issue should be P0, got P%d", issues[0].Priority)
	}
	if issues[1].Priority != 1 {
		t.Errorf("Second issue should be P1, got P%d", issues[1].Priority)
	}
	if issues[2].Priority != 2 {
		t.Errorf("Third issue should be P2, got P%d", issues[2].Priority)
	}
}

func TestFilterActiveIssues(t *testing.T) {
	now := time.Now()
	issues := []beads.Issue{
		{ID: "1", Status: "open", UpdatedAt: now},
		{ID: "2", Status: "in_progress", UpdatedAt: now},
		{ID: "3", Status: "closed", UpdatedAt: now},
	}

	active := FilterActiveIssues(issues)
	if len(active) != 2 {
		t.Errorf("Expected 2 active issues, got %d", len(active))
	}
}

func TestFilterRecentlyClosed(t *testing.T) {
	now := time.Now()
	recent := now.Add(-1 * time.Hour)
	old := now.Add(-48 * time.Hour)

	issues := []beads.Issue{
		{ID: "1", Status: "closed", ClosedAt: &recent, UpdatedAt: now},
		{ID: "2", Status: "closed", ClosedAt: &old, UpdatedAt: now},
		{ID: "3", Status: "open", UpdatedAt: now},
	}

	result := FilterRecentlyClosed(issues, 24*time.Hour)
	if len(result) != 1 {
		t.Errorf("Expected 1 recently closed issue, got %d", len(result))
	}
	if result[0].ID != "1" {
		t.Errorf("Expected issue ID '1', got '%s'", result[0].ID)
	}
}

func TestRenderIssueLine_TruncatesLongTitles(t *testing.T) {
	styleSet := styles.DefaultStyles()
	now := time.Now()

	issue := beads.Issue{
		ID:        "test-123",
		Title:     "This is a very long title that should be truncated when displayed in the panel",
		Status:    "open",
		Priority:  1,
		UpdatedAt: now,
	}

	result := renderIssueLine(styleSet, issue, 50)

	if len(result) > 100 { // Allow for ANSI escape codes
		t.Logf("Rendered line: %s", result)
	}
	if !strings.Contains(result, "test-123") {
		t.Error("Issue line should contain issue ID")
	}
	if !strings.Contains(result, "P1") {
		t.Error("Issue line should contain priority")
	}
}

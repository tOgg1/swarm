package components

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/beads"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// BeadsPanelData contains data for rendering the beads task panel.
type BeadsPanelData struct {
	// Issues is the full list of issues loaded from the beads file.
	Issues []beads.Issue

	// RepoPath is the path to the repository (for loading issues).
	RepoPath string

	// MaxIssues is the maximum number of issues to display per section (default: 5).
	MaxIssues int
}

// RenderBeadsPanel renders a panel showing beads tasks grouped by status.
func RenderBeadsPanel(styleSet styles.Styles, data BeadsPanelData, width int) string {
	if len(data.Issues) == 0 {
		lines := []string{
			styleSet.Muted.Render("No tasks found"),
			"",
			styleSet.Muted.Render("Use 'bd create' to add tasks"),
		}
		return renderBeadsPanelContainer(styleSet, "Tasks", lines, width)
	}

	maxPerSection := data.MaxIssues
	if maxPerSection <= 0 {
		maxPerSection = 5
	}

	// Group issues by status
	inProgress := filterByStatus(data.Issues, "in_progress")
	open := filterByStatus(data.Issues, "open")
	closed := filterByStatus(data.Issues, "closed")

	// Sort by priority (P0 first), then by updated_at (most recent first)
	sortByPriority(inProgress)
	sortByPriority(open)
	sortByPriority(closed)

	lines := []string{}

	// Counts summary
	countLine := fmt.Sprintf("%d open, %d in progress, %d closed",
		len(open), len(inProgress), len(closed))
	lines = append(lines, styleSet.Muted.Render(countLine))
	lines = append(lines, "")

	// In Progress section
	if len(inProgress) > 0 {
		lines = append(lines, styleSet.Warning.Render("● In Progress"))
		lines = append(lines, renderIssueList(styleSet, inProgress, maxPerSection, width-4)...)
		lines = append(lines, "")
	}

	// Open/Ready section
	if len(open) > 0 {
		lines = append(lines, styleSet.Accent.Render("○ Open"))
		lines = append(lines, renderIssueList(styleSet, open, maxPerSection, width-4)...)
		lines = append(lines, "")
	}

	// Recently closed section (last 3)
	if len(closed) > 0 {
		// Sort closed by closed_at descending
		sortByClosedAt(closed)
		recentClosed := closed
		if len(recentClosed) > 3 {
			recentClosed = recentClosed[:3]
		}
		lines = append(lines, styleSet.Success.Render("✓ Recently Closed"))
		lines = append(lines, renderIssueList(styleSet, recentClosed, 3, width-4)...)
	}

	// Trim trailing empty lines
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return renderBeadsPanelContainer(styleSet, "Tasks", lines, width)
}

// RenderBeadsSummary renders a compact one-line summary of beads tasks.
// Format: "3 open, 1 in progress"
func RenderBeadsSummary(styleSet styles.Styles, issues []beads.Issue) string {
	if len(issues) == 0 {
		return styleSet.Muted.Render("No tasks")
	}

	inProgress := len(filterByStatus(issues, "in_progress"))
	open := len(filterByStatus(issues, "open"))

	parts := []string{}
	if open > 0 {
		parts = append(parts, styleSet.Text.Render(fmt.Sprintf("%d open", open)))
	}
	if inProgress > 0 {
		parts = append(parts, styleSet.Warning.Render(fmt.Sprintf("%d active", inProgress)))
	}

	if len(parts) == 0 {
		return styleSet.Success.Render("All done!")
	}

	return strings.Join(parts, ", ")
}

// RenderBeadsBadge renders a badge showing the number of ready tasks.
func RenderBeadsBadge(styleSet styles.Styles, issues []beads.Issue) string {
	open := len(filterByStatus(issues, "open"))
	inProgress := len(filterByStatus(issues, "in_progress"))

	if open == 0 && inProgress == 0 {
		return styleSet.Success.Render("✓")
	}

	total := open + inProgress
	if inProgress > 0 {
		return styleSet.Warning.Render(fmt.Sprintf("⚡%d", total))
	}
	return styleSet.Muted.Render(fmt.Sprintf("◌%d", total))
}

// filterByStatus returns issues matching the given status.
func filterByStatus(issues []beads.Issue, status string) []beads.Issue {
	result := []beads.Issue{}
	for _, issue := range issues {
		if issue.Status == status {
			result = append(result, issue)
		}
	}
	return result
}

// sortByPriority sorts issues by priority (P0 first), then by updated_at (most recent first).
func sortByPriority(issues []beads.Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Priority != issues[j].Priority {
			return issues[i].Priority < issues[j].Priority // Lower priority number = higher priority
		}
		return issues[i].UpdatedAt.After(issues[j].UpdatedAt)
	})
}

// sortByClosedAt sorts issues by closed_at (most recent first).
func sortByClosedAt(issues []beads.Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].ClosedAt == nil {
			return false
		}
		if issues[j].ClosedAt == nil {
			return true
		}
		return issues[i].ClosedAt.After(*issues[j].ClosedAt)
	})
}

// renderIssueList renders a list of issues with truncation.
func renderIssueList(styleSet styles.Styles, issues []beads.Issue, max int, width int) []string {
	lines := []string{}

	for i, issue := range issues {
		if i >= max {
			lines = append(lines, styleSet.Muted.Render(fmt.Sprintf("  ... and %d more", len(issues)-max)))
			break
		}
		lines = append(lines, renderIssueLine(styleSet, issue, width))
	}

	return lines
}

// renderIssueLine renders a single issue line.
func renderIssueLine(styleSet styles.Styles, issue beads.Issue, width int) string {
	// Format: "  [P0] ID: Title"
	priority := fmt.Sprintf("P%d", issue.Priority)
	var priorityStyle = styleSet.Muted

	switch issue.Priority {
	case 0:
		priorityStyle = styleSet.Error
	case 1:
		priorityStyle = styleSet.Warning
	case 2:
		priorityStyle = styleSet.Text
	default:
		priorityStyle = styleSet.Muted
	}

	id := issue.ID
	if len(id) > 12 {
		id = id[:12]
	}

	// Calculate remaining width for title
	prefix := fmt.Sprintf("  [%s] %s: ", priority, id)
	prefixLen := len(prefix)
	titleWidth := width - prefixLen
	if titleWidth < 10 {
		titleWidth = 10
	}

	title := issue.Title
	if len(title) > titleWidth {
		title = title[:titleWidth-3] + "..."
	}

	return fmt.Sprintf("  [%s] %s: %s",
		priorityStyle.Render(priority),
		styleSet.Accent.Render(id),
		styleSet.Text.Render(title))
}

// renderBeadsPanelContainer wraps content in a panel with title.
func renderBeadsPanelContainer(styleSet styles.Styles, title string, lines []string, width int) string {
	header := styleSet.Accent.Render(title)
	content := strings.Join(append([]string{header}, lines...), "\n")
	return styleSet.Panel.Copy().Width(width).Padding(0, 1).Render(content)
}

// LoadBeadsData loads beads issues from a repository path.
func LoadBeadsData(repoPath string) ([]beads.Issue, error) {
	hasBeads, err := beads.HasBeadsDir(repoPath)
	if err != nil {
		return nil, err
	}
	if !hasBeads {
		return nil, nil
	}

	issuesPath := beads.IssuesPath(repoPath)
	issues, err := beads.LoadIssues(issuesPath)
	if err != nil {
		return nil, err
	}

	return issues, nil
}

// FilterActiveIssues returns only open and in_progress issues.
func FilterActiveIssues(issues []beads.Issue) []beads.Issue {
	result := []beads.Issue{}
	for _, issue := range issues {
		if issue.Status == "open" || issue.Status == "in_progress" {
			result = append(result, issue)
		}
	}
	return result
}

// FilterRecentlyClosed returns issues closed within the given duration.
func FilterRecentlyClosed(issues []beads.Issue, within time.Duration) []beads.Issue {
	result := []beads.Issue{}
	cutoff := time.Now().Add(-within)
	for _, issue := range issues {
		if issue.Status == "closed" && issue.ClosedAt != nil && issue.ClosedAt.After(cutoff) {
			result = append(result, issue)
		}
	}
	return result
}

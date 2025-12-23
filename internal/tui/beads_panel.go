// Package tui implements the Swarm terminal user interface.
package tui

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/swarm/internal/beads"
	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

type beadsSnapshot struct {
	Detected     bool
	IssuesPath   string
	Tasks        []beads.TaskSummary
	StatusCounts map[string]int
	Total        int
	LoadedAt     time.Time
	Err          string
}

func (m *model) refreshBeadsCache() {
	if m.workspacesPreview {
		if m.beadsCache == nil {
			m.beadsCache = sampleBeadsSnapshots()
		}
		return
	}
	if m.workspaceGrid == nil {
		return
	}
	if m.beadsCache == nil {
		m.beadsCache = make(map[string]beadsSnapshot)
	}
	for _, ws := range m.workspaceGrid.Workspaces {
		if ws == nil {
			continue
		}
		m.beadsCache[ws.ID] = loadBeadsSnapshot(ws.RepoPath)
	}
}

func (m model) beadsPanelLines(ws *models.Workspace) []string {
	if ws == nil {
		return []string{m.styles.Muted.Render("Beads: --")}
	}

	snapshot, ok := m.beadsCache[ws.ID]
	if !ok && !m.workspacesPreview {
		snapshot = loadBeadsSnapshot(ws.RepoPath)
	}
	return renderBeadsPanel(m.styles, snapshot, m.inspectorContentWidth())
}

func loadBeadsSnapshot(repoPath string) beadsSnapshot {
	snapshot := beadsSnapshot{}
	if strings.TrimSpace(repoPath) == "" {
		return snapshot
	}

	detected, err := beads.HasBeadsDir(repoPath)
	if err != nil {
		snapshot.Err = err.Error()
		return snapshot
	}
	if !detected {
		return snapshot
	}

	snapshot.Detected = true
	snapshot.IssuesPath = beads.IssuesPath(repoPath)
	issues, err := beads.LoadIssues(snapshot.IssuesPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			snapshot.Err = "issues.jsonl not found"
		} else {
			snapshot.Err = err.Error()
		}
		return snapshot
	}

	tasks := beads.Summaries(issues)
	sort.Slice(tasks, func(i, j int) bool {
		left := tasks[i]
		right := tasks[j]
		if beadsStatusRank(left.Status) != beadsStatusRank(right.Status) {
			return beadsStatusRank(left.Status) < beadsStatusRank(right.Status)
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})

	snapshot.Tasks = tasks
	snapshot.Total = len(tasks)
	snapshot.StatusCounts = summarizeBeadsStatusCounts(tasks)
	snapshot.LoadedAt = time.Now()

	return snapshot
}

func renderBeadsPanel(styleSet styles.Styles, snapshot beadsSnapshot, maxWidth int) []string {
	lines := []string{styleSet.Accent.Render("Beads")}

	if !snapshot.Detected {
		if snapshot.Err != "" {
			lines = append(lines, styleSet.Warning.Render("  "+snapshot.Err))
			return lines
		}
		lines = append(lines, styleSet.Muted.Render("  Not detected"))
		return lines
	}
	if snapshot.Err != "" {
		lines = append(lines, styleSet.Warning.Render("  "+snapshot.Err))
		return lines
	}

	statusCounts := snapshot.StatusCounts
	lines = append(lines, styleSet.Muted.Render(fmt.Sprintf("  Total: %d", snapshot.Total)))
	lines = append(lines, styleSet.Muted.Render(fmt.Sprintf(
		"  open=%d in_progress=%d blocked=%d closed=%d",
		statusCounts["open"],
		statusCounts["in_progress"],
		statusCounts["blocked"],
		statusCounts["closed"],
	)))

	if snapshot.Total == 0 {
		lines = append(lines, styleSet.Muted.Render("  No beads issues found."))
		return lines
	}

	grouped := groupBeadsTasks(snapshot.Tasks)
	for _, status := range orderedBeadsStatuses(grouped) {
		tasks := grouped[status]
		if len(tasks) == 0 {
			continue
		}
		header := "  " + beadsStatusLabel(status)
		switch status {
		case "open":
			header = styleSet.Accent.Render(header)
		case "in_progress":
			header = styleSet.Info.Render(header)
		case "blocked":
			header = styleSet.Warning.Render(header)
		case "closed":
			header = styleSet.Muted.Render(header)
		default:
			header = styleSet.Muted.Render(header)
		}
		lines = append(lines, header)

		limit := 3
		for i, task := range tasks {
			if i >= limit {
				break
			}
			line := fmt.Sprintf("    - P%d %s %s", task.Priority, task.ID, task.Title)
			if maxWidth > 0 {
				line = truncateText(line, maxWidth)
			}
			switch status {
			case "in_progress":
				lines = append(lines, styleSet.Info.Render(line))
			case "blocked":
				lines = append(lines, styleSet.Warning.Render(line))
			case "closed":
				lines = append(lines, styleSet.Muted.Render(line))
			default:
				lines = append(lines, styleSet.Text.Render(line))
			}
		}
		if len(tasks) > limit {
			moreLine := fmt.Sprintf("    +%d more", len(tasks)-limit)
			if maxWidth > 0 {
				moreLine = truncateText(moreLine, maxWidth)
			}
			lines = append(lines, styleSet.Muted.Render(moreLine))
		}
	}

	return lines
}

func summarizeBeadsStatusCounts(tasks []beads.TaskSummary) map[string]int {
	counts := map[string]int{}
	for _, task := range tasks {
		counts[task.Status]++
	}
	return counts
}

func groupBeadsTasks(tasks []beads.TaskSummary) map[string][]beads.TaskSummary {
	grouped := map[string][]beads.TaskSummary{}
	for _, task := range tasks {
		grouped[task.Status] = append(grouped[task.Status], task)
	}
	return grouped
}

func orderedBeadsStatuses(grouped map[string][]beads.TaskSummary) []string {
	base := []string{"in_progress", "open", "blocked", "closed"}
	seen := map[string]bool{}
	for _, status := range base {
		seen[status] = true
	}
	extras := make([]string, 0)
	for status := range grouped {
		if !seen[status] {
			extras = append(extras, status)
		}
	}
	sort.Strings(extras)
	return append(base, extras...)
}

func beadsStatusRank(status string) int {
	switch status {
	case "open":
		return 0
	case "in_progress":
		return 1
	case "blocked":
		return 2
	case "closed":
		return 3
	default:
		return 99
	}
}

func beadsStatusLabel(status string) string {
	switch status {
	case "open":
		return "Open"
	case "in_progress":
		return "In progress"
	case "blocked":
		return "Blocked"
	case "closed":
		return "Closed"
	default:
		return titleCaseStatus(status)
	}
}

func titleCaseStatus(value string) string {
	normalized := strings.ReplaceAll(value, "_", " ")
	parts := strings.Fields(normalized)
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func sampleBeadsSnapshots() map[string]beadsSnapshot {
	now := time.Now()
	snapshots := map[string]beadsSnapshot{}

	snapshots["ws-1"] = buildSampleBeadsSnapshot(
		"/home/user/projects/api-service/.beads/issues.jsonl",
		[]beads.TaskSummary{
			{
				ID:        "swarm-vyxb",
				Title:     "Display beads task panel in TUI",
				Status:    "in_progress",
				Priority:  2,
				IssueType: "task",
				UpdatedAt: now.Add(-15 * time.Minute),
			},
			{
				ID:        "swarm-ma6h",
				Title:     "Detect Agent Mail MCP in workspace",
				Status:    "open",
				Priority:  2,
				IssueType: "task",
				UpdatedAt: now.Add(-90 * time.Minute),
			},
			{
				ID:        "swarm-dga",
				Title:     "EPIC 1: Project Foundation and Core Infrastructure",
				Status:    "open",
				Priority:  0,
				IssueType: "epic",
				UpdatedAt: now.Add(-2 * time.Hour),
			},
		},
	)

	snapshots["ws-2"] = buildSampleBeadsSnapshot(
		"/home/user/projects/frontend-ui/.beads/issues.jsonl",
		[]beads.TaskSummary{
			{
				ID:        "swarm-eli6.4",
				Title:     "TUI polish and premium feel",
				Status:    "in_progress",
				Priority:  1,
				IssueType: "task",
				UpdatedAt: now.Add(-25 * time.Minute),
			},
			{
				ID:        "swarm-eli6.4.1",
				Title:     "Workspace empty states and onboarding prompts",
				Status:    "blocked",
				Priority:  1,
				IssueType: "task",
				UpdatedAt: now.Add(-40 * time.Minute),
			},
			{
				ID:        "swarm-cmtw",
				Title:     "TUI mailbox view",
				Status:    "closed",
				Priority:  2,
				IssueType: "task",
				UpdatedAt: now.Add(-2 * time.Hour),
			},
		},
	)

	snapshots["ws-3"] = beadsSnapshot{}

	snapshots["ws-4"] = buildSampleBeadsSnapshot(
		"/home/user/projects/ml-models/.beads/issues.jsonl",
		[]beads.TaskSummary{},
	)

	return snapshots
}

func buildSampleBeadsSnapshot(path string, tasks []beads.TaskSummary) beadsSnapshot {
	if tasks == nil {
		tasks = []beads.TaskSummary{}
	}
	sort.Slice(tasks, func(i, j int) bool {
		left := tasks[i]
		right := tasks[j]
		if beadsStatusRank(left.Status) != beadsStatusRank(right.Status) {
			return beadsStatusRank(left.Status) < beadsStatusRank(right.Status)
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})
	return beadsSnapshot{
		Detected:     true,
		IssuesPath:   path,
		Tasks:        tasks,
		StatusCounts: summarizeBeadsStatusCounts(tasks),
		Total:        len(tasks),
		LoadedAt:     time.Now(),
	}
}

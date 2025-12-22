// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"strings"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// GitStatusData contains data for rendering git status information.
type GitStatusData struct {
	Info           *models.GitInfo
	StagedFiles    []string // List of staged file paths
	ChangedFiles   []string // List of unstaged changed file paths
	UntrackedFiles []string // List of untracked file paths
}

// RenderGitStatusPanel renders a detailed git status panel.
func RenderGitStatusPanel(styleSet styles.Styles, data GitStatusData, width int) string {
	if data.Info == nil || !data.Info.IsRepo {
		lines := []string{
			styleSet.Muted.Render("Not a git repository"),
		}
		return renderGitPanelContainer(styleSet, "Git", lines, width)
	}

	lines := []string{}

	// Branch info
	branchLine := renderBranchLine(styleSet, data.Info)
	lines = append(lines, branchLine)

	// Remote sync status
	if data.Info.Ahead > 0 || data.Info.Behind > 0 {
		syncLine := renderSyncStatus(styleSet, data.Info)
		lines = append(lines, syncLine)
	}

	// Dirty status
	if data.Info.IsDirty {
		lines = append(lines, styleSet.Warning.Render("● Uncommitted changes"))
	} else {
		lines = append(lines, styleSet.Success.Render("○ Clean working tree"))
	}

	// Staged files
	if len(data.StagedFiles) > 0 {
		lines = append(lines, "")
		lines = append(lines, styleSet.Accent.Render("Staged"))
		for i, file := range data.StagedFiles {
			if i >= 5 {
				lines = append(lines, styleSet.Muted.Render(fmt.Sprintf("  ... and %d more", len(data.StagedFiles)-5)))
				break
			}
			lines = append(lines, styleSet.Success.Render(fmt.Sprintf("  + %s", truncateFilePath(file, width-6))))
		}
	}

	// Changed files (unstaged)
	if len(data.ChangedFiles) > 0 {
		lines = append(lines, "")
		lines = append(lines, styleSet.Accent.Render("Modified"))
		for i, file := range data.ChangedFiles {
			if i >= 5 {
				lines = append(lines, styleSet.Muted.Render(fmt.Sprintf("  ... and %d more", len(data.ChangedFiles)-5)))
				break
			}
			lines = append(lines, styleSet.Warning.Render(fmt.Sprintf("  ~ %s", truncateFilePath(file, width-6))))
		}
	}

	// Untracked files
	if len(data.UntrackedFiles) > 0 {
		lines = append(lines, "")
		lines = append(lines, styleSet.Accent.Render("Untracked"))
		for i, file := range data.UntrackedFiles {
			if i >= 3 {
				lines = append(lines, styleSet.Muted.Render(fmt.Sprintf("  ... and %d more", len(data.UntrackedFiles)-3)))
				break
			}
			lines = append(lines, styleSet.Muted.Render(fmt.Sprintf("  ? %s", truncateFilePath(file, width-6))))
		}
	}

	// Last commit
	if data.Info.LastCommit != "" {
		lines = append(lines, "")
		shortHash := data.Info.LastCommit
		if len(shortHash) > 7 {
			shortHash = shortHash[:7]
		}
		lines = append(lines, styleSet.Muted.Render(fmt.Sprintf("Last: %s", shortHash)))
	}

	return renderGitPanelContainer(styleSet, "Git", lines, width)
}

// RenderGitStatusCompact renders a compact single-line git status.
// Format: "main ↑2↓1 *dirty"
func RenderGitStatusCompact(styleSet styles.Styles, info *models.GitInfo) string {
	if info == nil || !info.IsRepo {
		return styleSet.Muted.Render("--")
	}

	parts := []string{}

	// Branch name
	branch := info.Branch
	if branch == "" {
		branch = "HEAD"
	}
	parts = append(parts, styleSet.Accent.Render(branch))

	// Ahead/behind
	if info.Ahead > 0 {
		parts = append(parts, styleSet.Success.Render(fmt.Sprintf("↑%d", info.Ahead)))
	}
	if info.Behind > 0 {
		parts = append(parts, styleSet.Warning.Render(fmt.Sprintf("↓%d", info.Behind)))
	}

	// Dirty indicator
	if info.IsDirty {
		parts = append(parts, styleSet.Warning.Render("*"))
	}

	return strings.Join(parts, " ")
}

// RenderGitBranchBadge renders a branch badge.
func RenderGitBranchBadge(styleSet styles.Styles, info *models.GitInfo) string {
	if info == nil || !info.IsRepo {
		return ""
	}

	branch := info.Branch
	if branch == "" {
		branch = "detached"
	}

	// Truncate long branch names
	if len(branch) > 20 {
		branch = branch[:17] + "..."
	}

	icon := "⎇"
	if info.IsDirty {
		return styleSet.Warning.Render(fmt.Sprintf("%s %s*", icon, branch))
	}
	return styleSet.Muted.Render(fmt.Sprintf("%s %s", icon, branch))
}

// RenderGitSyncStatus renders the ahead/behind status with icons.
func RenderGitSyncStatus(styleSet styles.Styles, info *models.GitInfo) string {
	if info == nil || !info.IsRepo {
		return ""
	}

	if info.Ahead == 0 && info.Behind == 0 {
		return styleSet.Success.Render("✓ synced")
	}

	parts := []string{}
	if info.Ahead > 0 {
		parts = append(parts, styleSet.Success.Render(fmt.Sprintf("↑%d ahead", info.Ahead)))
	}
	if info.Behind > 0 {
		parts = append(parts, styleSet.Warning.Render(fmt.Sprintf("↓%d behind", info.Behind)))
	}

	return strings.Join(parts, " ")
}

// RenderGitChangeSummary renders a summary of changes.
// Format: "3 staged, 5 modified, 2 untracked"
func RenderGitChangeSummary(styleSet styles.Styles, data GitStatusData) string {
	if data.Info == nil || !data.Info.IsRepo {
		return ""
	}

	if !data.Info.IsDirty && len(data.StagedFiles) == 0 && len(data.ChangedFiles) == 0 && len(data.UntrackedFiles) == 0 {
		return styleSet.Muted.Render("No changes")
	}

	parts := []string{}
	if len(data.StagedFiles) > 0 {
		parts = append(parts, styleSet.Success.Render(fmt.Sprintf("%d staged", len(data.StagedFiles))))
	}
	if len(data.ChangedFiles) > 0 {
		parts = append(parts, styleSet.Warning.Render(fmt.Sprintf("%d modified", len(data.ChangedFiles))))
	}
	if len(data.UntrackedFiles) > 0 {
		parts = append(parts, styleSet.Muted.Render(fmt.Sprintf("%d untracked", len(data.UntrackedFiles))))
	}

	if len(parts) == 0 {
		if data.Info.IsDirty {
			return styleSet.Warning.Render("Changes pending")
		}
		return styleSet.Muted.Render("No changes")
	}

	return strings.Join(parts, ", ")
}

func renderBranchLine(styleSet styles.Styles, info *models.GitInfo) string {
	branch := info.Branch
	if branch == "" {
		branch = "detached HEAD"
	}

	icon := "⎇"
	branchPart := fmt.Sprintf("%s %s", icon, branch)

	if info.RemoteURL != "" {
		// Extract remote name from URL
		remote := extractRemoteName(info.RemoteURL)
		if remote != "" {
			branchPart += styleSet.Muted.Render(fmt.Sprintf(" (%s)", remote))
		}
	}

	return styleSet.Text.Render(branchPart)
}

func renderSyncStatus(styleSet styles.Styles, info *models.GitInfo) string {
	parts := []string{}

	if info.Ahead > 0 {
		parts = append(parts, styleSet.Success.Render(fmt.Sprintf("↑%d to push", info.Ahead)))
	}
	if info.Behind > 0 {
		parts = append(parts, styleSet.Warning.Render(fmt.Sprintf("↓%d to pull", info.Behind)))
	}

	if len(parts) == 0 {
		return styleSet.Success.Render("✓ In sync with remote")
	}

	return strings.Join(parts, " | ")
}

func renderGitPanelContainer(styleSet styles.Styles, title string, lines []string, width int) string {
	header := styleSet.Accent.Render(title)
	content := strings.Join(append([]string{header}, lines...), "\n")
	return styleSet.Panel.Copy().Width(width).Padding(0, 1).Render(content)
}

func truncateFilePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	if maxLen <= 5 {
		return "..."
	}
	return "..." + path[len(path)-maxLen+3:]
}

func extractRemoteName(url string) string {
	// Extract owner/repo from common URL formats
	// git@github.com:owner/repo.git -> owner/repo
	// https://github.com/owner/repo.git -> owner/repo

	url = strings.TrimSuffix(url, ".git")

	// SSH format
	if strings.HasPrefix(url, "git@") {
		parts := strings.SplitN(url, ":", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}

	// HTTPS format
	if strings.Contains(url, "://") {
		parts := strings.Split(url, "/")
		if len(parts) >= 2 {
			return strings.Join(parts[len(parts)-2:], "/")
		}
	}

	return ""
}

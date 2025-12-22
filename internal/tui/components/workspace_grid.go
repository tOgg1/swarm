// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// WorkspaceGrid holds state for the workspace grid display.
type WorkspaceGrid struct {
	Workspaces    []*models.Workspace
	SelectedIndex int
	ScrollOffset  int
	Filter        string
	Width         int
	Height        int
	Columns       int
}

// NewWorkspaceGrid creates a new workspace grid.
func NewWorkspaceGrid() *WorkspaceGrid {
	return &WorkspaceGrid{
		Workspaces:    make([]*models.Workspace, 0),
		SelectedIndex: 0,
		ScrollOffset:  0,
		Columns:       2, // Default to 2 columns
	}
}

// SetWorkspaces updates the workspace list.
func (g *WorkspaceGrid) SetWorkspaces(workspaces []*models.Workspace) {
	g.Workspaces = workspaces
	g.clampSelection()
}

// FilteredWorkspaces returns workspaces matching the current filter.
func (g *WorkspaceGrid) FilteredWorkspaces() []*models.Workspace {
	if g.Filter == "" {
		return g.Workspaces
	}

	filter := strings.ToLower(strings.TrimSpace(g.Filter))
	if filter == "" {
		return g.Workspaces
	}
	tokens := strings.Fields(filter)
	filtered := make([]*models.Workspace, 0)
	for _, ws := range g.Workspaces {
		haystack := strings.ToLower(strings.Join([]string{ws.Name, ws.RepoPath}, " "))
		if matchesTokens(haystack, tokens) {
			filtered = append(filtered, ws)
		}
	}
	return filtered
}

// SetFilter sets the search filter and resets selection.
func (g *WorkspaceGrid) SetFilter(filter string) {
	g.Filter = filter
	g.SelectedIndex = 0
	g.ScrollOffset = 0
}

// MoveUp moves selection up.
func (g *WorkspaceGrid) MoveUp() {
	if g.SelectedIndex >= g.Columns {
		g.SelectedIndex -= g.Columns
		g.ensureVisible()
	}
}

// MoveDown moves selection down.
func (g *WorkspaceGrid) MoveDown() {
	filtered := g.FilteredWorkspaces()
	if g.SelectedIndex+g.Columns < len(filtered) {
		g.SelectedIndex += g.Columns
		g.ensureVisible()
	}
}

// MoveLeft moves selection left.
func (g *WorkspaceGrid) MoveLeft() {
	if g.SelectedIndex > 0 {
		g.SelectedIndex--
		g.ensureVisible()
	}
}

// MoveRight moves selection right.
func (g *WorkspaceGrid) MoveRight() {
	filtered := g.FilteredWorkspaces()
	if g.SelectedIndex < len(filtered)-1 {
		g.SelectedIndex++
		g.ensureVisible()
	}
}

// SelectedWorkspace returns the currently selected workspace.
func (g *WorkspaceGrid) SelectedWorkspace() *models.Workspace {
	filtered := g.FilteredWorkspaces()
	if g.SelectedIndex < 0 || g.SelectedIndex >= len(filtered) {
		return nil
	}
	return filtered[g.SelectedIndex]
}

// SelectByID selects a workspace by ID within the current filtered list.
func (g *WorkspaceGrid) SelectByID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	filtered := g.FilteredWorkspaces()
	for idx, ws := range filtered {
		if ws != nil && strings.EqualFold(ws.ID, id) {
			g.SelectedIndex = idx
			g.ensureVisible()
			return true
		}
	}
	return false
}

// SelectByName selects a workspace by name within the current filtered list.
func (g *WorkspaceGrid) SelectByName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	filtered := g.FilteredWorkspaces()
	for idx, ws := range filtered {
		if ws != nil && strings.EqualFold(ws.Name, name) {
			g.SelectedIndex = idx
			g.ensureVisible()
			return true
		}
	}
	return false
}

// clampSelection ensures selection is within bounds.
func (g *WorkspaceGrid) clampSelection() {
	filtered := g.FilteredWorkspaces()
	if len(filtered) == 0 {
		g.SelectedIndex = 0
		return
	}
	if g.SelectedIndex >= len(filtered) {
		g.SelectedIndex = len(filtered) - 1
	}
	if g.SelectedIndex < 0 {
		g.SelectedIndex = 0
	}
}

// ensureVisible adjusts scroll offset to keep selection visible.
func (g *WorkspaceGrid) ensureVisible() {
	if g.Height <= 0 {
		return
	}

	// Calculate visible rows (assuming ~3 lines per card)
	cardHeight := 4
	visibleRows := g.Height / cardHeight
	if visibleRows < 1 {
		visibleRows = 1
	}

	selectedRow := g.SelectedIndex / g.Columns

	// Scroll up if needed
	if selectedRow < g.ScrollOffset {
		g.ScrollOffset = selectedRow
	}

	// Scroll down if needed
	if selectedRow >= g.ScrollOffset+visibleRows {
		g.ScrollOffset = selectedRow - visibleRows + 1
	}
}

// Render renders the workspace grid.
func (g *WorkspaceGrid) Render(styleSet styles.Styles) string {
	filtered := g.FilteredWorkspaces()
	if len(filtered) == 0 {
		if g.Filter != "" {
			return styleSet.Warning.Render(fmt.Sprintf("No workspaces match '%s'.", g.Filter)) + "\n" +
				styleSet.Muted.Render("Press / to edit or clear the filter.")
		}
		return styleSet.Muted.Render("No workspaces. Use 'swarm ws create' to create one.")
	}

	// Calculate card width based on available width
	cardWidth := agentCardWidth
	if g.Width > 0 && g.Columns > 0 {
		cardWidth = (g.Width - (g.Columns-1)*2) / g.Columns // 2 chars gap between columns
		if cardWidth < 25 {
			cardWidth = 25
		}
		if cardWidth > agentCardWidth {
			cardWidth = agentCardWidth
		}
	}

	// Build rows
	var rows []string
	for i := 0; i < len(filtered); i += g.Columns {
		var cards []string
		for j := 0; j < g.Columns && i+j < len(filtered); j++ {
			idx := i + j
			ws := filtered[idx]
			isSelected := idx == g.SelectedIndex
			card := renderGridCard(styleSet, ws, isSelected, cardWidth)
			cards = append(cards, card)
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top, cards...)
		rows = append(rows, row)
	}

	// Apply scroll offset
	startRow := g.ScrollOffset
	if startRow < 0 {
		startRow = 0
	}
	if startRow >= len(rows) {
		startRow = 0
	}

	visibleRows := rows
	if startRow > 0 {
		visibleRows = rows[startRow:]
	}

	grid := lipgloss.JoinVertical(lipgloss.Left, visibleRows...)

	// Add filter status if filtering
	if g.Filter != "" {
		header := styleSet.Muted.Render(fmt.Sprintf("Filter: %s (%d/%d)", g.Filter, len(filtered), len(g.Workspaces)))
		grid = header + "\n" + grid
	}

	return grid
}

// renderGridCard renders a single workspace card for the grid.
func renderGridCard(styleSet styles.Styles, ws *models.Workspace, selected bool, width int) string {
	if width < 20 {
		width = 20
	}

	// Build card content
	name := truncate(ws.Name, width-4)
	if name == "" {
		name = truncate(filepath.Base(ws.RepoPath), width-4)
	}

	// Status badge
	statusBadge := renderWorkspaceStatus(styleSet, ws.Status)

	// Branch info
	branchLine := ""
	if ws.GitInfo != nil && ws.GitInfo.Branch != "" {
		branch := truncate(ws.GitInfo.Branch, width-8)
		branchLine = styleSet.Muted.Render("  " + branch)
		if ws.GitInfo.IsDirty {
			branchLine += styleSet.Warning.Render(" *")
		}
	}

	// Agent count
	agentLine := styleSet.Text.Render(fmt.Sprintf("  Agents: %d", ws.AgentCount))

	// Alert indicator
	alertLine := ""
	if len(ws.Alerts) > 0 {
		token := renderAlertToken(styleSet, ws.Alerts[0])
		alertLabel := styleSet.Muted.Render("  Alerts:")
		alertCount := styleSet.Muted.Render(fmt.Sprintf(" %d", len(ws.Alerts)))
		alertLine = fmt.Sprintf("%s %s%s", alertLabel, token, alertCount)
	}

	// Build card
	lines := []string{
		name + " " + statusBadge,
	}
	if branchLine != "" {
		lines = append(lines, branchLine)
	}
	lines = append(lines, agentLine)
	if alertLine != "" {
		lines = append(lines, alertLine)
	}

	content := strings.Join(lines, "\n")

	// Card style
	cardStyle := lipgloss.NewStyle().
		Width(width).
		Padding(0, 1).
		Margin(0, 1, 1, 0).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(styleSet.Theme.Tokens.Border))

	if selected {
		cardStyle = cardStyle.
			BorderForeground(lipgloss.Color(styleSet.Theme.Tokens.Focus)).
			Background(lipgloss.Color(styleSet.Theme.Tokens.Panel))
	}

	return cardStyle.Render(content)
}

func renderAlertToken(styleSet styles.Styles, alert models.Alert) string {
	switch alert.Type {
	case models.AlertTypeApprovalNeeded:
		return styleSet.Info.Render("[APP]")
	case models.AlertTypeRateLimit:
		return styleSet.Warning.Render("[RL]")
	case models.AlertTypeCooldown:
		return styleSet.Warning.Render("[CD]")
	case models.AlertTypeError:
		return styleSet.Error.Render("[ERR]")
	default:
		return styleSet.Warning.Render("[!]")
	}
}

func renderWorkspaceStatus(styleSet styles.Styles, status models.WorkspaceStatus) string {
	switch status {
	case models.WorkspaceStatusActive:
		return styleSet.Success.Render("[OK]")
	case models.WorkspaceStatusInactive:
		return styleSet.Muted.Render("[--]")
	case models.WorkspaceStatusError:
		return styleSet.Error.Render("[ERR]")
	default:
		return styleSet.Muted.Render("[?]")
	}
}

func truncate(s string, maxLen int) string {
	if maxLen < 3 {
		maxLen = 3
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func matchesTokens(haystack string, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			continue
		}
		if !fuzzyMatch(haystack, token) {
			return false
		}
	}
	return true
}

func fuzzyMatch(haystack, token string) bool {
	if token == "" {
		return true
	}
	needle := []rune(strings.ReplaceAll(token, " ", ""))
	if len(needle) == 0 {
		return true
	}
	matchIdx := 0
	for _, r := range []rune(haystack) {
		if r == needle[matchIdx] {
			matchIdx++
			if matchIdx == len(needle) {
				return true
			}
		}
	}
	return false
}

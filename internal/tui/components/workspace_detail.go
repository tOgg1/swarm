// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// WorkspaceHeaderData contains data for the workspace header.
type WorkspaceHeaderData struct {
	Workspace *models.Workspace
	// BeadsTasks is the number of open/in_progress beads tasks.
	BeadsTasks int
	// BeadsActive is the number of in_progress beads tasks.
	BeadsActive int
}

// WorkspaceHeader renders the header for a workspace detail view.
func WorkspaceHeader(styleSet styles.Styles, ws *models.Workspace, width int) string {
	return WorkspaceHeaderWithBeads(styleSet, WorkspaceHeaderData{Workspace: ws}, width)
}

// WorkspaceHeaderWithBeads renders the header with optional beads task info.
func WorkspaceHeaderWithBeads(styleSet styles.Styles, data WorkspaceHeaderData, width int) string {
	ws := data.Workspace
	if ws == nil {
		return styleSet.Muted.Render("No workspace selected")
	}

	// Name and status
	statusBadge := renderWorkspaceStatusBadge(styleSet, ws.Status)
	nameLine := fmt.Sprintf("%s  %s", styleSet.Title.Render(ws.Name), statusBadge)

	// Git branch info
	branchLine := ""
	if ws.GitInfo != nil && ws.GitInfo.Branch != "" {
		dirty := ""
		if ws.GitInfo.IsDirty {
			dirty = styleSet.Warning.Render(" *")
		}
		branchLine = fmt.Sprintf("%s %s%s", styleSet.Muted.Render("branch:"), ws.GitInfo.Branch, dirty)
	}

	// Agent counts
	agentLine := styleSet.Muted.Render(fmt.Sprintf("Agents: %d", ws.AgentCount))

	// Alert summary
	alertLine := ""
	if len(ws.Alerts) > 0 {
		alertLine = styleSet.Warning.Render(fmt.Sprintf("ALERTS:%d", len(ws.Alerts)))
	}

	// Beads tasks summary
	tasksLine := ""
	if data.BeadsTasks > 0 || data.BeadsActive > 0 {
		if data.BeadsActive > 0 {
			tasksLine = styleSet.Warning.Render(fmt.Sprintf("TASKS:%d(%d)", data.BeadsTasks, data.BeadsActive))
		} else {
			tasksLine = styleSet.Muted.Render(fmt.Sprintf("TASKS:%d", data.BeadsTasks))
		}
	}

	// Build header
	parts := []string{nameLine}
	if branchLine != "" {
		parts = append(parts, branchLine)
	}
	parts = append(parts, agentLine)
	if tasksLine != "" {
		parts = append(parts, tasksLine)
	}
	if alertLine != "" {
		parts = append(parts, alertLine)
	}

	content := strings.Join(parts, "  |  ")

	headerStyle := lipgloss.NewStyle().
		Width(width).
		Padding(0, 1).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(lipgloss.Color("240"))

	return headerStyle.Render(content)
}

func renderWorkspaceStatusBadge(styleSet styles.Styles, status models.WorkspaceStatus) string {
	switch status {
	case models.WorkspaceStatusActive:
		return styleSet.Success.Render("[ACTIVE]")
	case models.WorkspaceStatusInactive:
		return styleSet.Muted.Render("[INACTIVE]")
	case models.WorkspaceStatusError:
		return styleSet.Error.Render("[ERROR]")
	default:
		return styleSet.Muted.Render("[?]")
	}
}

// RenderAgentPaneCard renders a single agent card for the workspace pane grid.
func RenderAgentPaneCard(styleSet styles.Styles, agent *models.Agent, selected bool, width int) string {
	if agent == nil {
		return ""
	}

	if width < 20 {
		width = 20
	}

	// Agent ID (short)
	idShort := agent.ID
	if len(idShort) > 8 {
		idShort = idShort[:8]
	}

	// State badge
	stateBadge := RenderAgentStateBadge(styleSet, agent.State)

	// Type
	typeLine := styleSet.Muted.Render(string(agent.Type))

	// Queue info
	queueLine := ""
	if agent.QueueLength > 0 {
		queueLine = styleSet.Info.Render(fmt.Sprintf("Q:%d", agent.QueueLength))
	}

	// Confidence indicator (if available)
	confLine := ""
	if agent.StateInfo.Confidence != "" {
		confLine = renderConfidence(styleSet, agent.StateInfo.Confidence)
	}

	// Build card content
	lines := []string{
		fmt.Sprintf("%s  %s", styleSet.Accent.Render(idShort), stateBadge),
		typeLine,
	}
	if queueLine != "" {
		lines = append(lines, queueLine)
	}
	if confLine != "" {
		lines = append(lines, confLine)
	}

	content := strings.Join(lines, "\n")

	// Card style
	cardStyle := lipgloss.NewStyle().
		Width(width).
		Padding(0, 1).
		Margin(0, 1, 1, 0)

	if selected {
		cardStyle = cardStyle.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4"))
	} else {
		cardStyle = cardStyle.
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240"))
	}

	return cardStyle.Render(content)
}

func renderConfidence(styleSet styles.Styles, conf models.StateConfidence) string {
	switch conf {
	case models.StateConfidenceHigh:
		return styleSet.Success.Render("###")
	case models.StateConfidenceMedium:
		return styleSet.Warning.Render("##-")
	case models.StateConfidenceLow:
		return styleSet.Error.Render("#--")
	default:
		return styleSet.Muted.Render("---")
	}
}

// AgentInspector renders a side panel with detailed agent info.
type AgentInspector struct {
	Agent *models.Agent
	Width int
}

// NewAgentInspector creates a new agent inspector.
func NewAgentInspector() *AgentInspector {
	return &AgentInspector{
		Width: 40,
	}
}

// SetAgent sets the agent to inspect.
func (i *AgentInspector) SetAgent(agent *models.Agent) {
	i.Agent = agent
}

// Render renders the inspector panel.
func (i *AgentInspector) Render(styleSet styles.Styles) string {
	if i.Agent == nil {
		return i.renderEmpty(styleSet)
	}

	lines := []string{
		styleSet.Accent.Render("Agent Inspector"),
		"",
		fmt.Sprintf("ID:    %s", i.Agent.ID),
		fmt.Sprintf("Type:  %s", i.Agent.Type),
		fmt.Sprintf("Pane:  %s", i.Agent.TmuxPane),
		"",
		styleSet.Accent.Render("State"),
		fmt.Sprintf("State: %s", RenderAgentStateBadge(styleSet, i.Agent.State)),
		fmt.Sprintf("Conf:  %s", renderConfidence(styleSet, i.Agent.StateInfo.Confidence)),
	}

	if i.Agent.StateInfo.Reason != "" {
		lines = append(lines, "")
		lines = append(lines, styleSet.Muted.Render("Reason:"))
		// Word wrap reason
		wrapped := wordWrap(i.Agent.StateInfo.Reason, i.Width-4)
		for _, line := range wrapped {
			lines = append(lines, "  "+line)
		}
	}

	if len(i.Agent.StateInfo.Evidence) > 0 {
		lines = append(lines, "")
		lines = append(lines, styleSet.Muted.Render("Evidence:"))
		for _, ev := range i.Agent.StateInfo.Evidence {
			if len(ev) > i.Width-6 {
				ev = ev[:i.Width-9] + "..."
			}
			lines = append(lines, "  - "+ev)
		}
	}

	if i.Agent.QueueLength > 0 {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Queue: %d items", i.Agent.QueueLength))
	}

	content := strings.Join(lines, "\n")

	panelStyle := lipgloss.NewStyle().
		Width(i.Width).
		Padding(0, 1).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240"))

	return panelStyle.Render(content)
}

func (i *AgentInspector) renderEmpty(styleSet styles.Styles) string {
	content := styleSet.Muted.Render("Select an agent to inspect")

	panelStyle := lipgloss.NewStyle().
		Width(i.Width).
		Padding(1, 1).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240"))

	return panelStyle.Render(content)
}

func wordWrap(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	currentLine := words[0]

	for _, word := range words[1:] {
		if len(currentLine)+1+len(word) <= width {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = word
		}
	}
	lines = append(lines, currentLine)

	return lines
}

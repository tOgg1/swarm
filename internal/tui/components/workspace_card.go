// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// WorkspaceCard contains data needed to render a workspace card.
type WorkspaceCard struct {
	Repo          string
	Node          string
	Branch        string
	Pulse         string
	AgentsWorking int
	AgentsIdle    int
	AgentsBlocked int
	Alerts        []string
}

// RenderWorkspaceCard renders a workspace card with basic metadata.
func RenderWorkspaceCard(styleSet styles.Styles, card WorkspaceCard) string {
	header := styleSet.Accent.Render(fmt.Sprintf("%s @ %s", card.Repo, card.Node))
	branch := styleSet.Muted.Render(fmt.Sprintf("Branch: %s", defaultIfEmpty(card.Branch, "--")))
	pulse := styleSet.Info.Render(fmt.Sprintf("Pulse: %s", defaultIfEmpty(card.Pulse, "idle")))

	agents := fmt.Sprintf(
		"Agents: %s %s %s",
		styleSet.StatusWork.Render(fmt.Sprintf("W:%d", card.AgentsWorking)),
		styleSet.StatusIdle.Render(fmt.Sprintf("I:%d", card.AgentsIdle)),
		styleSet.Warning.Render(fmt.Sprintf("B:%d", card.AgentsBlocked)),
	)

	alertLine := styleSet.Muted.Render("Alerts: none")
	if len(card.Alerts) > 0 {
		alertLine = styleSet.Warning.Render(fmt.Sprintf("Alerts: %s", card.Alerts[0]))
	}

	content := strings.Join([]string{
		header,
		branch,
		pulse,
		agents,
		alertLine,
	}, "\n")

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(styleSet.Theme.Tokens.Border)).
		Padding(0, 1).
		Width(agentCardWidth).
		MaxWidth(agentCardWidth)

	return cardStyle.Render(content)
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

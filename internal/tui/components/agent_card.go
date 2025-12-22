// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

const maxReasonLength = 44

// AgentCard contains data needed to render an agent card.
type AgentCard struct {
	Name         string
	Type         models.AgentType
	Model        string
	Profile      string
	State        models.AgentState
	Reason       string
	QueueLength  int
	LastActivity *time.Time
}

// RenderAgentCard renders a compact agent summary card.
func RenderAgentCard(styleSet styles.Styles, card AgentCard) string {
	header := styleSet.Accent.Render(defaultIfEmpty(card.Name, "Agent"))
	typeLine := styleSet.Text.Render(fmt.Sprintf("Type: %s  Model: %s", formatAgentType(card.Type), defaultIfEmpty(card.Model, "--")))
	profileLine := styleSet.Muted.Render(fmt.Sprintf("Profile: %s", defaultIfEmpty(card.Profile, "--")))

	reason := strings.TrimSpace(card.Reason)
	if reason == "" {
		reason = "No reason reported"
	}
	reason = truncate(reason, maxReasonLength)
	stateBadge := RenderAgentStateBadge(styleSet, card.State)
	stateLine := fmt.Sprintf("State: %s %s", stateBadge, styleSet.Muted.Render(reason))

	queueValue := "--"
	if card.QueueLength >= 0 {
		queueValue = fmt.Sprintf("%d", card.QueueLength)
	}
	queueLine := styleSet.Text.Render(fmt.Sprintf("Queue: %s  Last: %s", queueValue, formatLastActivity(card.LastActivity)))

	content := strings.Join([]string{
		header,
		typeLine,
		profileLine,
		stateLine,
		queueLine,
	}, "\n")

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1)

	return cardStyle.Render(content)
}

func formatAgentType(agentType models.AgentType) string {
	if strings.TrimSpace(string(agentType)) == "" {
		return "unknown"
	}
	return string(agentType)
}

func formatLastActivity(ts *time.Time) string {
	if ts == nil || ts.IsZero() {
		return "--"
	}
	return ts.Format("15:04:05")
}

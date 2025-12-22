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

const (
	maxReasonLength = 44
	agentCardWidth  = 60
)

// AgentCard contains data needed to render an agent card.
type AgentCard struct {
	Name          string
	Type          models.AgentType
	Model         string
	Profile       string
	State         models.AgentState
	Confidence    models.StateConfidence
	Reason        string
	QueueLength   int
	LastActivity  *time.Time
	CooldownUntil *time.Time
}

// RenderAgentCard renders a compact agent summary card.
func RenderAgentCard(styleSet styles.Styles, card AgentCard, selected bool) string {
	headerStyle := styleSet.Accent
	textStyle := styleSet.Text
	mutedStyle := styleSet.Muted
	if !selected {
		headerStyle = styleSet.Muted
		textStyle = styleSet.Muted
		mutedStyle = styleSet.Muted
	}

	header := headerStyle.Render(defaultIfEmpty(card.Name, "Agent"))
	typeLine := textStyle.Render(fmt.Sprintf("Type: %s  Model: %s", formatAgentType(card.Type), defaultIfEmpty(card.Model, "--")))
	profileLine := mutedStyle.Render(fmt.Sprintf("Profile: %s", defaultIfEmpty(card.Profile, "--")))

	reason := strings.TrimSpace(card.Reason)
	if reason == "" {
		reason = "No reason reported"
	}
	reason = truncate(reason, maxReasonLength)
	stateBadge := RenderAgentStateBadge(styleSet, card.State)
	stateLabel := "State:"
	if !selected {
		stateLabel = styleSet.Muted.Render("State:")
	}
	stateLine := fmt.Sprintf("%s %s", stateLabel, stateBadge)
	reasonLine := mutedStyle.Render(fmt.Sprintf("Why: %s", reason))
	cooldownLine := renderCooldownLine(styleSet, card.CooldownUntil)
	confidenceLine := renderConfidenceLine(styleSet, card.Confidence)

	queueValue := "--"
	if card.QueueLength >= 0 {
		queueValue = fmt.Sprintf("%d", card.QueueLength)
	}
	queueLine := textStyle.Render(fmt.Sprintf("Queue: %s  Last: %s", queueValue, formatLastActivity(card.LastActivity)))

	lines := []string{
		header,
		typeLine,
		profileLine,
		stateLine,
		reasonLine,
	}
	if cooldownLine != "" {
		lines = append(lines, cooldownLine)
	}
	lines = append(lines, confidenceLine, queueLine)

	content := strings.Join(lines, "\n")

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(agentCardWidth).
		MaxWidth(agentCardWidth)
	if selected {
		cardStyle = cardStyle.
			BorderForeground(lipgloss.Color(styleSet.Theme.Tokens.Focus)).
			Background(lipgloss.Color(styleSet.Theme.Tokens.Panel))
	} else {
		cardStyle = cardStyle.
			BorderForeground(lipgloss.Color(styleSet.Theme.Tokens.Border)).
			Foreground(lipgloss.Color(styleSet.Theme.Tokens.TextMuted))
	}

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

func renderConfidenceLine(styleSet styles.Styles, confidence models.StateConfidence) string {
	label, bars, style := confidenceDescriptor(styleSet, confidence)
	prefix := styleSet.Muted.Render("Confidence:")
	return fmt.Sprintf("%s %s", prefix, style.Render(fmt.Sprintf("%s %s", label, bars)))
}

func confidenceDescriptor(styleSet styles.Styles, confidence models.StateConfidence) (string, string, lipgloss.Style) {
	switch confidence {
	case models.StateConfidenceHigh:
		return "High", "###", styleSet.Success
	case models.StateConfidenceMedium:
		return "Medium", "##-", styleSet.Warning
	case models.StateConfidenceLow:
		return "Low", "#--", styleSet.Error
	default:
		return "Unknown", "---", styleSet.Muted
	}
}

func renderCooldownLine(styleSet styles.Styles, cooldownUntil *time.Time) string {
	if cooldownUntil == nil || cooldownUntil.IsZero() {
		return ""
	}
	remaining := time.Until(*cooldownUntil)
	if remaining <= 0 {
		return styleSet.Muted.Render("Cooldown: expired")
	}
	return styleSet.Warning.Render(fmt.Sprintf("Cooldown: %s", formatDuration(remaining)))
}

func formatDuration(value time.Duration) string {
	if value < time.Second {
		return "<1s"
	}
	if value < time.Minute {
		return value.Round(time.Second).String()
	}
	if value < time.Hour {
		rounded := value.Round(time.Second)
		minutes := int(rounded.Minutes())
		seconds := int(rounded.Seconds()) % 60
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return value.Round(time.Minute).String()
}

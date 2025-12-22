// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// RenderAgentStateBadge renders an agent state with icon and color.
func RenderAgentStateBadge(styleSet styles.Styles, state models.AgentState) string {
	icon, label, style := stateDescriptor(styleSet, state)
	return style.Render(fmt.Sprintf("%s %s", icon, label))
}

func stateDescriptor(styleSet styles.Styles, state models.AgentState) (string, string, lipgloss.Style) {
	switch state {
	case models.AgentStateWorking:
		return ">", "Working", styleSet.StatusWork
	case models.AgentStateIdle:
		return "OK", "Idle", styleSet.StatusIdle
	case models.AgentStateAwaitingApproval:
		return "APP", "Approval", styleSet.Info
	case models.AgentStateRateLimited:
		return "RL", "Rate limit", styleSet.Warning
	case models.AgentStateError:
		return "ERR", "Error", styleSet.StatusError
	case models.AgentStatePaused:
		return "P", "Paused", styleSet.StatusPaused
	case models.AgentStateStarting:
		return "~", "Starting", styleSet.Info
	case models.AgentStateStopped:
		return "-", "Stopped", styleSet.Muted
	default:
		return "-", normalizeStateLabel(state), styleSet.Muted
	}
}

func normalizeStateLabel(state models.AgentState) string {
	value := strings.TrimSpace(strings.ReplaceAll(string(state), "_", " "))
	if value == "" {
		return "Unknown"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

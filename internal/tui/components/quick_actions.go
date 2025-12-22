// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/swarm/internal/models"
	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// QuickAction represents a keyboard-triggered action.
type QuickAction struct {
	Key     string // Keyboard key (e.g., "P", "I")
	Label   string // Display label (e.g., "Pause", "Interrupt")
	Enabled bool   // Whether the action is available
}

// RenderQuickActionBar renders a horizontal bar of available quick actions.
// Format: "P:Pause  I:Interrupt  R:Restart  V:View  E:Export"
func RenderQuickActionBar(styleSet styles.Styles, actions []QuickAction) string {
	if len(actions) == 0 {
		return ""
	}

	var parts []string
	for _, action := range actions {
		if !action.Enabled {
			continue
		}
		keyStyle := styleSet.Accent.Copy().Bold(true)
		labelStyle := styleSet.Muted
		part := fmt.Sprintf("%s:%s", keyStyle.Render(action.Key), labelStyle.Render(action.Label))
		parts = append(parts, part)
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "  ")
}

// AgentQuickActions returns available quick actions for an agent based on its state.
func AgentQuickActions(state models.AgentState) []QuickAction {
	actions := []QuickAction{
		{Key: "V", Label: "View", Enabled: true},
		{Key: "E", Label: "Export", Enabled: true},
	}

	// Add state-dependent actions
	switch state {
	case models.AgentStateWorking, models.AgentStateIdle, models.AgentStateAwaitingApproval:
		actions = append([]QuickAction{
			{Key: "I", Label: "Interrupt", Enabled: true},
			{Key: "P", Label: "Pause", Enabled: true},
			{Key: "R", Label: "Restart", Enabled: true},
		}, actions...)
	case models.AgentStatePaused:
		actions = append([]QuickAction{
			{Key: "P", Label: "Resume", Enabled: true},
			{Key: "R", Label: "Restart", Enabled: true},
		}, actions...)
	case models.AgentStateError, models.AgentStateStopped:
		actions = append([]QuickAction{
			{Key: "R", Label: "Restart", Enabled: true},
		}, actions...)
	case models.AgentStateRateLimited:
		actions = append([]QuickAction{
			{Key: "I", Label: "Interrupt", Enabled: true},
			{Key: "R", Label: "Restart", Enabled: true},
		}, actions...)
	default:
		actions = append([]QuickAction{
			{Key: "I", Label: "Interrupt", Enabled: true},
			{Key: "R", Label: "Restart", Enabled: true},
		}, actions...)
	}

	return actions
}

// RenderQuickActionHint renders a compact action hint for card footers.
// Format: "[I]nterrupt [R]estart [V]iew"
func RenderQuickActionHint(styleSet styles.Styles, state models.AgentState) string {
	actions := AgentQuickActions(state)
	return RenderQuickActionBar(styleSet, actions)
}

// RenderSelectedCardActions renders the action bar for a selected card.
// This is meant to be displayed below the card when selected.
func RenderSelectedCardActions(styleSet styles.Styles, card AgentCard, width int) string {
	actions := AgentQuickActions(card.State)
	bar := RenderQuickActionBar(styleSet, actions)
	if bar == "" {
		return ""
	}

	// Create a styled container for the action bar
	containerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(styleSet.Theme.Tokens.TextMuted)).
		Width(width).
		Align(lipgloss.Center)

	return containerStyle.Render(bar)
}

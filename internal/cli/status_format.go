// Package cli provides status formatting helpers.
package cli

import (
	"fmt"
	"strings"

	"github.com/opencode-ai/swarm/internal/models"
)

func formatNodeStatus(status models.NodeStatus) string {
	label, color := statusLabelForNode(status)
	return colorize(formatStatusLabel(label, string(status)), color)
}

func formatWorkspaceStatus(status models.WorkspaceStatus) string {
	label, color := statusLabelForWorkspace(status)
	return colorize(formatStatusLabel(label, string(status)), color)
}

func formatAgentState(state models.AgentState) string {
	label, color := statusLabelForAgent(state)
	return colorize(formatStatusLabel(label, string(state)), color)
}

func statusLabelForNode(status models.NodeStatus) (string, string) {
	switch status {
	case models.NodeStatusOnline:
		return "OK", colorGreen
	case models.NodeStatusOffline:
		return "ERR", colorRed
	default:
		return "WARN", colorYellow
	}
}

func statusLabelForWorkspace(status models.WorkspaceStatus) (string, string) {
	switch status {
	case models.WorkspaceStatusActive:
		return "OK", colorGreen
	case models.WorkspaceStatusError:
		return "ERR", colorRed
	default:
		return "WARN", colorYellow
	}
}

func statusLabelForAgent(state models.AgentState) (string, string) {
	switch state {
	case models.AgentStateIdle:
		return "OK", colorGreen
	case models.AgentStateWorking, models.AgentStateStarting:
		return "BUSY", colorCyan
	case models.AgentStateAwaitingApproval, models.AgentStatePaused:
		return "WAIT", colorYellow
	case models.AgentStateRateLimited, models.AgentStateStopped:
		return "WARN", colorMagenta
	case models.AgentStateError:
		return "ERR", colorRed
	default:
		return "WARN", colorYellow
	}
}

func formatStatusLabel(label, status string) string {
	normalized := strings.TrimSpace(status)
	if normalized != "" {
		normalized = strings.ReplaceAll(normalized, "_", " ")
	}
	if normalized == "" {
		return label
	}
	return fmt.Sprintf("%s %s", label, normalized)
}

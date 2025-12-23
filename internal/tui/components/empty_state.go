// Package components provides reusable TUI components.
package components

import (
	"fmt"
	"strings"

	"github.com/opencode-ai/swarm/internal/tui/styles"
)

// EmptyState represents an empty state message with optional suggestions.
type EmptyState struct {
	// Icon is an optional icon to display (e.g., "📭", "🔍", "🚀").
	Icon string
	// Title is the main empty state message.
	Title string
	// Subtitle is an optional secondary message.
	Subtitle string
	// Suggestions are actionable commands the user can run.
	Suggestions []Suggestion
}

// Suggestion represents a suggested command with description.
type Suggestion struct {
	// Command is the CLI command to run (e.g., "swarm ws create <path>").
	Command string
	// Description explains what the command does.
	Description string
}

// Render renders the empty state with the given styles.
func (e EmptyState) Render(styleSet styles.Styles) string {
	var lines []string

	// Icon + Title
	titleLine := e.Title
	if e.Icon != "" {
		titleLine = e.Icon + "  " + titleLine
	}
	lines = append(lines, styleSet.Muted.Render(titleLine))

	// Subtitle
	if e.Subtitle != "" {
		lines = append(lines, styleSet.Muted.Render(e.Subtitle))
	}

	// Suggestions
	if len(e.Suggestions) > 0 {
		lines = append(lines, "")
		lines = append(lines, styleSet.Text.Render("Get started:"))
		for _, s := range e.Suggestions {
			cmdLine := fmt.Sprintf("  %s", styleSet.Accent.Render(s.Command))
			if s.Description != "" {
				cmdLine += styleSet.Muted.Render(fmt.Sprintf("  # %s", s.Description))
			}
			lines = append(lines, cmdLine)
		}
	}

	return strings.Join(lines, "\n")
}

// RenderCompact renders a compact single-line empty state.
func (e EmptyState) RenderCompact(styleSet styles.Styles) string {
	line := e.Title
	if e.Icon != "" {
		line = e.Icon + " " + line
	}
	if len(e.Suggestions) > 0 {
		line += fmt.Sprintf(" Try: %s", e.Suggestions[0].Command)
	}
	return styleSet.Muted.Render(line)
}

// Common empty states for reuse across views.

// EmptyNodes returns an empty state for when no nodes are registered.
func EmptyNodes() EmptyState {
	return EmptyState{
		Icon:     "🖥️",
		Title:    "No nodes registered yet",
		Subtitle: "Nodes are machines where agents run.",
		Suggestions: []Suggestion{
			{Command: "swarm node add <host>", Description: "add a remote node via SSH"},
			{Command: "swarm node add --local", Description: "register the local machine"},
		},
	}
}

// EmptyWorkspaces returns an empty state for when no workspaces exist.
func EmptyWorkspaces() EmptyState {
	return EmptyState{
		Icon:     "📁",
		Title:    "No workspaces yet",
		Subtitle: "Workspaces are project directories managed by agents.",
		Suggestions: []Suggestion{
			{Command: "swarm ws create <path>", Description: "create a new workspace"},
			{Command: "swarm ws import <path>", Description: "import an existing project"},
		},
	}
}

// EmptyAgents returns an empty state for when no agents are running.
func EmptyAgents() EmptyState {
	return EmptyState{
		Icon:     "🤖",
		Title:    "No agents running",
		Subtitle: "Agents are AI coding assistants working in workspaces.",
		Suggestions: []Suggestion{
			{Command: "swarm agent spawn <workspace>", Description: "spawn an agent in a workspace"},
		},
	}
}

// EmptyAgentsFiltered returns an empty state for when filter matches nothing.
func EmptyAgentsFiltered(filter string) EmptyState {
	return EmptyState{
		Icon:     "🔍",
		Title:    fmt.Sprintf("No agents match '%s'", filter),
		Subtitle: "Press / to edit or clear the filter.",
	}
}

// EmptyWorkspacesFiltered returns an empty state for when filter matches nothing.
func EmptyWorkspacesFiltered(filter string) EmptyState {
	return EmptyState{
		Icon:     "🔍",
		Title:    fmt.Sprintf("No workspaces match '%s'", filter),
		Subtitle: "Press / to edit or clear the filter.",
	}
}

// EmptyQueue returns an empty state for when the queue is empty.
func EmptyQueue() EmptyState {
	return EmptyState{
		Icon:     "📭",
		Title:    "Queue is empty",
		Subtitle: "Messages and pause commands appear here.",
		Suggestions: []Suggestion{
			{Command: "m", Description: "add a message"},
			{Command: "p", Description: "add a pause"},
		},
	}
}

// EmptyApprovals returns an empty state for when no approvals are pending.
func EmptyApprovals() EmptyState {
	return EmptyState{
		Icon:     "✅",
		Title:    "No pending approvals",
		Subtitle: "Agents will request approval for risky operations.",
	}
}

// EmptyAudit returns an empty state for when no audit events exist.
func EmptyAudit() EmptyState {
	return EmptyState{
		Icon:     "📋",
		Title:    "No audit events yet",
		Subtitle: "Events are logged as agents perform actions.",
	}
}

// EmptyDashboard returns an empty state for when the dashboard has no data.
func EmptyDashboard() EmptyState {
	return EmptyState{
		Icon:     "🚀",
		Title:    "Welcome to Swarm!",
		Subtitle: "Start by adding a node and creating a workspace.",
		Suggestions: []Suggestion{
			{Command: "swarm init", Description: "run the setup wizard"},
			{Command: "swarm node add --local", Description: "register this machine"},
			{Command: "swarm ws create <path>", Description: "create your first workspace"},
		},
	}
}

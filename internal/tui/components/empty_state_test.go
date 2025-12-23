package components

import (
	"strings"
	"testing"

	"github.com/opencode-ai/swarm/internal/tui/styles"
)

func TestEmptyStateRender(t *testing.T) {
	styleSet := styles.DefaultStyles()

	t.Run("basic empty state", func(t *testing.T) {
		es := EmptyState{
			Title: "No items found",
		}
		result := es.Render(styleSet)
		if !strings.Contains(result, "No items found") {
			t.Errorf("Expected title in output, got: %s", result)
		}
	})

	t.Run("empty state with icon", func(t *testing.T) {
		es := EmptyState{
			Icon:  "📭",
			Title: "Empty inbox",
		}
		result := es.Render(styleSet)
		if !strings.Contains(result, "📭") {
			t.Errorf("Expected icon in output, got: %s", result)
		}
		if !strings.Contains(result, "Empty inbox") {
			t.Errorf("Expected title in output, got: %s", result)
		}
	})

	t.Run("empty state with subtitle", func(t *testing.T) {
		es := EmptyState{
			Title:    "No data",
			Subtitle: "Check back later",
		}
		result := es.Render(styleSet)
		if !strings.Contains(result, "Check back later") {
			t.Errorf("Expected subtitle in output, got: %s", result)
		}
	})

	t.Run("empty state with suggestions", func(t *testing.T) {
		es := EmptyState{
			Title: "No workspaces",
			Suggestions: []Suggestion{
				{Command: "swarm ws create <path>", Description: "create new"},
			},
		}
		result := es.Render(styleSet)
		if !strings.Contains(result, "Get started") {
			t.Errorf("Expected 'Get started' header, got: %s", result)
		}
		if !strings.Contains(result, "swarm ws create") {
			t.Errorf("Expected command in output, got: %s", result)
		}
	})
}

func TestEmptyStateRenderCompact(t *testing.T) {
	styleSet := styles.DefaultStyles()

	t.Run("compact without suggestions", func(t *testing.T) {
		es := EmptyState{
			Icon:  "🔍",
			Title: "No results",
		}
		result := es.RenderCompact(styleSet)
		if !strings.Contains(result, "🔍") {
			t.Errorf("Expected icon in compact output, got: %s", result)
		}
		if !strings.Contains(result, "No results") {
			t.Errorf("Expected title in compact output, got: %s", result)
		}
	})

	t.Run("compact with suggestion", func(t *testing.T) {
		es := EmptyState{
			Title: "Empty",
			Suggestions: []Suggestion{
				{Command: "add item"},
			},
		}
		result := es.RenderCompact(styleSet)
		if !strings.Contains(result, "Try: add item") {
			t.Errorf("Expected suggestion hint in compact output, got: %s", result)
		}
	})
}

func TestPrebuiltEmptyStates(t *testing.T) {
	styleSet := styles.DefaultStyles()

	tests := []struct {
		name     string
		es       EmptyState
		expected []string
	}{
		{
			name:     "EmptyNodes",
			es:       EmptyNodes(),
			expected: []string{"No nodes", "swarm node add"},
		},
		{
			name:     "EmptyWorkspaces",
			es:       EmptyWorkspaces(),
			expected: []string{"No workspaces", "swarm ws create"},
		},
		{
			name:     "EmptyAgents",
			es:       EmptyAgents(),
			expected: []string{"No agents", "swarm agent spawn"},
		},
		{
			name:     "EmptyQueue",
			es:       EmptyQueue(),
			expected: []string{"Queue is empty"},
		},
		{
			name:     "EmptyApprovals",
			es:       EmptyApprovals(),
			expected: []string{"No pending approvals"},
		},
		{
			name:     "EmptyDashboard",
			es:       EmptyDashboard(),
			expected: []string{"Welcome to Swarm", "swarm init"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.es.Render(styleSet)
			for _, exp := range tt.expected {
				if !strings.Contains(result, exp) {
					t.Errorf("Expected %q in %s output, got: %s", exp, tt.name, result)
				}
			}
		})
	}
}

func TestEmptyAgentsFiltered(t *testing.T) {
	styleSet := styles.DefaultStyles()
	es := EmptyAgentsFiltered("test-filter")
	result := es.Render(styleSet)

	if !strings.Contains(result, "test-filter") {
		t.Errorf("Expected filter in output, got: %s", result)
	}
	if !strings.Contains(result, "Press /") {
		t.Errorf("Expected filter hint in output, got: %s", result)
	}
}

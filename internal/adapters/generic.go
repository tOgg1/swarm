package adapters

import (
	"strings"

	"github.com/opencode-ai/swarm/internal/models"
)

// GenericAdapter provides basic tmux-only integration for any agent CLI.
// It uses heuristic screen analysis to detect state.
type GenericAdapter struct {
	// name is the adapter identifier
	name string

	// command is the CLI command to spawn
	command string

	// idleIndicators are strings that suggest the agent is idle/waiting for input
	idleIndicators []string

	// busyIndicators are strings that suggest the agent is working
	busyIndicators []string
}

// GenericAdapterOption configures a GenericAdapter.
type GenericAdapterOption func(*GenericAdapter)

// WithIdleIndicators sets custom idle state indicators.
func WithIdleIndicators(indicators ...string) GenericAdapterOption {
	return func(a *GenericAdapter) {
		a.idleIndicators = indicators
	}
}

// WithBusyIndicators sets custom busy state indicators.
func WithBusyIndicators(indicators ...string) GenericAdapterOption {
	return func(a *GenericAdapter) {
		a.busyIndicators = indicators
	}
}

// NewGenericAdapter creates a new generic adapter.
func NewGenericAdapter(name, command string, opts ...GenericAdapterOption) *GenericAdapter {
	a := &GenericAdapter{
		name:    name,
		command: command,
		// Default indicators - common patterns across CLI agents
		idleIndicators: []string{
			"❯", ">", "$", "%", // common prompts
			"waiting for input",
			"ready",
			"idle",
		},
		busyIndicators: []string{
			"thinking",
			"working",
			"processing",
			"generating",
			"...",
			"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", // spinner chars
		},
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// Name returns the adapter name.
func (a *GenericAdapter) Name() string {
	return a.name
}

// Tier returns the adapter integration tier.
func (a *GenericAdapter) Tier() models.AdapterTier {
	return models.AdapterTierGeneric
}

// SpawnCommand returns the command and args to launch the agent.
func (a *GenericAdapter) SpawnCommand(opts SpawnOptions) (cmd string, args []string) {
	// Simple command parsing - split on spaces
	// For more complex commands, adapters should override this
	parts := strings.Fields(a.command)
	if len(parts) == 0 {
		return a.command, nil
	}
	return parts[0], parts[1:]
}

// DetectReady reports whether the agent is ready based on screen output.
func (a *GenericAdapter) DetectReady(screen string) (bool, error) {
	lower := strings.ToLower(screen)

	// Check for idle indicators (suggests ready for input)
	for _, indicator := range a.idleIndicators {
		if strings.Contains(lower, strings.ToLower(indicator)) {
			return true, nil
		}
	}

	// Check for busy indicators (not ready yet)
	for _, indicator := range a.busyIndicators {
		if strings.Contains(lower, strings.ToLower(indicator)) {
			return false, nil
		}
	}

	// Default: assume not ready if we can't determine
	return false, nil
}

// DetectState returns the current state with a reason.
func (a *GenericAdapter) DetectState(screen string, meta any) (models.AgentState, StateReason, error) {
	lower := strings.ToLower(screen)

	// Check for error indicators
	errorIndicators := []string{"error:", "error!", "failed:", "exception:", "panic:"}
	for _, indicator := range errorIndicators {
		if strings.Contains(lower, indicator) {
			return models.AgentStateError, StateReason{
				Reason:     "error indicator detected in output",
				Confidence: models.StateConfidenceMedium,
				Evidence:   []string{indicator},
			}, nil
		}
	}

	// Check for rate limit indicators
	rateLimitIndicators := []string{"rate limit", "too many requests", "quota exceeded", "try again later"}
	for _, indicator := range rateLimitIndicators {
		if strings.Contains(lower, indicator) {
			return models.AgentStateRateLimited, StateReason{
				Reason:     "rate limit indicator detected",
				Confidence: models.StateConfidenceMedium,
				Evidence:   []string{indicator},
			}, nil
		}
	}

	// Check for approval indicators
	approvalIndicators := []string{"approve", "confirm", "allow", "proceed?", "y/n", "[y/n]"}
	for _, indicator := range approvalIndicators {
		if strings.Contains(lower, indicator) {
			return models.AgentStateAwaitingApproval, StateReason{
				Reason:     "approval prompt detected",
				Confidence: models.StateConfidenceLow,
				Evidence:   []string{indicator},
			}, nil
		}
	}

	// Check for busy indicators
	for _, indicator := range a.busyIndicators {
		if strings.Contains(lower, strings.ToLower(indicator)) {
			return models.AgentStateWorking, StateReason{
				Reason:     "activity indicator detected",
				Confidence: models.StateConfidenceLow,
				Evidence:   []string{indicator},
			}, nil
		}
	}

	// Check for idle indicators
	for _, indicator := range a.idleIndicators {
		if strings.Contains(lower, strings.ToLower(indicator)) {
			return models.AgentStateIdle, StateReason{
				Reason:     "idle indicator detected",
				Confidence: models.StateConfidenceLow,
				Evidence:   []string{indicator},
			}, nil
		}
	}

	// Default: unknown, report as working with low confidence
	return models.AgentStateWorking, StateReason{
		Reason:     "no clear state indicator, assuming working",
		Confidence: models.StateConfidenceLow,
	}, nil
}

// SendMessage dispatches a message to the agent.
func (a *GenericAdapter) SendMessage(tmux TmuxClient, pane, message string) error {
	// Send the message with Enter key
	return tmux.SendKeys(pane, message+"\n", true)
}

// Interrupt sends an interrupt signal (Ctrl-C).
func (a *GenericAdapter) Interrupt(tmux TmuxClient, pane string) error {
	return tmux.SendKeys(pane, "C-c", false)
}

// SupportsApprovals indicates if the adapter supports approvals routing.
func (a *GenericAdapter) SupportsApprovals() bool {
	return false
}

// SupportsUsageMetrics indicates if the adapter reports usage metrics.
func (a *GenericAdapter) SupportsUsageMetrics() bool {
	return false
}

// SupportsDiffMetadata indicates if the adapter reports diff metadata.
func (a *GenericAdapter) SupportsDiffMetadata() bool {
	return false
}

// Ensure GenericAdapter implements AgentAdapter
var _ AgentAdapter = (*GenericAdapter)(nil)

// Built-in generic adapters for common agent types

// OpenCodeAdapter creates an adapter for OpenCode CLI.
func OpenCodeAdapter() *GenericAdapter {
	return NewGenericAdapter(
		string(models.AgentTypeOpenCode),
		"opencode",
		WithIdleIndicators(
			"❯",
			"opencode>",
			"waiting for input",
		),
		WithBusyIndicators(
			"thinking",
			"generating",
			"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
		),
	)
}

// ClaudeCodeAdapter creates an adapter for Claude Code CLI.
func ClaudeCodeAdapter() *GenericAdapter {
	return NewGenericAdapter(
		string(models.AgentTypeClaudeCode),
		"claude",
		WithIdleIndicators(
			">",
			"claude>",
			"ready",
		),
		WithBusyIndicators(
			"thinking",
			"writing",
			"reading",
		),
	)
}

// CodexAdapter creates an adapter for OpenAI Codex CLI.
func CodexAdapter() *GenericAdapter {
	return NewGenericAdapter(
		string(models.AgentTypeCodex),
		"codex",
		WithIdleIndicators(
			">",
			"codex>",
		),
		WithBusyIndicators(
			"processing",
			"generating",
		),
	)
}

// GeminiAdapter creates an adapter for Google Gemini CLI.
func GeminiAdapter() *GenericAdapter {
	return NewGenericAdapter(
		string(models.AgentTypeGemini),
		"gemini",
		WithIdleIndicators(
			">",
			"gemini>",
		),
		WithBusyIndicators(
			"thinking",
			"processing",
		),
	)
}

// GenericFallbackAdapter creates a fallback adapter for unknown agent types.
func GenericFallbackAdapter() *GenericAdapter {
	return NewGenericAdapter(
		string(models.AgentTypeGeneric),
		"",
	)
}

// RegisterBuiltinAdapters registers all built-in adapters with the given registry.
func RegisterBuiltinAdapters(r *Registry) {
	r.MustRegister(OpenCodeAdapter())
	r.MustRegister(ClaudeCodeAdapter())
	r.MustRegister(CodexAdapter())
	r.MustRegister(GeminiAdapter())
	r.MustRegister(GenericFallbackAdapter())
}

// init registers built-in adapters with the default registry.
func init() {
	RegisterBuiltinAdapters(DefaultRegistry)
}

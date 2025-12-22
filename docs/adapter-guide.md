# Swarm Adapter Development Guide

This guide explains how to add new agent adapters. Adapters normalize different
agent CLIs behind a shared interface so Swarm can spawn, control, and detect
agent state consistently.

Note: the adapter interface is planned but not fully implemented yet. This
document describes the intended shape based on the product spec and epics.

## Concepts

### AgentType

Agent types are enumerated in `internal/models/agent.go`:

- `opencode`
- `claude-code`
- `codex`
- `gemini`
- `generic`

If you add a new adapter, add a new agent type and keep the string stable
because it will appear in configs and persisted records.

### AdapterTier

Adapters are classified by how much telemetry they can provide:

- Tier 1 (generic): tmux-only, heuristic state detection
- Tier 2 (telemetry): log or CLI-based state detection
- Tier 3 (native): structured events (preferred)

The tier value controls confidence and feature availability in the UI.

## Planned adapter interface

The intended interface (from EPIC 6) looks like this:

```go
type AgentAdapter interface {
    // Identity
    Name() string
    Tier() AdapterTier

    // Lifecycle
    SpawnCommand(opts SpawnOptions) (cmd string, args []string)
    DetectReady(screen string) (bool, error)
    DetectState(screen string, meta any) (AgentState, StateReason, error)

    // Control
    SendMessage(tmux TmuxClient, pane, message string) error
    Interrupt(tmux TmuxClient, pane string) error

    // Capabilities
    SupportsApprovals() bool
    SupportsUsageMetrics() bool
    SupportsDiffMetadata() bool
}
```

If you implement a new adapter, mirror this shape and keep method names stable.

## Directory layout (planned)

Adapters are expected to live under `internal/adapters/<name>` and register
with an adapter registry (also planned). Keep adapter code isolated and avoid
cross-package dependencies beyond models, tmux helpers, and logging.

## State detection patterns

State detection should be deterministic when possible. Recommended signals:

- Ready detection: prompt signature or known banner text.
- Idle detection: stable prompt plus no screen changes for N seconds.
- Working detection: ongoing output or activity markers.
- Approval detection: explicit prompt text like "approval required".
- Rate limit detection: provider error strings, retry-after hints.
- Error detection: known error phrases or exit codes.

When in doubt, return a lower confidence tier and include a human-readable
reason string.

## Send/interrupt patterns

- Use tmux `send-keys` with literal mode for user messages.
- Interrupt should map to Ctrl-C by default.
- Avoid sending multi-line payloads unless the adapter explicitly supports it.

## Authentication and profiles

Adapters should accept account/profile hints via `SpawnOptions`. The intended
flow is:

1. Select profile in the scheduler/service layer.
2. Pass profile metadata into adapter spawn options.
3. Adapter translates profile into environment variables or CLI args.

Do not log secrets. If you must log environment or commands, redact values.

## Example adapter skeleton (planned)

```go
type CodexAdapter struct{}

func (a CodexAdapter) Name() string { return "codex" }
func (a CodexAdapter) Tier() AdapterTier { return AdapterTierTelemetry }

func (a CodexAdapter) SpawnCommand(opts SpawnOptions) (string, []string) {
    return "codex", []string{"--workspace", opts.WorkspacePath}
}

func (a CodexAdapter) DetectReady(screen string) (bool, error) {
    return strings.Contains(screen, "Codex ready"), nil
}
```

## Testing checklist

- Unit tests for DetectReady and DetectState with sample screen outputs.
- A fixture set of sample transcripts for errors and rate limits.
- If possible, an integration test that spawns a tmux pane and validates
  send/interrupt behavior.

## Logging

Use `logging.Component("adapter.<name>")` for structured logs. Prefer debug
logs for noisy state checks and include adapter name and agent ID.

## Contribution tips

- Keep detection logic table-driven and easy to extend.
- Return stable, user-facing reason strings.
- Avoid tight coupling to UI code.
- Keep adapter dependencies minimal.

# OpenCode Plugin Pack for Swarm (Design)

This document outlines a Phase 2 plugin pack that enhances Swarm's OpenCode
integration. The plugin pack is optional and should be safe to run alongside
normal OpenCode sessions.

## Goals

- Emit structured heartbeat events so Swarm can track liveness without
  scraping tmux output.
- Attach Swarm context to telemetry (agent_id, workspace_id, node_id, session_id).
- Provide policy hooks for risky actions (file writes, shell commands, network).
- Keep the pack self-contained and easy to install/enable per session.

## Non-goals

- Replace Swarm's SSH/tmux control plane.
- Implement full approvals UI inside OpenCode.
- Enforce global org-level policy (this is session-scoped by default).

## Plugin Pack Overview

The pack can be a small set of plugins bundled together:

1. Heartbeat plugin
2. Telemetry enrichment plugin
3. Policy hook plugin

Each plugin should be independently toggleable via config or env flags.

## Event Transport

Preferred transport is JSON Lines (JSONL) emitted to stdout/stderr in a stable
schema. Swarm can parse these events from OpenCode's structured output or from
sidecar log files.

If OpenCode provides a native event bus, the pack should register a handler and
forward events into a JSONL stream with a stable schema.

## Event Schema (Draft)

All events include:

- event_type: string (e.g., "swarm.heartbeat")
- timestamp: RFC3339 string
- agent_id: string (Swarm agent ID)
- workspace_id: string (Swarm workspace ID)
- node_id: string (Swarm node ID)
- session_id: string (OpenCode session ID)
- payload: object (event-specific data)

Example heartbeat event:

```
{"event_type":"swarm.heartbeat","timestamp":"2025-12-23T08:00:00Z","agent_id":"agent-123","workspace_id":"ws-456","node_id":"node-1","session_id":"session-789","payload":{"uptime_seconds":120,"state":"working"}}
```

## Heartbeat Plugin

- Emits a heartbeat event at a fixed interval (default: 5s).
- Includes state hints when available (idle/working/blocked), but should not
  replace Swarm's state engine.
- Includes process metadata if safe to expose (pid, version, model name).

## Telemetry Enrichment Plugin

- Attaches Swarm identifiers to OpenCode telemetry events.
- Records command metadata (tool name, duration, exit code) when available.
- Emits usage summary events when OpenCode reports them.

## Policy Hook Plugin

Purpose: enforce or surface risky actions before execution.

Targets:

- File writes (path, size, diff summary)
- Shell command execution (command string, cwd)
- Network access (target host/port)

Decision modes:

- allow: permit without prompt
- deny: block and emit denial event
- prompt: emit approval request event and wait for Swarm response

Swarm approval responses should be injected back via a control channel (e.g.,
OpenCode plugin API, stdin command, or session control endpoint).

## Configuration

Configuration should be simple and session-scoped:

- Enable/disable plugin pack
- Set heartbeat interval
- Set policy mode per action type
- Provide Swarm context (agent_id, workspace_id, node_id)

Suggested inputs:

- Environment variables: SWARM_AGENT_ID, SWARM_WORKSPACE_ID, SWARM_NODE_ID
- Optional JSON config file path

## Security and Privacy

- Never emit secrets or full file contents.
- Redact command arguments when marked sensitive.
- Allow disabling telemetry enrichment or policy hooks entirely.

## Open Questions

- What is the best transport for approvals (stdin vs OpenCode API)?
- Should the plugin pack emit a session start/stop event automatically?
- Do we want per-tool overrides for policy (e.g., allow git, prompt for curl)?

## Implementation Notes

- Keep event schema versioned (e.g., event_version: 1).
- Keep payloads small; avoid large diffs.
- Prefer stable identifiers that Swarm already tracks.
- Provide a tiny test harness to validate JSONL output.

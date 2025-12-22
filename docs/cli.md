# Swarm CLI Reference

This document describes the current CLI surface and planned commands. The CLI
is still early-stage; items marked as "planned" are not implemented yet.

## Global usage

```bash
swarm [flags] [command]
```

### Global flags

- `--config <path>`: Path to config file (default: `~/.config/swarm/config.yaml`).
- `--json`: Emit JSON output (where supported).
- `--jsonl`: Emit JSON Lines output (reserved for streaming).
- `--watch`: Stream updates until interrupted (reserved for future commands).
- `-v, --verbose`: Enable verbose output (currently forces log level `debug`).
- `--log-level <level>`: Override logging level (`debug`, `info`, `warn`, `error`).
- `--log-format <format>`: Override logging format (`json`, `console`).

## Commands

### `swarm`

Launches the TUI (placeholder for now).

```bash
swarm
```

### `swarm migrate`

Manage database migrations.

```bash
swarm migrate [command]
```

#### `swarm migrate up`

Apply migrations. By default, applies all pending migrations. Use `--to` to
migrate to a specific version.

```bash
swarm migrate up
swarm migrate up --to 1
```

#### `swarm migrate down`

Roll back migrations. Defaults to one step. Use `--steps` to roll back multiple.

```bash
swarm migrate down
swarm migrate down --steps 2
```

#### `swarm migrate status`

Show migration status. Supports `--json`.

```bash
swarm migrate status
swarm migrate status --json
```

#### `swarm migrate version`

Show current schema version. Supports `--json`.

```bash
swarm migrate version
swarm migrate version --json
```

## Planned commands

These are defined in the product spec and epics but are not wired up yet.

### Nodes (planned)

```bash
swarm node list
swarm node add --ssh user@host --name <node>
swarm node remove <node>
swarm node bootstrap --ssh root@host
swarm node doctor <node>
swarm node exec <node> -- <cmd>
```

### Workspaces (planned)

```bash
swarm ws create --node <node> --path <repo>
swarm ws import --node <node> --tmux-session <name>
swarm ws list
swarm ws status <ws>
swarm ws attach <ws>
swarm ws unmanage <ws>
swarm ws kill <ws>
```

### Agents (planned)

```bash
swarm agent spawn --ws <ws> --type opencode --count 3
swarm agent list [--ws <ws>]
swarm agent status <agent>
swarm agent send <agent> "message"
swarm agent queue <agent> --file prompts.txt
swarm agent pause <agent> --minutes 20
swarm agent resume <agent>
swarm agent interrupt <agent>
swarm agent restart <agent>
swarm agent approve <agent> [--all]
```

### Accounts (planned)

```bash
swarm accounts list
swarm accounts add
swarm accounts import-caam
swarm accounts rotate
swarm accounts cooldown list|set|clear
```

### Export/Integration (planned)

```bash
swarm export status --json
swarm export events --since 1h --jsonl
swarm hook on-event --cmd <script>
```

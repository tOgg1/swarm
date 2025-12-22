# Swarm CLI Reference

This document describes the current CLI surface. Commands that are not wired
up yet are listed under Planned.

## Global usage

```bash
swarm [flags] [command]
```

### Global flags

- `--config <path>`: Path to config file (default: `~/.config/swarm/config.yaml`).
- `--json`: Emit JSON output (where supported).
- `--jsonl`: Emit JSON Lines output (streaming friendly).
- `--watch`: Stream updates until interrupted (reserved for future commands).
- `--no-color`: Disable colored output in human mode.
- `-v, --verbose`: Enable verbose output (forces log level `debug`).
- `--log-level <level>`: Override logging level (`debug`, `info`, `warn`, `error`).
- `--log-format <format>`: Override logging format (`json`, `console`).

## Commands

### `swarm`

Launches the TUI. Current builds print a placeholder message.

```bash
swarm
```

### `swarm migrate`

Manage database migrations.

```bash
swarm migrate [command]
```

#### `swarm migrate up`

```bash
swarm migrate up
swarm migrate up --to 1
```

#### `swarm migrate down`

```bash
swarm migrate down
swarm migrate down --steps 2
```

#### `swarm migrate status`

```bash
swarm migrate status
swarm migrate status --json
```

#### `swarm migrate version`

```bash
swarm migrate version
swarm migrate version --json
```

### `swarm node`

Manage nodes.

```bash
swarm node list
swarm node add --name local --local
swarm node add --name prod --ssh ubuntu@host --key ~/.ssh/id_rsa
swarm node remove <name-or-id> --force
swarm node doctor <name-or-id>
swarm node refresh [name-or-id]
swarm node exec <name-or-id> -- uname -a
```

Notes:
- `swarm node bootstrap` exists but only reports missing deps today.
- Use `--no-test` on `node add` to skip connection test.

### `swarm ws`

Manage workspaces.

```bash
swarm ws create --path /path/to/repo --node local
swarm ws import --session repo-session --node local
swarm ws list
swarm ws status <id-or-name>
swarm ws beads-status <id-or-name>
swarm ws attach <id-or-name>
swarm ws remove <id-or-name> --destroy
swarm ws refresh [id-or-name]
```

Notes:
- `ws remove --destroy` kills the tmux session after removing the workspace.
- Use `ws create --no-tmux` to track an existing session without creating one.

### `swarm agent`

Manage agents.

```bash
swarm agent spawn --workspace <ws> --type opencode --count 1
swarm agent list --workspace <ws>
swarm agent status <agent-id>
swarm agent send <agent-id> "message"
swarm agent send <agent-id> --file prompt.txt
swarm agent send <agent-id> --stdin
swarm agent send <agent-id> --editor
swarm agent queue <agent-id> --file prompts.txt
swarm agent pause <agent-id> --duration 5m
swarm agent resume <agent-id>
swarm agent interrupt <agent-id>
swarm agent restart <agent-id>
swarm agent terminate <agent-id>
```

Notes:
- `agent send` preserves newlines when using `--file`, `--stdin`, or `--editor`.
- `agent send --skip-idle-check` bypasses the idle requirement.

### `swarm export`

Export Swarm status.

```bash
swarm export status --json
```

Human mode prints a summary; JSON/JSONL return full payloads.

## Planned commands

These are defined in the product spec but not wired up yet.

### `swarm agent approve`

```bash
swarm agent approve <agent-id> [--all]
```

### `swarm accounts`

```bash
swarm accounts list
swarm accounts add
swarm accounts rotate
swarm accounts cooldown list|set|clear
```

### `swarm ws kill` / `swarm ws unmanage`

```bash
swarm ws kill <id-or-name>
swarm ws unmanage <id-or-name>
```

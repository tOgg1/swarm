# Swarm

A control plane for running and supervising AI coding agents across multiple repositories and servers.

## Overview

Swarm provides a unified interface for managing AI coding agents (OpenCode, Claude Code, Codex, Gemini, etc.) running in tmux sessions across local and remote machines. It features:

- **TUI Dashboard** - Real-time monitoring of agent status, queues, and progress
- **CLI** - Full automation support with JSON output for scripting
- **Multi-Node Orchestration** - Manage agents across many servers via SSH
- **Account Management** - Handle multiple provider accounts with cooldown and rotation
- **Message Queuing** - Queue instructions with conditional dispatch and pauses

## Core Concepts

| Concept | Description |
|---------|-------------|
| **Node** | A machine (local or remote) that Swarm controls via SSH and tmux |
| **Workspace** | A managed unit binding a node + repo path + tmux session + agents |
| **Agent** | A running AI coding CLI (OpenCode, Claude Code, etc.) in a tmux pane |
| **Queue** | Per-agent message queue with conditional dispatch |

## Architecture

```
                    ┌─────────────────────────────────────────┐
                    │              Control Plane               │
                    │  ┌─────────┐  ┌─────────┐  ┌─────────┐  │
                    │  │   TUI   │  │   CLI   │  │Scheduler│  │
                    │  └────┬────┘  └────┬────┘  └────┬────┘  │
                    │       │            │            │        │
                    │  ┌────┴────────────┴────────────┴────┐  │
                    │  │          State Engine             │  │
                    │  │     (SQLite + Event Log)          │  │
                    │  └───────────────┬───────────────────┘  │
                    └──────────────────┼──────────────────────┘
                                       │
              ┌────────────────────────┼────────────────────────┐
              │                        │                        │
        ┌─────┴─────┐            ┌─────┴─────┐            ┌─────┴─────┐
        │  Node A   │            │  Node B   │            │  Node C   │
        │  (local)  │            │  (remote) │            │  (remote) │
        │           │            │           │            │           │
        │ ┌───────┐ │            │ ┌───────┐ │            │ ┌───────┐ │
        │ │swarmd │ │    SSH     │ │swarmd │ │    SSH     │ │swarmd │ │
        │ └───┬───┘ │◄──────────►│ └───┬───┘ │◄──────────►│ └───┬───┘ │
        │     │     │            │     │     │            │     │     │
        │ ┌───┴───┐ │            │ ┌───┴───┐ │            │ ┌───┴───┐ │
        │ │ tmux  │ │            │ │ tmux  │ │            │ │ tmux  │ │
        │ │session│ │            │ │session│ │            │ │session│ │
        │ └───────┘ │            │ └───────┘ │            │ └───────┘ │
        └───────────┘            └───────────┘            └───────────┘
```

### Runtime Modes

- **SSH-only (Mode A)**: Control plane uses SSH for tmux operations. Simple, minimal footprint.
- **Daemon (Mode B)**: `swarmd` runs on nodes for real-time operations, better performance.

## Installation

### Prerequisites

- Go 1.25+
- tmux
- Git
- SSH (for remote nodes)

### Build from Source

```bash
git clone https://github.com/tOgg1/swarm.git
cd swarm
make build
```

Binaries are written to `./build/swarm` and `./build/swarmd`.

### Bootstrap a Node

For fresh servers, use the bootstrap script:

```bash
# One-liner (downloads and verifies before running)
curl -fsSL https://raw.githubusercontent.com/tOgg1/swarm/main/scripts/install.sh | bash -s -- --install-extras

# With Claude Code
curl -fsSL https://raw.githubusercontent.com/tOgg1/swarm/main/scripts/install.sh | bash -s -- --install-extras --install-claude
```

## Quick Start

### 1. Initialize Database

```bash
swarm migrate up
```

### 2. Add a Node

```bash
# Local node
swarm node add --name local --local

# Remote node
swarm node add --name server1 --ssh user@hostname
```

### 3. Create a Workspace

```bash
swarm ws create --node local --path /path/to/your/repo
```

### 4. Spawn an Agent

```bash
swarm agent spawn --workspace <workspace-id> --type opencode --count 1
```

### 5. Send Instructions

```bash
swarm agent send <agent-id> "Implement the login feature"
```

### 6. Launch the TUI

```bash
swarm
```

## Configuration

Create a config file at `~/.config/swarm/config.yaml`:

```yaml
global:
  data_dir: ~/.local/share/swarm
  auto_register_local_node: true

database:
  max_connections: 10
  busy_timeout_ms: 5000

logging:
  level: info
  format: console

node_defaults:
  ssh_backend: auto
  ssh_timeout: 30s
  health_check_interval: 60s

agent_defaults:
  default_type: opencode
  state_polling_interval: 2s
  idle_timeout: 10s
  approval_policy: strict

scheduler:
  dispatch_interval: 1s
  max_retries: 3
  auto_rotate_on_rate_limit: true
```

See [docs/config.md](docs/config.md) for the full configuration reference.

## CLI Reference

### Nodes

```bash
swarm node list                    # List all nodes
swarm node add --name <n> --local  # Add local node
swarm node add --name <n> --ssh user@host  # Add remote node
swarm node remove <node>           # Remove a node
swarm node doctor <node>           # Diagnose node issues
swarm node exec <node> -- <cmd>    # Execute command on node
```

### Workspaces

```bash
swarm ws create --node <n> --path <repo>  # Create workspace
swarm ws import --node <n> --tmux <sess>  # Import existing tmux session
swarm ws list                             # List workspaces
swarm ws status <ws>                      # Show workspace status
swarm ws attach <ws>                      # Attach to tmux session
swarm ws remove <ws>                      # Remove workspace
```

### Agents

```bash
swarm agent spawn --ws <ws> --type opencode --count 3  # Spawn agents
swarm agent list [--workspace <ws>]                    # List agents
swarm agent status <agent>                             # Show agent status
swarm agent send <agent> "message"                     # Send instruction
swarm agent queue <agent> --file prompts.txt           # Queue messages
swarm agent pause <agent> --minutes 20                 # Pause agent
swarm agent resume <agent>                             # Resume agent
swarm agent interrupt <agent>                          # Send Ctrl+C
swarm agent restart <agent>                            # Restart agent
swarm agent terminate <agent>                          # Kill agent
swarm agent approve <agent> [--all]                    # Handle approvals
```

### Accounts

```bash
swarm accounts list                    # List accounts
swarm accounts add --provider openai   # Add account
swarm accounts cooldown list           # Show cooldowns
swarm accounts rotate --provider X     # Rotate account
```

### Export & Audit

```bash
swarm export status --json             # Export status as JSON
swarm export events --since 1h --jsonl # Export events
swarm audit                            # View audit log
```

## Agent Adapters

Swarm supports multiple AI coding CLIs through adapters:

| Tier | Adapter | Integration Level | Features |
|------|---------|-------------------|----------|
| 3 | `opencode` | Native | Full telemetry, structured events, API control |
| 2 | `claude-code` | Telemetry | Stream JSON parsing, good state detection |
| 2 | `gemini` | Telemetry | Log-based state detection |
| 2 | `codex` | Telemetry | CLI state detection |
| 1 | `generic` | Basic | tmux-only, heuristic detection |

## Agent States

| State | Description |
|-------|-------------|
| `working` | Agent is actively processing |
| `idle` | Agent is waiting for instructions |
| `awaiting_approval` | Agent needs human approval |
| `rate_limited` | Provider rate limit hit |
| `error` | Agent encountered an error |
| `paused` | Agent is manually paused |
| `starting` | Agent is initializing |
| `stopped` | Agent has terminated |

## TUI Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `q` / `Ctrl+C` | Quit |
| `:` / `Ctrl+K` | Command palette |
| `Tab` | Cycle focus |
| `Enter` | Select / Expand |
| `?` | Help |
| `r` | Refresh |
| `a` | Approve action |
| `p` | Pause agent |

## swarmd (Node Daemon)

The optional `swarmd` daemon provides enhanced features:

- **Real-time screen capture** with content hashing
- **Resource monitoring** with CPU/memory limits
- **Rate limiting** for API protection
- **gRPC API** for control plane communication

### Running swarmd

```bash
swarmd --port 50051 --log-level info
```

### Resource Limits

Configure per-agent resource caps:

```bash
swarm agent spawn --ws <ws> --type opencode \
  --max-memory 2G \
  --max-cpu 200  # 200% = 2 cores
```

## Project Structure

```
swarm/
├── cmd/
│   ├── swarm/      # Main CLI/TUI binary
│   └── swarmd/     # Node daemon binary
├── internal/
│   ├── cli/        # CLI commands (Cobra)
│   ├── tui/        # TUI components (Bubble Tea)
│   ├── swarmd/     # Daemon implementation
│   ├── agent/      # Agent service
│   ├── adapters/   # Agent CLI adapters
│   ├── state/      # State engine
│   ├── scheduler/  # Message dispatch
│   ├── node/       # Node management
│   ├── workspace/  # Workspace management
│   ├── queue/      # Message queue
│   ├── account/    # Account management
│   ├── db/         # SQLite repositories
│   ├── config/     # Configuration
│   ├── ssh/        # SSH execution
│   ├── tmux/       # tmux client
│   ├── events/     # Event logging
│   └── models/     # Domain models
├── proto/
│   └── swarmd/v1/  # gRPC protocol definitions
├── gen/
│   └── swarmd/v1/  # Generated protobuf code
├── docs/           # Documentation
└── scripts/        # Bootstrap and install scripts
```

## Development

### Running Tests

```bash
make test
```

### Generating Protobuf Code

```bash
make proto
```

### Linting

```bash
make lint
```

### Building for Release

```bash
make release
```

## Integrations

### beads/bv Issue Tracking

Swarm integrates with [beads](https://github.com/Dicklesworthstone/beads_viewer) for issue tracking. When a workspace contains a `.beads/` directory, Swarm displays task status in the TUI.

### Agent Mail (MCP)

Optional integration with Agent Mail for:
- Workspace mailbox view
- File/path claims to prevent agent conflicts
- Multi-agent coordination

## Troubleshooting

### Common Issues

**Agent not detecting state correctly**
- Check adapter tier - Tier 1 (generic) uses heuristics
- Increase `state_polling_interval` for more frequent checks
- Use `swarm agent status <id>` to see detection confidence

**SSH connection failures**
- Run `swarm node doctor <node>` to diagnose
- Check `~/.ssh/config` is correct
- Try `ssh_backend: system` in config

**Database locked errors**
- Increase `busy_timeout_ms` in config
- Ensure only one Swarm instance is running

See [docs/troubleshooting.md](docs/troubleshooting.md) for more solutions.

## Roadmap

### v0.1 (MVP)
- [x] Workspace/agent management
- [x] Basic TUI dashboard
- [x] CLI parity
- [x] SSH-only mode
- [x] OpenCode adapter

### v0.2
- [x] swarmd daemon
- [x] Resource caps enforcement
- [x] Approvals inbox (TUI integration)
- [x] Account rotation

### v1.0
- [ ] Distributed workspaces (single workspace across multiple nodes)
- [ ] Recipes/roles system
- [ ] Worktree isolation
- [ ] Supervisor AI mode

## Contributing

Contributions are welcome! Please read the contributing guidelines before submitting PRs.

## License

Apache-2.0

---

Built with Go, [Bubble Tea](https://github.com/charmbracelet/bubbletea), and [gRPC](https://grpc.io/).

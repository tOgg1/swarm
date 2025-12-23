# Swarm Quickstart

This guide walks through building Swarm, configuring it, and the first steps to
create a workspace and spawn agents. The CLI is still early-stage; commands that
are not implemented yet are marked as planned.

## Prerequisites

- Go 1.25+ (see `go.mod`)
- Git
- tmux (required for workspace/agent orchestration)
- ssh (for remote nodes)

## Bootstrap a node (optional)

Use the bootstrap script to install dependencies on a fresh node.

```bash
# One-liner (downloads + verifies bootstrap.sh before running)
curl -fsSL https://raw.githubusercontent.com/opencode-ai/swarm/main/scripts/install.sh | bash -s -- --install-extras --install-claude

# Manual download + verify
curl -fsSL https://raw.githubusercontent.com/opencode-ai/swarm/main/scripts/bootstrap.sh -o bootstrap.sh
curl -fsSL https://raw.githubusercontent.com/opencode-ai/swarm/main/scripts/bootstrap.sh.sha256 -o bootstrap.sh.sha256
sha256sum -c bootstrap.sh.sha256
sudo bash bootstrap.sh --install-extras --install-claude
```

Notes:
- `--install-claude` is opt-in; omit it if you do not want Claude Code installed.
- `scripts/install.sh` verifies `bootstrap.sh` against `bootstrap.sh.sha256`.
- The checksum file in `scripts/bootstrap.sh.sha256` must be kept in sync with the script.

## Build

```bash
make build
```

Binaries are written to `./build/swarm` and `./build/swarmd`.

## Configure

Copy the example config and adjust values as needed:

```bash
mkdir -p ~/.config/swarm
cp docs/config.example.yaml ~/.config/swarm/config.yaml
```

For a full reference, see `docs/config.md`.

## Initialize the database

```bash
./build/swarm migrate up
```

This creates `~/.local/share/swarm/swarm.db` by default.

## Launch the TUI (preview)

```bash
./build/swarm
```

The TUI is currently a stub that prints a placeholder message.

## First workspace

```bash
# Add a local node
./build/swarm node add --name local --local

# Create a workspace
./build/swarm ws create --node local --path /path/to/repo

# Spawn an agent
./build/swarm agent spawn --workspace <workspace-id> --type opencode --count 1
```

## Basic commands

```bash
./build/swarm node list
./build/swarm ws list
./build/swarm agent list
```

## Troubleshooting

See `docs/troubleshooting.md` for common fixes and copy-paste commands.

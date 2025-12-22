# Swarm Quickstart

This guide walks through building Swarm, configuring it, and the first steps to
create a workspace and spawn agents. The CLI is still early-stage; commands that
are not implemented yet are marked as planned.

## Prerequisites

- Go 1.25+ (see `go.mod`)
- Git
- tmux (required for workspace/agent orchestration)
- ssh (for remote nodes)

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

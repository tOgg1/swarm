# Swarm Troubleshooting

This guide covers common setup and runtime issues with copy-paste fixes.
If you are in JSON mode, add `--no-color` to keep output plain.

## Quick triage

```bash
./build/swarm --version
./build/swarm node list
./build/swarm ws list
./build/swarm agent list
```

If any command fails, see the matching section below.

## tmux not found

Symptoms:
- `tmux is required for this command`
- workspace/agent commands fail immediately

Fix (Linux):
```bash
sudo apt-get update && sudo apt-get install -y tmux
```

Fix (macOS):
```bash
brew install tmux
```

Verify:
```bash
tmux -V
```

## Config file missing

Symptoms:
- warning about missing config
- config-related errors during startup

Fix (manual setup):
```bash
mkdir -p ~/.config/swarm
cp docs/config.example.yaml ~/.config/swarm/config.yaml
```

If your build supports `swarm init`, you can run:
```bash
./build/swarm init
```

## Database not migrated

Symptoms:
- `database not migrated`
- `database has no migrations applied`

Fix:
```bash
./build/swarm migrate up
```

If you customized the data directory, confirm the config:
```bash
rg -n "data_dir|database.path" ~/.config/swarm/config.yaml
```

## SSH issues (remote nodes)

Symptoms:
- connection test fails
- `ssh binary not found for system backend`
- `permission denied` when adding a node

Fix (system ssh):
```bash
ssh -T user@host
```

If you rely on system SSH and do not have the binary installed, install it or
switch to the native backend in `config.yaml`:
```yaml
node_defaults:
  ssh_backend: native
```

If you use a custom key:
```bash
./build/swarm node add --name mynode --ssh user@host --key ~/.ssh/id_rsa
```

## Workspace path not found

Symptoms:
- `workspace path not found`

Fix:
```bash
./build/swarm ws create --path /absolute/path/to/repo --node <node>
```

Use absolute paths for remote nodes.

## tmux session or pane missing

Symptoms:
- agent operations fail with pane/session errors
- `pane is dead or inaccessible`

Fix:
```bash
tmux ls
```

If the workspace session is missing, recreate it:
```bash
./build/swarm ws create --path /path/to/repo --node <node>
```

If the session exists but a pane is missing, restart the agent:
```bash
./build/swarm agent restart <agent-id>
```

## Agent stuck or not idle

Symptoms:
- `agent is not idle`

Fix:
```bash
./build/swarm agent interrupt <agent-id>
./build/swarm agent send <agent-id> "Retry the last step"
```

If you need to send anyway:
```bash
./build/swarm agent send <agent-id> --skip-idle-check "Force this message"
```

## Need more detail

Run with verbose logs:
```bash
./build/swarm --verbose <command>
```

If you are still stuck, collect:
- `swarm --version`
- the exact command you ran
- the error output

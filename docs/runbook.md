# Forge Operational Runbook

This runbook covers day-to-day operational tasks: monitoring, troubleshooting,
backup/restore, and scaling. Forge is early-stage; some commands are planned
but not yet wired up. Planned steps are labeled.

## Scope and assumptions

- Control plane runs locally via `./build/forge`.
- Data is stored in a local SQLite database (default: `~/.local/share/forge/forge.db`).
- tmux and ssh are required for most orchestration workflows.

## Monitoring

### Current (implemented)

- **Logs**: Forge logs to stderr by default. Enable verbose logging with:

  ```bash
  ./build/forge --log-level debug
  # or
  ./build/forge -v
  ```

- **Database health**: Verify migrations are applied:

  ```bash
  ./build/forge migrate status
  ./build/forge migrate version
  ```

### TUI cutover health checks (FrankenTUI runtime)

Use this checklist after upgrading `forge-tui` or changing pane/shell behavior:

1. Launch TUI from a real terminal:

   ```bash
   ./build/forge tui
   ```

2. Confirm expected tabs render and switch correctly:
   - `Overview`, `Logs`, `Runs`, `Multi Logs`, `Inbox`
3. Runtime fallback policy:
   - interactive path: FrankenTUI runtime only (no silent snapshot fallback)
   - non-TTY local path: explicit preflight error unless fallback is opted in
4. Optional explicit dev fallback (diagnostics only):

   ```bash
   FORGE_TUI_DEV_SNAPSHOT_FALLBACK=1 ./build/forge tui
   ```

### `rforged` daemon mode health checks (Rust parity)

For daemon-owned loops (`--spawn-owner daemon`) use these checks:

1. Confirm daemon startup logs show both readiness lines:
   - `rforged ready`
   - `rforged gRPC serving`
2. Confirm loop ownership and liveness:

   ```bash
   ./build/rforge ps --json | jq '.[]? | {name,state,runs,runner_owner,runner_daemon_alive,runner_instance_id}'
   ```

3. Confirm fleet alerts do not show runner health failures:

   ```bash
   ./build/rforge status --json | jq '.alerts.items[]? | select(.message | test("runner health check failed"))'
   ```

If daemon health is bad, follow recovery flow below.

### Planned

- Event stream with `--watch` and JSONL output.

## `rforged` daemon operator flow

### Launch daemon with explicit target

```bash
make build-rust-cli build-rust-daemon
./build/rforged --config ~/.config/forge/config.yaml --port 50061
export FORGE_DAEMON_TARGET=http://127.0.0.1:50061
```

### Start daemon-owned loops

```bash
./build/rforge up --name daemon-ops --profile <profile> --spawn-owner daemon
```

### Stop and recover daemon-owned loops

Graceful stop path:

```bash
./build/rforge stop <loop-name-or-short-id>
```

Resume path after daemon restart or stale reconciliation:

```bash
./build/rforge resume <loop-name-or-short-id> --spawn-owner daemon
./build/rforge ps --json | jq '.[]? | {name,state,runner_owner,runner_daemon_alive}'
```

## Troubleshooting

### Config loading errors

- Check file locations (first match wins):
  - `$XDG_CONFIG_HOME/forge/config.yaml`
  - `~/.config/forge/config.yaml`
  - `./config.yaml`
- Validate YAML format; start from `docs/config.example.yaml`.

### Migration failures

- Ensure the `global.data_dir` path is writable.
- Remove stale lock files if present (none used today).
- Retry:

  ```bash
  ./build/forge migrate up
  ```

### SSH/tmux issues (planned workflows)

- Ensure `tmux` is installed and in PATH.
- Ensure `ssh` is installed and the target host is reachable.
- Confirm private key permissions: `chmod 600 ~/.ssh/id_ed25519`.
- For passphrase-protected keys, be ready to enter the passphrase when prompted.

## Backup and restore

### Backup

1. Stop any running Forge process.
2. Copy the SQLite database and config:

   ```bash
   cp ~/.local/share/forge/forge.db /backup/location/forge.db
   cp ~/.config/forge/config.yaml /backup/location/config.yaml
   ```

3. (Optional) Track your git state if repositories are managed in workspaces.

### Restore

1. Stop any running Forge process.
2. Restore the database and config to their original locations.
3. Run migrations to ensure schema is current:

   ```bash
   ./build/forge migrate up
   ```

## Scaling nodes

### Current (manual)

- Ensure remote nodes have `tmux`, `git`, and agent runtimes installed.
- Validate SSH access from the control plane host.

### Planned

- `forge node add` for registration.
- `forge node bootstrap` to provision dependencies.
- `forge node doctor` for diagnostics.

## forged systemd service (optional)

If you install `forged` on a node, you can run it as a systemd service.
Use the template in `scripts/forged.service`, copy it to `/etc/systemd/system/`,
then enable it:

```bash
sudo cp scripts/forged.service /etc/systemd/system/forged.service
sudo systemctl daemon-reload
sudo systemctl enable --now forged
```

## forged launchd service (macOS, optional)

For macOS login auto-start, install a per-user LaunchAgent:

```bash
scripts/install-macos-forged-launchagent.sh install
```

Optional immediate bootstrap (without waiting for next login):

```bash
scripts/install-macos-forged-launchagent.sh install --start-now
```

Status and uninstall:

```bash
scripts/install-macos-forged-launchagent.sh status
scripts/install-macos-forged-launchagent.sh uninstall
```

Make shortcuts:

```bash
make forged-launchd-install
make forged-launchd-status
make forged-launchd-uninstall
```

## Secure remote access (SSH port forwarding)

When you need to reach a service running on a remote node (for example an agent
runtime or local HTTP UI), use SSH port forwarding instead of opening public
ports.

```bash
# Forward local 8080 to a service bound on the remote node
forge node forward prod-server --local-port 8080 --remote 127.0.0.1:3000
```

Tips:
- Keep remote services bound to `127.0.0.1` on the node.
- Forward to a local `127.0.0.1` bind unless you explicitly need to share.

## Incident checklist

- Capture logs (re-run with `--log-level debug`).
- Record failing command and stderr output.
- Confirm migrations are applied.
- Validate config file location and values.
- Verify system prerequisites (`tmux`, `ssh`, `git`).
- Escalate with reproduction steps and environment info.

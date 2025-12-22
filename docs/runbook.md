# Swarm Operational Runbook

This runbook covers day-to-day operational tasks: monitoring, troubleshooting,
backup/restore, and scaling. Swarm is early-stage; some commands are planned
but not yet wired up. Planned steps are labeled.

## Scope and assumptions

- Control plane runs locally via `./build/swarm`.
- Data is stored in a local SQLite database (default: `~/.local/share/swarm/swarm.db`).
- tmux and ssh are required for most orchestration workflows.

## Monitoring

### Current (implemented)

- **Logs**: Swarm logs to stderr by default. Enable verbose logging with:

  ```bash
  ./build/swarm --log-level debug
  # or
  ./build/swarm -v
  ```

- **Database health**: Verify migrations are applied:

  ```bash
  ./build/swarm migrate status
  ./build/swarm migrate version
  ```

### Planned

- TUI dashboard for agent/workspace state.
- Event stream with `--watch` and JSONL output.

## Troubleshooting

### Config loading errors

- Check file locations (first match wins):
  - `$XDG_CONFIG_HOME/swarm/config.yaml`
  - `~/.config/swarm/config.yaml`
  - `./config.yaml`
- Validate YAML format; start from `docs/config.example.yaml`.

### Migration failures

- Ensure the `global.data_dir` path is writable.
- Remove stale lock files if present (none used today).
- Retry:

  ```bash
  ./build/swarm migrate up
  ```

### SSH/tmux issues (planned workflows)

- Ensure `tmux` is installed and in PATH.
- Ensure `ssh` is installed and the target host is reachable.
- Confirm private key permissions: `chmod 600 ~/.ssh/id_ed25519`.
- For passphrase-protected keys, be ready to enter the passphrase when prompted.

## Backup and restore

### Backup

1. Stop any running Swarm process.
2. Copy the SQLite database and config:

   ```bash
   cp ~/.local/share/swarm/swarm.db /backup/location/swarm.db
   cp ~/.config/swarm/config.yaml /backup/location/config.yaml
   ```

3. (Optional) Track your git state if repositories are managed in workspaces.

### Restore

1. Stop any running Swarm process.
2. Restore the database and config to their original locations.
3. Run migrations to ensure schema is current:

   ```bash
   ./build/swarm migrate up
   ```

## Scaling nodes

### Current (manual)

- Ensure remote nodes have `tmux`, `git`, and agent runtimes installed.
- Validate SSH access from the control plane host.

### Planned

- `swarm node add` for registration.
- `swarm node bootstrap` to provision dependencies.
- `swarm node doctor` for diagnostics.

## Incident checklist

- Capture logs (re-run with `--log-level debug`).
- Record failing command and stderr output.
- Confirm migrations are applied.
- Validate config file location and values.
- Verify system prerequisites (`tmux`, `ssh`, `git`).
- Escalate with reproduction steps and environment info.

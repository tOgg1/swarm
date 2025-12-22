# Swarm Configuration Reference

Swarm uses a YAML configuration file loaded with the following precedence:

1. Defaults (built into the binary)
2. Config file
3. Environment variables
4. CLI flags (when available)

## Config file locations

Swarm looks for `config.yaml` in these locations (first match wins):

- `$XDG_CONFIG_HOME/swarm/config.yaml`
- `~/.config/swarm/config.yaml`
- `./config.yaml` (current directory)

You can also pass an explicit path with `--config` (see `internal/cli/root.go`).

## Environment variable overrides

Environment variables use the prefix `SWARM_` and replace dots with underscores.

Examples:

- `logging.level` -> `SWARM_LOGGING_LEVEL`
- `database.max_connections` -> `SWARM_DATABASE_MAX_CONNECTIONS`

## Duration format

Duration fields use Go duration strings, for example: `250ms`, `2s`, `5m`, `1h`.

## Full example

See `docs/config.example.yaml` for a complete example file.

## Configuration options

### global

- `global.data_dir` (string): Data directory. Default: `~/.local/share/swarm`.
- `global.config_dir` (string): Config directory. Default: `~/.config/swarm`.
- `global.auto_register_local_node` (bool): Register local node on startup. Default: `true`.

### database

- `database.path` (string): SQLite database file path. Default: empty (uses `{data_dir}/swarm.db`).
- `database.max_connections` (int): Maximum DB connections. Default: `10`.
- `database.busy_timeout_ms` (int): SQLite busy timeout in milliseconds. Default: `5000`.

### logging

- `logging.level` (string): `debug`, `info`, `warn`, `error`. Default: `info`.
- `logging.format` (string): `json` or `console`. Default: `console`.
- `logging.file` (string): Optional log file path. Default: empty.
- `logging.enable_caller` (bool): Include caller info in logs. Default: `false`.

### node_defaults

Defaults applied when creating new nodes. Per-node overrides are not implemented yet.

- `node_defaults.ssh_backend` (string): `native`, `system`, or `auto`. Default: `auto`.
- `node_defaults.ssh_timeout` (duration): SSH connect timeout. Default: `30s`.
- `node_defaults.ssh_key_path` (string): Default SSH private key path. Default: empty.
- `node_defaults.health_check_interval` (duration): Node health check interval. Default: `60s`.

### workspace_defaults

Defaults applied when creating new workspaces. Per-workspace overrides are not implemented yet.

- `workspace_defaults.tmux_prefix` (string): Prefix for generated tmux sessions. Default: `swarm`.
- `workspace_defaults.default_agent_type` (string): `opencode`, `claude-code`, `codex`, `gemini`, `generic`. Default: `opencode`.
- `workspace_defaults.auto_import_existing` (bool): Auto import existing tmux sessions. Default: `false`.

### agent_defaults

- `agent_defaults.default_type` (string): Default agent type. Default: `opencode`.
- `agent_defaults.state_polling_interval` (duration): State polling interval. Default: `2s`.
- `agent_defaults.idle_timeout` (duration): Idle timeout before marking idle. Default: `10s`.
- `agent_defaults.transcript_buffer_size` (int): Max transcript lines. Default: `10000`.
- `agent_defaults.approval_policy` (string): `strict` or `permissive`. Default: `strict`.

### scheduler

- `scheduler.dispatch_interval` (duration): Scheduler loop interval. Default: `1s`.
- `scheduler.max_retries` (int): Dispatch retry limit. Default: `3`.
- `scheduler.retry_backoff` (duration): Base backoff between retries. Default: `5s`.
- `scheduler.default_cooldown_duration` (duration): Cooldown after rate limit. Default: `5m`.
- `scheduler.auto_rotate_on_rate_limit` (bool): Rotate account automatically. Default: `true`.

### tui

- `tui.refresh_interval` (duration): UI refresh rate. Default: `500ms`.
- `tui.theme` (string): `default`, `dark`, or `light`. Default: `default`.
- `tui.show_timestamps` (bool): Show timestamps in UI. Default: `true`.
- `tui.compact_mode` (bool): Use compact UI layout. Default: `false`.

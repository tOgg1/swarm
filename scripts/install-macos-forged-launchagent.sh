#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

LABEL="${FORGED_LAUNCHD_LABEL:-com.forge.forged}"
PLIST_PATH="${FORGED_LAUNCHD_PLIST:-$HOME/Library/LaunchAgents/${LABEL}.plist}"
LOG_DIR="${FORGED_LAUNCHD_LOG_DIR:-$HOME/.local/share/forge/logs}"
STDOUT_LOG="${FORGED_LAUNCHD_STDOUT_LOG:-$LOG_DIR/forged.launchd.stdout.log}"
STDERR_LOG="${FORGED_LAUNCHD_STDERR_LOG:-$LOG_DIR/forged.launchd.stderr.log}"
CONFIG_PATH="${FORGE_CONFIG_PATH:-$HOME/.config/forge/config.yaml}"
PATH_VALUE="/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:${HOME}/.cargo/bin:${HOME}/.local/bin"
DOMAIN="gui/$(id -u)"

log() {
  printf '%s\n' "[forged-launchd] $*"
}

warn() {
  printf '%s\n' "[forged-launchd] WARN: $*" >&2
}

fail() {
  printf '%s\n' "[forged-launchd] ERROR: $*" >&2
  exit 1
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

usage() {
  cat <<EOF
Usage:
  scripts/install-macos-forged-launchagent.sh install [--start-now] [--forged-bin <path>]
  scripts/install-macos-forged-launchagent.sh uninstall
  scripts/install-macos-forged-launchagent.sh status

Environment overrides:
  FORGED_LAUNCHD_LABEL            launchd label (default: ${LABEL})
  FORGED_LAUNCHD_PLIST            plist path (default: ${PLIST_PATH})
  FORGED_LAUNCHD_LOG_DIR          log directory (default: ${LOG_DIR})
  FORGED_LAUNCHD_STDOUT_LOG       stdout log file
  FORGED_LAUNCHD_STDERR_LOG       stderr log file
  FORGE_CONFIG_PATH               forge config path (default: ${CONFIG_PATH})
  FORGED_BIN                      explicit forged binary path
EOF
}

ensure_macos() {
  if [ "$(uname -s)" != "Darwin" ]; then
    fail "this installer is macOS-only (launchd)"
  fi
}

is_loaded() {
  launchctl print "${DOMAIN}/${LABEL}" >/dev/null 2>&1
}

bootout_if_loaded() {
  if is_loaded; then
    launchctl bootout "${DOMAIN}/${LABEL}" >/dev/null 2>&1 || true
  fi
}

resolve_forged_bin() {
  local explicit="${1:-}"
  if [ -n "$explicit" ]; then
    [ -x "$explicit" ] || fail "FORGED_BIN/--forged-bin is not executable: $explicit"
    printf '%s' "$explicit"
    return
  fi

  local resolved
  resolved="$(command -v forged || true)"
  [ -n "$resolved" ] || fail "forged not found on PATH"
  printf '%s' "$resolved"
}

write_plist() {
  local forged_bin="$1"
  local include_config="$2"

  mkdir -p "$(dirname "$PLIST_PATH")" "$LOG_DIR"

  cat >"$PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${forged_bin}</string>
EOF
  if [ "$include_config" -eq 1 ]; then
    cat >>"$PLIST_PATH" <<EOF
    <string>--config</string>
    <string>${CONFIG_PATH}</string>
EOF
  fi
  cat >>"$PLIST_PATH" <<EOF
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>${PATH_VALUE}</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${STDOUT_LOG}</string>
  <key>StandardErrorPath</key>
  <string>${STDERR_LOG}</string>
</dict>
</plist>
EOF

  plutil -lint "$PLIST_PATH" >/dev/null
  log "wrote plist: $PLIST_PATH"
}

start_now() {
  bootout_if_loaded
  launchctl bootstrap "$DOMAIN" "$PLIST_PATH"
  launchctl enable "${DOMAIN}/${LABEL}" >/dev/null 2>&1 || true
  launchctl kickstart -k "${DOMAIN}/${LABEL}"
  log "started launchd job: ${DOMAIN}/${LABEL}"
}

print_status() {
  if [ -f "$PLIST_PATH" ]; then
    log "plist present: $PLIST_PATH"
  else
    warn "plist missing: $PLIST_PATH"
  fi

  if is_loaded; then
    log "launchd job loaded: ${DOMAIN}/${LABEL}"
    launchctl print "${DOMAIN}/${LABEL}" | sed -n '1,80p'
  else
    warn "launchd job not loaded: ${DOMAIN}/${LABEL}"
  fi
}

do_install() {
  local start_immediately="$1"
  local forged_bin_input="$2"
  local forged_bin include_config

  forged_bin="$(resolve_forged_bin "$forged_bin_input")"
  include_config=0
  if [ -f "$CONFIG_PATH" ]; then
    include_config=1
  else
    warn "config not found at ${CONFIG_PATH}; forged will run with built-in defaults"
  fi

  write_plist "$forged_bin" "$include_config"
  if [ "$start_immediately" -eq 1 ]; then
    start_now
  else
    log "installed for next login (RunAtLoad=true). use --start-now to bootstrap immediately"
  fi
}

do_uninstall() {
  bootout_if_loaded
  if [ -f "$PLIST_PATH" ]; then
    if command_exists trash; then
      trash "$PLIST_PATH"
    else
      rm -f "$PLIST_PATH"
    fi
    log "removed plist: $PLIST_PATH"
  else
    warn "plist already missing: $PLIST_PATH"
  fi
}

main() {
  ensure_macos
  command_exists launchctl || fail "launchctl not found"
  command_exists plutil || fail "plutil not found"

  local action="${1:-install}"
  shift || true

  local start_immediately=0
  local forged_bin_arg="${FORGED_BIN:-}"

  while [ $# -gt 0 ]; do
    case "$1" in
      --start-now)
        start_immediately=1
        ;;
      --forged-bin)
        shift
        [ $# -gt 0 ] || fail "--forged-bin requires a value"
        forged_bin_arg="$1"
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        fail "unknown option: $1"
        ;;
    esac
    shift
  done

  case "$action" in
    install)
      do_install "$start_immediately" "$forged_bin_arg"
      ;;
    uninstall)
      do_uninstall
      ;;
    status)
      print_status
      ;;
    *)
      fail "unknown action: $action"
      ;;
  esac
}

main "$@"

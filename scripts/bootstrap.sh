#!/usr/bin/env bash
# Swarm bootstrap script (idempotent).
#
# Environment:
#   SWARM_USER                User to create (default: swarm)
#   SWARM_SHELL               User shell (default: /bin/bash)
#   SWARM_AUTHORIZED_KEYS     Newline-delimited SSH keys to add
#   SWARM_AUTHORIZED_KEYS_FILE Path to authorized_keys file to import
#   SWARM_INHERIT_ROOT_KEYS   Copy /root/.ssh/authorized_keys (default: 1)
#   SWARM_INSTALL_EXTRAS      Install optional extras (default: 0)
#   SWARM_INSTALL_OPENCODE    Install OpenCode CLI (default: 1)
#   SWARM_OPENCODE_VERSION    OpenCode release version (default: latest)
#   SWARM_INSTALL_CLAUDE      Install Claude Code CLI (default: 0)
#   SWARM_CLAUDE_VERSION      Claude Code npm version (default: latest)
#   SWARM_CLAUDE_NPM_PACKAGE  Claude Code npm package (default: @anthropic-ai/claude-code)
set -euo pipefail
IFS=$'\n\t'

SWARM_USER="${SWARM_USER:-swarm}"
SWARM_SHELL="${SWARM_SHELL:-/bin/bash}"
SWARM_AUTHORIZED_KEYS="${SWARM_AUTHORIZED_KEYS:-}"
SWARM_AUTHORIZED_KEYS_FILE="${SWARM_AUTHORIZED_KEYS_FILE:-}"
SWARM_INHERIT_ROOT_KEYS="${SWARM_INHERIT_ROOT_KEYS:-1}"
SWARM_INSTALL_EXTRAS="${SWARM_INSTALL_EXTRAS:-0}"
SWARM_INSTALL_OPENCODE="${SWARM_INSTALL_OPENCODE:-1}"
SWARM_OPENCODE_VERSION="${SWARM_OPENCODE_VERSION:-latest}"
SWARM_INSTALL_CLAUDE="${SWARM_INSTALL_CLAUDE:-0}"
SWARM_CLAUDE_VERSION="${SWARM_CLAUDE_VERSION:-latest}"
SWARM_CLAUDE_NPM_PACKAGE="${SWARM_CLAUDE_NPM_PACKAGE:-@anthropic-ai/claude-code}"

usage() {
  cat <<'EOF'
Usage: bootstrap.sh [options]

Options:
  --install-extras       Install optional packages (jq, ripgrep, rsync).
  --no-install-extras    Skip optional packages (default).
  --install-claude       Install Claude Code CLI via npm.
  --no-install-claude    Skip Claude Code install (default).
  --claude-version <v>   Pin Claude Code npm version (default: latest).
  -h, --help             Show this help text.
EOF
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --install-extras)
        SWARM_INSTALL_EXTRAS="1"
        ;;
      --no-install-extras)
        SWARM_INSTALL_EXTRAS="0"
        ;;
      --install-claude)
        SWARM_INSTALL_CLAUDE="1"
        ;;
      --no-install-claude)
        SWARM_INSTALL_CLAUDE="0"
        ;;
      --claude-version)
        shift
        if [ "$#" -eq 0 ]; then
          fail "--claude-version requires a value"
        fi
        SWARM_CLAUDE_VERSION="$1"
        ;;
      --claude-version=*)
        SWARM_CLAUDE_VERSION="${1#*=}"
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        warn "unknown argument: $1"
        ;;
    esac
    shift
  done
}

log() {
  printf '%s\n' "[swarm-bootstrap] $*"
}

warn() {
  printf '%s\n' "[swarm-bootstrap] WARN: $*" >&2
}

fail() {
  printf '%s\n' "[swarm-bootstrap] ERROR: $*" >&2
  exit 1
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    fail "must run as root"
  fi
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

detect_arch() {
  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64)
      echo "amd64"
      ;;
    aarch64|arm64)
      echo "arm64"
      ;;
    *)
      fail "unsupported architecture: $arch"
      ;;
  esac
}

detect_pkg_manager() {
  if command_exists apt-get; then
    echo "apt"
    return
  fi
  if command_exists dnf; then
    echo "dnf"
    return
  fi
  if command_exists yum; then
    echo "yum"
    return
  fi
  fail "supported package manager not found (apt, dnf, yum)"
}

apt_update_once() {
  if [ "${_APT_UPDATED:-0}" -eq 1 ]; then
    return
  fi
  log "updating apt package index"
  apt-get update -y
  _APT_UPDATED=1
}

install_packages() {
  local manager="$1"
  shift
  if [ "$#" -eq 0 ]; then
    return
  fi

  case "$manager" in
    apt)
      apt_update_once
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@"
      ;;
    dnf)
      dnf -y install "$@"
      ;;
    yum)
      yum -y install "$@"
      ;;
    *)
      fail "unsupported package manager: $manager"
      ;;
  esac
}

download_file() {
  local url="$1"
  local dest="$2"
  curl -fsSL "$url" -o "$dest"
}

opencode_release_base_url() {
  local version="$1"
  if [ "$version" = "latest" ]; then
    echo "https://github.com/opencode-ai/opencode/releases/latest/download"
    return
  fi
  case "$version" in
    v*)
      echo "https://github.com/opencode-ai/opencode/releases/download/$version"
      ;;
    *)
      echo "https://github.com/opencode-ai/opencode/releases/download/v$version"
      ;;
  esac
}

install_opencode() {
  local manager="$1"

  if [ "$SWARM_INSTALL_OPENCODE" != "1" ]; then
    log "skipping OpenCode install (SWARM_INSTALL_OPENCODE=$SWARM_INSTALL_OPENCODE)"
    return
  fi

  if command_exists opencode; then
    log "opencode already installed"
    return
  fi

  local arch base_url asset tmpfile
  arch="$(detect_arch)"
  base_url="$(opencode_release_base_url "$SWARM_OPENCODE_VERSION")"

  case "$manager" in
    apt)
      asset="opencode-linux-${arch}.deb"
      ;;
    dnf|yum)
      asset="opencode-linux-${arch}.rpm"
      ;;
    *)
      fail "unsupported package manager for opencode install: $manager"
      ;;
  esac

  tmpfile="$(mktemp -t opencode.XXXXXX)"
  log "downloading OpenCode ($SWARM_OPENCODE_VERSION) for $arch"
  download_file "$base_url/$asset" "$tmpfile"

  case "$manager" in
    apt)
      if ! dpkg -i "$tmpfile"; then
        apt_update_once
        DEBIAN_FRONTEND=noninteractive apt-get install -y -f
        dpkg -i "$tmpfile"
      fi
      ;;
    dnf)
      dnf -y install "$tmpfile"
      ;;
    yum)
      yum -y install "$tmpfile"
      ;;
  esac

  rm -f "$tmpfile"

  if ! command_exists opencode; then
    fail "opencode install failed"
  fi
  log "opencode installed"
}

install_claude() {
  if [ "$SWARM_INSTALL_CLAUDE" != "1" ]; then
    log "skipping Claude Code install (SWARM_INSTALL_CLAUDE=$SWARM_INSTALL_CLAUDE)"
    return
  fi

  if command_exists claude; then
    log "claude already installed"
    return
  fi

  if ! command_exists npm; then
    warn "npm not available; cannot install Claude Code CLI"
    return
  fi

  local pkg="$SWARM_CLAUDE_NPM_PACKAGE"
  if [ "$SWARM_CLAUDE_VERSION" != "latest" ] && [ -n "$SWARM_CLAUDE_VERSION" ]; then
    pkg="${pkg}@${SWARM_CLAUDE_VERSION}"
  fi

  log "installing Claude Code CLI via npm ($pkg)"
  if npm install -g "$pkg"; then
    log "claude installed"
  else
    warn "Claude Code CLI install failed; try manual install steps"
  fi

  if ! command_exists claude; then
    warn "claude not found after install"
  fi
}

verify_runtime() {
  local label="$1"
  local cmd="$2"
  shift 2

  if ! command_exists "$cmd"; then
    warn "runtime check failed: $label not found"
    return 1
  fi

  if [ "$#" -eq 0 ]; then
    log "runtime check ok: $label"
    return 0
  fi

  if "$cmd" "$@" >/dev/null 2>&1; then
    log "runtime check ok: $label"
    return 0
  fi

  warn "runtime check failed: $label not working"
  return 1
}

verify_runtimes() {
  local failures=0

  if command_exists node; then
    verify_runtime "node" "node" "--version" || failures=1
  elif command_exists nodejs; then
    verify_runtime "nodejs" "nodejs" "--version" || failures=1
  else
    warn "runtime check failed: node not found"
    failures=1
  fi
  verify_runtime "npm" "npm" "--version" || failures=1
  verify_runtime "python3" "python3" "--version" || failures=1
  verify_runtime "pip3" "pip3" "--version" || failures=1
  if [ "$SWARM_INSTALL_OPENCODE" = "1" ]; then
    verify_runtime "opencode" "opencode" || failures=1
  fi

  if [ "$failures" -ne 0 ]; then
    fail "one or more runtimes failed verification"
  fi
}

ensure_user() {
  if id "$SWARM_USER" >/dev/null 2>&1; then
    log "user '$SWARM_USER' already exists"
  else
    log "creating user '$SWARM_USER'"
    useradd -m -s "$SWARM_SHELL" "$SWARM_USER"
  fi

  local sudo_group=""
  if getent group sudo >/dev/null 2>&1; then
    sudo_group="sudo"
  elif getent group wheel >/dev/null 2>&1; then
    sudo_group="wheel"
  else
    warn "no sudo group found; user will not be granted sudo access"
  fi

  if [ -n "$sudo_group" ]; then
    usermod -aG "$sudo_group" "$SWARM_USER"
  fi
}

home_for_user() {
  local home
  home="$(getent passwd "$SWARM_USER" | cut -d: -f6)"
  if [ -z "$home" ]; then
    home="/home/$SWARM_USER"
  fi
  echo "$home"
}

trim_line() {
  printf '%s' "$1" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

ensure_ssh_keys() {
  local home ssh_dir auth_file
  home="$(home_for_user)"
  ssh_dir="$home/.ssh"
  auth_file="$ssh_dir/authorized_keys"

  mkdir -p "$ssh_dir"
  chmod 700 "$ssh_dir"
  touch "$auth_file"
  chmod 600 "$auth_file"
  chown -R "$SWARM_USER:$SWARM_USER" "$ssh_dir"

  local key
  add_key() {
    key="$(trim_line "$1")"
    if [ -z "$key" ]; then
      return
    fi
    if [ "${key#\#}" != "$key" ]; then
      return
    fi
    if ! grep -qxF "$key" "$auth_file"; then
      printf '%s\n' "$key" >>"$auth_file"
    fi
  }

  if [ -n "$SWARM_AUTHORIZED_KEYS_FILE" ] && [ -f "$SWARM_AUTHORIZED_KEYS_FILE" ]; then
    while IFS= read -r line; do
      add_key "$line"
    done <"$SWARM_AUTHORIZED_KEYS_FILE"
  fi

  if [ -n "$SWARM_AUTHORIZED_KEYS" ]; then
    while IFS= read -r line; do
      add_key "$line"
    done <<<"$SWARM_AUTHORIZED_KEYS"
  fi

  if [ "$SWARM_INHERIT_ROOT_KEYS" = "1" ] && [ -f /root/.ssh/authorized_keys ]; then
    while IFS= read -r line; do
      add_key "$line"
    done </root/.ssh/authorized_keys
  fi

  if [ ! -s "$auth_file" ]; then
    warn "no SSH keys were added for user '$SWARM_USER'"
  fi

  chown "$SWARM_USER:$SWARM_USER" "$auth_file"
}

configure_shell_defaults() {
  cat >/etc/profile.d/swarm.sh <<'EOF'
# Swarm defaults
umask 022
export EDITOR="${EDITOR:-vim}"
export VISUAL="${VISUAL:-vim}"
export PAGER="${PAGER:-less}"
EOF
}

install_dependencies() {
  local manager="$1"
  local base_packages=()
  local runtime_packages=()

  case "$manager" in
    apt)
      base_packages=(sudo tmux git curl ca-certificates openssh-client)
      runtime_packages=(nodejs npm python3 python3-pip)
      ;;
    dnf|yum)
      base_packages=(sudo tmux git curl ca-certificates openssh-clients)
      runtime_packages=(nodejs npm python3 python3-pip)
      ;;
  esac

  install_packages "$manager" "${base_packages[@]}"
  install_packages "$manager" "${runtime_packages[@]}"

  if [ "$SWARM_INSTALL_EXTRAS" = "1" ]; then
    case "$manager" in
      apt)
        install_packages "$manager" jq ripgrep rsync
        ;;
      dnf|yum)
        install_packages "$manager" jq ripgrep rsync
        ;;
    esac
  fi
}

enable_service_if_present() {
  local service="$1"

  if ! command_exists systemctl; then
    return
  fi

  if ! systemctl list-unit-files >/dev/null 2>&1; then
    return
  fi

  if systemctl list-unit-files | grep -q "^${service}\\.service"; then
    if systemctl enable --now "$service" >/dev/null 2>&1; then
      log "enabled service: $service"
    else
      warn "failed to enable service: $service"
    fi
  fi
}

ensure_services() {
  enable_service_if_present ssh
  enable_service_if_present sshd
}

main() {
  require_root
  parse_args "$@"

  local manager
  manager="$(detect_pkg_manager)"
  log "detected package manager: $manager"

  install_dependencies "$manager"
  install_opencode "$manager"
  install_claude
  ensure_user
  ensure_ssh_keys
  configure_shell_defaults
  ensure_services
  verify_runtimes

  log "bootstrap complete"
}

main "$@"

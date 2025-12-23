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
#   SWARM_INSTALL_CODEX       Install Codex CLI (default: 0)
#   SWARM_CODEX_VERSION       Codex npm version (default: latest)
#   SWARM_CODEX_NPM_PACKAGE   Codex npm package (default: @openai/codex)
#   SWARM_INSTALL_GEMINI      Install Gemini CLI (default: 0)
#   SWARM_GEMINI_VERSION      Gemini npm version (default: latest)
#   SWARM_GEMINI_NPM_PACKAGE  Gemini npm package (default: @google/gemini-cli)
#   SWARM_INSTALL_SWARMD      Install swarmd daemon binary (default: 0)
#   SWARM_SWARMD_VERSION      Swarm release version for swarmd (default: latest)
#   SWARM_INTERACTIVE         Prompt for configuration (default: 0)
#   SWARM_SKIP_USER_SETUP     Skip user creation and SSH key setup (default: 0)
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
SWARM_INSTALL_CODEX="${SWARM_INSTALL_CODEX:-0}"
SWARM_CODEX_VERSION="${SWARM_CODEX_VERSION:-latest}"
SWARM_CODEX_NPM_PACKAGE="${SWARM_CODEX_NPM_PACKAGE:-@openai/codex}"
SWARM_INSTALL_GEMINI="${SWARM_INSTALL_GEMINI:-0}"
SWARM_GEMINI_VERSION="${SWARM_GEMINI_VERSION:-latest}"
SWARM_GEMINI_NPM_PACKAGE="${SWARM_GEMINI_NPM_PACKAGE:-@google/gemini-cli}"
SWARM_INSTALL_SWARMD="${SWARM_INSTALL_SWARMD:-0}"
SWARM_SWARMD_VERSION="${SWARM_SWARMD_VERSION:-latest}"
SWARM_INTERACTIVE="${SWARM_INTERACTIVE:-0}"
SWARM_SKIP_USER_SETUP="${SWARM_SKIP_USER_SETUP:-0}"

usage() {
  cat <<'EOF'
Usage: bootstrap.sh [options]

Options:
  --install-extras       Install optional packages (jq, ripgrep, rsync).
  --no-install-extras    Skip optional packages (default).
  --install-claude       Install Claude Code CLI via npm.
  --no-install-claude    Skip Claude Code install (default).
  --claude-version <v>   Pin Claude Code npm version (default: latest).
  --install-codex        Install Codex CLI via npm.
  --no-install-codex     Skip Codex install (default).
  --codex-version <v>    Pin Codex npm version (default: latest).
  --install-gemini       Install Gemini CLI via npm.
  --no-install-gemini    Skip Gemini install (default).
  --gemini-version <v>   Pin Gemini npm version (default: latest).
  --install-swarmd       Install swarmd daemon binary from releases.
  --no-install-swarmd    Skip swarmd install (default).
  --swarmd-version <v>   Swarm release version for swarmd (default: latest).
  --interactive          Prompt for configuration before running.
  --non-interactive      Do not prompt (default).
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
      --install-codex)
        SWARM_INSTALL_CODEX="1"
        ;;
      --no-install-codex)
        SWARM_INSTALL_CODEX="0"
        ;;
      --codex-version)
        shift
        if [ "$#" -eq 0 ]; then
          fail "--codex-version requires a value"
        fi
        SWARM_CODEX_VERSION="$1"
        ;;
      --codex-version=*)
        SWARM_CODEX_VERSION="${1#*=}"
        ;;
      --install-gemini)
        SWARM_INSTALL_GEMINI="1"
        ;;
      --no-install-gemini)
        SWARM_INSTALL_GEMINI="0"
        ;;
      --gemini-version)
        shift
        if [ "$#" -eq 0 ]; then
          fail "--gemini-version requires a value"
        fi
        SWARM_GEMINI_VERSION="$1"
        ;;
      --gemini-version=*)
        SWARM_GEMINI_VERSION="${1#*=}"
        ;;
      --install-swarmd)
        SWARM_INSTALL_SWARMD="1"
        ;;
      --no-install-swarmd)
        SWARM_INSTALL_SWARMD="0"
        ;;
      --swarmd-version)
        shift
        if [ "$#" -eq 0 ]; then
          fail "--swarmd-version requires a value"
        fi
        SWARM_SWARMD_VERSION="$1"
        ;;
      --swarmd-version=*)
        SWARM_SWARMD_VERSION="${1#*=}"
        ;;
      --interactive)
        SWARM_INTERACTIVE="1"
        ;;
      --non-interactive)
        SWARM_INTERACTIVE="0"
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

can_prompt() {
  [ -t 0 ] && [ -t 1 ]
}

prompt_yes_no() {
  local prompt="$1"
  local default="${2:-n}"
  local reply suffix

  case "$default" in
    y|Y|yes|YES)
      default="y"
      suffix="Y/n"
      ;;
    *)
      default="n"
      suffix="y/N"
      ;;
  esac

  while true; do
    read -r -p "$prompt [$suffix] " reply || return 1
    reply="$(trim_line "${reply,,}")"
    if [ -z "$reply" ]; then
      reply="$default"
    fi
    case "$reply" in
      y|yes)
        return 0
        ;;
      n|no)
        return 1
        ;;
      *)
        printf '%s\n' "Please answer y or n."
        ;;
    esac
  done
}

prompt_input() {
  local prompt="$1"
  local default="$2"
  local reply

  if [ -n "$default" ]; then
    if ! read -r -p "$prompt [$default]: " reply; then
      printf '%s' "$default"
      return 0
    fi
  else
    if ! read -r -p "$prompt: " reply; then
      printf '%s' "$default"
      return 0
    fi
  fi
  reply="$(trim_line "$reply")"
  if [ -z "$reply" ]; then
    printf '%s' "$default"
  else
    printf '%s' "$reply"
  fi
}

run_interactive() {
  if [ "$SWARM_INTERACTIVE" != "1" ]; then
    return
  fi

  if ! can_prompt; then
    warn "interactive mode requested but no TTY available; continuing with defaults"
    return
  fi

  log "interactive mode enabled"

  SWARM_USER="$(prompt_input "Swarm user name" "$SWARM_USER")"
  SWARM_SHELL="$(prompt_input "Shell for ${SWARM_USER:-user}" "$SWARM_SHELL")"

  if prompt_yes_no "Manage user '${SWARM_USER}' (create if missing)" "y"; then
    SWARM_SKIP_USER_SETUP="0"
  else
    SWARM_SKIP_USER_SETUP="1"
  fi

  if prompt_yes_no "Copy root authorized_keys to '${SWARM_USER}'" "$( [ "$SWARM_INHERIT_ROOT_KEYS" = "1" ] && echo y || echo n )"; then
    SWARM_INHERIT_ROOT_KEYS="1"
  else
    SWARM_INHERIT_ROOT_KEYS="0"
  fi

  if prompt_yes_no "Install OpenCode CLI" "$( [ "$SWARM_INSTALL_OPENCODE" = "1" ] && echo y || echo n )"; then
    SWARM_INSTALL_OPENCODE="1"
  else
    SWARM_INSTALL_OPENCODE="0"
  fi

  if prompt_yes_no "Install Claude Code CLI" "$( [ "$SWARM_INSTALL_CLAUDE" = "1" ] && echo y || echo n )"; then
    SWARM_INSTALL_CLAUDE="1"
  else
    SWARM_INSTALL_CLAUDE="0"
  fi

  if prompt_yes_no "Install Codex CLI" "$( [ "$SWARM_INSTALL_CODEX" = "1" ] && echo y || echo n )"; then
    SWARM_INSTALL_CODEX="1"
  else
    SWARM_INSTALL_CODEX="0"
  fi

  if prompt_yes_no "Install Gemini CLI" "$( [ "$SWARM_INSTALL_GEMINI" = "1" ] && echo y || echo n )"; then
    SWARM_INSTALL_GEMINI="1"
  else
    SWARM_INSTALL_GEMINI="0"
  fi

  if prompt_yes_no "Install swarmd daemon binary" "$( [ "$SWARM_INSTALL_SWARMD" = "1" ] && echo y || echo n )"; then
    SWARM_INSTALL_SWARMD="1"
  else
    SWARM_INSTALL_SWARMD="0"
  fi

  if prompt_yes_no "Install optional extras (jq, ripgrep, rsync)" "$( [ "$SWARM_INSTALL_EXTRAS" = "1" ] && echo y || echo n )"; then
    SWARM_INSTALL_EXTRAS="1"
  else
    SWARM_INSTALL_EXTRAS="0"
  fi
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

detect_os() {
  local os
  os="$(uname -s)"
  case "$os" in
    Linux|linux)
      echo "linux"
      ;;
    Darwin|darwin)
      echo "darwin"
      ;;
    *)
      fail "unsupported OS: $os"
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

resolve_swarm_tag() {
  local version="$1"
  if [ "$version" = "latest" ]; then
    if ! command_exists curl; then
      fail "curl is required to resolve latest swarmd version"
    fi
    local tag
    tag="$(curl -fsSL https://api.github.com/repos/tOgg1/swarm/releases/latest | tr -d '\r' | awk -F\" '/tag_name/{print $4; exit}')"
    if [ -z "$tag" ]; then
      fail "failed to resolve latest swarmd version"
    fi
    printf '%s' "$tag"
    return
  fi
  case "$version" in
    v*)
      printf '%s' "$version"
      ;;
    *)
      printf 'v%s' "$version"
      ;;
  esac
}

swarm_archive_version() {
  local tag="$1"
  printf '%s' "${tag#v}"
}

install_swarmd() {
  local manager="$1"

  if [ "$SWARM_INSTALL_SWARMD" != "1" ]; then
    log "skipping swarmd install (SWARM_INSTALL_SWARMD=$SWARM_INSTALL_SWARMD)"
    return
  fi

  if command_exists swarmd; then
    log "swarmd already installed"
    return
  fi

  if ! command_exists tar; then
    install_packages "$manager" tar
  fi

  if ! command_exists tar; then
    warn "tar not available; cannot install swarmd"
    return
  fi

  local os arch tag version asset url tmpdir
  os="$(detect_os)"
  arch="$(detect_arch)"
  tag="$(resolve_swarm_tag "$SWARM_SWARMD_VERSION")"
  version="$(swarm_archive_version "$tag")"
  asset="swarm_${version}_${os}_${arch}.tar.gz"
  url="https://github.com/tOgg1/swarm/releases/download/${tag}/${asset}"

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' RETURN

  log "downloading swarmd (${tag}) for ${os}/${arch}"
  download_file "$url" "$tmpdir/$asset"

  tar -xzf "$tmpdir/$asset" -C "$tmpdir"
  if [ ! -f "$tmpdir/swarmd" ]; then
    fail "swarmd binary not found in archive"
  fi

  install -m 0755 "$tmpdir/swarmd" /usr/local/bin/swarmd
  log "swarmd installed"
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

install_codex() {
  if [ "$SWARM_INSTALL_CODEX" != "1" ]; then
    log "skipping Codex install (SWARM_INSTALL_CODEX=$SWARM_INSTALL_CODEX)"
    return
  fi

  if command_exists codex; then
    log "codex already installed"
    return
  fi

  if ! command_exists npm; then
    warn "npm not available; cannot install Codex CLI"
    return
  fi

  local pkg="$SWARM_CODEX_NPM_PACKAGE"
  if [ "$SWARM_CODEX_VERSION" != "latest" ] && [ -n "$SWARM_CODEX_VERSION" ]; then
    pkg="${pkg}@${SWARM_CODEX_VERSION}"
  fi

  log "installing Codex CLI via npm ($pkg)"
  if npm install -g "$pkg"; then
    log "codex installed"
  else
    warn "Codex CLI install failed; try manual install steps"
  fi

  if ! command_exists codex; then
    warn "codex not found after install"
  fi
}

install_gemini() {
  if [ "$SWARM_INSTALL_GEMINI" != "1" ]; then
    log "skipping Gemini install (SWARM_INSTALL_GEMINI=$SWARM_INSTALL_GEMINI)"
    return
  fi

  if command_exists gemini; then
    log "gemini already installed"
    return
  fi

  if ! command_exists npm; then
    warn "npm not available; cannot install Gemini CLI"
    return
  fi

  local pkg="$SWARM_GEMINI_NPM_PACKAGE"
  if [ "$SWARM_GEMINI_VERSION" != "latest" ] && [ -n "$SWARM_GEMINI_VERSION" ]; then
    pkg="${pkg}@${SWARM_GEMINI_VERSION}"
  fi

  log "installing Gemini CLI via npm ($pkg)"
  if npm install -g "$pkg"; then
    log "gemini installed"
  else
    warn "Gemini CLI install failed; try manual install steps"
  fi

  if ! command_exists gemini; then
    warn "gemini not found after install"
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
  if [ "$SWARM_INSTALL_CLAUDE" = "1" ]; then
    verify_runtime "claude" "claude" "--version" || failures=1
  fi
  if [ "$SWARM_INSTALL_CODEX" = "1" ]; then
    verify_runtime "codex" "codex" "--version" || failures=1
  fi
  if [ "$SWARM_INSTALL_GEMINI" = "1" ]; then
    verify_runtime "gemini" "gemini" "--version" || failures=1
  fi
  if [ "$SWARM_INSTALL_SWARMD" = "1" ]; then
    verify_runtime "swarmd" "swarmd" "--version" || failures=1
  fi

  if [ "$failures" -ne 0 ]; then
    fail "one or more runtimes failed verification"
  fi
}

ensure_user() {
  if [ "$SWARM_SKIP_USER_SETUP" = "1" ]; then
    log "skipping user setup"
    return
  fi

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
  if [ "$SWARM_SKIP_USER_SETUP" = "1" ]; then
    log "skipping SSH key setup"
    return
  fi

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
  run_interactive

  local manager
  manager="$(detect_pkg_manager)"
  log "detected package manager: $manager"

  install_dependencies "$manager"
  install_opencode "$manager"
  install_claude
  install_codex
  install_gemini
  install_swarmd "$manager"
  ensure_user
  ensure_ssh_keys
  configure_shell_defaults
  ensure_services
  verify_runtimes

  log "bootstrap complete"
}

main "$@"

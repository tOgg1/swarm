#!/usr/bin/env bash
# Swarm bootstrap installer. Downloads and verifies bootstrap.sh before running.
set -euo pipefail
IFS=$'\n\t'

SWARM_BOOTSTRAP_BASE_URL="${SWARM_BOOTSTRAP_BASE_URL:-https://raw.githubusercontent.com/tOgg1/swarm/main/scripts}"
SWARM_BOOTSTRAP_URL="${SWARM_BOOTSTRAP_URL:-${SWARM_BOOTSTRAP_BASE_URL}/bootstrap.sh}"
SWARM_BOOTSTRAP_SHA_URL="${SWARM_BOOTSTRAP_SHA_URL:-${SWARM_BOOTSTRAP_BASE_URL}/bootstrap.sh.sha256}"

log() {
  printf '%s\n' "[swarm-install] $*"
}

fail() {
  printf '%s\n' "[swarm-install] ERROR: $*" >&2
  exit 1
}

download() {
  local url="$1"
  local dest="$2"

  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
    return 0
  fi

  if command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
    return 0
  fi

  fail "curl or wget is required to download bootstrap assets"
}

verify_checksum() {
  local dir="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$dir" && sha256sum -c bootstrap.sh.sha256)
    return 0
  fi

  if command -v shasum >/dev/null 2>&1; then
    (cd "$dir" && shasum -a 256 -c bootstrap.sh.sha256)
    return 0
  fi

  fail "sha256sum or shasum is required to verify bootstrap.sh"
}

run_bootstrap() {
  local script="$1"
  shift

  if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
      exec sudo bash "$script" "$@"
    fi
    fail "must run as root (sudo not available)"
  fi

  exec bash "$script" "$@"
}

main() {
  local tmpdir
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  log "downloading bootstrap script"
  download "$SWARM_BOOTSTRAP_URL" "$tmpdir/bootstrap.sh"
  download "$SWARM_BOOTSTRAP_SHA_URL" "$tmpdir/bootstrap.sh.sha256"

  log "verifying checksum"
  verify_checksum "$tmpdir"

  log "running bootstrap"
  run_bootstrap "$tmpdir/bootstrap.sh" "$@"
}

main "$@"

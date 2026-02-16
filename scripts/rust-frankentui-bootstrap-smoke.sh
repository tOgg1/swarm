#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

mkdir -p build

cargo build -p forge-tui --features frankentui-bootstrap

python3 - <<'PY'
import fcntl
import os
import pty
import select
import signal
import struct
import sys
import termios
import time
from pathlib import Path

repo_root = Path.cwd()
out_path = repo_root / "build" / "rust-frankentui-bootstrap-smoke.txt"

cmd = [
    "cargo",
    "run",
    "-p",
    "forge-tui",
    "--bin",
    "forge-tui",
    "--features",
    "frankentui-bootstrap",
]

env = os.environ.copy()
env["FORGE_TUI_RUNTIME"] = "frankentui"
env["FORGE_TUI_DEV_SNAPSHOT_FALLBACK"] = "0"

def set_winsize(fd: int, cols: int, rows: int) -> None:
    payload = struct.pack("HHHH", rows, cols, 0, 0)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, payload)

def pump(fd: int, duration: float) -> bytes:
    deadline = time.time() + duration
    chunks = bytearray()
    while time.time() < deadline:
        readable, _, _ = select.select([fd], [], [], 0.05)
        if fd not in readable:
            continue
        try:
            data = os.read(fd, 65536)
        except OSError:
            break
        if not data:
            break
        chunks.extend(data)
    return bytes(chunks)

pid, fd = pty.fork()
if pid == 0:
    os.execvpe(cmd[0], cmd, env)

capture = bytearray()
failed = None

try:
    set_winsize(fd, cols=100, rows=30)
    capture.extend(pump(fd, 1.8))

    os.write(fd, b"r")
    capture.extend(pump(fd, 0.8))

    set_winsize(fd, cols=120, rows=40)
    capture.extend(pump(fd, 0.8))

    os.write(fd, b"q")

    exit_status = None
    deadline = time.time() + 5.0
    while time.time() < deadline:
        done_pid, status = os.waitpid(pid, os.WNOHANG)
        if done_pid == pid:
            exit_status = status
            break
        capture.extend(pump(fd, 0.2))

    if exit_status is None:
        os.kill(pid, signal.SIGKILL)
        failed = "runtime did not exit after quit key"
    else:
        code = 1
        if os.WIFEXITED(exit_status):
            code = os.WEXITSTATUS(exit_status)
        elif os.WIFSIGNALED(exit_status):
            code = 128 + os.WTERMSIG(exit_status)
        if code != 0:
            failed = f"runtime exited with code {code}"

    text = capture.decode("utf-8", errors="ignore")
    out_path.write_text(text, encoding="utf-8")

    required = [
        "Forge Loops",
        "1:Overview",
        "5:Inbox",
    ]
    for marker in required:
        if marker not in text:
            failed = failed or f"missing marker: {marker}"

finally:
    try:
        os.close(fd)
    except OSError:
        pass

if failed:
    print(f"rust-frankentui-bootstrap-smoke: FAIL ({failed})", file=sys.stderr)
    print(f"capture: {out_path}", file=sys.stderr)
    raise SystemExit(1)

print("rust-frankentui-bootstrap-smoke: PASS")
print(f"capture: {out_path}")
PY

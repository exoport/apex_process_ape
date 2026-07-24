#!/usr/bin/env bash
# ape-sandbox entrypoint (PLAN-16 D6/D7). Starts sshd for the loopback /
# VS Code Remote access path, then hands off to the container command
# (default: stay alive so the workspace is long-lived and `nerdctl exec`
# /`attach` work across sessions).
set -euo pipefail

# Generate host keys on first boot if the image didn't ship them.
if [ ! -e /etc/ssh/ssh_host_ed25519_key ]; then
  ssh-keygen -A || true
fi

# Start sshd in the background (best-effort — `nerdctl exec`/`attach` work
# whether or not sshd is up; sshd is only for the ssh / VS Code Remote path).
if command -v /usr/sbin/sshd >/dev/null 2>&1; then
  /usr/sbin/sshd || true
fi

exec "$@"

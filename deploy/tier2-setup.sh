#!/usr/bin/env bash
# tier2-setup.sh — provision a Linux+KVM host to run the full ape/aped
# Kata-QEMU stack (PLAN-18 Tier-2) and install + start aped.
#
# It performs, idempotently (safe to re-run):
#   1. prereqs (curl tar xz-utils zstd)
#   2. containerd + nerdctl + CNI + runc via the nerdctl-full bundle → /usr/local
#   3. Kata Containers static release → /opt/kata + per-VMM shim wrappers
#   4. containerd LimitMEMLOCK=infinity / raised LimitNOFILE drop-in
#   5. a Kata-QEMU smoke test (guest kernel must differ from the host kernel)
#   6. build + install ape + aped → /usr/local/bin
#   7. the `ape` group + `aped` service user
#   8. deploy assets (policy, tmpfiles, systemd units) + enable/start the daemon
#   9. an operator credential the invoking human user can read
#
# Run as root from a checkout:   sudo bash deploy/tier2-setup.sh
# Tunables (env):  NERDCTL_VERSION KATA_VERSION SMOKE_IMAGE APE_NODE
#                  MOUNT_ROOT WITH_AUDIT SKIP_SMOKE REPO_DIR
#
# IMPORTANT re: the shim config snag — `ctr`/`nerdctl` do NOT honor the
# containerd `ConfigPath` shim option (only the CRI/Kubernetes path does), so a
# per-runtime ConfigPath stanza in /etc/containerd/config.toml would silently do
# nothing here. This script instead installs KATA_CONF_FILE wrapper shims, which
# work for nerdctl AND pick the correct per-hypervisor config (qemu vs clh).
set -euo pipefail

# ---- tunables --------------------------------------------------------------
NERDCTL_VERSION="${NERDCTL_VERSION:-2.3.4}"
KATA_VERSION="${KATA_VERSION:-3.32.0}"
SMOKE_IMAGE="${SMOKE_IMAGE:-alpine:latest}"
PROBE_IMAGE="${PROBE_IMAGE:-ape-tier2-probe:latest}"   # long-running stand-in for the acceptance test
APE_NODE="${APE_NODE:-$(hostname)}"
MOUNT_ROOT="${MOUNT_ROOT:-/srv/workspaces}"  # host-fs mount root allowed by policy (NOT /home — see note in step 8: aped runs ProtectHome=yes)
WITH_AUDIT="${WITH_AUDIT:-0}"              # 1 → install the immutable auditd rules
SKIP_SMOKE="${SKIP_SMOKE:-0}"             # 1 → skip the Kata-QEMU guest-kernel smoke test
KATA_DEFAULTS="/opt/kata/share/defaults/kata-containers"
CONTAINERD_SOCK="/run/containerd/containerd.sock"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="${REPO_DIR:-$(cd "$SCRIPT_DIR/.." && pwd)}"

# ---- logging ---------------------------------------------------------------
c_blue=$'\e[34m'; c_green=$'\e[32m'; c_yellow=$'\e[33m'; c_red=$'\e[31m'; c_reset=$'\e[0m'
step() { printf '%s\n' "${c_blue}==> $*${c_reset}"; }
ok()   { printf '%s\n' "${c_green}  ✓ $*${c_reset}"; }
warn() { printf '%s\n' "${c_yellow}  ! $*${c_reset}"; }
die()  { printf '%s\n' "${c_red}  ✗ $*${c_reset}" >&2; exit 1; }

# ---- preflight -------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "run as root (sudo bash deploy/tier2-setup.sh)"

case "$(uname -m)" in
  x86_64)  ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *) die "unsupported arch $(uname -m) (need x86_64 or aarch64)" ;;
esac
HOST_KERNEL="$(uname -r)"

step "Preflight"
ok "arch=$ARCH  host-kernel=$HOST_KERNEL  node=$APE_NODE"
[ -e /dev/kvm ] || die "/dev/kvm absent — Kata microVMs need KVM (bare-metal or nested-virt VM)"
ok "/dev/kvm present"
[ -d "$REPO_DIR/cmd/aped" ] || die "REPO_DIR=$REPO_DIR is not an ape checkout (no cmd/aped)"
ok "repo=$REPO_DIR"

# ---- 1. prereqs ------------------------------------------------------------
step "1/9 prerequisites (curl tar xz-utils zstd)"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl tar xz-utils zstd ca-certificates >/dev/null
ok "installed"

# ---- 2. nerdctl-full bundle (containerd + nerdctl + CNI + runc) ------------
step "2/9 nerdctl-full ${NERDCTL_VERSION} (containerd + nerdctl + CNI + runc)"
if command -v nerdctl >/dev/null && nerdctl version 2>/dev/null | grep -q "${NERDCTL_VERSION}"; then
  ok "nerdctl ${NERDCTL_VERSION} already installed"
else
  tarball="nerdctl-full-${NERDCTL_VERSION}-linux-${ARCH}.tar.gz"
  url="https://github.com/containerd/nerdctl/releases/download/v${NERDCTL_VERSION}/${tarball}"
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
  step "  downloading ${url}"
  curl -fsSL -o "$tmp/$tarball" "$url" || die "download failed: $url"
  tar Cxzf /usr/local "$tmp/$tarball"
  ok "extracted to /usr/local"
fi
command -v containerd >/dev/null || die "containerd not on PATH after install"
systemctl daemon-reload
systemctl enable --now containerd >/dev/null 2>&1 || systemctl restart containerd
# wait for the control socket
for _ in $(seq 1 20); do [ -S "$CONTAINERD_SOCK" ] && break; sleep 0.5; done
[ -S "$CONTAINERD_SOCK" ] || die "containerd socket $CONTAINERD_SOCK never appeared (systemctl status containerd)"
ok "containerd running ($CONTAINERD_SOCK)"

# ---- 3. Kata Containers static release --------------------------------------
step "3/9 Kata Containers ${KATA_VERSION} static release → /opt/kata"
if [ -x /opt/kata/bin/containerd-shim-kata-v2 ] && \
   /opt/kata/bin/kata-runtime --version 2>/dev/null | grep -q "${KATA_VERSION}"; then
  ok "Kata ${KATA_VERSION} already installed"
else
  # The Kata static asset switched to zstd compression (.tar.zst); the older
  # .tar.xz name 404s. Prefer .tar.zst, fall back to .tar.xz for old releases.
  base="https://github.com/kata-containers/kata-containers/releases/download/${KATA_VERSION}"
  tmp2="$(mktemp -d)"
  if curl -fsSL -o "$tmp2/kata.tzst" "${base}/kata-static-${KATA_VERSION}-${ARCH}.tar.zst"; then
    tar --zstd -xf "$tmp2/kata.tzst" -C /
  elif curl -fsSL -o "$tmp2/kata.txz" "${base}/kata-static-${KATA_VERSION}-${ARCH}.tar.xz"; then
    tar -xf "$tmp2/kata.txz" -C /
  else
    rm -rf "$tmp2"; die "could not download kata-static ${KATA_VERSION} (.tar.zst or .tar.xz)"
  fi
  rm -rf "$tmp2"
  ok "extracted to /opt/kata"
fi
[ -x /opt/kata/bin/containerd-shim-kata-v2 ] || die "kata shim missing under /opt/kata/bin"

# per-VMM shim wrappers that export KATA_CONF_FILE (nerdctl-compatible; a plain
# symlink would make BOTH qemu and clh handlers read the default config).
install_kata_wrapper() {
  local vmm="$1" cfg="$KATA_DEFAULTS/configuration-$1.toml"
  [ -f "$cfg" ] || die "kata config $cfg not found (VMM $vmm unavailable in this build)"
  cat > "/usr/local/bin/containerd-shim-kata-${vmm}-v2" <<EOF
#!/bin/sh
# ape Tier-2: pin the ${vmm} Kata config for nerdctl (ConfigPath is CRI-only).
exec env KATA_CONF_FILE="$cfg" /opt/kata/bin/containerd-shim-kata-v2 "\$@"
EOF
  chmod 0755 "/usr/local/bin/containerd-shim-kata-${vmm}-v2"
  ok "shim wrapper containerd-shim-kata-${vmm}-v2 → $cfg"
}
install_kata_wrapper qemu
install_kata_wrapper clh
ln -sf /opt/kata/bin/containerd-shim-kata-v2 /usr/local/bin/containerd-shim-kata-v2
ok "generic shim containerd-shim-kata-v2 symlinked onto PATH"

# ---- 4. containerd memlock / nofile drop-in --------------------------------
step "4/9 containerd LimitMEMLOCK=infinity drop-in"
install -d -m 0755 /etc/systemd/system/containerd.service.d
cat > /etc/systemd/system/containerd.service.d/10-aped.conf <<'EOF'
# PLAN-18 Appendix A host-config: VFIO pins all guest RAM (device tier) and QEMU
# locks memory, so the containerd cgroup (which parents the Kata shim + QEMU)
# needs an unbounded memlock and a raised fd ceiling. The shim/QEMU inherit
# these; aped itself sets none of them.
[Service]
LimitMEMLOCK=infinity
LimitNOFILE=1048576
EOF
systemctl daemon-reload
systemctl restart containerd
for _ in $(seq 1 20); do [ -S "$CONTAINERD_SOCK" ] && break; sleep 0.5; done
[ -S "$CONTAINERD_SOCK" ] || die "containerd did not come back after the drop-in restart"
ok "drop-in installed, containerd restarted"

# ---- 5. Kata-QEMU smoke test ------------------------------------------------
if [ "$SKIP_SMOKE" = "1" ]; then
  warn "5/9 smoke test skipped (SKIP_SMOKE=1)"
else
  step "5/9 Kata-QEMU smoke test (guest kernel must differ from host)"
  nerdctl pull -q "$SMOKE_IMAGE" >/dev/null 2>&1 || warn "pull $SMOKE_IMAGE failed (will try during run)"
  set +e
  GUEST_KERNEL="$(nerdctl run --rm --runtime io.containerd.kata-qemu.v2 "$SMOKE_IMAGE" uname -r 2>/tmp/kata-smoke.err)"
  rc=$?
  set -e
  if [ $rc -ne 0 ] || [ -z "$GUEST_KERNEL" ]; then
    warn "kata-runtime host check follows:"; /opt/kata/bin/kata-runtime check 2>&1 | sed 's/^/    /' || true
    echo "--- smoke stderr ---"; sed 's/^/    /' /tmp/kata-smoke.err || true
    die "Kata-QEMU smoke test FAILED (rc=$rc). Host kernel $HOST_KERNEL may be incompatible with Kata ${KATA_VERSION}."
  fi
  [ "$GUEST_KERNEL" != "$HOST_KERNEL" ] || die "guest kernel == host kernel ($GUEST_KERNEL) — container did NOT run under Kata/KVM"
  ok "Kata-QEMU works: guest=$GUEST_KERNEL host=$HOST_KERNEL"
fi

# A tiny long-lived image for the TestTier2Provision acceptance (independent of
# the smoke test, so it is built even with SKIP_SMOKE=1): the real ape-sandbox
# image is heavy/unpublished, but the test only needs a pullable image that
# STAYS running and has a shell for `exec`. CMD is `sleep 2147483647` (~68y), not
# `sleep infinity` — busybox `sleep` wants a number.
step "  building long-running probe image $PROBE_IMAGE (for TestTier2Provision)"
nerdctl pull -q "$SMOKE_IMAGE" >/dev/null 2>&1 || true
# `nerdctl build` needs buildkitd; the nerdctl-full bundle ships the unit but
# does not start it. The `nerdctl commit` path is the buildkit-free fallback.
systemctl enable --now buildkit.service >/dev/null 2>&1 || true
pctx="$(mktemp -d)"
cat > "$pctx/Dockerfile" <<EOF
FROM $SMOKE_IMAGE
CMD ["sleep", "2147483647"]
EOF
if nerdctl build -t "$PROBE_IMAGE" "$pctx" >/tmp/probe-build.err 2>&1; then
  ok "built $PROBE_IMAGE (buildkit)"
else
  nerdctl rm -f ape-probe-src >/dev/null 2>&1 || true
  if nerdctl run -d --name ape-probe-src "$SMOKE_IMAGE" sleep 2147483647 >/dev/null 2>&1 && \
     nerdctl commit --change 'CMD ["sleep", "2147483647"]' ape-probe-src "$PROBE_IMAGE" >/dev/null 2>&1; then
    ok "built $PROBE_IMAGE (commit fallback)"
  else
    warn "could not build $PROBE_IMAGE — set APE_APED_IT_IMAGE to a pullable long-running image for the test"
    sed 's/^/    /' /tmp/probe-build.err 2>/dev/null | tail -5 || true
  fi
  nerdctl rm -f ape-probe-src >/dev/null 2>&1 || true
fi
rm -rf "$pctx"

# The opt-in containerd driver (`aped run --driver containerd`) reads its OWN
# `aped` containerd namespace, not nerdctl's `default`. Stage the probe there too
# (best-effort) so the barrier-3-free driver + interactive attach work without a
# manual import. --network none avoids a CNI dependency just to hold a container
# open for the commit.
if command -v nerdctl >/dev/null && ! ctr -n aped images ls -q 2>/dev/null | grep -qF "$PROBE_IMAGE"; then
  nerdctl --namespace aped rm -f ape-probe-src >/dev/null 2>&1 || true
  if nerdctl --namespace aped run -d --network none --name ape-probe-src "$SMOKE_IMAGE" sleep 2147483647 >/dev/null 2>&1 && \
     nerdctl --namespace aped commit --change 'CMD ["sleep", "2147483647"]' ape-probe-src "$PROBE_IMAGE" >/dev/null 2>&1; then
    ctr -n aped images unpack "$PROBE_IMAGE" >/dev/null 2>&1 || true
    ok "staged $PROBE_IMAGE into the 'aped' namespace (--driver containerd)"
  else
    warn "could not stage $PROBE_IMAGE into the 'aped' namespace (needed for --driver containerd)"
  fi
  nerdctl --namespace aped rm -f ape-probe-src >/dev/null 2>&1 || true
fi

# ---- 6. build + install ape + aped -----------------------------------------
step "6/9 build + install ape + aped → /usr/local/bin"
GO="$(command -v go || true)"; [ -n "$GO" ] || { [ -x /usr/local/go/bin/go ] && GO=/usr/local/go/bin/go; }
[ -n "$GO" ] || die "go toolchain not found (need go to build ape/aped)"
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  # build as the invoking user so their module cache is reused (faster, no root
  # re-fetch); the temp dir must be user-owned so the build can write into it.
  bin_tmp="$(sudo -u "$SUDO_USER" mktemp -d)"
  sudo -u "$SUDO_USER" -H env PATH="$(dirname "$GO"):$PATH" \
    sh -c "cd '$REPO_DIR' && go build -o '$bin_tmp/ape' ./cmd/ape && go build -o '$bin_tmp/aped' ./cmd/aped"
else
  bin_tmp="$(mktemp -d)"
  ( cd "$REPO_DIR" && "$GO" build -o "$bin_tmp/ape" ./cmd/ape && "$GO" build -o "$bin_tmp/aped" ./cmd/aped )
fi
install -m 0755 "$bin_tmp/ape" /usr/local/bin/ape
install -m 0755 "$bin_tmp/aped" /usr/local/bin/aped
rm -rf "$bin_tmp"
ok "installed /usr/local/bin/ape + /usr/local/bin/aped"

# ---- 7. ape group + aped service user --------------------------------------
step "7/9 ape group + aped service user"
getent group ape >/dev/null || groupadd --system ape
ok "group ape (gid $(getent group ape | cut -d: -f3))"
if getent passwd aped >/dev/null; then
  ok "user aped exists (uid $(id -u aped))"
else
  useradd --system --gid ape --no-create-home --shell /usr/sbin/nologin aped
  ok "created user aped (uid $(id -u aped))"
fi
# let the invoking human operator connect to the priv group + read operator creds
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  if id -nG "$SUDO_USER" | tr ' ' '\n' | grep -qx ape; then
    ok "$SUDO_USER already in group ape"
  else
    usermod -aG ape "$SUDO_USER"
    warn "added $SUDO_USER to group ape — log out/in (or 'newgrp ape') for it to take effect"
  fi
fi

# ---- 8. deploy assets + start ----------------------------------------------
step "8/9 deploy assets + start aped"
install -D -m 0644 "$SCRIPT_DIR/policy.yaml"                    /etc/aped/policy.yaml
install -D -m 0644 "$SCRIPT_DIR/tmpfiles.d/aped.conf"          /etc/tmpfiles.d/aped.conf
install -D -m 0644 "$SCRIPT_DIR/systemd/aped-priv.socket"      /etc/systemd/system/aped-priv.socket
install -D -m 0644 "$SCRIPT_DIR/systemd/aped.service"          /etc/systemd/system/aped.service
install -D -m 0644 "$SCRIPT_DIR/systemd/aped-front.service"    /etc/systemd/system/aped-front.service
ok "installed policy + tmpfiles + 3 units"
# Allow the locally-built probe image in the DEPLOYED policy (never the shipped
# repo policy.yaml) so the `ape sandbox` CLI validation loop can create with it.
# Done before the daemon starts, so the executor loads it fresh (policy is read
# at startup). checkImage is an exact match, so the string must match --image.
if ! grep -qE "^[[:space:]]*-[[:space:]]*${PROBE_IMAGE}[[:space:]]*$" /etc/aped/policy.yaml; then
  sed -i "/^images:/a\\  - ${PROBE_IMAGE}" /etc/aped/policy.yaml
  ok "added $PROBE_IMAGE to /etc/aped/policy.yaml (validation image)"
fi
# host-fs mount root: create it and ensure policy allows it. aped runs with
# ProtectHome=yes, so a root UNDER /home or /root is invisible to the daemon and
# a mount there fails ("not reachable by aped"); it needs a systemd BindPaths=
# drop-in (see deploy/systemd/aped.service.d/mount-root.conf.example +
# docs/how-to/run-aped.md). A root outside those (the default /srv/workspaces)
# works with no unit changes.
mkdir -p "$MOUNT_ROOT" 2>/dev/null || warn "could not create MOUNT_ROOT=$MOUNT_ROOT"
if ! grep -qE "^\s*-\s*${MOUNT_ROOT//\//\\/}\s*$" /etc/aped/policy.yaml; then
  sed -i "/^mount_roots:/a\\  - ${MOUNT_ROOT}" /etc/aped/policy.yaml
  ok "added $MOUNT_ROOT to /etc/aped/policy.yaml mount_roots"
fi
case "$MOUNT_ROOT" in
  /home | /home/* | /root | /root/*)
    warn "MOUNT_ROOT=$MOUNT_ROOT is under a ProtectHome-masked path — host-fs mounts there need a BindPaths= drop-in on aped.service + aped-front.service (see deploy/systemd/aped.service.d/mount-root.conf.example)" ;;
esac
systemd-tmpfiles --create /etc/tmpfiles.d/aped.conf
ok "runtime/state dirs created (systemd-tmpfiles)"

if [ "$WITH_AUDIT" = "1" ]; then
  if command -v augenrules >/dev/null; then
    install -D -m 0640 "$SCRIPT_DIR/audit/50-aped.rules" /etc/audit/rules.d/50-aped.rules
    augenrules --load && ok "auditd rules loaded (immutable until reboot: -e 2)"
  else
    warn "auditd not installed; skipping audit rules (apt-get install auditd)"
  fi
else
  warn "audit rules NOT installed (WITH_AUDIT=1 to enable; note -e 2 makes them immutable until reboot)"
fi

systemctl daemon-reload
# clear any failed/rate-limited state from a previous run so a re-run restarts cleanly
systemctl reset-failed aped-priv.socket aped.service aped-front.service 2>/dev/null || true
systemctl enable aped-priv.socket aped.service aped-front.service >/dev/null 2>&1 || true
systemctl restart aped-priv.socket aped.service aped-front.service
sleep 1
for u in aped.service aped-front.service; do
  systemctl is-active --quiet "$u" || {
    warn "$u not active — recent logs:"; journalctl -u "$u" -n 25 --no-pager | sed 's/^/    /'
    die "$u failed to start"
  }
done
ok "aped-priv.socket + aped.service + aped-front.service active"

# ---- 9. operator credential for the human user -----------------------------
step "9/9 operator credential"
OP_SRC="/var/lib/aped/creds/operator.creds"
for _ in $(seq 1 20); do [ -f "$OP_SRC" ] && break; sleep 0.5; done
[ -f "$OP_SRC" ] || die "$OP_SRC never appeared (journalctl -u aped-front)"
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  op_home="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
  op_dst="$op_home/.config/ape/aped-operator.creds"
  install -d -m 0700 -o "$SUDO_USER" -g "$(id -gn "$SUDO_USER")" "$op_home/.config/ape"
  install -m 0600 -o "$SUDO_USER" -g "$(id -gn "$SUDO_USER")" "$OP_SRC" "$op_dst"
  ok "operator creds copied to $op_dst (owned by $SUDO_USER)"
  echo
  echo "  point the ape CLI at aped:"
  echo "    export APE_NATS_URL=nats://127.0.0.1:4223"
  echo "    export APE_NATS_CREDS=$op_dst"
  echo "    ape sandbox ls --node $APE_NODE"
  warn "operator.creds is re-minted on every aped-front restart — re-run this step (or the script) to refresh the copy"
else
  ok "operator creds at $OP_SRC (root-readable)"
fi

echo
step "Done. Validate with:"
echo "    ape doctor    # kvm.available / containerd.running / kata.runtime → OK"
echo "    ( cd $REPO_DIR && sudo APE_APED_IT=1 APE_APED_IT_IMAGE=$PROBE_IMAGE $GO test ./internal/aped/ -run TestTier2Provision -v )"
echo "    systemd-analyze security aped.service aped-front.service"

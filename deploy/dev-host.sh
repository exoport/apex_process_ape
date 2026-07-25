#!/usr/bin/env bash
# dev-host.sh — the root-only host configuration for the sandbox roadmap
# (PLAN-20 mounts, PLAN-21 egress, PLAN-22 toolchain caches) on a box that ALREADY
# ran deploy/tier2-setup.sh (containerd + Kata + aped installed and running).
#
# It exists so every privileged step is batched into two idempotent, re-runnable
# verbs instead of being sprinkled through a work session:
#
#   sudo bash deploy/dev-host.sh prereqs    # host config only — no ape/aped code needed
#   sudo bash deploy/dev-host.sh redeploy   # install prebuilt ./ape + ./aped + units, restart
#   sudo bash deploy/dev-host.sh all        # prereqs then redeploy
#
# `prereqs` does:
#   1. persist + load the kernel modules egress needs (nf_tables, nf_conntrack, veth, bridge)
#   2. generate /etc/aped/egress.env + /etc/aped/nftables-egress.conf and install,
#      enable and start aped-netbr.service (the host↔guest bridge + nft wall)
#   3. create the host dirs the mount model needs: the framework worktree root and
#      the durable toolchain caches
#   4. install the systemd drop-ins: BindReadOnlyPaths for the /home project root
#      (ProtectHome=yes hides it otherwise) on BOTH aped units, plus the front's
#      egress drop-in (the CONNECT proxy must be able to dial upstream)
#   5. add those roots to /etc/aped/policy.yaml mount_roots
#   6. daemon-reload + socket-first restart, then refresh the operator cred copy
#
# `redeploy` installs the PREBUILT ./ape and ./aped from the checkout (go-free on
# purpose: sudo strips PATH so `go` is not found — build first as your user with
# `make build`), reinstalls every unit in deploy/systemd/ (so newly added units
# like aped-netd.service get picked up automatically), reinstalls the repo policy
# with the dev-local additions re-injected, and restarts socket-first.
#
# Tunables (env):
#   PROJECT_ROOT  host dir holding your repos            (default /home/$SUDO_USER/_dev)
#   FW_ROOT       framework materialize root             (default /srv/apex-framework)
#   FW_REF        default framework ref for workspaces   (default: none)
#   CACHE_ROOT    durable toolchain cache root           (default /srv/ape-caches)
#   MOUNT_ROOT    plain workspace mount root             (default /srv/workspaces)
#   BRIDGE        egress bridge name                     (default apebr0)
#   HOST_CIDR     bridge host address                    (default 169.254.42.1/24)
#   PROXY_PORTS   guest-reachable proxy port range       (default 3128-3999)
#   PROBE_IMAGE   long-running Tier-2 validation image   (default ape-tier2-probe:latest)
#   FRONT_EGRESS  1 → let the front dial upstream (needed by the proxy; default 1)
#   ENABLE_EGRESS 1 → set egress.enabled + allowed_domains in the deployed policy
#   EGRESS_DOMAINS space-separated allow-list written when ENABLE_EGRESS=1
set -euo pipefail

PROJECT_ROOT="${PROJECT_ROOT:-}"
FW_ROOT="${FW_ROOT:-/srv/apex-framework}"
# The framework ref workspaces get by default. It must be materialized on this host
# (ape sandbox framework materialize <ref>) before a workspace can use it.
FW_REF="${FW_REF:-}"
CACHE_ROOT="${CACHE_ROOT:-/srv/ape-caches}"
MOUNT_ROOT="${MOUNT_ROOT:-/srv/workspaces}"
BRIDGE="${BRIDGE:-apebr0}"
HOST_CIDR="${HOST_CIDR:-169.254.42.1/24}"
PROXY_PORTS="${PROXY_PORTS:-3128-3999}"
PROBE_IMAGE="${PROBE_IMAGE:-ape-tier2-probe:latest}"
FRONT_EGRESS="${FRONT_EGRESS:-1}"
ENABLE_EGRESS="${ENABLE_EGRESS:-1}"
# The starter allow-list for a dev box: source control, the Go/npm/PyPI registries,
# and the Anthropic API. A project can NARROW this per repo (.apesandbox.yaml) but
# never widen it, so keep it to what development actually needs.
EGRESS_DOMAINS="${EGRESS_DOMAINS:-github.com *.githubusercontent.com codeload.github.com proxy.golang.org sum.golang.org registry.npmjs.org pypi.org files.pythonhosted.org api.anthropic.com}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="${REPO_DIR:-$(cd "$SCRIPT_DIR/.." && pwd)}"
POLICY=/etc/aped/policy.yaml

c_blue=$'\e[34m'; c_green=$'\e[32m'; c_yellow=$'\e[33m'; c_red=$'\e[31m'; c_reset=$'\e[0m'
step() { printf '%s\n' "${c_blue}==> $*${c_reset}"; }
ok()   { printf '%s\n' "${c_green}  ✓ $*${c_reset}"; }
warn() { printf '%s\n' "${c_yellow}  ! $*${c_reset}"; }
die()  { printf '%s\n' "${c_red}  ✗ $*${c_reset}" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root (sudo bash deploy/dev-host.sh <prereqs|redeploy|all>)"
[ -d "$REPO_DIR/cmd/aped" ] || die "REPO_DIR=$REPO_DIR is not an ape checkout (no cmd/aped)"

OP_USER="${SUDO_USER:-}"
if [ -z "$OP_USER" ] || [ "$OP_USER" = "root" ]; then
  warn "no SUDO_USER — skipping the per-user steps (project root, operator cred copy)"
  OP_HOME=""
else
  OP_HOME="$(getent passwd "$OP_USER" | cut -d: -f6)"
  [ -n "$PROJECT_ROOT" ] || PROJECT_ROOT="$OP_HOME/_dev"
fi

# ---- shared helpers --------------------------------------------------------

# yaml_add_list_item <file> <top-level key> <value> — insert "  - <value>" right
# after "<key>:" unless an identical entry already exists. aped's policy loader
# REJECTS unknown keys, so this only ever appends to lists that already exist;
# it never introduces a new key.
yaml_add_list_item() {
  local file="$1" key="$2" val="$3"
  grep -qE "^${key}:" "$file" || { warn "$file has no '${key}:' key — skipping $val"; return 0; }
  if grep -qE "^[[:space:]]*-[[:space:]]*${val//\//\\/}[[:space:]]*$" "$file"; then
    ok "$key already contains $val"
  else
    sed -i "/^${key}:/a\\  - ${val}" "$file"
    ok "added $val to $key"
  fi
}

install_dropin() {  # install_dropin <unit> <name> <content...>
  local unit="$1" name="$2"; shift 2
  install -d -m 0755 "/etc/systemd/system/${unit}.d"
  printf '%s\n' "$@" > "/etc/systemd/system/${unit}.d/${name}"
  chmod 0644 "/etc/systemd/system/${unit}.d/${name}"
  ok "drop-in ${unit}.d/${name}"
}

# aped_supports <subcommand> <flag> — does the INSTALLED /usr/local/bin/aped know
# this flag? systemd applies a drop-in to whatever binary is installed NOW, so
# writing a flag a older binary does not understand turns a config step into an
# outage ("Error: unknown flag" on start). Every flag-bearing drop-in below is
# therefore probed, and `redeploy` re-runs the same step after installing the new
# binary — so `prereqs` alone never breaks a running daemon, and `all` converges.
aped_supports() {
  local sub="$1" flag="$2"
  [ -x /usr/local/bin/aped ] || return 1
  /usr/local/bin/aped "$sub" --help 2>/dev/null | grep -q -- "$flag"
}

# install_exec_dropins writes the two ExecStart drop-ins, degrading to the flags the
# installed binary actually supports. Called by BOTH verbs.
install_exec_dropins() {
  local exec_line="/usr/local/bin/aped run --policy /etc/aped/policy.yaml --state-dir /var/lib/aped --audit-log /var/log/aped/audit.jsonl --node %H --allow-user aped --driver containerd"
  if aped_supports run --netd-socket; then
    exec_line="$exec_line --netd-socket /run/aped/netd.sock"
  else
    warn "installed aped has no --netd-socket (pre-PLAN-21 build) — leaving egress unwired until the redeploy verb runs"
  fi
  install_dropin aped.service 10-driver.conf \
    "# Generated by deploy/dev-host.sh — validated containerd driver + egress helper." \
    "[Service]" \
    "ExecStart=" \
    "ExecStart=$exec_line"

  if aped_supports front --policy && aped_supports front --framework-root; then
    local front_ref_flag=""
    [ -n "$FW_REF" ] && front_ref_flag=" --framework-ref $FW_REF"
    install_dropin aped-front.service 10-sandbox-roots.conf \
      "# Generated by deploy/dev-host.sh — policy + framework root + tool caches." \
      "[Service]" \
      "ExecStart=" \
      "ExecStart=/usr/local/bin/aped front --node %H --state-dir /var/lib/aped --mgmt-host 127.0.0.1 --mgmt-port 4223 --operator-creds /var/lib/aped/creds/operator.creds --policy /etc/aped/policy.yaml --framework-root ${FW_ROOT}${front_ref_flag} --cache-root $CACHE_ROOT"
  else
    # An older front would die on these flags; the shipped ExecStart still works
    # (egress simply stays off), so remove a stale drop-in rather than keep it.
    rm -f /etc/systemd/system/aped-front.service.d/10-sandbox-roots.conf
    warn "installed aped front has no --policy/--framework-root — skipping the roots drop-in until the redeploy verb runs"
  fi
}

# restart_aped [soft] — socket-first restart. In "soft" mode a unit that fails to
# come up is a warning, not fatal: the `all` verb restarts once during prereqs and
# again after redeploy, and the first pass must not abort the run that installs the
# binaries which would fix it.
restart_aped() {
  local mode="${1:-hard}"
  step "restart (socket-first)"
  systemctl daemon-reload
  systemctl reset-failed aped-priv.socket aped.service aped-front.service aped-netd.service 2>/dev/null || true
  systemctl stop aped-front.service aped.service 2>/dev/null || true
  systemctl restart aped-priv.socket
  # The netns helper must be up before the executor, which references its socket.
  # It is only installed once the binary carrying `aped netd` is deployed, so a
  # prereqs-only run skips it.
  if [ -f /etc/systemd/system/aped-netd.service ]; then
    systemctl enable aped-netd.service >/dev/null 2>&1 || true
    if systemctl restart aped-netd.service; then
      ok "aped-netd.service active"
    else
      warn "aped-netd.service failed to start — recent logs:"
      journalctl -u aped-netd.service -n 20 --no-pager | sed 's/^/    /'
      warn "egress creates will fail until it starts; non-egress workspaces are unaffected"
    fi
  fi
  systemctl start aped.service aped-front.service
  sleep 1
  local u
  for u in aped.service aped-front.service; do
    if systemctl is-active --quiet "$u"; then
      ok "$u active"
      continue
    fi
    warn "$u not active — recent logs:"
    journalctl -u "$u" -n 30 --no-pager | sed 's/^/    /'
    [ "$mode" = "soft" ] || die "$u failed to start"
    warn "continuing: the redeploy step installs the binaries this may be waiting on"
  done
}

refresh_operator_creds() {
  [ -n "$OP_HOME" ] || return 0
  local src=/var/lib/aped/creds/operator.creds dst="$OP_HOME/.config/ape/aped-operator.creds"
  local i
  for i in $(seq 1 20); do [ -f "$src" ] && break; sleep 0.5; done
  [ -f "$src" ] || { warn "$src never appeared (journalctl -u aped-front)"; return 0; }
  install -d -m 0700 -o "$OP_USER" -g "$(id -gn "$OP_USER")" "$OP_HOME/.config/ape"
  install -m 0600 -o "$OP_USER" -g "$(id -gn "$OP_USER")" "$src" "$dst"
  ok "operator cred → $dst"
}

# ---- verb: prereqs ---------------------------------------------------------

do_prereqs() {
  step "1/6 kernel modules for egress"
  cat > /etc/modules-load.d/aped-egress.conf <<'EOF'
# ape sandbox egress (PLAN-21): the per-VM netns wall uses nftables with conntrack
# state matching, and the host↔guest link is a veth pair on a bridge.
nf_tables
nf_conntrack
veth
bridge
EOF
  local m
  for m in nf_tables nf_conntrack veth bridge; do
    modprobe "$m" 2>/dev/null || warn "modprobe $m failed (may be built into the kernel)"
  done
  ok "modules persisted (/etc/modules-load.d/aped-egress.conf) + loaded"

  step "2/6 host↔guest egress bridge ($BRIDGE $HOST_CIDR)"
  install -d -m 0755 /etc/aped
  cat > /etc/aped/egress.env <<EOF
# Generated by deploy/dev-host.sh — read by aped-netbr.service.
APE_EGRESS_BRIDGE=$BRIDGE
APE_EGRESS_HOST_CIDR=$HOST_CIDR
APE_EGRESS_PROXY_PORTS=$PROXY_PORTS
EOF
  chmod 0644 /etc/aped/egress.env
  ok "/etc/aped/egress.env"
  # Scoped to its own table so a re-run (and ExecStop) can never touch the host's
  # other rulesets. The add/delete/add prelude makes `nft -f` idempotent whether
  # or not the table already exists.
  cat > /etc/aped/nftables-egress.conf <<EOF
#!/usr/sbin/nft -f
# Generated by deploy/dev-host.sh — the HOST half of the sandbox egress wall
# (PLAN-21). The per-VM netns carries its own "only reach the proxy" wall; this
# table is the host-side backstop: a guest may talk to the proxy port range on
# the bridge IP and nothing else, and NOTHING is forwarded across the bridge.
table inet ape_egress
delete table inet ape_egress
table inet ape_egress {
  chain input {
    type filter hook input priority 0; policy accept;
    iifname != "$BRIDGE" return
    ct state established,related accept
    tcp dport $PROXY_PORTS accept
    icmp type echo-request accept
    drop
  }
  chain forward {
    type filter hook forward priority 0; policy accept;
    iifname "$BRIDGE" drop
    oifname "$BRIDGE" drop
  }
}
EOF
  chmod 0644 /etc/aped/nftables-egress.conf
  ok "/etc/aped/nftables-egress.conf (proxy ports $PROXY_PORTS)"
  install -D -m 0644 "$SCRIPT_DIR/systemd/aped-netbr.service" /etc/systemd/system/aped-netbr.service
  systemctl daemon-reload
  systemctl enable aped-netbr.service >/dev/null 2>&1 || true
  systemctl restart aped-netbr.service
  ip -br addr show "$BRIDGE" >/dev/null 2>&1 || die "$BRIDGE did not come up (systemctl status aped-netbr)"
  ok "$(ip -br addr show "$BRIDGE" | tr -s ' ')"
  nft list table inet ape_egress >/dev/null 2>&1 || die "nft table inet ape_egress missing"
  ok "nft table inet ape_egress loaded"

  step "3/6 host dirs (framework worktrees, durable caches, mount root)"
  mkdir -p "$MOUNT_ROOT"
  ok "$MOUNT_ROOT"
  # The framework worktree is materialized by the CLIENT (your user), so this root
  # is user-owned, not root-owned.
  install -d -m 0755 "$FW_ROOT"
  if [ -n "$OP_USER" ]; then chown "$OP_USER:$(id -gn "$OP_USER")" "$FW_ROOT"; fi
  ok "$FW_ROOT (owner ${OP_USER:-root})"
  # Toolchain caches are written by the GUEST (root inside the VM, so root on the
  # host through virtiofsd) and pre-warmable by you: root:ape 2775 + setgid.
  local d
  for d in asdf go cargo npm pub-cache; do
    install -d -m 2775 -o root -g ape "$CACHE_ROOT/$d"
  done
  ok "$CACHE_ROOT/{asdf,go,cargo,npm,pub-cache} (root:ape 2775)"

  step "4/6 systemd drop-ins"
  if [ -n "$PROJECT_ROOT" ] && [ -d "$PROJECT_ROOT" ]; then
    # ProtectHome=yes makes /home invisible inside both units, so a host-fs mount
    # of a project under /home fails the policy lstat. Read-only is enough: aped
    # only stats the path — Kata's virtiofsd (outside these namespaces) does the
    # real guest I/O, so the guest still gets rw where the mount says rw.
    local body="[Service]
BindReadOnlyPaths=$PROJECT_ROOT"
    install_dropin aped.service 10-mount-root.conf \
      "# Generated by deploy/dev-host.sh — expose the project root through ProtectHome=yes." "$body"
    install_dropin aped-front.service 10-mount-root.conf \
      "# Generated by deploy/dev-host.sh — expose the project root through ProtectHome=yes." "$body"
  else
    warn "PROJECT_ROOT=${PROJECT_ROOT:-<unset>} missing — skipped the BindReadOnlyPaths drop-ins"
  fi
  # The ExecStart drop-ins (containerd driver, egress helper socket, framework +
  # cache roots) are written by the shared installer, which only emits flags the
  # INSTALLED binary understands.
  install_exec_dropins

  if [ "$FRONT_EGRESS" = "1" ]; then
    # The front hosts the CONNECT proxy, so it must reach the allowlisted upstreams
    # AND the resolver. IPAddressAllow is address-based only (no hostnames, no
    # ports), and the allowlist is by DOMAIN — so cgroup-level filtering cannot
    # express it and the enforcement lives in-proxy (deny-by-default + audited).
    # TRADEOFF, deliberate: the front is the guest-reachable surface and this gives
    # it outbound IP reach. It stays non-root, capability-less, AF_UNIX/INET only.
    # Remove this file to revert to the shipped posture (no proxy egress).
    install_dropin aped-front.service 20-egress.conf \
      "# Generated by deploy/dev-host.sh — PLAN-21: let the in-front CONNECT proxy dial upstream." \
      "[Unit]" \
      "Wants=aped-netbr.service" \
      "After=aped-netbr.service" \
      "" \
      "[Service]" \
      "IPAddressAllow=any"
  else
    rm -f /etc/systemd/system/aped-front.service.d/20-egress.conf
    warn "FRONT_EGRESS=0 — the front cannot dial upstream (proxy will fail closed)"
  fi

  step "5/6 policy mount_roots"
  [ -f "$POLICY" ] || die "$POLICY missing — run deploy/tier2-setup.sh first"
  cp -a "$POLICY" "$POLICY.bak"
  local r
  for r in "$MOUNT_ROOT" "$FW_ROOT" "$CACHE_ROOT" ${PROJECT_ROOT:+"$PROJECT_ROOT"}; do
    yaml_add_list_item "$POLICY" mount_roots "$r"
  done
  ok "backup at $POLICY.bak"

  step "6/6 restart + creds"
  restart_aped "${PREREQ_RESTART_MODE:-hard}"
  refresh_operator_creds
}

# ---- verb: redeploy --------------------------------------------------------

do_redeploy() {
  step "1/5 prebuilt binaries"
  # go-free on purpose: sudo strips PATH, so `go build` under sudo fails with
  # "make: go: No such file or directory". Build as your user first: make build.
  for b in ape aped; do
    [ -x "$REPO_DIR/$b" ] || die "$REPO_DIR/$b missing — run 'make build' as your user first"
  done
  local newest_src
  newest_src="$(find "$REPO_DIR" -name '*.go' -newer "$REPO_DIR/aped" -print -quit 2>/dev/null || true)"
  [ -z "$newest_src" ] || warn "$newest_src is newer than ./aped — is the build stale?"
  install -m 0755 "$REPO_DIR/ape" /usr/local/bin/ape
  install -m 0755 "$REPO_DIR/aped" /usr/local/bin/aped
  ok "/usr/local/bin/{ape,aped} installed ($("$REPO_DIR/aped" version 2>/dev/null | head -1))"

  step "2/5 units + tmpfiles"
  local f n
  for f in "$SCRIPT_DIR"/systemd/*.service "$SCRIPT_DIR"/systemd/*.socket; do
    [ -f "$f" ] || continue
    n="$(basename "$f")"
    install -D -m 0644 "$f" "/etc/systemd/system/$n"
    ok "unit $n"
  done
  install -D -m 0644 "$SCRIPT_DIR/tmpfiles.d/aped.conf" /etc/tmpfiles.d/aped.conf
  systemd-tmpfiles --create /etc/tmpfiles.d/aped.conf
  ok "tmpfiles applied"

  step "3/5 policy (repo policy + dev-local additions)"
  install -D -m 0644 "$SCRIPT_DIR/policy.yaml" "$POLICY"
  yaml_add_list_item "$POLICY" images "$PROBE_IMAGE"
  local r
  for r in "$MOUNT_ROOT" "$FW_ROOT" "$CACHE_ROOT" ${PROJECT_ROOT:+"$PROJECT_ROOT"}; do
    yaml_add_list_item "$POLICY" mount_roots "$r"
  done
  if [ "$ENABLE_EGRESS" = "1" ]; then
    # The shipped policy keeps egress off (correct default for a fresh node). This
    # rewrites the whole `egress:` block — which is the LAST key in the shipped file
    # — so the result is deterministic instead of sed-patching individual lines.
    awk '/^egress:/{exit} {print}' "$POLICY" > "$POLICY.new"
    {
      echo "# Rewritten by deploy/dev-host.sh (ENABLE_EGRESS=1)."
      echo "egress:"
      echo "  enabled: true"
      echo "  allowed_domains:"
      for d in $EGRESS_DOMAINS; do echo "    - \"$d\""; done
      echo "  max_domains: 32"
    } >> "$POLICY.new"
    mv "$POLICY.new" "$POLICY"
    chmod 0644 "$POLICY"
    ok "egress ENABLED for $(echo "$EGRESS_DOMAINS" | wc -w) domain(s)"
  else
    warn "egress left DISABLED in policy (ENABLE_EGRESS=1 to turn it on)"
  fi

  step "4/5 ExecStart drop-ins + bridge unit"
  # Re-run now that the NEW binary is installed: this is where the flags the older
  # build lacked (--netd-socket, --policy, --framework-root, --cache-root) get wired.
  install_exec_dropins
  if [ -f /etc/systemd/system/aped-netbr.service ] && [ -f /etc/aped/egress.env ]; then
    systemctl daemon-reload
    systemctl enable aped-netbr.service >/dev/null 2>&1 || true
    systemctl restart aped-netbr.service
    ok "aped-netbr.service active"
  else
    warn "aped-netbr not configured — run the 'prereqs' verb for egress host config"
  fi

  step "5/5 restart + creds"
  restart_aped
  refresh_operator_creds
}

# ---- main ------------------------------------------------------------------

case "${1:-}" in
  prereqs)  do_prereqs ;;
  redeploy) do_redeploy ;;
  all)      PREREQ_RESTART_MODE=soft do_prereqs; do_redeploy ;;
  *) die "usage: sudo bash deploy/dev-host.sh <prereqs|redeploy|all>" ;;
esac

echo
step "Done. Verify as your user:"
echo "    export APE_NATS_URL=nats://127.0.0.1:4223 APE_NATS_CREDS=~/.config/ape/aped-operator.creds"
echo "    ape sandbox ls --node \"\$(hostname)\""
echo "    ip -br addr show $BRIDGE; sudo nft list table inet ape_egress"
echo "    ape sandbox framework materialize <ref> --root $FW_ROOT   # then: ape sandbox up dev --framework-ref <ref>"

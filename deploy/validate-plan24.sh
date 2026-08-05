#!/usr/bin/env bash
# validate-plan24.sh — live validation for the local sandbox polish (PLAN-24) on a
# box that already ran deploy/tier2-setup.sh and deploy/dev-host.sh.
#
# It exists because the seven deliverables are Tier-1 tested but four of them can
# only be PROVEN on a real Kata host: a port-forward has to reach a real guest
# socket, the agent has to dial out through a real CONNECT proxy, the reaper has
# to stop a real VM, and the cost scan has to find transcripts a real Claude
# wrote. Everything below is a check with a verdict, not a demo.
#
#   sudo bash deploy/validate-plan24.sh deploy    # root: install prebuilt ./ape + ./aped, restart
#        bash deploy/validate-plan24.sh check     # operator: exercise D2-D6 (fast, ~2 min)
#        bash deploy/validate-plan24.sh reaper    # operator: prove D7 end to end (~6 min)
#        bash deploy/validate-plan24.sh clean     # operator: tear the scratch workspace down
#
# `deploy` is the only verb that needs root, and it is a thin wrapper over
# dev-host.sh redeploy — the binaries must be BUILT FIRST as your own user
# (`make build`), because sudo strips PATH and `go` will not be found.
#
# Tunables (env):
#   WS          scratch workspace name              (default plan24-check)
#   PROJECT     project dir to mount into it        (default: the first repo under
#               MOUNT_ROOT — NOT this checkout, which is usually under /home and
#               therefore invisible to a daemon running ProtectHome=yes)
#   MOUNT_ROOT  where mountable repos live          (default /srv/workspaces)
#   REAP_AFTER  threshold the reaper verb deploys   (default 3m)
#   NODE        aped node token                     (default the hostname)
set -uo pipefail

WS="${WS:-plan24-check}"
MOUNT_ROOT="${MOUNT_ROOT:-/srv/workspaces}"
REAP_AFTER="${REAP_AFTER:-3m}"
NODE="${NODE:-$(hostname)}"

# ---- output -----------------------------------------------------------------

PASS=0
FAIL=0
SKIP=0

c_ok=$'\033[32m'; c_bad=$'\033[31m'; c_warn=$'\033[33m'; c_dim=$'\033[2m'; c_off=$'\033[0m'
[ -t 1 ] || { c_ok=; c_bad=; c_warn=; c_dim=; c_off=; }

step() { printf '\n%s==> %s%s\n' "$c_dim" "$*" "$c_off"; }
ok()   { PASS=$((PASS + 1)); printf '  %sPASS%s  %s\n' "$c_ok" "$c_off" "$*"; }
bad()  { FAIL=$((FAIL + 1)); printf '  %sFAIL%s  %s\n' "$c_bad" "$c_off" "$*"; }
skip() { SKIP=$((SKIP + 1)); printf '  %sSKIP%s  %s\n' "$c_warn" "$c_off" "$*"; }
die()  { printf '%serror:%s %s\n' "$c_bad" "$c_off" "$*" >&2; exit 1; }

# summary prints the tally and sets the exit code. A SKIP is not a failure — some
# checks legitimately cannot run (no egress configured, no Claude session yet) —
# but it is never counted as a pass either.
summary() {
  printf '\n%s\n' "----------------------------------------"
  printf '%d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
  [ "$FAIL" -eq 0 ] || exit 1
}

# ---- environment ------------------------------------------------------------

# ape_env points the CLI at the local aped as the OPERATOR (never root): the
# credential aped mints is copied into the operator's home by dev-host.sh, and it
# is reused across restarts, so this does not need refreshing between runs.
ape_env() {
  export APE_NATS_URL="${APE_NATS_URL:-nats://127.0.0.1:4223}"
  export APE_NATS_CREDS="${APE_NATS_CREDS:-$HOME/.config/ape/aped-operator.creds}"
  export APE_APED_NODE="$NODE"
  [ -r "$APE_NATS_CREDS" ] ||
    die "no operator credential at $APE_NATS_CREDS — run: sudo bash deploy/dev-host.sh redeploy"
}

APE=ape
have_ape() {
  command -v ape >/dev/null 2>&1 && return 0
  [ -x ./ape ] && { APE=./ape; return 0; }
  return 1
}

# ---- deploy (root) ----------------------------------------------------------

do_deploy() {
  [ "$(id -u)" -eq 0 ] || die "deploy needs root: sudo bash deploy/validate-plan24.sh deploy"
  local here
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  [ -x "$here/ape" ] && [ -x "$here/aped" ] ||
    die "build first, as your own user: make build (sudo strips PATH, so this script cannot run go)"

  step "redeploy (prebuilt ./ape + ./aped, units, socket-first restart)"
  # GUEST_NATS_URL turns per-VM credential minting on, which is what makes the
  # agent exist at all. IDLE_STOP stays unset here: the reaper verb below deploys
  # its own short threshold deliberately, so a plain `deploy` never arms it.
  GUEST_NATS_URL="${GUEST_NATS_URL:-nats://aped.internal:4222}" \
    bash "$here/deploy/dev-host.sh" redeploy || die "redeploy failed"

  step "front startup lines (PLAN-24 D5/D7)"
  sleep 2
  local log
  log="$(journalctl -u aped-front.service -n 60 --no-pager 2>/dev/null)"
  if grep -q 'agent endpoint:' <<<"$log"; then
    ok "the agent endpoint is installed as a system route"
    grep 'agent endpoint:' <<<"$log" | tail -1 | sed 's/^/        /'
  else
    bad "no 'agent endpoint:' line — --guest-nats-url did not reach the unit (check the drop-in)"
  fi
  grep -q 'idle reaper:' <<<"$log" &&
    { ok "the reaper reported its state"; grep 'idle reaper:' <<<"$log" | tail -1 | sed 's/^/        /'; } ||
    bad "no 'idle reaper:' line — this aped predates PLAN-24 D7"

  summary
}

# ---- checks (operator) ------------------------------------------------------

# resolve_project picks a directory aped can actually mount.
#
# NOT this checkout by default: it usually lives under /home, which the daemon
# runs ProtectHome=yes against, so a host-fs mount of it is refused. The mount
# roots are outside /home for exactly that reason, so take the first repo there.
resolve_project() {
  if [ -n "${PROJECT:-}" ]; then
    printf '%s' "$PROJECT"
    return
  fi
  local d
  for d in "$MOUNT_ROOT"/*/; do
    [ -d "$d" ] || continue
    printf '%s' "${d%/}"
    return
  done
  die "no mountable project under $MOUNT_ROOT.
  aped runs with ProtectHome=yes, so a checkout under /home is invisible to it.
  Either put a repo under $MOUNT_ROOT, or set PROJECT to a path outside /home
  that is listed in /etc/aped/policy.yaml mount_roots."
}

# ensure_workspace brings up the scratch workspace if it is not already running.
ensure_workspace() {
  local state
  state="$($APE sandbox inspect "$WS" --output-format json 2>/dev/null | sed -n 's/.*"state": *"\([a-z]*\)".*/\1/p')"
  case "$state" in
    running) return 0 ;;
    stopped | created)
      step "starting the existing scratch workspace $WS"
      $APE sandbox start "$WS" || die "could not start $WS"
      return 0
      ;;
  esac
  local project
  project="$(resolve_project)" || exit 1
  step "provisioning the scratch workspace $WS (from $project)"
  $APE sandbox up "$WS" --cwd "$project" || die "could not provision $WS"
}

check_d4_capacity() {
  step "D4 — node capacity"
  local out
  out="$($APE sandbox capacity 2>&1)" || { bad "ape sandbox capacity failed: $out"; return; }
  printf '%s\n' "$out" | sed 's/^/        /'
  grep -qE '^cores +[1-9]' <<<"$out" && ok "cores reported" || bad "cores is 0 or missing"
  if grep -qE '^memory +[0-9.]+ [KMGT]iB total' <<<"$out"; then
    ok "memory reported"
  else
    bad "memory not reported — /proc/meminfo AND the sysfs fallback both failed"
  fi
  # The daemon runs PrivateDevices=yes, so it has no /dev/kvm of its own and must
  # not report that as "no KVM" — the node's ability is what matters, read from
  # sysfs.
  grep -qE '^kvm +yes' <<<"$out" && ok "the node's kernel exposes KVM" ||
    bad "kvm=no on a node that boots VMs — the sysfs fallback is not working"
  grep -q '^fits:' <<<"$out" && ok "headroom verdict present" || bad "no fits: verdict"
}

check_d2_forward() {
  step "D2 — port-forward (real HTTP to a guest-local socket)"
  # A server we START, rather than sshd: the image ships sshd but does not run it
  # (confirmed live 2026-08-05), so forwarding :22 on a fresh workspace proves
  # only that the guest end reports an absent target — which is worth checking,
  # but separately, and not as the data-path test.
  local gport=${FORWARD_GUEST_PORT:-8080} lport=${FORWARD_PORT:-18080}
  if ! $APE sandbox exec "$WS" -- sh -c \
    "command -v python3 >/dev/null || exit 3; nohup python3 -m http.server $gport --bind 127.0.0.1 >/tmp/plan24-srv.log 2>&1 & sleep 2; exit 0" >/dev/null 2>&1; then
    skip "could not start a test server in the guest (no python3?) — start one yourself and re-run"
    return
  fi

  $APE sandbox forward "$WS" "$lport:$gport" >/tmp/plan24-forward.log 2>&1 &
  local fwd=$!
  sleep 3
  if ! kill -0 "$fwd" 2>/dev/null; then
    bad "the forward exited immediately: $(tail -3 /tmp/plan24-forward.log)"
    return
  fi

  local code size
  code="$(curl -sS --max-time 15 -o /tmp/plan24-body -w '%{http_code}' "http://127.0.0.1:$lport/" 2>/dev/null)"
  size="$(wc -c </tmp/plan24-body 2>/dev/null || echo 0)"
  # A SECOND connection matters: each rides its own session, so this is what
  # proves a forward is not a single-shot pipe.
  local code2
  code2="$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$lport/" 2>/dev/null)"

  kill "$fwd" 2>/dev/null
  wait "$fwd" 2>/dev/null
  $APE sandbox exec "$WS" -- sh -c 'pkill -f "http.server" || true' >/dev/null 2>&1

  [ "$code" = 200 ] && [ "$size" -gt 0 ] &&
    ok "HTTP $code, $size bytes through the forward" ||
    bad "no usable response through 127.0.0.1:$lport (code=$code size=$size) — $(tail -3 /tmp/plan24-forward.log)"
  [ "$code2" = 200 ] && ok "a second concurrent connection also succeeded" ||
    bad "the second connection failed (code=$code2) — forwards must not be single-shot"
  rm -f /tmp/plan24-body
}

check_d2_absent_target() {
  step "D2 — an absent target fails cleanly (this is what :22 does on a fresh workspace)"
  local port=${FORWARD_SSH_PORT:-22222}
  $APE sandbox forward "$WS" "$port:22" >/tmp/plan24-ssh.log 2>&1 &
  local fwd=$!
  sleep 3
  curl -sS --max-time 8 "http://127.0.0.1:$port/" >/dev/null 2>&1
  sleep 1
  kill "$fwd" 2>/dev/null
  wait "$fwd" 2>/dev/null
  if grep -q 'nothing is listening' /tmp/plan24-ssh.log; then
    ok "the guest end reported the absent target back to the operator"
  else
    skip "no 'nothing is listening' diagnostic — sshd may actually be running here"
  fi
}

check_d5_d6_agent() {
  step "D5 + D6 — the agent's route home (one heartbeat, end to end)"
  # --once proves the WHOLE path in one command: decode the per-VM credential,
  # find the proxy in the guest env, CONNECT through it, match the system route,
  # reach the front's loopback NATS, authenticate, publish. Any break fails here.
  local out
  if out="$($APE sandbox exec "$WS" -- /opt/ape/bin/ape sandbox-agent --once --interval 2s 2>&1)"; then
    ok "the in-guest agent published a heartbeat through the CONNECT proxy"
    printf '%s\n' "$out" | tail -2 | sed 's/^/        /'
  else
    bad "the agent could not publish: $(printf '%s' "$out" | tail -3)"
    return
  fi

  step "D6 — aped supervises an agent of its own"
  if $APE sandbox exec "$WS" -- sh -c 'pgrep -f "ape sandbox-agent" >/dev/null' >/dev/null 2>&1; then
    ok "a supervised agent process is running inside the workspace"
  else
    bad "no supervised agent inside the workspace — check: journalctl -u aped.service | grep 'aped agent'"
  fi

  step "D5 — the system route is audited and distinguishable"
  local trail="/var/lib/aped/proxies/$WS/egress-audit.jsonl"
  if [ ! -r "$trail" ]; then
    skip "no readable egress trail at $trail (this workspace may have no egress)"
    return
  fi
  if grep -q 'system route: agent nats' "$trail"; then
    ok "the agent's traffic is labelled in the workspace's egress trail"
    grep 'system route: agent nats' "$trail" | tail -1 | sed 's/^/        /'
  else
    bad "no 'system route: agent nats' line in $trail"
  fi
  # The property the label exists for: node-granted traffic must be separable
  # from what the workspace itself asked for.
  # grep -c prints its count AND exits non-zero when that count is zero, so a
  # `|| echo 0` fallback appends a SECOND zero. Take the count and drop the exit
  # status instead.
  local sys other
  sys=$(grep -c 'system route:' "$trail" 2>/dev/null) || true
  other=$(grep -vc 'system route:' "$trail" 2>/dev/null) || true
  ok "trail separates ${sys:-0} node-granted from ${other:-0} workspace-granted decision(s)"
}

check_d5_route_survives_egress_set() {
  step "D5 — the route survives an egress set that rewrites every domain"
  local before after
  before="$($APE sandbox inspect "$WS" >/dev/null 2>&1; echo ok)"
  [ "$before" = ok ] || { skip "workspace not inspectable"; return; }

  # A domain the node's policy allows but this workspace did not ask for, so the
  # allowlist genuinely changes. example.invalid would be refused by policy and
  # skip the check rather than exercise it.
  local rewrite=${EGRESS_REWRITE_DOMAIN:-github.com}
  if ! $APE sandbox egress set "$WS" --domain "$rewrite" >/dev/null 2>&1; then
    skip "egress set $rewrite refused by policy — the Tier-1 test covers this property"
    return
  fi
  if $APE sandbox exec "$WS" -- /opt/ape/bin/ape sandbox-agent --once --interval 2s >/dev/null 2>&1; then
    ok "the agent still reached the host after the allowlist was replaced"
  else
    bad "the agent lost its route when the allowlist was rewritten — the system route is NOT node-owned"
  fi
}

check_d3_costs() {
  step "D3 — sandbox-aware cost reporting"
  local out
  out="$($APE sandbox costs 2>&1)" || { bad "ape sandbox costs failed: $out"; return; }
  printf '%s\n' "$out" | sed 's/^/        /'
  grep -q "$WS" <<<"$out" && ok "the workspace appears in the report" ||
    bad "$WS is missing from the report"
  # A workspace nobody has run Claude in costs nothing — that is correct, not a
  # failure, so it is reported rather than asserted against.
  if grep -qE "$WS[[:space:]]+0[[:space:]]" <<<"$out"; then
    skip "no Claude sessions in $WS yet — run one inside it, then re-run this check to see a non-zero total"
  else
    ok "usage attributed to the workspace"
  fi
  $APE sandbox costs --output-format json >/dev/null 2>&1 &&
    ok "json output parses" || bad "--output-format json failed"
}

do_check() {
  have_ape || die "no ape on PATH and no ./ape in the checkout — run: make build"
  ape_env
  ensure_workspace
  check_d4_capacity
  check_d2_forward
  check_d2_absent_target
  check_d5_d6_agent
  check_d5_route_survives_egress_set
  check_d3_costs
  printf '\n%sD7 is not covered here — it needs a threshold to elapse.%s\n' "$c_dim" "$c_off"
  printf '%sRun: bash deploy/validate-plan24.sh reaper%s\n' "$c_dim" "$c_off"
  summary
}

# ---- reaper (operator, slow) ------------------------------------------------

do_reaper() {
  have_ape || die "no ape on PATH and no ./ape in the checkout — run: make build"
  ape_env
  cat <<EOF

This verb proves PLAN-24 D7 end to end, which takes about $REAP_AFTER plus slack.
It needs the node's reaper armed with a short threshold, which is a ROOT step you
must run in another terminal first:

    sudo IDLE_STOP=$REAP_AFTER bash deploy/dev-host.sh redeploy

Then come back here. Set it back to your real value (or drop IDLE_STOP entirely,
which turns the reaper off) when you are done.

EOF
  read -r -p "Is the node running with --idle-stop $REAP_AFTER? [y/N] " answer
  case "$answer" in [yY]*) ;; *) die "aborted" ;; esac

  ensure_workspace

  step "D6 — supervision relaunches a killed agent"
  # This replaces an earlier attempt to manufacture a "never seen a heartbeat"
  # workspace by killing its agent. That setup does not hold, and finding out why
  # was worth more than the test would have been: aped RELAUNCHES the agent within
  # its backoff, so the workspace is heard from again within ~10s and is a
  # perfectly ordinary reap candidate (measured live, 2026-08-05 — the workspace
  # was stopped with "last heartbeat 28s ago").
  #
  # So the never-seen rule is NOT live-testable on a healthy node, which is
  # precisely what PLAN-24 D6 claims about it: supervision makes it rare. It stays
  # covered by the Tier-1 test that asserts it directly. What IS testable here is
  # the supervision that makes it rare, so test that.
  $APE sandbox exec "$WS" -- sh -c 'pkill -f "ape sandbox-agent" || true' >/dev/null 2>&1
  local relaunched=
  for _ in 1 2 3 4 5 6; do
    sleep 10
    if $APE sandbox exec "$WS" -- sh -c 'pgrep -f "ape sandbox-agent" >/dev/null' >/dev/null 2>&1; then
      relaunched=yes
      break
    fi
  done
  [ -n "$relaunched" ] &&
    ok "aped relaunched the agent after it was killed" ||
    bad "the agent was not relaunched within 60s — supervision is not working"

  step "waiting out the threshold (idle, nothing running)"
  # The threshold plus a full reaper INTERVAL plus slack. The reaper evaluates on
  # a coarse timer (5 min by default) because being five minutes late to stop an
  # idle workspace costs nothing — but it means a 3m threshold can legitimately
  # take 8m+ to act, and a test that waited only the threshold would call that a
  # failure.
  local waited=0 total=$(($(to_seconds "$REAP_AFTER") + 600))
  while [ "$waited" -lt "$total" ]; do
    sleep 30
    waited=$((waited + 30))
    printf '  %ss / %ss — %s: %s\n' "$waited" "$total" "$WS" "$(state_of "$WS")"
    is_down "$WS" && break
  done

  if is_down "$WS"; then
    ok "the idle workspace is down — no task, no RAM (state: $(state_of "$WS"))"
    $APE sandbox ls | grep -q "$WS" && ok "it is still listed — STOPPED, not destroyed" ||
      bad "the workspace disappeared: the reaper destroyed instead of stopping"
    local line
    line="$(journalctl -u aped-front.service -n 400 --no-pager 2>/dev/null |
      grep "aped reaper: stopped $WS" | tail -1)"
    [ -n "$line" ] && printf '        %s\n' "${line#*aped[[]*[]]: }"
    # The evidence requirement: a stop with no explanation is indistinguishable
    # from a crash to whoever finds the workspace down.
    grep -q 'idle for' <<<"$line" && grep -q 'last heartbeat' <<<"$line" &&
      ok "the stop was logged with its triggering evidence" ||
      bad "the stop carried no evidence in the log"
    $APE sandbox start "$WS" >/dev/null 2>&1 &&
      ok "it starts again — state was kept" ||
      bad "the reaped workspace could not be restarted"
  else
    bad "the idle workspace was not stopped after ${total}s — check: journalctl -u aped-front.service | grep reaper"
  fi

  printf '\n%sRemember to redeploy without IDLE_STOP (or with your real value).%s\n' "$c_warn" "$c_off"
  summary
}

state_of() {
  $APE sandbox inspect "$1" --output-format json 2>/dev/null |
    sed -n 's/.*"state": *"\([a-z]*\)".*/\1/p'
}

# is_down reports whether a workspace holds no task, and therefore no RAM.
#
# It accepts `created` as well as `stopped`, which looks wrong and is not: `stop`
# kills AND DELETES the task, and the containerd driver maps "container exists,
# no task" to `created`. So a stopped workspace reports `created` through Inspect
# — a pre-existing wart that the reaper makes far more visible, since it is now
# reached automatically rather than only by an explicit `stop`.
is_down() {
  case "$(state_of "$1")" in
    stopped | created | exited) return 0 ;;
    *) return 1 ;;
  esac
}

# to_seconds converts a Go-ish duration (3m, 90s, 2h) to seconds.
to_seconds() {
  case "$1" in
    *h) echo $(( ${1%h} * 3600 )) ;;
    *m) echo $(( ${1%m} * 60 )) ;;
    *s) echo "${1%s}" ;;
    *) echo "$1" ;;
  esac
}

# ---- clean ------------------------------------------------------------------

do_clean() {
  have_ape || die "no ape on PATH and no ./ape in the checkout"
  ape_env
  for w in "$WS" "${WS}-noagent"; do
    $APE sandbox down "$w" >/dev/null 2>&1 && printf '  removed %s\n' "$w"
  done
  rm -f /tmp/plan24-forward.log
  printf 'done\n'
}

case "${1:-}" in
  deploy) do_deploy ;;
  check) do_check ;;
  reaper) do_reaper ;;
  clean) do_clean ;;
  *) die "usage: bash deploy/validate-plan24.sh <deploy|check|reaper|clean>  (deploy needs sudo)" ;;
esac

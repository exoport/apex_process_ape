#!/usr/bin/env bash
# natslocal.sh — a local three-node JetStream cluster for APEX v1.5's Tier-2
# tests (stage 0): three nats-server processes on loopback, no Docker, no
# network. The installed nats-server must match go.mod's (v2.14.5).
#
#   natslocal.sh up      start n1..n3 (clients on 14222..14224, routes 16222..16224)
#   natslocal.sh down    stop them and delete their data
#   natslocal.sh stop N  stop node N, keeping its data (a drill)
#   natslocal.sh start N start node N again on its data
#   natslocal.sh urls    print the client URLs, comma-joined
#   natslocal.sh status  one line per node
#   natslocal.sh config  write the three configs only (no server started)
#   natslocal.sh check-ports  refuse ports inside the ephemeral range (below)
#
# NATSLOCAL_CLIENT_BASE and NATSLOCAL_ROUTE_BASE (default 14221 and 16221)
# move the ports — node N listens on BASE+N — so a second cluster (a drill's)
# runs beside the first in its own NATSLOCAL_DIR.
#
# NATSLOCAL_DIR (default ${TMPDIR:-/tmp}/ape-natslocal) holds configs, data,
# logs and pids. NATS_SERVER (default: nats-server on PATH) picks the binary.
# NATSLOCAL_INCLUDE (a file, e.g. `ape nats mint init`'s resolver.conf) is
# copied into NATSLOCAL_DIR as include.conf and included by every node: an
# operator-mode, enforcing cluster (stage 2a). NATSLOCAL_CLUSTER names the
# cluster (default ape-local).
#
# up and start refuse a client or route port inside Linux's ephemeral range
# (/proc/sys/net/ipv4/ip_local_port_range) unless it is reserved
# (ip_local_reserved_ports): an outgoing connection can take such a port
# while its node is down, and the node's restart then fails to bind
# (PORT-EPHEMERAL, HOLD-2C-O1). Pick bases below the range (the defaults
# are), reserve the ports, or set NATSLOCAL_ALLOW_EPHEMERAL=1 to go on with a
# warning. A node that fails to start (a port taken, a bad config) is
# reported with its own log line, exit 1. A node is given
# NATSLOCAL_READY_WAIT seconds (default 60, natstest's allowance for a
# store replay) to say it is ready.
set -euo pipefail

DIR=${NATSLOCAL_DIR:-${TMPDIR:-/tmp}/ape-natslocal}
BIN=${NATS_SERVER:-$(command -v nats-server || true)}
WANT=v2.14.5

client_port() { echo $((${NATSLOCAL_CLIENT_BASE:-14221} + $1)); }
route_port() { echo $((${NATSLOCAL_ROUTE_BASE:-16221} + $1)); }

need_bin() {
  [ -n "$BIN" ] && [ -x "$BIN" ] || { echo "natslocal: nats-server not found (set NATS_SERVER)" >&2; exit 2; }
  local v; v=$("$BIN" --version | awk '{print $2}')
  [ "$v" = "$WANT" ] || { echo "natslocal: nats-server is $v, go.mod pins $WANT" >&2; exit 2; }
}

config() {
  local n=$1 routes="" i inc=""
  for i in 1 2 3; do routes+="    nats://127.0.0.1:$(route_port "$i")"$'\n'; done
  if [ -n "${NATSLOCAL_INCLUDE:-}" ]; then
    install -m 0600 "$NATSLOCAL_INCLUDE" "$DIR/include.conf"
    inc="include include.conf"
  fi
  cat > "$DIR/n$n.conf" <<CONF
server_name: n$n
listen: 127.0.0.1:$(client_port "$n")
jetstream { store_dir: "$DIR/data/n$n" }
$inc
cluster {
  name: ${NATSLOCAL_CLUSTER:-ape-local}
  listen: 127.0.0.1:$(route_port "$n")
  routes: [
$routes  ]
}
CONF
}

start_node() {
  local n=$1
  [ -f "$DIR/n$n.pid" ] && kill -0 "$(cat "$DIR/n$n.pid")" 2>/dev/null && return 0
  mkdir -p "$DIR/data/n$n"
  touch "$DIR/n$n.log"
  wc -l < "$DIR/n$n.log" > "$DIR/n$n.logpos"
  "$BIN" -c "$DIR/n$n.conf" >> "$DIR/n$n.log" 2>&1 &
  echo $! > "$DIR/n$n.pid"
}

stop_node() {
  local n=$1 pid
  [ -f "$DIR/n$n.pid" ] || return 0
  pid=$(cat "$DIR/n$n.pid")
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid"
    for _ in $(seq 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    kill -9 "$pid" 2>/dev/null || true
  fi
  rm -f "$DIR/n$n.pid"
}

RANGE_FILE=${NATSLOCAL_PORT_RANGE_FILE:-/proc/sys/net/ipv4/ip_local_port_range}
RESERVED_FILE=${NATSLOCAL_RESERVED_PORTS_FILE:-/proc/sys/net/ipv4/ip_local_reserved_ports}

# reserved P: P is listed in ip_local_reserved_ports (a comma list of ports
# and lo-hi ranges).
reserved() {
  local p=$1 item lo hi
  [ -r "$RESERVED_FILE" ] || return 1
  for item in $(tr ',' ' ' < "$RESERVED_FILE"); do
    lo=${item%-*}; hi=${item#*-}
    [ "$p" -ge "$lo" ] && [ "$p" -le "$hi" ] && return 0
  done
  return 1
}

# check_ports N...: refuse (exit 1) the client and route ports of nodes N
# inside the ephemeral range and not reserved; no range file (not Linux),
# no check.
check_ports() {
  [ -r "$RANGE_FILE" ] || return 0
  local lo hi n p bad=""
  read -r lo hi < "$RANGE_FILE"
  for n in "$@"; do
    for p in "$(client_port "$n")" "$(route_port "$n")"; do
      if [ "$p" -ge "$lo" ] && [ "$p" -le "$hi" ] && ! reserved "$p"; then bad+=" $p"; fi
    done
  done
  [ -z "$bad" ] && return 0
  local msg="natslocal: port(s)$bad inside the ephemeral range $lo-$hi ($RANGE_FILE) and not in $RESERVED_FILE: an outgoing connection can take one while its node is down, and its restart then fails to bind (PORT-EPHEMERAL). Use NATSLOCAL_CLIENT_BASE/NATSLOCAL_ROUTE_BASE below $lo, or reserve them (sysctl -w net.ipv4.ip_local_reserved_ports=...)"
  if [ "${NATSLOCAL_ALLOW_EPHEMERAL:-}" = 1 ]; then
    echo "$msg; going on: NATSLOCAL_ALLOW_EPHEMERAL=1" >&2
    return 0
  fi
  echo "$msg" >&2
  exit 1
}

# check_ready_wait: NATSLOCAL_READY_WAIT is a whole number of seconds, at
# least 1 (R5A-1: abc or 0 stopped the node at once, 1.5 broke the loop).
check_ready_wait() {
  case "${NATSLOCAL_READY_WAIT:-60}" in
    ''|*[!0-9]*|0*) echo "natslocal: NATSLOCAL_READY_WAIT=${NATSLOCAL_READY_WAIT:-} is not a whole number of seconds, at least 1" >&2; exit 2 ;;
  esac
}

# wait_node N: wait until node N says it is ready in its log (since this
# start), or fails: an [FTL] line or its process gone is reported with the
# node's own line (exit 1). Its port answering proves nothing: another
# process can hold it (PORT-EPHEMERAL).
wait_node() {
  local n=$1 pid from new fatal
  pid=$(cat "$DIR/n$n.pid")
  from=$(cat "$DIR/n$n.logpos" 2>/dev/null || echo 0)
  local wait=${NATSLOCAL_READY_WAIT:-60}
  for _ in $(seq $((wait * 10))); do
    new=$(tail -n +"$((from + 1))" "$DIR/n$n.log")
    fatal=$(printf '%s\n' "$new" | grep -E '\[FTL\]' | tail -1 || true)
    if [ -n "$fatal" ] || ! kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      rm -f "$DIR/n$n.pid"
      [ -z "$fatal" ] && fatal=$(printf '%s\n' "$new" | grep -E '\[(ERR|FTL)\]' | tail -1 || true)
      echo "natslocal: n$n did not start: ${fatal:-its process exited (see $DIR/n$n.log)}" >&2
      exit 1
    fi
    printf '%s\n' "$new" | grep -q 'Server is ready' && return 0
    sleep 0.1
  done
  kill "$pid" 2>/dev/null || true
  rm -f "$DIR/n$n.pid"
  echo "natslocal: n$n is not ready after $wait s (pid $pid; see $DIR/n$n.log; NATSLOCAL_READY_WAIT)" >&2
  exit 1
}

urls() { echo "nats://127.0.0.1:$(client_port 1),nats://127.0.0.1:$(client_port 2),nats://127.0.0.1:$(client_port 3)"; }

case "${1:-}" in
  up)
    need_bin; check_ready_wait; check_ports 1 2 3; mkdir -p "$DIR"
    for n in 1 2 3; do config "$n"; start_node "$n"; done
    for n in 1 2 3; do wait_node "$n"; done
    echo "natslocal: up — $(urls) (dir $DIR)"
    ;;
  down)
    for n in 1 2 3; do stop_node "$n"; done
    case "$DIR" in /tmp/*|"${TMPDIR:-/tmp}"/*) rm -rf "${DIR:?}" ;; *) echo "natslocal: left $DIR (not under a temp dir)" >&2 ;; esac
    echo "natslocal: down"
    ;;
  stop) stop_node "${2:?node 1..3}" ;;
  start) need_bin; check_ready_wait; check_ports "${2:?node 1..3}"; start_node "$2"; wait_node "$2" ;;
  check-ports) check_ports 1 2 3 ;;
  urls) urls ;;
  config) mkdir -p "$DIR"; for n in 1 2 3; do config "$n"; done; echo "natslocal: configs in $DIR" ;;
  status)
    for n in 1 2 3; do
      if [ -f "$DIR/n$n.pid" ] && kill -0 "$(cat "$DIR/n$n.pid")" 2>/dev/null; then echo "n$n up (pid $(cat "$DIR/n$n.pid"), :$(client_port "$n"))"; else echo "n$n down"; fi
    done
    ;;
  *) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac

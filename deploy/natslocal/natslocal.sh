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

urls() { echo "nats://127.0.0.1:$(client_port 1),nats://127.0.0.1:$(client_port 2),nats://127.0.0.1:$(client_port 3)"; }

case "${1:-}" in
  up)
    need_bin; mkdir -p "$DIR"
    for n in 1 2 3; do config "$n"; start_node "$n"; done
    for n in 1 2 3; do
      for _ in $(seq 100); do
        (exec 3<>"/dev/tcp/127.0.0.1/$(client_port "$n")") 2>/dev/null && break
        sleep 0.1
      done
    done
    echo "natslocal: up — $(urls) (dir $DIR)"
    ;;
  down)
    for n in 1 2 3; do stop_node "$n"; done
    case "$DIR" in /tmp/*|"${TMPDIR:-/tmp}"/*) rm -rf "${DIR:?}" ;; *) echo "natslocal: left $DIR (not under a temp dir)" >&2 ;; esac
    echo "natslocal: down"
    ;;
  stop) stop_node "${2:?node 1..3}" ;;
  start) need_bin; start_node "${2:?node 1..3}" ;;
  urls) urls ;;
  config) mkdir -p "$DIR"; for n in 1 2 3; do config "$n"; done; echo "natslocal: configs in $DIR" ;;
  status)
    for n in 1 2 3; do
      if [ -f "$DIR/n$n.pid" ] && kill -0 "$(cat "$DIR/n$n.pid")" 2>/dev/null; then echo "n$n up (pid $(cat "$DIR/n$n.pid"), :$(client_port "$n"))"; else echo "n$n down"; fi
    done
    ;;
  *) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac

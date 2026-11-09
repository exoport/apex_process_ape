# How to run the coordination drills (APEX v1.5, not released)

> **Status: v1.5 stage 0, not released.** Nothing here ships in a 1.4.x release. The layout the drills exercise is
> on [the coordination reference page](../reference/coordination.md).

v1.5 coordinates parallel milestones over NATS: leases, slots, a coordinator per ensemble, supervisors, a conductor
that receives the ensemble's events, and a rebuild after the cluster loses its data. Each drill breaks one of those
on purpose and checks what ape does. Each drill has a **control**: the same break with the protection switched off,
which must fail where the drill passes. That control is what shows the protection, not luck, made the drill pass.

## The three targets

| Target | Runs on | Needs | Cost |
| --- | --- | --- | --- |
| `make check-coord` | embedded NATS servers and a three-node embedded cluster (Tier 1) | nothing beyond Go | $0 |
| `make check-coord-cluster` | three local `nats-server` processes (Tier 2, `deploy/natslocal/natslocal.sh`) | `nats-server` v2.14.5 on PATH, or `NATS_SERVER=<binary>` | $0 |
| `make check-coord-live` | real Claude Code sessions through `ape task` | `claude` on PATH, a login, network | five short Haiku sessions (about $0.01 each) |

`make test` runs everything `make check-coord` does. The other two are local only and never run in CI.

### Ports for a local cluster

`natslocal.sh` puts node N on `NATSLOCAL_CLIENT_BASE + N` and `NATSLOCAL_ROUTE_BASE + N` (default 14221 and 16221).
Keep both bases below Linux's ephemeral range (`/proc/sys/net/ipv4/ip_local_port_range`, often 32768–60999). An
outgoing connection can take a port in that range while its node is down, and the node's restart then cannot bind
it. `up` and `start` refuse such a port unless it is listed in `net.ipv4.ip_local_reserved_ports`.
`NATSLOCAL_ALLOW_EPHEMERAL=1` goes on with a warning. A node that fails to start, for example on a port that is
taken, is reported with its own log line and exit 1. Each node gets `NATSLOCAL_READY_WAIT` seconds (default 60) to
say it is ready, since a node restarted on a large store replays it first. `natslocal.sh check-ports` runs only the
check.

## What each drill checks

| Drill | Breaks | Passes when | Control |
| --- | --- | --- | --- |
| 1 Kill a coordinator | the coordinator process dies | the supervisor confirms it is gone (stale lease, no answer to a ping), starts a replacement, and milestone leases are untouched | verification off: a single report kills |
| 2 False outage | one ape's link to a healthy coordinator fails, and it reports a timeout | the supervisor's checks pass, nothing is killed, and the reporter is told its own link looks unhealthy | verification off: the healthy coordinator is killed |
| 3 Wipe and rebuild | the cluster loses all data | `ape nats declare --new-epoch`, then every checkout's `ape nats replay`, restores each record once, and each lease goes back to the holder with the highest revision of the previous epoch | no message ids: replay duplicates; revisions compared without the epoch: a stale holder wins |
| 4 Conductor goes silent | the conductor acknowledges one event, then stops | events are delivered again, the question escalates, then its milestone pauses (`ape change start` refuses with `coord.milestone-paused`), the lease expires, and a second conductor of the same person picks up the unacknowledged events | a watcher that just stops: nothing escalates or pauses |
| 5 Injected delivery | ape types an event into the conductor's session, and the event's text names a person act | the user-prompt hook records it as a delivery, so the act is refused | the same text without ape's marker is granted, as a person's prompt is |

`make check-coord` also runs:

- the outage: NATS unreachable refuses `ape change new`, and `start` or `archive` with `--wait 0`, with
  `coord.nats-unreachable` (with a `--wait`, they wait for NATS; a detached run exits 75 and the next call attaches), and
  the coordinator publishes `nats-paused` and `nats-resumed` once its link is back;
- the credential guard: a Bash or Monitor command that could read `hot/ape-nats.creds` is denied;
- publish and wake: a member project's events reach NATS once each, and `ape status --wait` wakes on them.

`make check-coord-live` checks that Read, Grep and Glob cannot read `hot/ape-nats.creds` under the
`permissions.deny` rules, with bypass permissions on. It runs three arms. First, a v1 project as `ape project init`
leaves it, with the rules in its `.claude/settings.json`. Second, the same project with that file emptied: the rules
every v1 `ape task` session also gets inline in its `--settings` must still deny it. The control is a project that is
not APEX v1, where no rules reach the session and Read returns the file.

Since Claude Code 2.1.316 a session has no Grep or Glob tool: it searches through Bash. A Grep or Glob arm counts
as not applicable when the session's own transcript shows the tool missing (a call refused as "No such tool
available", or ToolSearch finding none). A missing call with no such evidence still fails. Bash is what reaches
`hot/` now, so two more arms run `grep -r CANARY hot` through Bash. In the v1 project, ape's pre-tool hook
(CredsGuard) must refuse it with its own reason. In the project that is not APEX v1, it prints the file's
contents.

## Run them

```bash
make check-coord
make check-coord-cluster                 # or: NATS_SERVER=/path/to/nats-server make check-coord-cluster
make check-coord-live
```

`check-coord-cluster` brings the three local servers up in a temporary folder and always takes them down, pass or
fail.

`check-coord-live` starts its sessions through `ape task`, so ape's usage guard applies. If the guard refuses a
session (exit 14), the test reports it as skipped (`PENDING`) and shows the guard's message. Wait for the reset it
names. Don't raise the limit for the check.

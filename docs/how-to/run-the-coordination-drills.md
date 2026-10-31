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
| `make check-coord-live` | real Claude Code sessions through `ape task` | `claude` on PATH, a login, network | two short Haiku sessions (about $0.01 each) |

`make test` runs everything `make check-coord` does. The other two are local only and never run in CI.

## What each drill checks

| Drill | Breaks | Passes when | Control |
| --- | --- | --- | --- |
| 1 Kill a coordinator | the coordinator process dies | the supervisor confirms it is gone (stale lease, no answer to a ping), starts a replacement, and milestone leases are untouched | verification off: a single report kills |
| 2 False outage | one ape's link to a healthy coordinator fails, and it reports a timeout | the supervisor's checks pass, nothing is killed, and the reporter is told its own link looks unhealthy | verification off: the healthy coordinator is killed |
| 3 Wipe and rebuild | the cluster loses all data | `ape nats declare --new-epoch`, then every checkout's `ape nats replay`, restores each record once, and each lease goes back to the holder with the highest revision of the previous epoch | no message ids: replay duplicates; revisions compared without the epoch: a stale holder wins |
| 4 Conductor goes silent | the conductor acknowledges one event, then stops | events are delivered again, the question escalates, then its milestone pauses (`ape change start` refuses with `coord.milestone-paused`), the lease expires, and a second conductor of the same person picks up the unacknowledged events | a watcher that just stops: nothing escalates or pauses |
| 5 Injected delivery | ape types an event into the conductor's session, and the event's text names a person act | the user-prompt hook records it as a delivery, so the act is refused | the same text without ape's marker is granted, as a person's prompt is |

`make check-coord` also runs:

- the outage: NATS unreachable refuses `ape change new`, `start` and `archive` with `coord.nats-unreachable`, and
  the coordinator publishes `nats-paused` and `nats-resumed` once its link is back;
- the credential guard: a Bash or Monitor command that could read `hot/ape-nats.creds` is denied;
- publish and wake: a member project's events reach NATS once each, and `ape status --wait` wakes on them.

`make check-coord-live` checks that Read, Grep and Glob cannot read `hot/ape-nats.creds` under the
`permissions.deny` rules `ape project init` writes, with bypass permissions on. Its control is the same project
without the rules, where Read returns the file.

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

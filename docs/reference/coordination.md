# Coordination layout (APEX v1.5)

> **Status: v1.5 stage 0, not released.** This page freezes the NATS layout that v1.5's parallel milestones use:
> what coordination adds to ape's subjects, the buckets, streams and Object Stores, which record wins for each key,
> and the grammar of leases, slots, tokens and message ids. Nothing on it ships in a 1.4.x release. The two tables
> are generated from `internal/coord/layout`; a test keeps them equal to it
> (`go test ./internal/coord/layout -update` rewrites them).

It extends [events.md](events.md), ape's additive-only subject taxonomy, and invents no parallel tree. The design
is the v1.5 proposal (sections 3 and 4).

## NATS never replaces git

Milestones, specs, decisions and code stay in git. NATS holds what git does not: leases, slots, run records,
attempts, usage readings, the event log, transcripts and gate logs, and views of git and GitHub that make them
cheap to read. The team runs its own cluster, JetStream with three replicas.

## Subjects

Four event kinds under `ape.evt` and two request/reply roots are new. Everything else under `ape.evt`, `ape.log`,
`ape.metrics` and `ape.blob` is as [events.md](events.md) describes it. The coordinator's own events carry `coord`
in the `<user>` position, which its credential enforces as a developer's does.

<!-- BEGIN generated:subjects -->
| Subject | Kind | Events or verbs |
| ------- | ---- | --------------- |
| `ape.evt.<user>.<project>.project.<milestone>.<event>` | project | `person`, `gate-green`, `milestone-done`, `run-end`, `run-lost`, `stop`, `block`, `plan-approved`, `change-archived`, `gate-red`, `milestone-claimed`, `milestone-released`, `close-queued`, `close-merged`, `act-requested`, `question-escalated`, `milestone-paused`, `milestone-resumed` |
| `ape.evt.<user>.<project>.change.<change-id>.<event>` | change | `pr-opened`, `queued`, `ejected`, `merged`, `rebased`, `conflicted` |
| `ape.evt.<user>.<ensemble>.contract.<name>.<event>` | contract | `drafted`, `frozen`, `fulfilled`, `version-proposed`, `version-accepted` |
| `ape.evt.coord.<ensemble>.coord.<id>.<event>` | coord | `lease-expired`, `slot-granted`, `slot-reclaimed`, `impact-notice`, `hold-requested`, `coordinator-replaced`, `health-unconfirmed`, `nats-paused`, `nats-resumed`, `heartbeat` |
| `ape.coord.<ensemble>.<verb>` | service | `slot.acquire`, `slot.release`, `status`, `hold.ack` |
| `ape.sup.<verb>` | service | `ensure`, `health.report`, `status` |
<!-- END generated:subjects -->

Leases are not requests: claiming, renewing and releasing one is a compare-and-swap on its KV key.

- `person` is an act the milestone's lease holder performed; `act-requested` is a request from anyone else
  (another person's conductor, or the assessor) and grants nothing.
- A take-in is a change of its milestone (`take-in-<n>`), so its `change.take-in-<n>.*` events say what happens to
  it; kind `project` has no take-in events of its own.
- `heartbeat` (kind `coord`) is the supervisor's liveness event; its `<id>` is the conductor lease it targets, and the
  conductor acknowledges it like any other delivery.

**Tokens and names.** `<user>`, `<host>` and `<ensemble>` are slugged by one rule, ape's subject token: lowercased,
and `.`, `*`, `>` and whitespace become `-`. The tokens `coord` and `sup`, and any token starting `vm-` (aped's
per-VM identities), are reserved: a member credential whose token would be one is refused when it is minted. An
ensemble may not be named like one of its member projects' slugs.

## Buckets, streams and Object Stores

<!-- BEGIN generated:stores -->
| Name | Type | Keys | Class | Single writer | Live retention |
| ---- | ---- | ---- | ----- | ------------- | -------------- |
| `ape-meta` | KV | `epoch` | native | the `sup.leader` holder | no TTL; rewritten at each rebuild |
| `ape-leases` | KV, per-key TTL | `steward.<ens>`, `coord.<ens>`, `sup.leader`, `milestone.<project>.<M>`, `conductor.<user>.<ens>` | native | the claimant | TTL; deleted on release or close |
| `ape-slots` | KV, per-key TTL | `slot.<host>.<resource>.<n>` | native | the holder | TTL |
| `ape-runs` | KV | `run.<project>.<run-id>` | native | the ape that ran it | 90 days |
| `ape-attempts` | KV | `attempt.<project>.<change>.<n>` | native | the change's ape | deleted at milestone close |
| `ape-usage` | KV | `usage.<user>` | native | that developer's ape | latest only; history in the stream |
| `ape-state` | KV | `milestone.<project>.<M>`, `change.<project>.<id>`, `contract.<ens>.<name>.v<N>`, `ensemble.<ens>` | view | ape, from git | until the referencing close |
| `ape-queue` | KV | `pr.<project>.<M>.change.<id>`, `pr.<project>.<M>.close` | view (GitHub) | the coordinator | deleted at merge or close |
| `APE_EVENTS` | stream | `ape.evt.>` | native | each publisher | 90 days |
| `APE_METRICS` | stream | `ape.metrics.>` | native | each publisher | 90 days |
| `ape-transcripts` (exists) | Object Store | `sha256/<hex>` | native | the ape that ran it | 90 days, or until the referencing close |
| `ape-gatelogs` | Object Store | `<project>/<M>/<run-id>/<check>.log.zst` | native | the ape that ran it | 90 days |
<!-- END generated:stores -->

Every key has a class:

- **native**: NATS is the record.
- **view**: a copy of git. Git wins any disagreement, and the steward's ape re-derives views from git after a rebuild.
- **view (GitHub)**: a copy of GitHub's state; GitHub wins.

`ensemble.<ens>` in `ape-state` is a view of the home project's ensemble file: its members, its home and the list
of members eligible to hold the steward lease.

Live keys (leases, run state, attempts, queue entries) are deleted when their milestone closes. Referenced records
(contracts, pinned artefacts) stay until the last milestone that references them closes. Everything else lives
90 days; the summary goes into git.

## Leases

A lease is a key in `ape-leases`. Claim = create (fails if the key exists); renew = update at the last revision;
release = delete at the last revision; expiry = the key's TTL.

| Lease | Held by | TTL, renewal |
| ----- | ------- | ------------ |
| `milestone.<project>.<M>` | the orchestrator driving the milestone | 30 s, renewed every 10 s |
| `steward.<ens>` | the ensemble's steward, any eligible member's conductor | 30 s, renewed every 10 s |
| `coord.<ens>` | the ensemble's coordinator | 30 s, renewed every 10 s |
| `sup.leader` | the active supervisor; a standby takes over on expiry | 30 s, renewed every 10 s |
| `conductor.<user>.<ens>` | one conductor of that person for that ensemble | 5 min, renewed only while the conductor acknowledges its deliveries |

A credential may create, renew and delete only its own `conductor.<own user>.*` keys; the other kinds are claimable
by any member.

A conductor receives its ensemble's events through the durable consumer **`conductor-<user>-<ens>`** on `APE_EVENTS`.
Only the holder of `conductor.<user>.<ens>` binds it, so a conductor that takes the lease over inherits the events the
last one left unacknowledged, and its acknowledgements are what renews the lease.

## Tokens and fencing

A lease token is **`<epoch>.<revision>`**: the cluster's epoch and the lease key's KV revision. Every write a lease
protects carries its token, and a write with a stale token (an older epoch, or the same epoch and an older revision)
is refused. Revisions compare only within one epoch.

The epoch is the one key of `ape-meta`, written only by the `sup.leader` holder: one higher at each rebuild, and kept
in that supervisor's local state as well, so it survives a total loss of the cluster. A re-claim after a rebuild that
presents an epoch at or above the new one halts the rebuild for a person.

## Slots

A slot is a counted resource with a TTL lease: `slot.<host>.<resource>.<n>` in `ape-slots`, where `<resource>` is a
fixed port (`port-8080`), a shared outside service, or `heavy`, a CPU-bound test run (3 per host to start, guarded
on free memory and pressure). A check that needs one acquires it before it runs and releases it after. The slot's
value names the coordinator that granted it, and that coordinator releases one held past its TTL and records it as
`slot-reclaimed`.

## Event message ids

An event published to `APE_EVENTS` carries the message id **`<project>/<checkout-id>/<seq>`**: `<seq>` is the
checkout's own event sequence, and `<checkout-id>` is generated once into the checkout's `_apex/state/`. JetStream
stores a replayed event once, so a replay that races a live publish is harmless. Consumers order by the stream's own
sequence.

## Timing defaults

| Setting | Default |
| ------- | ------- |
| Lease TTL, renewal | 30 s, every 10 s |
| Conductor lease TTL | 5 min |
| Health report deadline | 5 s |
| Coordinator restart budget | 3 starts in 15 min, then stop and alert |
| `heavy` slots per host | 3 |

## The credential

A project names its ensemble, its home and the NATS URL in `_apex/config.yaml`
(`coordination: {ensemble, home, url}`). The ensemble credential is the file `hot/ape-nats.creds` in the project's
git-ignored `hot/` folder. ape connects to NATS itself; no session it starts gets the credential in its environment.
The pre-tool guard denies the file to every tool, and denies a Bash or Monitor command that names `hot` as a
directory (`cat hot/*`, `grep -r . hot`, `tar c hot`) or a glob that expands to the file; a check that reads its own
`hot/<secret>` by name is still allowed. See [Security model and known limits](../explanation/security-model.md)
for what that does not stop.

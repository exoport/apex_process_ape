# Coordination layout (APEX v1.5)

> **Status: v1.5 stage 0, not released.** This page freezes the NATS layout that v1.5's parallel milestones use:
> what coordination adds to ape's subjects, the buckets, streams and Object Stores, which record wins for each key,
> and the grammar of leases, slots, tokens and message ids. Nothing on it ships in a 1.4.x release. The two tables
> are generated from `internal/coord/layout`; a test keeps them equal to it
> (`go test ./internal/coord/layout -update` rewrites them).

The drills that exercise it are in [How to run the coordination drills](../how-to/run-the-coordination-drills.md).

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
| `milestone.<project>.<M>` | one checkout of the project, as `<user>/<checkout>`: taken by `ape milestone claim <M>`, renewed by the milestone's `apex-loop` run | 30 s, renewed every 10 s while the run lives |
| `steward.<ens>` | the ensemble's steward, any eligible member's conductor | 30 s, renewed every 10 s |
| `coord.<ens>` | the ensemble's coordinator | 30 s, renewed every 10 s |
| `sup.leader` | the active supervisor; a standby takes over on expiry | 30 s, renewed every 10 s |
| `conductor.<user>.<ens>` | one conductor of that person for that ensemble | 5 min, renewed only while the conductor acknowledges its deliveries |

A credential may create, renew and delete only its own `conductor.<own user>.*` keys; the other kinds are claimable
by any member.

A milestone's lease is a person's claim kept alive by a run (stage 1a). `ape milestone claim <M>` takes it for this
checkout, or keeps it when this checkout already holds it. When another checkout holds it, the claim is refused with
`session.not-lease-holder`, naming the holder. The milestone's loop, `ape task apex-loop --args <M>`, re-takes the lease
at launch and renews it for the run's life. A loop on a milestone this checkout never claimed, or released, is
refused before any session starts. In an ensemble's project the loop lock is per milestone, so loops on two
milestones run side by side; a second loop on the same one is `session.loop-running`. `change new --milestone <M>` and
`change start` of M's change need this checkout to hold the lease.

Between runs the lease can lapse. A later run or change start takes it again from this checkout's lease book
(`_apex/state/coord/tokens.json`), unless another member claimed M meanwhile. In that case it is refused, and never
taken back. `ape milestone release <M>` deletes the key and marks the book released.

In a project integrating through GitHub (`coordination.github`), the claim also prepares the milestone's branch,
`milestone/<M>`. When main's remote already has it, the claim fetches it. Otherwise it cuts it from the remote's
`main` and pushes it. Either way a local `milestone/<M>` tracks it. `change new --milestone <M>` and `change start` of
M's change run on that branch, switching to it from another base branch when the tree is clean. A checkout that never
claimed M has no such branch and is refused with `milestone.branch-missing`. A failed fetch or push is
`base.fetch-failed`. The lease stays held then, and claiming again retries.

In such a project main is protected and ape never pushes to it. The roadmap records others must see reach it as
**record PRs** through main's merge queue: `ape milestone new` (proposed), `ape milestone approve` (approved) and the
claim. new and approve run on main, switching there when the tree is clean. They make their commit as before, then push it
to `apex/record-<M>-<what>-<sha>`, open its PR into main and enqueue it. The claim record (`apex: <M> claimed by
<holder>`) is a commit with no change on the remote's main, and decides no lease. A record whose PR did not open is
sent by the next record verb. Once the queue merged it, main fast-forwards at the next fetch. A record reaches a
milestone's branch by its next take-in.

**Milestone branches are ape's (D702); GitHub's merge queue is main's only.** A change's archive and a take-in land
on `milestone/<M>` with one push: this checkout's lease on M is fenced right before it, and the push only
fast-forwards. A branch that moved since the fetch, which means another writer, refuses the push (`milestone.moved`);
nothing lands, and running the command again fetches and catches up. The single writer is the lease holder's ape:
- `change new/start/archive`, `milestone take-in` and `milestone close` need M's lease;
- the conductor's `push-branch` refuses `milestone/*` and `apex/*`;
- sessions cannot push.

The rulesets R1–R4, set once per repository, keep everyone else off on GitHub's side.

Main's merge queue check runs in CI. `ape ci workflow --write` writes `.github/workflows/apex-queue.yml`, which runs on
self-hosted runners:

- On a merge group into main it runs `ape ci check`, the required check. A milestone's close PR, which carries the
  milestone's changes and take-ins, gets the milestone gate with its full run, where an accepted red passes only on
  the red set accepted. Records get the project check. Anything else, such as a change on its own, is
  `ci.outside-flow`.
- On a red group it publishes `ejected`, and on a push to main `close-merged`, with `ape ci event` and the ensemble
  credential from the `APE_NATS_CREDS` secret. They wake the waiting close, which then reads GitHub.

**Tokens come from the GitHub App service** when `coordination.github.app` is true (PLAN-V15-GHAPP). `ape ghapp serve`
runs on the coordination host as `apex-ghapp.service` under its own user and holds the keys of two Apps:

- **apex-pusher** (contents and pull requests): R1's bypass actor.
- **apex-warden** (administration, secrets and workflows): set-up acts only.

ape asks it over NATS, as the member's user, for a one-hour token narrowed to the repository and a purpose: `push`,
`pr` or `read`, never `workflows`. A push goes to the repository over https, with the token in an `http.extraHeader`
passed only through git's `GIT_CONFIG_*` environment, never argv, a URL or disk. Without `app`, `hot/github.token`
and git's own credentials are used, as before. The operator's set-up acts are `ape ghapp setup rulesets|workflow|
secrets|runner-token`. `ape ghapp print-config` and `ape ghapp print-unit` print the files the operator installs.

`ape project check` reads main's merge queue. It must merge with a merge commit, or it is a `coord.queue-settings`
finding.

A conductor receives its ensemble's events through the durable consumer **`conductor-<user>-<ens>`** on `APE_EVENTS`.
Only the holder of `conductor.<user>.<ens>` binds it, so a conductor that takes the lease over inherits the events the
last one left unacknowledged, and its acknowledgements are what renews the lease.

`ape conductor attach --project <dir>… --pane <tmux pane>` is that conductor's ape. It types each event into your own
Claude Code session's tmux pane by the same rule as `ape conductor watcher` (v1.4.2): the session pinned at the first
look, with no turn running, an empty input row and a steady screen. A different session in the pane (after a
`/clear`) pauses it until `ape conductor watcher --repin` runs from that session. Each delivery is marked as ape's, so
it never grants a person act, and ends with the `ape conductor ack $JS.ACK.…` that acknowledges it.

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

# NATS subjects & event payloads

> **Status: PARTIALLY IMPLEMENTED.** This is the single, authoritative,
> **additive-only** taxonomy for the NATS work in PLAN-13 (eventing + transcript
> blobs), PLAN-14 (`ape service`), PLAN-17 (reporting CLI + identity), and
> PLAN-18 (`ape`/`aped` VM management).
>
> **Shipped (PLAN-13 + PLAN-17):** the `ape.evt.<user>.<project>.…` progress-event
> root and the `ape.blob.uri-request` transcript-offload contract (PLAN-13); the
> `ape.log.<user>.…` and `ape.metrics.<user>.…` reporting roots and the standalone
> `ape event`/`log`/`metrics`/`transcript` commands (PLAN-17), including the
> `session` event kind for agent-initiated reporting. Every subject carries the
> `<user>` identity token decoded from the `.creds` credential (PLAN-17 D1), and
> the PTY runners publish the same shapes at finalize. Also shipped: the
> `ape.svc.<name>.<project-slug>.<endpoint>` job-daemon request/reply root and
> its `svc`-kind lifecycle events (PLAN-14, `ape service`). See
> [How to report from a session](../how-to/report-from-a-session.md),
> [How to publish progress to NATS](../how-to/publish-progress-to-nats.md),
> [How to upload transcripts](../how-to/upload-transcripts.md), and
> [How to run ape as a service](../how-to/run-ape-as-a-service.md).
>
> **Implemented (PLAN-18 Phase 2):** the `ape.vmm` management service + the
> `ape.audit` root + per-VM telemetry creds, served by `aped` (see
> [How to run aped](../how-to/run-aped.md)). Tier-1 (embedded-server) tests are
> green; Tier-2 live Kata validation is gated on a KVM+containerd+Kata host.
> Each subtree notes the plan that owns it.
>
> The subject taxonomy is an external contract that **cannot be retrofitted** (a
> user token baked into a subject can't be added later without breaking
> consumers), which is why the whole taxonomy was frozen here before the first
> publisher shipped.

This is the routing surface a consumer subscribes to. `ape` is a *publisher* (and,
for `ape service`/`aped`, a request/reply *responder*); dashboards and collectors
consume with standard NATS tooling (`nats sub 'ape.>'`). Everything is opt-in per
invocation/environment — with no NATS URL + credentials configured, nothing is
published.

## Identity — the `<user>` / `<node>` token in every subject

`ape` decodes the **user JWT out of the configured `.creds` file** locally (no
server round-trip) and derives a **subject token** from the JWT `name` claim
(lowercased/slugged; `.`, `*`, `>`, whitespace → `-`; falls back to the user
public key). That token is the `<user>` segment below (PLAN-13 `natsconn.Identity`
/ PLAN-17 D1).

Because the token derives deterministically from the credential, an operator can
issue creds whose **publish permissions are scoped to `ape.*.<token>.>`** — then
the identity in the subject is **server-enforced**, not self-reported. For `aped`
VM management (PLAN-18) the per-VM credential's token is **`vm-<id>`** and the node
identity is `<node>`.

## Subject roots

| Root | Kind | Owner | Direction |
| ---- | ---- | ----- | --------- |
| `ape.evt.<user>.<project>.<kind>.<id>.<event>` | progress events | PLAN-13 (+ PLAN-17 `session` kind) | publish (fire-and-forget) |
| `ape.log.<user>.<project>.<session-id>.<level>` | structured logs | PLAN-17 | publish |
| `ape.metrics.<user>.<project>.<session-id>` | usage/cost metrics | PLAN-17 | publish |
| `ape.blob.uri-request` | transcript-blob offload | PLAN-13 | request/reply |
| `ape.svc.<name>.<project-slug>.<endpoint>` | job daemon control | PLAN-14 | request/reply |
| `ape.vmm.<node>.<verb>` | VM-management control | PLAN-18 | request/reply |
| `ape.vmm.<node>.exec.<sid>.<stream>` | interactive exec/attach | PLAN-18 | streamed |
| `ape.audit.<node>.<event>` | privileged-op audit | PLAN-18 | publish (append-only) |

Prefix `ape.evt` is overridable with `--events-subject-prefix`; the roots are a
versioned, additive-only contract otherwise.

### `ape.evt` — progress events (PLAN-13)

`ape.evt.<user>.<project>.<kind>.<id>.<event>` where:

- `<project>` — sanitized project-root slug.
- `<kind>` ∈ `pipeline | task | command | script | session | svc`.
  `session` = standalone/agent-initiated reporting (PLAN-17); `svc` = daemon
  lifecycle (PLAN-14).
- `<id>` — the run / command / session / job id. `APE_JOB_ID` (env) overrides the
  run-generated id (the PLAN-14 daemon injects it so child events carry the job id).
- `<event>` ∈ `run-start | stage-start | step-start | step-end | stage-end | hook |
  commit | error | run-end` (plus caller-chosen tokens under kind `session` and
  the daemon lifecycle tokens `job-accepted | job-rejected | job-end` under kind
  `svc`, all validated `[a-z0-9-]+`).

`step-end` carries the per-model telemetry block (PLAN-10); `run-end` carries
manifest totals + transcript-blob hashes.

Under kind `session` (standalone `ape event`), the `<id>` segment is the
session id and the payload adds `session_id` + `event` + a caller-supplied
`payload` (arbitrary JSON), e.g. `ape event status --payload '{"pct":60}'` →
`ape.evt.<user>.<project>.session.<session-id>.status`. `ape transcript upload`
publishes a companion `…session.<session-id>.transcript-uploaded` whose `payload`
is the uploaded blobs' digest map (keyed by transcript file base name).

Under kind `svc` (the PLAN-14 daemon), the `<id>` segment is the **job id** and
the daemon publishes three lifecycle events: `job-accepted` (a job was admitted;
payload adds `kind`, `exclusive`, `exclusivity_key`, `submitted_by`),
`job-rejected` (admission lost an exclusivity race; payload adds `reason` = the
busy code + `exclusivity_key`), and `job-end` (the child exited; payload adds
`state` ∈ `done | failed | stopped` + `exit_code`). The dispatched child
publishes its own `pipeline`/`task` progress events under the **same** job id —
the daemon injects `APE_JOB_ID` — so a consumer correlates the daemon's
`svc.<job>` lifecycle with the child's `<kind>.<job>` progress by the shared id.

### `ape.log` — structured logs (PLAN-17)

`ape.log.<user>.<project>.<session-id>.<level>`, `<level>` ∈
`debug | info | warn | error`. Consumers subscribe `ape.log.>` (or per-user/project
subtrees — the subject *is* the routing key). Payload: the common envelope plus
`"level"`, `"msg"`, and `"fields"` (a string→string map from repeated
`--field k=v`). Published by `ape log <level> <message>`.

### `ape.metrics` — usage/cost metrics (PLAN-17)

`ape.metrics.<user>.<project>.<session-id>`. Payload carries per-model token
counts + timestamps so a consumer can (re)price against Claude Code API rates at
any later moment (`per_model` tokens × the price table = `cost_usd`). Published by
`ape metrics` (a live scan of the session's main + sub-agent transcripts) and by
the PTY runners at finalize — the payloads are schema-identical, differing only in
`ts`. `ape metrics --run-id <id>` instead publishes a completed run's manifest
totals with `run_id` populated. Republishing is idempotent; consumers key on
`(session_id, ts)`.

### `ape.blob.uri-request` — transcript-blob offload (PLAN-13)

Request `{digest, size, compressed_size, content_type, project, run_id}` → reply
`{status:"upload", uri, method, headers}` or `{status:"exists"}`; `ape` then does
the HTTPS PUT. The offload service is out of `ape`'s tree; `ape` ships the client
half + this contract.

### `ape.svc` — job daemon (PLAN-14)

`ape service` registers a NATS-micro service whose endpoint group is rooted at
`ape.svc.<name>.<project-slug>` (`<name>` = `--name`, default `ape`;
`<project-slug>` = the daemon's primary project). Endpoints:
`pipeline.run | task.run | command.run | script.run | job.status | job.list |
job.stop | status | health`. NATS-micro `$SRV.{PING,INFO,STATS}` discovery is
free (liveness + per-endpoint request/error counters). Errors are returned via
micro `req.Error` with stable codes: `BUSY_EXCLUSIVE`, `BUSY_KEY`,
`PROJECT_NOT_ALLOWED`, `VALIDATION`, `NOT_FOUND`.

Every request/reply body is versioned JSON (`"v":1`). Requests:

| Endpoint | Request | Reply (or error) |
| --- | --- | --- |
| `pipeline.run` | `{project_root, pipeline, prompt?, from?, no_commit?, commit_allow_dirty?, upload_transcripts?, nonexclusive?, exclusivity_key?, submitted_by?}` | `{job_id, accepted:true}` · `BUSY_EXCLUSIVE`/`BUSY_KEY`/`PROJECT_NOT_ALLOWED`/`VALIDATION` |
| `task.run` | `{project_root, skill, agent?, model?, args?, prompt?, prompt_flag?, task_commit?, no_commit?, commit_allow_dirty?, upload_transcripts?, nonexclusive?, exclusivity_key?, submitted_by?}` | same |
| `command.run` / `script.run` | *(registered so `$SRV.INFO` matches this contract)* | `VALIDATION` — no backing runner ships yet (`ape command` / PLAN-15 `ape script`) |
| `job.status` | `{job_id}` | `{job_id, kind, state, started_at, pid?, exclusivity_key, exclusive, submitted_by?, log_path?, exit_code?}` · `NOT_FOUND` |
| `job.list` | `{}` | `{jobs:[…]}` |
| `job.stop` | `{job_id}` | `{stopped:bool}` (SIGTERMs the child's process group; the job's terminal `state` becomes `stopped`) · `NOT_FOUND` |
| `status` | `{}` | `{running_jobs, held_keys:{key:{exclusive,count}}, uptime_seconds, versions:{ape,claude}, project_root, allowlist, name, draining}` |
| `health` | `{}` | `{ok, checks:{nats, claude_bin, project_root}}` — a cheap `ape doctor` subset |

`task_commit` is a nullable string: omitted/`null` = no task-layer commit; `""` =
commit with the derived message `ape:task/<skill>`; a non-empty string = that
commit message. `state` ∈ `running | done | failed | stopped`.

**Admission** is keyed exclusivity, exclusive by default. Each job holds its
`exclusivity_key` (default `""`); an exclusive job blocks all others on that key
(`BUSY_EXCLUSIVE`), and an exclusive request is rejected while nonexclusive jobs
hold the key (`BUSY_KEY`). `nonexclusive:true` jobs share a key with unlimited
concurrency. Keys are independent; conflicts are rejected immediately, never
queued. A request whose `project_root` is not an exact match in the daemon's
allowlist is rejected `PROJECT_NOT_ALLOWED` — this and the NATS credential's
subject permissions are the daemon's trust boundary (see
[How to run ape as a service](../how-to/run-ape-as-a-service.md)).

### `ape.vmm` — VM management (PLAN-18)

`aped`'s NATS-micro `vmm` service, group `ape.vmm.<node>`; one endpoint per
`Backend` verb: `capabilities | create | start | stop | exec | attach.open |
freeze | unfreeze | suspend | resume | snapshot | list | inspect | destroy`.
Errors: `BUSY`, `VALIDATION`, `NOT_FOUND`, `UNSUPPORTED`, `DEVICE_UNAVAILABLE`,
`DENIED`. **The host operator account may publish here; per-VM (telemetry)
credentials are denied this root entirely** — the VM→host-escape barrier.

The `create` body is the thin `CreateRequest` (`{name, image?, runtime?, mount?,
mount_source?, profile?, devices?}`); `aped` resolves the composed home, egress,
and per-VM creds server-side. `mount_source` (additive) is the one caller-context
path on the wire — the canonical host path for a `host-fs` mount, which `aped`
symlink-resolves and re-checks against its policy mount-root allow-list before
binding. The id-verbs take `{id}`; `destroy`/`exec`/`snapshot`/`attach.open`
take `{id, …options}`.

Interactive exec/attach uses per-session subjects
`ape.vmm.<node>.exec.<sid>.{stdin,stdout,stderr,resize,control,exit}` with ≤32 KiB
frames + credit-based flow control (bulk stdio must not ride request/reply, which
disconnects slow consumers).

### `ape.audit` — privileged-op audit (PLAN-18)

`ape.audit.<node>.<event>` — one structured record per privileged `aped` op
(caller identity, operation, **resolved** args, policy rule + decision, outcome).
Append-only / forwarded; complements kernel `auditd` rules on `/dev/kvm` +
`/dev/vfio/*`.

### Per-VM telemetry (PLAN-18) reuses `ape.evt`/`log`/`metrics`

An in-VM `ape` agent is issued a per-VM credential whose token is **`vm-<id>`**, so
its telemetry flows on the **existing** roots — `ape.evt.vm-<id>.…`,
`ape.log.vm-<id>.…`, `ape.metrics.vm-<id>.…` (+ `ape.blob.uri-request`) — with **no
new taxonomy**. Its credential is scoped pub-only to `ape.{evt,log,metrics}.vm-<id>.>`
(+ `allow_responses`) and sub-only to `ape.svc.vm-<id>.>` + a scoped inbox; it is
**denied `ape.vmm.>`** and every other VM's `ape.*.vm-*.>`.

## Payload envelope

Every payload is versioned JSON. Common fields, present on every message for
traceability independent of the subject:

```json
{ "v": 1,
  "ts": "<RFC3339>",
  "user": { "name": "…", "public_key": "…" },
  "project": "<project-slug>",
  "session_id": "<claude session uuid, where bound>",
  "…": "kind-specific fields" }
```

- `ape.log`: `+ "level", "msg", "fields"`.
- `ape.metrics`: `+ "duration_seconds", "num_turns", "per_model": {model: {input_tokens,
  output_tokens, cache_read_input_tokens, cache_creation_5m, cache_creation_1h,
  turns, cost_usd}}, "first_turn_at", "last_turn_at", "claude_code_version"`.
- `ape.evt` `run-end`: `+` manifest totals + `transcript_blobs` digest map.

### `ape.evt` per-event payload fields (PLAN-13, implemented)

Every `ape.evt` payload carries the common envelope above plus `"event"` (the
`<event>` token) and `"run_id"` (the run/command/job id — the `<id>` segment).
Per-event additions:

| `event` | additional fields |
| ------- | ----------------- |
| `run-start` | `pipeline` (name), `stages` (count) |
| `stage-start` | `stage` |
| `step-start` | `stage`, `step` (1-based), `skill`, `agent`, `model` |
| `step-end` | `stage`, `step`, `skill`, `duration_seconds`, `session_id` (when bound), `metrics` (see below) |
| `hook` | `hook` (Claude Code hook name), `step`, `agent_id`, `session_id` (when present) |
| `commit` | `stage`, `step`, `sha`, `message` |
| `error` | `message` |
| `run-end` | `status`, `totals` (manifest totals), `transcript_blobs` (map, when uploaded), `upload_status` |

`step-end.metrics` mirrors the PLAN-10 transcript-derived telemetry:
`{cost_usd, tokens_input, tokens_output, tokens_cache_read, tokens_cache_creation,
num_turns, per_model: {model: {cost_usd, input_tokens, output_tokens,
cache_read_input_tokens, cache_creation_5m, cache_creation_1h, turns}}}`.

`run-end.transcript_blobs` maps each uploaded transcript's file base name to
`{session_id, digest, uri, bytes}`; the same map is stamped onto the run
`manifest.yaml` (`transcript_blobs:` block, additive under `schema_version: 2`)
alongside `upload_status:` (`ok` | `partial` | `failed`).

## Rules

- **Additive-only.** Never remove or repurpose a subject segment or payload field;
  add new ones. Bump `"v"` for a breaking payload change and document it here.
- **`<user>`/`<node>` tokens are load-bearing from day one** — they cannot be
  retrofitted into the subject; server-side publish permissions scope to
  `ape.*.<token>.>`.
- **stdout discipline.** Every NATS diagnostic (connect warnings, drop counters,
  upload failures) goes to stderr or the runlog — **never stdout** (the `ape task
  --output-format json` envelope is parsed from stdout).
- **Fire-and-forget publishing never blocks or fails a run** (runner taps); the
  standalone reporting commands (PLAN-17) *do* surface publish failures (exit 1).

## See also

- Design + phased plan for `aped`: `development/planning/plan-18_ape-aped-split.md`
  and `development/research/ape-aped-split-20260707.md`.
- Foundation plans (subject owners): PLAN-13 (eventing/blobs), PLAN-14
  (`ape service`), PLAN-17 (reporting + identity) under `development/planning/`.

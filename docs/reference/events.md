# NATS subjects & event payloads

> **Status: PROPOSED — this is a *contract*, not a shipped feature.** The subjects
> and payloads below are the single, authoritative, **additive-only** taxonomy for
> the NATS work in PLAN-13 (eventing + transcript blobs), PLAN-14 (`ape service`),
> PLAN-17 (reporting CLI + identity), and PLAN-18 (`ape`/`aped` VM management).
> Those features are **not yet implemented** (there is no `nats` dependency in
> `go.mod` yet). This page exists first, on purpose: the subject taxonomy is an
> external contract that **cannot be retrofitted** (a user token baked into a
> subject can't be added later without breaking consumers), so it is frozen here
> before any publisher ships. Each subtree notes the plan that owns it.

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
  commit | error | run-end` (plus caller-chosen tokens under kind `session`,
  validated `[a-z0-9-]+`).

`step-end` carries the per-model telemetry block (PLAN-10); `run-end` carries
manifest totals + transcript-blob hashes.

### `ape.log` — structured logs (PLAN-17)

`ape.log.<user>.<project>.<session-id>.<level>`, `<level>` ∈
`debug | info | warn | error`. Consumers subscribe `ape.log.>` (or per-user/project
subtrees — the subject *is* the routing key).

### `ape.metrics` — usage/cost metrics (PLAN-17)

`ape.metrics.<user>.<project>.<session-id>`. Payload carries per-model token
counts + timestamps so a consumer can (re)price against Claude Code API rates at
any later moment. Republishing is idempotent; consumers key on
`(session_id, ts)`.

### `ape.blob.uri-request` — transcript-blob offload (PLAN-13)

Request `{digest, size, compressed_size, content_type, project, run_id}` → reply
`{status:"upload", uri, method, headers}` or `{status:"exists"}`; `ape` then does
the HTTPS PUT. The offload service is out of `ape`'s tree; `ape` ships the client
half + this contract.

### `ape.svc` — job daemon (PLAN-14)

Endpoint group rooted at `ape.svc.<name>.<project-slug>`; endpoints
`pipeline.run | task.run | command.run | script.run | job.status | job.list |
job.stop | status | health`. NATS-micro `$SRV.{PING,INFO,STATS}` discovery is
free. Errors use stable codes: `BUSY_EXCLUSIVE`, `BUSY_KEY`, `PROJECT_NOT_ALLOWED`,
`VALIDATION`, `NOT_FOUND`.

### `ape.vmm` — VM management (PLAN-18)

`aped`'s NATS-micro `vmm` service, group `ape.vmm.<node>`; one endpoint per
`Backend` verb: `capabilities | create | start | stop | exec | attach.open |
freeze | unfreeze | suspend | resume | snapshot | list | inspect | destroy`.
Errors: `BUSY`, `VALIDATION`, `NOT_FOUND`, `UNSUPPORTED`, `DEVICE_UNAVAILABLE`,
`DENIED`. **The host operator account may publish here; per-VM (telemetry)
credentials are denied this root entirely** — the VM→host-escape barrier.

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

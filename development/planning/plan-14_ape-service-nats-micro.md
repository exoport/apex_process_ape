---
plan_id: PLAN-14
created_at: 2026-07-02
status: proposed
tags:
  - new-command
  - service
  - nats-micro
  - daemon
  - exclusivity
summary: New `ape service` — a daemon built on NATS micro (`github.com/nats-io/nats.go/micro`) that receives pipeline/task/command/script jobs as JSON request/reply, spawns each accepted job as an `ape` child process (PTY-only, headless), and streams progress via PLAN-13 events. Project-bound with an allowlist — the daemon serves one project plus its declared component repositories; requests naming any other root are rejected. Admission control: jobs are EXCLUSIVE BY DEFAULT with key-bound exclusivity (optional `exclusivity_key`, default ""), opt-out via `nonexclusive: true`; nonexclusive jobs run unlimited in parallel within a key; conflicts are rejected immediately (structured busy error), never queued. Health and status endpoints on top of micro's built-in $SRV PING/INFO/STATS.
origin:
  - 2026-07-02 user request — "start an ape service with nats micro so … it works as a daemon, receives commands with nats micro json req/rep and spawns processes for the pipelines/tasks/command commands"; exclusivity flag; status + health endpoints.
  - 2026-07-02 user decisions (Q&A) — scope: project-bound with allowlist, covering a project's component repositories; job model: unlimited unless exclusive; exclusivity is the default, disabled per-job by a `nonexclusive` flag; exclusivity is bound to an optional key (e.g. "chore", default ""); an exclusive request in a key with a running exclusive job is rejected; an exclusive request in a key with running nonexclusive jobs is rejected.
  - 2026-07-02 research — micro: `micro.AddService` (Name + SemVer Version required), `AddGroup`/`AddEndpoint`, `RespondJSON`, `req.Error(code, desc, data)` for structured errors, built-in `$SRV.{PING,INFO,STATS}` discovery (PING is the conventional liveness probe; richer health is a custom endpoint), graceful `srv.Stop()` + `nc.Drain()`.
---

# PLAN-14: `ape service` — NATS micro job daemon

## Goal

`ape service --nats-url … --nats-creds …` inside (or configured for) a
project turns that machine into a remotely drivable ape worker: submit a
pipeline/task/command/script over NATS, watch its progress on the PLAN-13
event subjects, query status/health, stop jobs. Job execution is identical to
running the CLI locally — because it literally spawns the CLI.

## Why now

Last in the dependency chain: consumes PLAN-9 (PTY-only surface), PLAN-11/12
(job kinds), PLAN-13 (connection + events + blobs), PLAN-15 (script job
kind, can be added when it lands).

## Non-goals

- No queueing/scheduling — admission is accept-or-reject (user decision).
- No in-process job execution — child processes only (isolation: a crashed
  job can't take the daemon down; the CLI stays the single source of
  behavior).
- No multi-tenant auth beyond NATS credentials + the project allowlist
  (whoever can publish on the service subjects is trusted; document this).
- No web UI for the service (existing per-run `--web` still works locally;
  remote UI is a future consumer of the event stream).

## Design

### D1: Service definition

`micro.AddService(nc, micro.Config{Name: "ape", Version: <ape semver>,
Metadata: {project, hostname, ape_version}})` — `--name` overrides for
running several daemons on one cluster. Endpoint group rooted at
`ape.svc.<name>.<project-slug>`:

| Endpoint | Request (JSON) | Reply |
| --- | --- | --- |
| `pipeline.run` | `{project_root, pipeline, prompt?, from?, no_commit?, commit_allow_dirty?, nonexclusive?, exclusivity_key?, upload_transcripts?}` | `{job_id, accepted:true}` or busy/validation error |
| `task.run` | `{project_root, skill, agent?, model?, args?, prompt?, prompt_flag?, task_commit?, no_commit?, nonexclusive?, exclusivity_key?, …}` | same |
| `command.run` | `{project_root, prompt? \| handoff?, agent?, model?, workflow?, nonexclusive?, exclusivity_key?, …}` | same |
| `script.run` | `{project_root, script_path \| script_source, args?, nonexclusive?, exclusivity_key?}` (PLAN-15; see security note) | same |
| `job.status` | `{job_id}` | `{job_id, kind, state: running\|done\|failed\|stopped, started_at, pid, exclusivity_key, exclusive, manifest_path?, exit_code?}` |
| `job.list` | `{}` | `{jobs: […]}` |
| `job.stop` | `{job_id}` | `{stopped: bool}` (SIGTERM the child's process group — the runner already handles group-signal shutdown) |
| `status` | `{}` | `{running_jobs, held_keys: {key: {exclusive, count}}, uptime, versions {ape, claude}, project_root, allowlist}` |
| `health` | `{}` | `{ok: bool, checks: {nats: ok, claude_bin: ok, project_root: ok, disk: ok}}` — a cheap `ape doctor` subset |

Errors use `req.Error` with stable codes: `BUSY_EXCLUSIVE`, `BUSY_KEY`,
`PROJECT_NOT_ALLOWED`, `VALIDATION`, `NOT_FOUND`. `$SRV.PING/INFO/STATS`
come free (per-endpoint request/error counters).

### D2: Project allowlist

Config `_apex/service.yaml` (or `~/.ape/service.yaml` when the daemon isn't
started inside the project):

```yaml
project_root: /abs/path/main-project
allow:
  - /abs/path/main-project
  - /abs/path/component-repo-a
  - /abs/path/component-repo-b
```

Every `*.run` request carries `project_root`; it must be an exact match
against the allowlist (each entry validated at startup: exists, is a git
repo, has `_apex/` where the job kind requires it). This is the "project
plus its component repositories" scope the user chose — one daemon, several
sibling repos, nothing else.

### D3: Admission — keyed exclusivity, exclusive by default

State per key `k` (default `""`): either **free**, **held-exclusive** (one
exclusive job), or **held-shared** (n ≥ 1 nonexclusive jobs). Rules:

| Request ↓ / key state → | free | held-exclusive | held-shared |
| --- | --- | --- | --- |
| exclusive (default) | accept (key → held-exclusive) | **reject** `BUSY_EXCLUSIVE` | **reject** `BUSY_KEY` |
| `nonexclusive: true` | accept (key → held-shared) | **reject** `BUSY_EXCLUSIVE` | accept (count++) |

Nonexclusive concurrency is unlimited (each job is its own child process).
Keys are independent — an exclusive `"chore"` job coexists with anything
running under `""`. No queue: rejected callers retry. Implementation is a
mutex-guarded `map[string]keyState`; keys release on child exit (any
outcome).

### D4: Job execution

Accepted job → `job_id` (`YYYYMMDD-HHMMSS-<7hex>`, consistent with run ids)
→ spawn `ape <kind> … --no-tui --quiet` with argv assembled from the request
(strictly typed field-to-flag mapping — request fields never concatenated
into a shell string), `cwd = project_root`, process group set (reusing
`configureProcessGroup`). NATS config is passed through (env) so **the child
itself** publishes PLAN-13 progress events; the daemon injects
`APE_JOB_ID=<job_id>` so the child's event subjects carry the job's id (small
PLAN-13 hook: id override via env). The daemon additionally publishes
`job-accepted|job-rejected|job-end` on `ape.evt.<project>.svc.<job_id>.*`.
Child stdout/stderr → `_output/ape/service/<job_id>.log`; exit code recorded.

Daemon lifecycle: SIGTERM → stop accepting, optionally `--drain-timeout`
waiting for children, then group-SIGTERM children → `srv.Stop()` +
`nc.Drain()`. A `--stop-jobs-on-exit=false` mode (children orphaned but
alive) is deliberately **not** offered v1 — children die with the daemon,
keeping key-state truthful.

### D5: Security note for `script.run`

`script_source` in a request is arbitrary code execution on the daemon host
by design (PLAN-15). v1 mitigations: `script.run` disabled unless
`service.yaml: allow_script_source: true`; `script_path` variant must resolve
inside an allowlisted root; `service.yaml: force_script_sandbox: true`
forces PLAN-15's `--sandbox` (interpreter-level restriction — no os/exec,
syscall, unsafe; apescript orchestration functions remain) onto every script
job, recommended whenever `allow_script_source` is enabled. Documented
loudly.

## Steps

1. `internal/service`: admission table (D3) as a pure package — exhaustive
   unit tests of the state machine first (it is the subtle part).
2. Spawner (D4) with a fake job binary for tests (pattern: the repl tests'
   bash stand-in).
3. micro endpoints (D1) against the embedded nats-server test helper from
   PLAN-13; table-driven request/reply tests including all rejection codes.
4. Allowlist config + validation (D2).
5. `apecmd/service.go` command wiring + graceful shutdown.
6. Docs: `how-to/run-ape-as-a-service.md` (nsc creds, `nats req` examples,
   exclusivity semantics table), `reference/service-api.md` (endpoint
   contracts, error codes).

## Acceptance

- Embedded-server integration test: submit `task.run` → accepted → fake
  child runs → `job.status` transitions running→done → key released.
- Exclusivity matrix: all six cells of D3's table asserted.
- Second exclusive submit while first runs (same key) → `BUSY_EXCLUSIVE`
  immediately; different key → accepted.
- `nats req '$SRV.PING.ape' ''` answers; `health` reports `ok:false` when
  `claude` is removed from PATH.
- `job.stop` terminates a running child's whole process group; key released.
- Request with a non-allowlisted `project_root` → `PROJECT_NOT_ALLOWED`.

## Risks / notes

- The daemon trusts its NATS credential boundary. The how-to must state:
  anyone who can request on the service subjects can run pipelines (and, if
  enabled, arbitrary scripts) on this machine.
- Child-publishes-events design means a hung child looks alive; `job.status`
  should include `last_event_at` (cheap: daemon subscribes to its own
  project's event subjects) — stretch goal, noted for review.
- Windows: process-group semantics differ; `configureProcessGroup` already
  has per-OS files — the service inherits that work, but CI must include the
  Windows cross-compile gate (`make xcompile-windows`) for the new packages.

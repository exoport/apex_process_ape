# How to run ape as a service (NATS job daemon)

`ape service` turns a machine into a remotely drivable ape worker (PLAN-14). It
registers a [NATS micro](https://docs.nats.io/using-nats/developer/services)
service and accepts JSON request/reply jobs: submit a pipeline or task, watch its
progress on the PLAN-13 event subjects, query status, and stop jobs — all over
NATS. Job execution is identical to running the CLI locally, because the daemon
literally spawns `ape` as a child process.

> **Trust boundary — read this first.** Anyone who can publish on the service
> subjects can run pipelines (and, later, arbitrary scripts) on this machine.
> The daemon's only guards are (1) the project allowlist and (2) the NATS
> credential's subject permissions. Scope creds on the server; see
> [Lock it down](#lock-it-down-server-enforced) below.

## 1. Write the allowlist config

The daemon serves one project plus its declared sibling component repositories,
listed in `_apex/service.yaml` (or `~/.ape/service.yaml`, or `--config <path>`):

```yaml
project_root: /abs/path/main-project
allow:
  - /abs/path/main-project
  - /abs/path/component-repo-a
  - /abs/path/component-repo-b
```

Every entry is validated at startup: it must be an absolute path to an existing
git working tree. `project_root` is always implicitly allowed. Every `*.run`
request carries a `project_root` that must be an **exact** match against this
list — anything else is rejected `PROJECT_NOT_ALLOWED`.

## 2. Start the daemon

Point it at a NATS server (flags or env; flags win):

```bash
ape service --nats-url nats://nats.example:4222 --nats-creds ~/.config/ape/daemon.creds
# or:
export APE_NATS_URL=nats://nats.example:4222
export APE_NATS_CREDS=~/.config/ape/daemon.creds
ape service
```

It prints its subject group and discovery name to stderr and serves until
signalled. Run several daemons on one cluster with distinct `--name` values
(the `<name>` subject segment + `$SRV` discovery name).

## 3. Discover it

NATS-micro discovery is free (with the [`nats` CLI](https://github.com/nats-io/natscli)):

```bash
nats req '$SRV.PING.ape' ''      # liveness
nats req '$SRV.INFO.ape' ''      # endpoint list
nats req '$SRV.STATS.ape' ''     # per-endpoint request/error counters
```

## 4. Submit and manage jobs

The endpoint group is `ape.svc.<name>.<project-slug>` (the daemon's banner prints
it). Submit a job:

```bash
# pipeline.run and task.run dispatch a headless `ape` child.
nats req ape.svc.ape.main-project.task.run \
  '{"project_root":"/abs/path/main-project","skill":"apex-shard-doc","args":"--doc prd"}'
# → {"v":1,"job_id":"20260709-083015-a1b2c3d","accepted":true}

nats req ape.svc.ape.main-project.pipeline.run \
  '{"project_root":"/abs/path/main-project","pipeline":"design","prompt":"a greeter CLI"}'

# prompt.run dispatches a headless `ape prompt` session. Provide EXACTLY ONE
# of "prompt" (positional text) or "handoff" (a handoff document); optional
# "agent", "model", and "workflow":true.
nats req ape.svc.ape.main-project.prompt.run \
  '{"project_root":"/abs/path/main-project","prompt":"add a CHANGELOG entry","agent":"apex-agent-dev","workflow":true}'

# script.run dispatches `ape script`. Provide EXACTLY ONE of "script_path"
# (a file inside an allowlisted root) or "script_source" (inline Go, gated —
# see below); "script_args" are exposed to the script as apescript.Args().
nats req ape.svc.ape.main-project.script.run \
  '{"project_root":"/abs/path/main-project","script_path":"ops/nightly.go","script_args":["--target","./component-a"]}'
```

Manage them:

```bash
nats req ape.svc.ape.main-project.job.status '{"job_id":"20260709-083015-a1b2c3d"}'
nats req ape.svc.ape.main-project.job.list   '{}'
nats req ape.svc.ape.main-project.job.stop   '{"job_id":"20260709-083015-a1b2c3d"}'
nats req ape.svc.ape.main-project.status     '{}'
nats req ape.svc.ape.main-project.health     '{}'
```

Watch progress on the PLAN-13 event subjects. The child publishes its own
`pipeline`/`task` events under the **injected job id**, and the daemon publishes
`svc`-kind lifecycle events under the same id:

```bash
nats sub 'ape.evt.*.main-project.svc.>'         # daemon: job-accepted/job-rejected/job-end
nats sub 'ape.evt.*.main-project.*.>'           # + the child's run/stage/step progress
```

All four `*.run` kinds — `pipeline.run`, `task.run`, `prompt.run`, `script.run` —
spawn a real headless `ape` child. `prompt.run` requires exactly one of `prompt`
or `handoff`; `script.run` requires exactly one of `script_path` or
`script_source` and is subject to the script gates below.

## Script jobs are code execution — gate them (D5)

`script.run` runs a Go orchestration script on the daemon host. Two
`service.yaml` flags (both default `false`) bound the blast radius:

```yaml
project_root: /abs/path/main-project
allow:
  - /abs/path/main-project
allow_script_source: false   # accept inline `script_source` (arbitrary code) — off by default
force_script_sandbox: false  # force `ape script --sandbox` on every script job
```

- **`script_path`** is always allowed, but the path (relative paths resolve
  against `project_root`) must resolve to an existing file **inside an
  allowlisted root** — anything outside is rejected `VALIDATION`.
- **`script_source`** (inline Go piped to `ape script -` on stdin) is arbitrary
  code execution on the daemon host. It is rejected `VALIDATION` unless
  `allow_script_source: true`.
- **`force_script_sandbox: true`** forces PLAN-15's interpreter-level `--sandbox`
  (blocks `os/exec`, `os.Exit`, `syscall`, `unsafe`; the apescript orchestration
  functions stay available) onto every script job. Setting it without
  `allow_script_source` is a config error. **Recommended whenever
  `allow_script_source` is enabled.**

`ape prompt` publishes no PLAN-13 progress events of its own, so a `prompt.run`
job is observable only through the daemon's `svc` lifecycle events and
`job.status`; `pipeline`/`task`/`script` children publish their own progress.

## Exclusivity: what runs alongside what

Jobs are **exclusive by default**, bound to an optional `exclusivity_key`
(default `""`). Conflicts are rejected immediately — never queued — so a rejected
caller simply retries.

| Request ↓ / key state → | free | held by an exclusive job | held by nonexclusive jobs |
| --- | --- | --- | --- |
| exclusive (default) | ✅ accept | ❌ `BUSY_EXCLUSIVE` | ❌ `BUSY_KEY` |
| `nonexclusive: true` | ✅ accept | ❌ `BUSY_EXCLUSIVE` | ✅ accept (unlimited) |

Keys are independent: an exclusive `"chore"` job coexists with anything running
under `""`. Set `"nonexclusive":true` for jobs that are safe to run in parallel
(read-only reports, independent components); leave it off for anything that
commits or mutates shared state.

## Lock it down (server-enforced)

The daemon trusts its NATS credential boundary. On the server, scope who may
publish on the service subjects — this is the real access control, enforced by
NATS, not by ape. With `nsc`, grant a submitter only this daemon's group:

```bash
nsc add user ci-submitter \
  --allow-pub 'ape.svc.ape.main-project.>' \
  --allow-sub '_INBOX.>'
```

The daemon's own credential (`--nats-creds`) needs to **subscribe** to its
service subjects + `$SRV.>` and **publish** replies + its `svc` lifecycle events
(`ape.evt.<daemon-user>.>`). The `<user>` token on those events is the daemon's
identity, not the submitter's — NATS micro does not expose the caller's identity
to the service, so pass an advisory `submitted_by` in requests for traceability
and rely on per-user creds + subject permissions for authoritative attribution.

## Shutdown drains gracefully

`SIGINT`/`SIGTERM` stops accepting new jobs and waits for in-flight children to
finish — **indefinitely by default**. Bound the wait with `--drain-timeout 5m`
(after which remaining jobs are terminated), or send a **second** signal to
terminate them immediately. Children die with the daemon, so held keys never
outlive the process.

## Notes

- **stdout stays clean.** All diagnostics (startup banner, drain progress, NATS
  warnings) go to stderr.
- **Per-job logs.** Each child's combined stdout/stderr is written to
  `<project_root>/_output/ape/service/<job_id>.log`.
- **Exit codes:** `0` clean shutdown · `1` connect/registration failure · `2`
  usage or config error (bad `--name`, missing/invalid `service.yaml`, no NATS
  URL).

## See also

- [events.md](../reference/events.md) — the `ape.svc` endpoint contract, error
  codes, and `svc` lifecycle event payloads.
- [How to publish run progress to NATS](publish-progress-to-nats.md) — the event
  subjects the child publishes.
- [CLI reference](../reference/cli.md) — `ape service` flags.

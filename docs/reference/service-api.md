# `ape service` endpoint contract

> **Status: SHIPPED (PLAN-14).** This is the authoritative request/reply
> reference for the `ape service` job daemon: every endpoint subject, request
> body, reply body, and error code. Its sibling
> [events.md](events.md) documents the **lifecycle-event stream** the daemon and
> its child processes publish (the `ape.evt.<user>.<project>.svc.<job>.<event>`
> subjects); this page documents the **request/reply endpoints** you drive the
> daemon with. For a task-oriented walkthrough (starting the daemon, `nats req`
> examples, locking it down) see
> [How to run ape as a service](../how-to/run-ape-as-a-service.md).

The daemon registers a [NATS micro](https://docs.nats.io/using-nats/developer/services)
service. Every endpoint is JSON request/reply. A successful call replies with a
JSON body; a rejection is returned as a micro `req.Error` (a stable code + a
human-readable description), never as a reply body. NATS-micro
`$SRV.{PING,INFO,STATS}` discovery is registered for free (liveness, endpoint
list, per-endpoint request/error counters).

## Subject taxonomy

The endpoint group is rooted at:

```
ape.svc.<name>.<project-slug>
```

- `ape.svc` — the fixed PLAN-14 job-daemon root.
- `<name>` — the `--name` value (default `ape`); also the `$SRV` discovery name.
  Run several daemons on one cluster with distinct names.
- `<project-slug>` — the sanitized slug of the daemon's **primary** project
  (`project_root` in `service.yaml`). It is a routing segment only; each request
  still carries its own `project_root`, which may be any allowlisted sibling
  repo, not just the primary.

Each endpoint's subject is the group plus the endpoint token:

| Endpoint      | Subject                                          | Direction      |
| ------------- | ------------------------------------------------ | -------------- |
| `pipeline.run`| `ape.svc.<name>.<project-slug>.pipeline.run`     | request/reply  |
| `task.run`    | `ape.svc.<name>.<project-slug>.task.run`         | request/reply  |
| `prompt.run`  | `ape.svc.<name>.<project-slug>.prompt.run`       | request/reply  |
| `script.run`  | `ape.svc.<name>.<project-slug>.script.run`       | request/reply  |
| `job.status`  | `ape.svc.<name>.<project-slug>.job.status`       | request/reply  |
| `job.list`    | `ape.svc.<name>.<project-slug>.job.list`         | request/reply  |
| `job.stop`    | `ape.svc.<name>.<project-slug>.job.stop`         | request/reply  |
| `status`      | `ape.svc.<name>.<project-slug>.status`           | request/reply  |
| `health`      | `ape.svc.<name>.<project-slug>.health`           | request/reply  |

## Wire version & compatibility

Every request and reply body is versioned JSON. The current wire version is
**`1`**.

- **Replies always stamp `"v":1`.**
- **Requests may omit `"v"`** — it is treated as the current version. (The
  daemon does not reject a mismatched `"v"`; the field is informational.)
- **The contract is additive-only.** New request/reply fields may be added
  without a version bump; a field is never removed, renamed, or repurposed. A
  breaking change would bump `"v"` and be documented in
  [events.md](events.md). New fields on `JobInfo` (e.g. `last_event_at`) are
  therefore additive and leave `"v"` at `1`.
- **Field names are snake_case** and are the stable wire contract.
- **Unknown request fields are ignored**, and fields a given kind does not read
  are silently dropped (see the field reference below).

## The four `*.run` endpoints

`pipeline.run`, `task.run`, `prompt.run`, and `script.run` share one request
type (`RunRequest`) and one accept reply (`RunReply`). Each endpoint reads only
the subset of fields its kind uses. Every accepted job is spawned as a real,
headless `ape` child process (never a shell): request fields are mapped to
discrete `ape` argv elements through a strict, typed field→flag mapping, so no
field value can inject extra flags or shell metacharacters.

### Request — `RunRequest`

```json
{
  "v": 1,
  "project_root": "/abs/path/repo",
  "pipeline": "design",
  "prompt": "a greeter CLI",
  "nonexclusive": false,
  "exclusivity_key": "",
  "submitted_by": "ci-bot"
}
```

`project_root` is always required. Each kind also requires its own selector
(`pipeline` / `skill` / one of `prompt`|`handoff` / one of
`script_path`|`script_source`). See the [RunRequest field reference](#runrequest-field-reference)
for the complete, per-kind field list and argv mapping.

### Reply — `RunReply` (accept)

```json
{ "v": 1, "job_id": "20260709-083015-a1b2c3d", "accepted": true }
```

| Field      | Type   | Notes                                                                       |
| ---------- | ------ | --------------------------------------------------------------------------- |
| `v`        | int    | Wire version (always `1`).                                                  |
| `job_id`   | string | Minted id, format `YYYYMMDD-HHMMSS-<7hex>`. Used on `job.status`/`job.stop` and as the `<id>` segment of the job's `ape.evt` subjects (the daemon injects it into the child as `APE_JOB_ID`). |
| `accepted` | bool   | Always `true` on this reply. A rejection is a `req.Error`, not this shape.  |

A rejected `*.run` request produces no `RunReply`; it returns one of the
[error codes](#error-codes) below.

### Errors (all four `*.run` endpoints)

| Code                  | When                                                                                                    |
| --------------------- | ------------------------------------------------------------------------------------------------------- |
| `VALIDATION`          | Malformed request JSON; missing `project_root`; missing/invalid kind selector; the `prompt`/`handoff` or `script_path`/`script_source` XOR is violated; `script_source` used while disabled; `script_path` resolves outside the allowlist; or the child failed to start. |
| `PROJECT_NOT_ALLOWED` | `project_root` is not an exact match in the daemon's allowlist. Checked **before** request shape or kind. |
| `BUSY_EXCLUSIVE`      | The request's `exclusivity_key` is held by an exclusive job.                                            |
| `BUSY_KEY`            | An exclusive slot was requested but the key is held by one or more nonexclusive jobs.                   |

## `job.status`

Returns one job's public state.

- **Request — `JobIDRequest`:** `{ "v": 1, "job_id": "20260709-083015-a1b2c3d" }`
  (`job_id` required; `v` optional).
- **Reply — `JobStatusReply`:** `{"v":1, …JobInfo}` — a `JobInfo` object (see the
  [JobInfo field reference](#jobinfo-field-reference)) with `v` alongside its fields.
- **Errors:** `VALIDATION` (malformed JSON) · `NOT_FOUND` (no job with that id).

## `job.list`

Lists every job the daemon has accepted this process lifetime (running and
terminal), sorted by `job_id` (chronological, since the id embeds a timestamp
prefix).

- **Request:** `{}` — the body is ignored.
- **Reply — `JobListReply`:** `{ "v": 1, "jobs": [ …JobInfo ] }`.
- **Errors:** none.

## `job.stop`

Requests an operator stop of a running job. It SIGTERMs the child's whole
process group; the child's exit is then recorded with the terminal `state`
`stopped`.

- **Request — `JobIDRequest`:** `{ "v": 1, "job_id": "…" }` (`job_id` required).
- **Reply — `JobStopReply`:** `{ "v": 1, "stopped": true|false }`. `stopped` is
  `false` when the job exists but is already terminal (nothing to signal).
- **Errors:** `VALIDATION` (malformed JSON) · `NOT_FOUND` (no job with that id).

The stop is asynchronous: `stopped:true` means the signal was sent, not that the
child has exited. Observe the `job-end` `svc` event or poll `job.status` for the
terminal state.

## `status`

Reports the daemon's own state.

- **Request:** `{}` — the body is ignored.
- **Reply — `StatusReply`:**

```json
{
  "v": 1,
  "running_jobs": 2,
  "held_keys": { "": { "exclusive": true, "count": 1 },
                 "reports": { "exclusive": false, "count": 3 } },
  "uptime_seconds": 1837.42,
  "versions": { "ape": "v0.0.45", "claude": "1.2.3 (Claude Code)" },
  "project_root": "/abs/path/main-project",
  "allowlist": [ "/abs/path/main-project", "/abs/path/component-a" ],
  "name": "ape",
  "draining": false
}
```

| Field            | Type                 | Notes                                                                                 |
| ---------------- | -------------------- | ------------------------------------------------------------------------------------- |
| `v`              | int                  | Wire version.                                                                          |
| `running_jobs`   | int                  | Count of jobs currently in state `running`.                                           |
| `held_keys`      | object               | Map of currently-held `exclusivity_key` → `{ "exclusive": bool, "count": int }`. Free keys are absent. The `""` (default) key appears under the empty-string key. |
| `uptime_seconds` | float                | Seconds since the daemon started.                                                     |
| `versions`       | object               | `{ "ape": string, "claude": string }`. `claude` is omitted when the `claude` binary was absent or slow at startup. |
| `project_root`   | string               | The daemon's primary project root.                                                    |
| `allowlist`      | string[]             | Every root a `*.run` request may target (primary + siblings, after path-cleaning).    |
| `name`           | string               | The `--name` value (the `<name>` subject segment).                                    |
| `draining`       | bool                 | `true` once shutdown has begun (no new jobs accepted; in-flight children finishing).  |

- **Errors:** none.

## `health`

A cheap `ape doctor` subset — a boolean rollup plus the individual probes.

- **Request:** `{}` — the body is ignored.
- **Reply — `HealthReply`:**

```json
{ "v": 1, "ok": true,
  "checks": { "nats": true, "claude_bin": true, "project_root": true } }
```

| Check          | Passes when                                            |
| -------------- | ------------------------------------------------------ |
| `nats`         | The daemon's NATS connection is live.                  |
| `claude_bin`   | `claude` is on `PATH`.                                 |
| `project_root` | The primary `project_root` is an existing directory.   |

`ok` is the AND of every check.

- **Errors:** none.

## RunRequest field reference

All fields are optional on the wire except `project_root` and each kind's
selector. Fields a given kind does not consume are ignored (e.g. `agent`/`model`
are read by `task.run`/`prompt.run` but not `pipeline.run`). The "→ argv" column
is the `ape` child flag the field maps to.

### Shared across all four kinds

Every `*.run` job is spawned headless: pipeline adds `--no-tui --quiet`, the
others `--quiet`, and all four `--cwd <project_root>`.

| Field             | Type   | Required | → argv               | Notes                                                                 |
| ----------------- | ------ | -------- | -------------------- | --------------------------------------------------------------------- |
| `project_root`    | string | **yes**  | `--cwd`              | Exact allowlist match; also the child's working directory.            |
| `v`               | int    | no       | —                    | Wire version; omit = current.                                         |
| `nonexclusive`    | bool   | no       | —                    | Admission control. Default `false` → exclusive. See [Admission](#admission--exclusivity). |
| `exclusivity_key` | string | no       | —                    | Admission key. Default `""`.                                          |
| `submitted_by`    | string | no       | —                    | Advisory caller attribution, echoed into `job-accepted`/`job-end` events and `job.status`. **Not** authoritative — see [security boundary](#allowlist--script-gates-the-security-boundary). |

### `pipeline.run`

| Field                | Type   | Required | → argv                   |
| -------------------- | ------ | -------- | ------------------------ |
| `pipeline`           | string | **yes**  | positional (`ape pipeline <pipeline>`) |
| `from`               | string | no       | `--from`                 |
| `prompt`             | string | no       | `--prompt`               |
| `no_commit`          | bool   | no       | `--no-commit`            |
| `commit_allow_dirty` | bool   | no       | `--commit-allow-dirty`   |
| `upload_transcripts` | bool   | no       | `--upload-transcripts`   |

### `task.run`

| Field                | Type          | Required | → argv                                  | Notes                                                    |
| -------------------- | ------------- | -------- | --------------------------------------- | -------------------------------------------------------- |
| `skill`              | string        | **yes**  | positional (`ape task <skill>`)         |                                                          |
| `agent`              | string        | no       | `--agent`                               |                                                          |
| `model`              | string        | no       | `--model`                               |                                                          |
| `args`               | string        | no       | `--args`                                | Passed to the skill.                                     |
| `prompt`             | string        | no       | `--prompt`                              |                                                          |
| `prompt_flag`        | string        | no       | `--prompt-flag`                         |                                                          |
| `task_commit`        | string\|null  | no       | `--task-commit` / `--task-commit=<msg>` | Nullable. Omitted/`null` = no commit; `""` = commit with the derived message `ape:task/<skill>`; a non-empty string = that commit message. |
| `no_commit`          | bool          | no       | `--no-commit`                           |                                                          |
| `commit_allow_dirty` | bool          | no       | `--commit-allow-dirty`                  |                                                          |
| `upload_transcripts` | bool          | no       | `--upload-transcripts`                  |                                                          |

### `prompt.run`

Requires **exactly one** of `prompt` or `handoff` (both set, or neither, →
`VALIDATION`). `ape prompt` publishes no PLAN-13 progress events, so a
`prompt.run` job is observable only through the daemon's `svc` lifecycle events
and `job.status`.

| Field      | Type   | Required          | → argv                          |
| ---------- | ------ | ----------------- | ------------------------------- |
| `prompt`   | string | one-of `prompt`/`handoff` | positional (`ape prompt <text>`)|
| `handoff`  | string | one-of `prompt`/`handoff` | `--handoff`                     |
| `agent`    | string | no                | `--agent`                       |
| `model`    | string | no                | `--model`                       |
| `workflow` | bool   | no                | `--workflow`                    |

### `script.run`

Requires **exactly one** of `script_path` or `script_source` (both, or neither,
→ `VALIDATION`). Gated by the `service.yaml` script flags — see the
[security boundary](#allowlist--script-gates-the-security-boundary).

| Field           | Type     | Required                        | → argv                    | Notes                                                                                          |
| --------------- | -------- | ------------------------------- | ------------------------- | ---------------------------------------------------------------------------------------------- |
| `script_path`   | string   | one-of `script_path`/`script_source` | positional (`ape script <path>`) | Absolute, or relative to `project_root`; must resolve to an existing file inside an allowlisted root, else `VALIDATION`. |
| `script_source` | string   | one-of `script_path`/`script_source` | positional `-` (stdin)    | Arbitrary Go source, piped to `ape script -` on stdin (never on the argv). Rejected `VALIDATION` unless `allow_script_source: true`. |
| `script_args`   | string[] | no                              | after `--`                | Exposed to the script as `apescript.Args()`.                                                   |

When `force_script_sandbox: true`, `--sandbox` is appended to every script job.

## JobInfo field reference

`JobInfo` is a job's public state, returned by `job.status` (wrapped with `v`)
and listed by `job.list`.

```json
{
  "job_id": "20260709-083015-a1b2c3d",
  "kind": "pipeline",
  "state": "running",
  "started_at": "2026-07-09T08:30:15.123456789Z",
  "last_event_at": "2026-07-09T08:30:15.123456789Z",
  "pid": 41337,
  "exclusivity_key": "",
  "exclusive": true,
  "submitted_by": "ci-bot",
  "log_path": "/abs/path/repo/_output/ape/service/20260709-083015-a1b2c3d.log",
  "exit_code": 0
}
```

| Field             | Type              | Notes                                                                                                          |
| ----------------- | ----------------- | -------------------------------------------------------------------------------------------------------------- |
| `job_id`          | string            | The minted job id (`YYYYMMDD-HHMMSS-<7hex>`).                                                                   |
| `kind`            | string            | `pipeline` \| `task` \| `prompt` \| `script`.                                                                  |
| `state`           | string            | `running` \| `done` (child exited 0) \| `failed` (child exited non-zero) \| `stopped` (operator `job.stop`).   |
| `started_at`      | RFC3339 timestamp | When the job was accepted (UTC).                                                                               |
| `last_event_at`   | RFC3339 timestamp | Timestamp of the job's most recent daemon lifecycle event. Equals `started_at` for a just-accepted job (its `job-accepted` event), and advances to the `job-end` time once the job goes terminal — the same instant stamped on the corresponding `svc` event's `ts`. |
| `pid`             | int               | Child process pid. Omitted (`0`) until the child has started.                                                  |
| `exclusivity_key` | string            | The admission key the job holds (default `""`).                                                                |
| `exclusive`       | bool              | Whether the job holds the key exclusively (`true` unless the request set `nonexclusive:true`).                 |
| `submitted_by`    | string            | Advisory attribution echoed from the request. Omitted when empty.                                              |
| `log_path`        | string            | The child's combined stdout/stderr log, at `<project_root>/_output/ape/service/<job_id>.log`. Omitted until the child has started. |
| `exit_code`       | int               | The child's exit code. Present only once the job is terminal; omitted while `running`. `-1` when the child was signalled or failed to run. |

## Error codes

Returned via micro `req.Error` as `(code, description)`. The codes are a frozen
external contract — never renamed or repurposed.

| Code                  | Returned by                          | Meaning                                                                    |
| --------------------- | ------------------------------------ | -------------------------------------------------------------------------- |
| `VALIDATION`          | the four `*.run`, `job.status`, `job.stop` | Malformed JSON, or (on `*.run`) an invalid request shape / kind selector, a violated XOR rule, a disabled/out-of-allowlist script, or a child that failed to start. |
| `PROJECT_NOT_ALLOWED` | the four `*.run`                     | `project_root` is not an exact allowlist match.                            |
| `BUSY_EXCLUSIVE`      | the four `*.run`                     | The `exclusivity_key` is held by an exclusive job.                         |
| `BUSY_KEY`            | the four `*.run`                     | An exclusive slot was requested while the key is held by nonexclusive jobs.|
| `NOT_FOUND`           | `job.status`, `job.stop`             | No job with the requested `job_id`.                                        |

`job.list`, `status`, and `health` never return an error code.

## Admission & exclusivity

Admission is **keyed exclusivity, exclusive by default**, and is accept-or-reject
— **never queued**. A rejected caller simply retries.

- Each job is bound to an `exclusivity_key` (default `""`).
- A job is **exclusive** unless the request sets `"nonexclusive": true`.
- Keys are independent: an exclusive job under `"chore"` coexists with anything
  running under `""`.

The admission matrix for a single key:

| Request ↓ / key state → | free          | held by an exclusive job | held by nonexclusive jobs   |
| ----------------------- | ------------- | ------------------------ | --------------------------- |
| exclusive (default)     | accept        | `BUSY_EXCLUSIVE`         | `BUSY_KEY`                  |
| `nonexclusive: true`    | accept        | `BUSY_EXCLUSIVE`         | accept (unlimited concurrency) |

A held key is released when its job ends (any outcome). The set of currently-held
keys is visible via the [`status`](#status) endpoint's `held_keys` map. An
admission rejection is also published as a `job-rejected` `svc` lifecycle event
(see [events.md](events.md)).

## Allowlist & script gates (the security boundary)

The daemon's trust boundary is two layers: **the project allowlist** and **the
NATS credential's subject permissions** (server-enforced — anyone who can publish
on the service subjects can run jobs on the host; scope the creds on the server).
NATS micro does not expose the caller's identity to the service, so `submitted_by`
is advisory only; authoritative attribution is the NATS server's audit domain.

### Project allowlist (`PROJECT_NOT_ALLOWED`)

`service.yaml` declares the primary `project_root` and the `allow` list of every
root a request may target (the project plus its sibling component repos):

```yaml
project_root: /abs/path/main-project
allow:
  - /abs/path/main-project
  - /abs/path/component-repo-a
```

- Every `allow` entry (and `project_root`) must be an absolute path to an
  existing git working tree — validated at daemon startup.
- `project_root` is always implicitly allowed (added to `allow` if absent).
- A `*.run` request's `project_root` must be an **exact** match against the
  cleaned allowlist (no prefix/subdirectory match) — anything else is rejected
  `PROJECT_NOT_ALLOWED`, checked before request shape or kind.

### Script gates (`script.run`, D5)

Two `service.yaml` flags (both default `false`) bound `script.run`:

| Flag                   | Effect                                                                                                   |
| ---------------------- | -------------------------------------------------------------------------------------------------------- |
| `allow_script_source`  | Enables the `script_source` variant (arbitrary Go on the daemon host). Off → `script_source` is rejected `VALIDATION`. |
| `force_script_sandbox` | Appends PLAN-15's interpreter-level `--sandbox` to every script job. Setting it without `allow_script_source` is a startup config error. |

- `script_path` is always accepted, but the resolved path (relative paths
  resolve against `project_root`) must be an existing file **inside an
  allowlisted root**; otherwise `VALIDATION`. This is the filesystem half of the
  allowlist boundary — distinct from the `project_root` exact-match check.
- `script_source` is delivered on the child's stdin (`ape script -`), never on
  the argv.

## See also

- [events.md](events.md) — the `ape.svc` root in the full NATS subject taxonomy,
  and the `svc`-kind lifecycle events (`job-accepted`/`job-rejected`/`job-end`)
  the daemon publishes for each job.
- [How to run ape as a service](../how-to/run-ape-as-a-service.md) — starting the
  daemon, `nats req` examples, exclusivity recipes, and locking it down.
- [CLI reference](cli.md) — `ape service` flags.

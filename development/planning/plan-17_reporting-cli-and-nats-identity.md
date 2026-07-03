---
plan_id: PLAN-17
created_at: 2026-07-02
status: proposed
tags:
  - new-command
  - nats
  - traceability
  - identity
  - logging
  - metrics
  - transcripts
summary: Four agent-invocable reporting commands — `ape event`, `ape log`, `ape metrics`, `ape transcript` — that publish over NATS with the user identity derived from the NATS credential (decoded from the .creds user JWT, baked into the subject) and the Claude Code session ID resolved automatically or passed explicitly. The same internal reporters are called by the PTY runners (pipeline/task/command/script) at finalize, so a supervised run and an agent self-reporting from a plain local interactive Claude Code session emit byte-compatible shapes on the same subject taxonomy. Builds directly on PLAN-13 (natsconn/eventing/blobstore) and PLAN-10 (per-turn scan + session-set discovery); requires the PLAN-13 amendments (identity in natsconn, user token in the subject taxonomy, session_id in payloads) to land inside PLAN-13 itself, since its subjects are an additive-only external contract from day one.
origin:
  - 2026-07-02 user request — "an 'event' command … send an event using NATS … must include user ID based on the NATS credential being used so we could have full traceability, could be baked in the subject"; "a 'transcript' command … upload the transcript of a session"; "a 'log' command … send logs to a centralized logging system, including user ID and session information"; "a 'metrics' command … send performance metrics … including session duration, turns or similar, tokens/model and timestamp so we could convert to Claude Code API prices any moment". Dual mode required: used by the PTY runner itself for pipeline/task/command/script runs, AND usable by an agent running claude code in a normal standard local interactive session that just uses the ape CLI with the NATS credentials.
  - 2026-07-02 gap review of PLAN-9…16 — PLAN-13 builds the right infrastructure (natsconn, publisher, blob store) but exposes it only as internal run-finalize taps; no CLI command exists for any of the four verbs, and no plan extracts user identity from the NATS credential. PLAN-10 produces exactly the metrics data required (per-turn timestamp, model, tokens, turns) but nothing publishes it. Session ID is already first-class in artifacts today: manifest per-step `session_id`/`parent_session_id` (`internal/pipeline/manifest.go:127`), runlog hook events, and the shipped `ape task` envelope (`internal/apecmd/task.go:220`).
---

# PLAN-17: Reporting CLI — event/log/metrics/transcript + NATS identity

## Goal

An agent (or a human, or a script) holding nothing but a NATS `.creds` file
can report from any Claude Code session:

```
ape event status --payload '{"phase":"implement","pct":60}'
ape log info "migration step 3 complete"
ape metrics                       # scan + publish this session's usage
ape transcript upload             # blob-upload this session's transcript set
```

Every message carries the **user identity decoded from the NATS credential**
(baked into the subject, enforceable server-side via publish permissions) and
the **Claude Code session ID** (explicit flag or auto-resolved). The PTY
runners call the same internal reporters at run finalize, so supervised runs
and self-reporting agents are indistinguishable to a consumer.

## Why now

- The user requirement is dual-mode by construction: PLAN-13's run-finalize
  taps cover supervised runs, but an agent in a plain local interactive
  claude session has no way to report at all. This plan is that missing half.
- PLAN-13's subject taxonomy is declared an additive-only external contract
  from day one — the user token in the subject **cannot be retrofitted**.
  The identity design must exist before PLAN-13 lands (hence the PLAN-13
  amendments this plan depends on).
- PLAN-10's per-turn records (timestamp, model, tokens, turns) are exactly
  the payload `ape metrics` needs to keep runs convertible to Claude Code
  API prices at any moment; publishing them is pure surface on top.

## Non-goals

- No consumer/dashboard tooling — ape publishes; `nats sub` / downstream
  systems consume.
- No log *file* shipping or tailing — `ape log` sends discrete structured
  records, not streams.
- No new storage backends — `ape transcript` reuses PLAN-13's `blobstore`
  unchanged.
- No identity minting or account management — the operator issues `.creds`;
  ape only decodes and propagates what the credential already asserts.
- No offline queueing/spooling in v1 — no NATS, no report (exit 2 with a
  clear error). Revisit if field use demands a spool-and-forward mode.

## Design

### D1: Identity from the NATS credential (PLAN-13 amendment, consumed here)

`internal/natsconn` gains `Identity() (Identity, error)`: parse the user JWT
out of the `.creds` file (standard `-----BEGIN NATS USER JWT-----` block),
decode its claims **locally, offline** — no server round-trip — and expose
`{Name, Subject (user public key), SubjectToken}`. `SubjectToken` is the
sanitized token used in subjects: JWT `name` claim lowercased/slugged, falling
back to the user public key when the name is empty or collides with token
syntax (`.`, `*`, `>`, whitespace → `-`).

**Server-enforceable traceability:** because the token derives
deterministically from the credential, an operator can issue creds whose
publish permissions are scoped to `ape.*.<token>.>` — then the identity in
the subject is enforced by the NATS server, not merely self-reported. The
how-to documents this pattern (nsc example included).

### D2: Session resolution (`internal/sessionref`)

All four commands resolve the target session in this order:

1. `--session-id <uuid>` — explicit.
2. `--transcript <path>` — explicit transcript file; session id parsed from
   the filename.
3. `APE_SESSION_ID` env — set by ape's own runners for in-run reporting, and
   settable by hooks/wrappers in plain claude sessions.
4. Auto-detect: newest `*.jsonl` under the `~/.claude/projects/<cwd-slug>/`
   directory for the current project (the `ScanLatestSession` heritage,
   PLAN-10 D2's `SessionFiles` for the full main+subagents set).

Resolution failures are exit 2 with the candidates listed. The resolved
`session_id` appears in every payload and in the subject where the taxonomy
carries an id token.

### D3: Command surfaces

Shared flags: `--nats-url/--nats-creds` (PLAN-13 D1 resolution order),
`--session-id/--transcript`, `--cwd`, `--output-format human|json`,
`--quiet`. All diagnostics to **stderr**; stdout carries only the result
object (stdout discipline per the PLAN-13 amendment — the eval parses
envelopes from stdout).

```
ape event <event> [--payload <json>|@file|-]      # '-' = stdin
```

Publishes on `ape.evt.<user>.<project>.session.<session-id>.<event>` —
the PLAN-13 taxonomy with kind `session` (reserved in the PLAN-13 amendment
for standalone/agent-initiated reporting; supervised runs keep
`pipeline|task|command|script` kinds and their run ids). `<event>` is a
caller-chosen token (validated: `[a-z0-9-]+`). Payload wrapped in the
versioned envelope `{"v":1, "ts", "user", "project", "session_id", "event",
"payload"}`.

```
ape log <level> <message> [--field k=v ...]       # level ∈ debug|info|warn|error
```

Subject `ape.log.<user>.<project>.<session-id>.<level>`. Payload
`{"v":1, "ts", "user", "project", "session_id", "level", "msg", "fields"}`.
Centralized-logging consumers subscribe `ape.log.>` (or per-user/project
subtrees — the subject *is* the routing key).

```
ape metrics [--run-id <id>]                       # session snapshot by default
```

Scans the resolved session set (PLAN-10 D1/D2: main + subagents) and
publishes `ape.metrics.<user>.<project>.<session-id>` with
`{"v":1, "ts", "user", "project", "session_id", "duration_seconds",
"num_turns", "per_model": {model: {input_tokens, output_tokens,
cache_read_input_tokens, cache_creation_5m, cache_creation_1h, turns,
cost_usd}}, "first_turn_at", "last_turn_at", "claude_code_version"}` — every
field needed to (re)price against Claude Code API rates at any later moment,
because per-turn timestamps and models are preserved in the per_model split.
`--run-id` publishes a run's manifest totals instead (reader over
`manifest.yaml`, same envelope with `run_id` populated). Republishing is
idempotent by design: consumers key on `(session_id, ts)` snapshots.

```
ape transcript upload [--store nats-object|uri-offload]
```

Uploads the resolved session set through PLAN-13 D3's `blobstore.Store`
(content-addressed, zstd, idempotent — re-upload is a cheap no-op). Result
object: `{session_id, files: [{path, session_id, digest, uri, bytes}]}`.
Publishes a companion `ape.evt.<user>.<project>.session.<session-id>.transcript-uploaded`
event carrying the digest map so consumers learn about the blobs without
polling the store.

### D4: Runner integration — one reporter, two entry points

`internal/reporting` owns the four report shapes. The CLI commands are thin
fronts; the PTY runners call the same functions:

- Run finalize (pipeline/task/command/script) → `reporting.Metrics` per step
  session + run totals, `reporting.TranscriptUpload` when
  `--upload-transcripts` (PLAN-13's flag becomes a front for this),
  lifecycle events as PLAN-13 D2 already specifies.
- Runners export `APE_SESSION_ID` (and existing NATS env) into the claude
  child's environment, so an agent *inside a supervised run* that calls
  `ape event`/`ape log` reports into the correct session without flags.
- Subjects from supervised runs use the run's kind and id; standalone use
  kind `session`. Payload schemas are identical — documented once in
  `docs/reference/events.md`.

### D5: Exit codes and failure semantics

Unlike the runner taps (fire-and-forget, never fail a run), the standalone
commands are *for* reporting — failure must be visible: `0` published/uploaded
· `1` NATS publish/upload failed (connect ok) · `2` usage error, no NATS
config, or session unresolvable. Registered in PLAN-9's exit-code table.

## Steps

1. PLAN-13 amendments land first (inside PLAN-13's own PRs): `Identity()` in
   natsconn, user token + `session` kind in the subject taxonomy, `user` +
   `session_id` in payload schemas, stdout discipline.
2. `internal/sessionref` (D2) — pure fs logic against fake `~/.claude` trees;
   table tests for the four-step resolution order.
3. `internal/reporting` + `ape event` / `ape log` (embedded nats-server
   tests: subject shape, payload schema, identity token, permission-denied
   surfaces as exit 1).
4. `ape metrics` (fixture JSONL from PLAN-10's testdata; assert the pricing
   round-trip: payload × price table = scanner cost).
5. `ape transcript upload` (embedded object-store tests; idempotent re-run;
   companion event).
6. Runner integration (D4): `APE_SESSION_ID` export + finalize calls; assert
   a supervised run and a standalone invocation produce schema-identical
   payloads.
7. Docs: `how-to/report-from-a-session.md` (agent usage — the "plain local
   claude session with only ape + creds" walkthrough, including the
   server-side `ape.*.<token>.>` permission pattern),
   `reference/events.md` additions (log/metrics subjects), CLI regen,
   README rows.

## Acceptance

- With only a `.creds` file configured and no ape-supervised run, from inside
  a normal interactive claude session: all four commands succeed against an
  embedded server; every subject carries the token decoded from that
  credential; every payload carries the session id of the *current* session
  (auto-resolved).
- `ape metrics --output-format json` for a fixture session reprices exactly
  to the scanner's `cost_usd` using the published per-model tokens and the
  price table — the "convert to API prices any moment" requirement.
- `ape transcript upload` twice → second run all no-ops (dedup), same
  digests; blobs contain the main + subagent files.
- A supervised `ape task` run and a standalone `ape metrics` for the same
  session publish payloads that differ only in subject kind/id fields.
- Creds scoped to `ape.*.<token>.>`: publishing succeeds; a forged
  `--debug-subject-user` override (test-only seam) is rejected by the server
  — demonstrating server-enforced identity.
- No NATS config → exit 2 with a one-line pointer; a supervised run in the
  same state still completes normally (runner taps stay fire-and-forget).

## Risks / notes

- **JWT name collisions:** two creds with the same `name` claim map to the
  same subject token; the public-key fallback disambiguates but is ugly.
  The how-to tells operators to issue unique names; the payload always
  carries the full public key for exact attribution.
- **Session auto-detect is heuristic** (newest transcript for the cwd);
  concurrent sessions in one project can misattribute. The env var
  (`APE_SESSION_ID`, settable by a SessionStart hook) is the reliable path;
  documented as the recommended setup for agent self-reporting.
- **Subjects are a contract:** `ape.log.` and `ape.metrics.` roots join
  `ape.evt.` as versioned, additive-only surfaces (payloads carry `"v"`).
- Standalone commands add no new dependencies beyond PLAN-13's (nats.go,
  klauspost/compress); JWT decode is base64 + JSON, stdlib-only — do not
  pull in a JWT library for this.

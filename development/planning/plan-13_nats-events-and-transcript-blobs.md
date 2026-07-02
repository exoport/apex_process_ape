---
plan_id: PLAN-13
created_at: 2026-07-02
status: proposed
tags:
  - nats
  - eventing
  - transcripts
  - blob-store
  - content-addressed
summary: Two shared infrastructure pieces. (1) Progress eventing — an optional NATS connection (URL + .creds file, via flag/env/_apex config) over which every pipeline/task/command run publishes structured JSON progress events (run/stage/step lifecycle with telemetry, hooks, commits, errors), tapped from the existing `pipeline.Observer` and `orchestrator.RuntimeEvent` streams; fire-and-forget, never blocks or fails a run. (2) Transcript blob upload — content-addressed (sha256), zstd-compressed upload of a run's full transcript set (main + subagent sessions, from PLAN-10's discovery/copy), cxdb-style idempotent semantics, behind a pluggable store interface with two backends: NATS JetStream Object Store (staging/quick storage) and a URI-request offload flow (NATS request returns an upload URI; ape performs an HTTPS PUT) so large fleets can land blobs in real object storage while the wire stays NATS+HTTPS.
origin:
  - 2026-07-02 user request — optional NATS credentials for remote-cluster progress events; transcript upload "as blobs in a similar way that is done with github.com/strongdm/cxdb".
  - 2026-07-02 user decisions (Q&A during planning) — concern about blob sizes inside NATS objects; use local session data to dimension; preferred shape: NATS request → Blob URI → HTTPS upload, with NATS Object Store as quick storage and an offload service; pluggable backends sharing cxdb's core concepts.
  - 2026-07-02 dimensioning on this machine — 442 main transcripts, 149 MB total: p50 206 KB, p90 0.5 MB, p99 3.3 MB, max 8.2 MB; 236 subagent files, p50 256 KB, max 0.9 MB. JSONL zstd-compresses ~5–10×, so worst case ~1–2 MB compressed. Comfortably within JetStream Object Store chunking; the URI-offload path is about fleet aggregation, not single-blob size.
  - 2026-07-02 research — cxdb (Apache-2.0): BLAKE3-256 CAS over zstd-compressed payloads, idempotent PUT_BLOB (client precomputes hash, server verifies + dedupes), GET /v1/blobs/:hash; Go client `github.com/strongdm/cxdb/clients/go`. NATS: `.creds` via `nats.UserCredentials`; JetStream Object Store chunks arbitrarily large objects. `internal/bridge/ipc/ipc.go:6-8` already earmarks NATS as a future transport.
---

# PLAN-13: NATS progress events + content-addressed transcript blobs

## Goal

Any ape run (pipeline / task / command, local one-shot or service-spawned)
can, when configured with a NATS cluster, (a) stream structured progress
events that a remote consumer can follow live, and (b) at run end, upload its
complete transcript set as deduplicated, content-addressed blobs. Both are
strictly optional and strictly non-blocking: an unreachable cluster degrades
to today's local-only behavior with a warning.

## Why now

PLAN-14 (`ape service`) is the consumer that makes this urgent — remote job
submission is useless without remote progress visibility. Landing the
publisher and blob store as shared infra first keeps the service plan thin.

## Non-goals

- No queue/worker semantics, no request/reply endpoints (PLAN-14).
- No cxdb server deployment and no turn-DAG/fork model — we borrow the CAS
  concepts (client-side hash, idempotent put, dedup), not the architecture.
  A cxdb *backend* for the store interface is future work if wanted.
- No transcript *download/browse* tooling (a `ape transcripts` viewer is a
  natural follow-up, out of scope).
- Not an events *consumer* — ape publishes; dashboards subscribe with
  standard NATS tooling.

## Design

### D1: Connection config (shared by events, blobs, and PLAN-14)

Resolution order: flags → env → project config.

```
--nats-url  / APE_NATS_URL   / _apex/config.yaml: ape.nats.url
--nats-creds/ APE_NATS_CREDS / _apex/config.yaml: ape.nats.creds
```

`internal/natsconn`: one small package producing a connected `*nats.Conn`
(`nats.UserCredentials(credsFile)`, name `ape/<version>`, reconnect with
capped backoff, `nc.Drain()` on shutdown). New direct dependency:
`github.com/nats-io/nats.go`. No NATS config → all downstream features
silently disabled.

### D2: Event publisher (`internal/eventing`)

Subjects: `ape.evt.<project>.<kind>.<id>.<event>` where `<project>` is a
sanitized project-root slug, `<kind>` ∈ `pipeline|task|command|script`,
`<id>` is the run/command id, `<event>` ∈
`run-start|stage-start|step-start|step-end|stage-end|hook|commit|error|run-end`.
Prefix `ape.evt` overridable (`--events-subject-prefix`).

Payloads: versioned JSON (`{"v":1, "ts":…, "project":…, "run_id":…, …}`);
`step-end` carries the PLAN-10 per-model telemetry block; `run-end` carries
manifest totals + transcript blob hashes (D3). Schemas documented in
`docs/reference/events.md`.

Taps (no runner surgery — both interfaces exist):
- `pipeline.Observer` wrapper (compose with the UI observer) → lifecycle
  events.
- `orchestrator.RuntimeEvent` subscription (same channel the SSE hub
  consumes, `hub.go:171-190` pattern) → hook/call events for interactive
  runs.

Delivery: buffered channel + single publisher goroutine; drop-with-counter on
overflow (log once at run end: "N events dropped"); publish errors logged,
never propagated. This mirrors the SSE broker's drop-on-full discipline.

### D3: Blob store (`internal/blobstore`)

```go
type Digest struct{ Algo, Hex string }        // "sha256", …
type Store interface {
    Has(ctx, Digest) (bool, error)
    Put(ctx, Digest, size int64, r io.Reader) (uri string, err error) // idempotent
}
```

- **Content addressing:** sha256 over the *uncompressed* payload (cxdb uses
  BLAKE3-256; sha256 keeps us in the stdlib and matches the existing
  `fileDigest` helper — the concept, hash-then-idempotent-put, is what we're
  borrowing). Payloads stored zstd-compressed
  (`github.com/klauspost/compress/zstd`, level 3 like cxdb).
- **Backend 1 — NATS Object Store:** bucket `ape-transcripts` (created if
  absent), object name = `<algo>/<hex>`; `Has` = object info lookup, `Put`
  skips existing. Dimensioning above says even the 8 MB outlier is fine
  (chunked transfer).
- **Backend 2 — URI-request offload:** `Put` sends a NATS request to
  `ape.blob.uri-request` with `{digest, size, compressed_size,
  content_type:"application/x-ndjson+zstd", project, run_id}`; a
  user-operated offload service replies `{status:"upload", uri, method,
  headers}` or `{status:"exists"}`; ape performs the HTTPS PUT (presigned-URL
  pattern — S3/GCS/azure all fit). The offload service itself is **out of
  ape's tree**; ape ships the client half plus a documented request/reply
  contract. (A reference offload service can live in a sibling repo later.)
- Backend selection: `_apex/config.yaml: ape.blob.backend: nats-object|uri-offload`
  + `--transcript-store`. `--upload-transcripts` (or config) turns the
  feature on.

Upload set per run (from PLAN-10's D2/D4): every copied transcript file
(main + subagents), uploaded at run finalize; resulting
`{file → digest, uri}` map written into the manifest
(`transcript_blobs:` block) and the `run-end` event. Failures: warn + record
`upload_status: failed` — never fail the run.

## Steps

1. `internal/natsconn` + config resolution (unit tests with a
   `nats-server -js` test instance via testcontainer-style helper or
   `go test` guard-skipped when no local server; add `nats-server` to CI or
   use `github.com/nats-io/nats-server/v2/test` embedded server — prefer
   embedded, it's the standard trick and keeps CI hermetic).
2. `internal/eventing` publisher + Observer/RuntimeEvent taps + fixture
   assertions on subject/payload shapes.
3. `internal/blobstore` interface + NATS Object Store backend + embedded
   -server tests (hash verify, dedup, idempotency).
4. URI-offload backend + contract doc (`docs/reference/blob-offload.md`);
   test against a stub HTTP server.
5. Wire into run finalize (pipeline/task/command) + manifest block.
6. Docs: `how-to/publish-progress-to-nats.md` (with a `nats sub 'ape.evt.>'`
   example), `how-to/upload-transcripts.md`, `reference/events.md`.

## Acceptance

- With an embedded test server: a fixture pipeline run publishes the full
  lifecycle sequence on the expected subjects; `run-end` includes totals.
- Same run with `--upload-transcripts`: transcripts land in the object store
  keyed by digest; re-running the upload is a no-op (dedup); manifest lists
  digests.
- With NATS unreachable: run completes normally; single warning; manifest
  `upload_status: failed`.
- URI-offload backend against the stub: correct request payload, HTTPS PUT
  performed, `exists` short-circuits.

## Risks / notes

- New dependency surface (nats.go, klauspost/compress) — both mature and
  pure-Go; keep them out of the binary's cold path (lazy connect).
- Subject taxonomy is an external contract from day one — version the
  payloads (`"v":1`) and document that subjects are additive-only.
- Transcripts contain the session's full content; uploading them is
  publishing to whatever the cluster/offload target is. The how-to must say
  this plainly and the feature stays opt-in.

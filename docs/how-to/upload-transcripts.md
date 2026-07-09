# How to upload run transcripts as content-addressed blobs

At the end of a run, ape can upload the run's full transcript set — the main
claude session plus every sub-agent session — as deduplicated,
content-addressed, zstd-compressed blobs. Re-uploading identical content is a
cheap no-op (dedup by digest). It is **opt-in** and **never fails a run**: an
upload problem downgrades the recorded status, it doesn't abort.

> **Uploading transcripts publishes the session's full content** to whatever the
> cluster / offload target is — prompts, tool output, code. Keep it off unless
> you intend to publish, and point it only at storage you control.

## Turn it on

Requires a NATS connection (see [publish progress to
NATS](publish-progress-to-nats.md)):

```bash
export APE_NATS_URL=nats://nats.example:4222
export APE_NATS_CREDS=~/.config/ape/user.creds

ape pipeline design --upload-transcripts
# or:
APE_UPLOAD_TRANSCRIPTS=1 ape task apex-create-prd --agent apex-agent-pm
```

## Content addressing & dedup

- The digest is **`sha256` over the uncompressed transcript**; the stored payload
  is **zstd-compressed** (level 3). The digest is stable across compression, so
  two runs that produced byte-identical transcripts upload once.
- After upload, the run `manifest.yaml` gains a `transcript_blobs:` block (file
  base name → `{session_id, digest, uri, bytes}`) and `upload_status:`
  (`ok` | `partial` | `failed`), and the `run-end` NATS event carries the same
  digest map. The block is additive under `schema_version: 2`.

## Backends

Choose with `--transcript-store` / `APE_TRANSCRIPT_STORE`:

| Backend | Value | What it does |
| ------- | ----- | ------------ |
| NATS JetStream Object Store (default) | `nats-object` | Stores blobs in the `ape-transcripts` bucket, object name `<algo>/<hex>`. Chunked transfer handles large transcripts. Good for staging / quick storage. |
| URI-request offload | `uri-offload` | Sends a NATS request to `ape.blob.uri-request`; a user-operated service replies with an upload URI (or "already have it"); ape does the HTTPS PUT. Lets large fleets land blobs in real object storage (S3/GCS/Azure) while the wire stays NATS + HTTPS. |

```bash
ape pipeline design --upload-transcripts --transcript-store uri-offload
```

The offload service itself is **out of ape's tree** — ape ships the client half
and the documented request/reply contract. See
[blob-offload.md](../reference/blob-offload.md).

## Failure semantics

- NATS unreachable / no URL configured, but upload was requested → the run
  completes normally, one stderr warning, and `manifest.yaml` records
  `upload_status: failed`.
- Some transcripts upload and some fail → `upload_status: partial` (the ones that
  landed are recorded).
- Nothing hits stdout — all diagnostics go to stderr.

## See also

- [events.md](../reference/events.md) — the `run-end` payload's `transcript_blobs`
  shape and `ape.blob.uri-request` contract.
- [blob-offload.md](../reference/blob-offload.md) — the URI-offload request/reply
  contract for building a service.
- [pipeline-run-manifest.md](../reference/pipeline-run-manifest.md) — the manifest
  schema the `transcript_blobs:` block extends.

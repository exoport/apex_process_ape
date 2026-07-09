# Transcript blob offload — request/reply contract

The `uri-offload` transcript backend (PLAN-13 D3) lets ape upload transcript
blobs into real object storage (S3/GCS/Azure) without ape holding any storage
credentials: ape sends a NATS **request** describing the blob, a user-operated
**offload service** replies with a presigned upload URI (or "already have it"),
and ape performs the HTTPS PUT itself.

**ape ships the client half only.** The offload service is out of ape's tree;
this page is the contract you implement to build one. A reference service may
live in a sibling repo later.

## Wire

- **Subject:** `ape.blob.uri-request` (request/reply).
- **Encoding:** JSON both directions.
- **Payload media type:** the blob ape uploads is `application/x-ndjson+zstd`
  (newline-delimited JSON transcript, zstd-compressed).

### Request (ape → service)

```json
{
  "digest": "sha256:3b1f…",          // content address (sha256 of the UNCOMPRESSED transcript)
  "size": 210433,                     // uncompressed bytes
  "compressed_size": 28751,           // zstd-compressed bytes (what the PUT body will be)
  "content_type": "application/x-ndjson+zstd",
  "project": "myproject",             // sanitized project slug
  "run_id": "20260709-141501-ab12cd3" // the ape run id
}
```

### Reply (service → ape)

Upload wanted:

```json
{
  "status": "upload",
  "uri": "https://bucket.s3.amazonaws.com/ape/sha256/3b1f…?X-Amz-Signature=…",
  "method": "PUT",                    // optional; defaults to PUT
  "headers": { "x-amz-meta-project": "myproject" }  // optional; set verbatim on the PUT
}
```

Already stored (dedup short-circuit — ape performs no PUT):

```json
{ "status": "exists", "uri": "https://bucket.s3.amazonaws.com/ape/sha256/3b1f…" }
```

## ape's behavior

1. Content-address the transcript (`sha256` of the uncompressed bytes),
   zstd-compress it, and send the request above.
2. On `status: "exists"` → record the returned `uri`, upload nothing.
3. On `status: "upload"` → HTTPS `<method>` the **compressed** payload to `uri`
   with `Content-Type: application/x-ndjson+zstd` plus any `headers` from the
   reply. A non-2xx response marks that blob failed.
4. Any other `status`, a NATS request timeout, or a transport error marks the
   blob failed; the run still completes (`upload_status: failed` / `partial` in
   the manifest).

## Implementation notes for a service

- **Dedup on `digest`.** It is the object key you should use; a `HEAD`/exists
  check against your bucket answers `exists` vs `upload` cheaply.
- **Presign narrowly.** Scope each upload URI to the single object key and a
  short TTL; ape uses it immediately.
- **`content_type` is fixed** (`application/x-ndjson+zstd`); if you re-serve
  blobs, set `Content-Encoding`/`Content-Type` accordingly.
- **Verify on ingest (optional).** You can recompute `sha256` after decompressing
  to confirm the client's `digest` before trusting it as the key.

## See also

- [How to upload transcripts](../how-to/upload-transcripts.md) — turning the
  feature on and choosing the backend.
- [events.md](events.md) — where `ape.blob.uri-request` sits in the taxonomy.

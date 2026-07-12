# `ape update` verification fixtures — real v0.0.43 release

These are **real artifacts from the published v0.0.43 GitHub release**, used by the
hermetic `ape update` verification tests (`update_verify_test.go`). They exercise
the exact production offline-verification path against a genuine keyless-cosign
signature, with no network access at test time.

| File                      | What it is                                                                 |
| ------------------------- | -------------------------------------------------------------------------- |
| `ape_checksums.txt`       | The signed SHA256 manifest shipped with v0.0.43 (the verification artifact).|
| `ape_checksums.txt.bundle`| A Sigstore protobuf bundle (`v0.3`) over `ape_checksums.txt`.               |

## Why the bundle is deterministic (won't expire)

Verification uses the **Rekor integrated timestamp** (the signing time,
2026-07-12) as the "current time" for Fulcio-certificate path validation — not
the wall clock. The Fulcio cert's ~10-minute validity window is therefore checked
against 2026-07-12 forever, so the fixture verifies identically regardless of when
the test runs. The embedded public-good `trusted_root.json` retains historical
key material with validity windows, so a trusted-root refresh does not break it.

## Provenance / how to regenerate

v0.0.43 shipped the *detached* signing artifacts `ape_checksums.txt.sig`
(base64 DER signature) and `ape_checksums.txt.pem` (base64 Fulcio cert) — it
predates the `--new-bundle-format` bundle that v0.0.44+ ship natively. The bundle
above was assembled once from those detached artifacts plus the public Rekor
transparency-log entry for the signature:

1. `sha256:$(sha256sum ape_checksums.txt)` → look up the Rekor UUID:
   `POST https://rekor.sigstore.dev/api/v1/index/retrieve {"hash":"sha256:<sum>"}`
2. Fetch the entry: `GET https://rekor.sigstore.dev/api/v1/log/entries/<uuid>`
3. Convert it with `rekor/pkg/tle.GenerateTransparencyLogEntry`, then assemble a
   `protobuf-specs` `bundle/v1.Bundle` with the leaf certificate, the tlog entry,
   and a `MessageSignature{digest: sha256(checksums), signature: <DER sig>}`, and
   `protojson`-marshal it.

Since v0.0.44 the release itself ships `ape_checksums.txt.bundle` directly (see
`.goreleaser.yaml`), so future fixtures can just be downloaded from the release.

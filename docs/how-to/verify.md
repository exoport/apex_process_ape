# How to verify a release artifact

Every tagged release of `ape` is signed with [Cosign](https://docs.sigstore.dev/cosign/overview/) using GitHub Actions OIDC + Sigstore Fulcio (keyless). The signature attests that the release was built and uploaded by this repository's `release.yml` workflow on the corresponding tag.

You don't need to verify to use `ape` — install via the [release tarball](install.md) and you'll get the same binary. But if you ship `ape` to others, into CI, or into a regulated environment, a verify step closes the supply-chain gap: even if GitHub itself were compromised and a tampered tarball substituted, the signature on the checksums file wouldn't match.

## What gets signed

Each release publishes:

- `ape_<os>_<arch>.tar.gz` (or `.zip` on Windows) — the binary archive.
- `ape_checksums.txt` — SHA-256 of every archive.
- **`ape_checksums.txt.bundle`** — a Sigstore bundle over the checksums file: the short-lived Fulcio certificate, the signature, the certificate-transparency SCT, and the Rekor inclusion proof, all in one file. Verifiable **fully offline** against the Sigstore public-good trusted root.

The pattern matches kubectl, gh CLI, and most goreleaser projects: sign the checksums file, then verify each tarball's hash against the signed file. One signature covers the whole release.

> Releases up to and including `v0.0.43` shipped the older detached pair `ape_checksums.txt.sig` + `ape_checksums.txt.pem` instead of the bundle. To verify one of those, use the pre-bundle form: `cosign verify-blob --certificate ape_checksums.txt.pem --signature ape_checksums.txt.sig …` (no `--bundle`/`--new-bundle-format`).

## Prerequisites

```bash
# Install cosign (Linux/macOS — see https://docs.sigstore.dev/cosign/installation/ for other platforms).
curl -fsSL https://github.com/sigstore/cosign/releases/latest/download/cosign-linux-amd64 -o /tmp/cosign
sudo install -m 0755 /tmp/cosign /usr/local/bin/cosign
cosign version
```

## Verify a release

```bash
VERSION=v0.1.0
ASSET=ape_linux_amd64.tar.gz
BASE="https://github.com/exoport/apex_process_ape/releases/download/${VERSION}"

# 1. Fetch the archive, the checksums file, and the signature bundle.
curl -fsSL -o "${ASSET}"                "${BASE}/${ASSET}"
curl -fsSL -o ape_checksums.txt         "${BASE}/ape_checksums.txt"
curl -fsSL -o ape_checksums.txt.bundle  "${BASE}/ape_checksums.txt.bundle"

# 2. Verify the signature bundle on the checksums file.
cosign verify-blob \
  --bundle ape_checksums.txt.bundle \
  --new-bundle-format \
  --certificate-identity-regexp \
    "^https://github\.com/exoport/apex_process_ape/\.github/workflows/release\.yml@refs/tags/v.*$" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ape_checksums.txt

# 3. Verify the archive's SHA-256 against the (now-trusted) checksums file.
sha256sum -c ape_checksums.txt --ignore-missing
```

If both steps print `Verified OK` and `<asset>: OK`, the binary is authentic.

`ape update` performs this exact chain automatically on every self-update — it downloads the archive, the checksums file, and the bundle; verifies the bundle offline against an embedded copy of the Sigstore trusted root (pinning this repo's `release.yml` identity + the GitHub Actions OIDC issuer for the resolved tag); then verifies the archive's SHA-256 against the trusted checksums before replacing the binary. No `cosign` binary is needed for `ape update`.

## What the verify command checks

- **`--certificate-identity-regexp`** — pins the signer to this repo's `release.yml` workflow on a `v*` tag. A signature minted from any other workflow (or a fork, or a different repo) will be rejected.
- **`--certificate-oidc-issuer`** — pins the OIDC issuer to GitHub Actions. A signature minted with a different issuer (e.g., a developer's personal Sigstore login) will be rejected.
- **Rekor transparency log** — `cosign verify-blob` also checks that the signature was logged to Rekor at sign time. A signature created off-log won't verify.

## Eval-side automation

The companion eval repo's `make ape-release` target runs this verification automatically before unpacking the binary. Set `APE_RELEASE_SKIP_VERIFY=1` to bypass during local-loop development if `cosign` isn't available — but CI runs should always verify.

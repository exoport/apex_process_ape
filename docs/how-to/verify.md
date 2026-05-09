# How to verify a release artifact

Every tagged release of `ape` is signed with [Cosign](https://docs.sigstore.dev/cosign/overview/) using GitHub Actions OIDC + Sigstore Fulcio (keyless). The signature attests that the release was built and uploaded by this repository's `release.yml` workflow on the corresponding tag.

You don't need to verify to use `ape` — install via the [release tarball](install.md) and you'll get the same binary. But if you ship `ape` to others, into CI, or into a regulated environment, a verify step closes the supply-chain gap: even if GitHub itself were compromised and a tampered tarball substituted, the signature on the checksums file wouldn't match.

## What gets signed

Each release publishes:

- `ape_<os>_<arch>.tar.gz` (or `.zip` on Windows) — the binary archive.
- `ape_checksums.txt` — SHA-256 of every archive.
- **`ape_checksums.txt.sig`** — Cosign signature of the checksums file.
- **`ape_checksums.txt.pem`** — short-lived Fulcio cert that minted the signature.

The pattern matches kubectl, gh CLI, and most goreleaser projects: sign the checksums file, then verify each tarball's hash against the signed file. One signature covers the whole release.

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
BASE="https://github.com/diegosz/apex_process_ape/releases/download/${VERSION}"

# 1. Fetch the archive, the checksums file, and the signature material.
curl -fsSL -o "${ASSET}"                "${BASE}/${ASSET}"
curl -fsSL -o ape_checksums.txt         "${BASE}/ape_checksums.txt"
curl -fsSL -o ape_checksums.txt.sig     "${BASE}/ape_checksums.txt.sig"
curl -fsSL -o ape_checksums.txt.pem     "${BASE}/ape_checksums.txt.pem"

# 2. Verify the signature on the checksums file.
cosign verify-blob \
  --certificate ape_checksums.txt.pem \
  --signature   ape_checksums.txt.sig \
  --certificate-identity-regexp \
    "^https://github\.com/diegosz/apex_process_ape/\.github/workflows/release\.yml@refs/tags/v.*$" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ape_checksums.txt

# 3. Verify the archive's SHA-256 against the (now-trusted) checksums file.
sha256sum -c ape_checksums.txt --ignore-missing
```

If both steps print `Verified OK` and `<asset>: OK`, the binary is authentic.

## What the verify command checks

- **`--certificate-identity-regexp`** — pins the signer to this repo's `release.yml` workflow on a `v*` tag. A signature minted from any other workflow (or a fork, or a different repo) will be rejected.
- **`--certificate-oidc-issuer`** — pins the OIDC issuer to GitHub Actions. A signature minted with a different issuer (e.g., a developer's personal Sigstore login) will be rejected.
- **Rekor transparency log** — `cosign verify-blob` also checks that the signature was logged to Rekor at sign time. A signature created off-log won't verify.

## Eval-side automation

The companion eval repo's `make ape-release` target runs this verification automatically before unpacking the binary. Set `APE_RELEASE_SKIP_VERIFY=1` to bypass during local-loop development if `cosign` isn't available — but CI runs should always verify.

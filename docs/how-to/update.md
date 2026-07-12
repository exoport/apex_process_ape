# How to update ape

ape ships with self-update built in. `ape update` checks the latest GitHub release, downloads the matching binary for your platform, and replaces the running executable in place. `ape rollback` reverses the most recent update.

## Update to the latest release

```bash
ape update
```

This compares your installed version against the latest published release. If you're already current, ape exits with `already up to date` and no changes are made. If a newer version exists, ape downloads the matching archive plus the release's signed `ape_checksums.txt` and its Sigstore bundle, then **verifies before applying**:

1. The bundle is cosign-verified **offline** against an embedded Sigstore trusted root — pinning this repository's `release.yml` workflow identity and the GitHub Actions OIDC issuer for the exact tag. No `cosign` binary is required.
2. The downloaded archive's SHA-256 is checked against the now-trusted checksums file.

Only then is the binary extracted and swapped in atomically. If any verification step fails — or the release has no signature bundle — ape aborts and leaves the running binary untouched. The replaced binary is kept as a backup so `ape rollback` can restore it. (`GITHUB_TOKEN` is optional; when set it raises the GitHub API rate limit.)

For scripted environments, machine-readable output:

```bash
ape update --output-format json
ape update --output-format yaml
```

## Roll back to the previous version

If an update introduces a regression, restore the prior binary:

```bash
ape rollback
```

Rollback only restores the immediately previous version — there's no multi-step undo history. For older versions, install a specific release manually using the `VERSION=vX.Y.Z` variant from the [install guide](install.md).

## Permission errors

`ape update` writes to the location of the running binary. If that path is system-owned (e.g., `/usr/local/bin/ape` installed via `sudo`), you'll need `sudo` to update:

```bash
sudo -E ape update
```

`-E` preserves environment variables that ape may need (e.g., proxy config). For per-user installs (`~/.local/bin/ape`, `~/go/bin/ape`), no sudo is needed.

## The update-available notice

Most ape commands run a quick background check for newer releases and print `update available: vX → run 'ape update'` to stderr when one exists. The check is cached so it only fires once per cache window. The notice is informational — it does not affect the running command's exit code.

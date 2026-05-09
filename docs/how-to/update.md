# How to update ape

ape ships with self-update built in. `ape update` checks the latest GitHub release, downloads the matching binary for your platform, and replaces the running executable in place. `ape rollback` reverses the most recent update.

## Update to the latest release

```bash
ape update
```

This compares your installed version against the latest published release. If you're already current, ape exits with `already up to date` and no changes are made. If a newer version exists, ape downloads the matching tarball, verifies it, and swaps the binary atomically. The replaced binary is kept as a backup so `ape rollback` can restore it.

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

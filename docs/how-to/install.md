# How to install ape

ape ships as a single static binary. Three install paths cover most environments. Linux x64 is the primary target; macOS and Windows builds are published with each release.

## Option 1 — Release tarball (recommended)

Download the pinned-version tarball for your platform and extract the binary onto your `$PATH`.

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/diegosz/apex_process_ape/releases/latest | jq -r .tag_name)
curl -fsSL "https://github.com/diegosz/apex_process_ape/releases/download/${VERSION}/ape_linux_amd64.tar.gz" \
  | sudo tar -xz -C /usr/local/bin ape
ape version
```

To pin a specific version, set `VERSION` directly: `VERSION=v0.0.6`.

The Linux asset is `ape_linux_amd64.tar.gz`. Replace with `ape_darwin_amd64.tar.gz`, `ape_darwin_arm64.tar.gz`, or `ape_windows_amd64.zip` as needed (Windows uses zip, not tar.gz).

## Option 2 — `go install`

If you have a Go toolchain (1.26 or later):

```bash
go install github.com/diegosz/apex_process_ape/cmd/ape@latest
```

The binary lands at `$(go env GOPATH)/bin/ape`. Make sure that directory is on your `$PATH`. Note that `go install`-built binaries report `Version=dev` from `ape version` rather than a release tag — they're built from source without goreleaser's ldflags.

## Option 3 — Build from source

```bash
git clone https://github.com/diegosz/apex_process_ape.git
cd apex_process_ape
make install        # → /usr/local/bin/ape
```

`make install` runs `go build` and copies the binary. Override the destination with `make install INSTALL_DIR=/opt/local/bin` if you don't have write access to `/usr/local/bin`.

## Verifying the install ran

```bash
ape version
```

Should print something like `ape v0.0.6 / build date: ... / git commit: ...`. If `ape: command not found`, the install location isn't on your `$PATH` — check `echo $PATH` and either move the binary or extend `PATH` in your shell rc.

## Verifying release authenticity (optional)

Every release tarball is signed with cosign. To confirm the tarball you downloaded was actually built and uploaded by this repo's release workflow (rather than a tampered substitute), see [How to verify a release artifact](verify.md). Required for hardened CI / regulated environments; optional for everyday installs.

## Uninstalling

```bash
sudo rm /usr/local/bin/ape
```

(or wherever you installed it). ape is a single binary with no system-wide config files.

## Next steps

ape is just the CLI binary. To run any pipeline you also need the framework assets (skills + pipeline specs) installed in your project:

- **[Install the APEX framework into a project](framework-update.md)** — copy the `apex-*` skills and the canonical pipeline specs from a checked-out `apex_process_framework` repo into `<project>/.claude/skills/` and `<project>/_apex/pipelines/`, and seed `_apex/config.yaml` if absent.
- [Update ape](update.md) — once installed, keep it current.
- [Tutorials](../tutorials/) — once a "first pipeline" tutorial exists, that's the next step after install.

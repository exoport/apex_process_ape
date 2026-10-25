# How to install ape

ape ships as a single static binary. Four install paths cover most environments. Linux x64 is the primary target; macOS and Windows builds are published with each release.

## Option 1 — Release tarball (recommended)

Download the pinned-version tarball for your platform and extract the binary onto your `$PATH`.

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/exoport/apex_process_ape/releases/latest | jq -r .tag_name)
curl -fsSL "https://github.com/exoport/apex_process_ape/releases/download/${VERSION}/ape_linux_amd64.tar.gz" \
  | sudo tar -xz -C /usr/local/bin ape
ape version
```

To pin a specific version, set `VERSION` directly: `VERSION=v1.3.0`.

The Linux asset is `ape_linux_amd64.tar.gz`. Replace with `ape_darwin_amd64.tar.gz`, `ape_darwin_arm64.tar.gz`, or `ape_windows_amd64.zip` as needed (Windows uses zip, not tar.gz).

On Windows, an APEX v1 project's checks, gate commands and `notify:` command, and the migration checks `ape framework update` runs, run under `sh -c`, so ape needs an `sh` on `PATH`: run it from Git Bash (Git for Windows), or put Git's `usr\bin` on `PATH`. Without one, every check exits 127, and `ape framework update` refuses with its migration checks unable to run.

## Option 2 — Pin via `bingo` (recommended for project repos)

If you want a specific ape version checked into a project's own tooling (so every contributor and CI run gets the same binary), use [bingo](https://github.com/bwplotka/bingo):

```bash
# In your project repo:
bingo get -l github.com/exoport/apex_process_ape/cmd/ape@latest
# or a specific release tag, e.g. v1.3.0
```

This adds a per-tool `.mod` file under `.bingo/` and a `Makefile`-friendly variable for invoking the pinned binary, the same pattern this repo itself uses for its own dev tooling (see `.bingo/Variables.mk`). Commit the generated `.bingo/` files so the pin travels with the repo.

A bare `ape` run anywhere in that repo hands the command to the pinned version, so a Claude session opened in the project runs the pin too. See [How to pin ape in a project](pin-ape-in-a-project.md).

## Option 3 — `go install`

If you have a Go toolchain at the version `go.mod` names (1.27.3 or later):

```bash
go install github.com/exoport/apex_process_ape/cmd/ape@latest
```

The binary lands at `$(go env GOPATH)/bin/ape`. Make sure that directory is on your `$PATH`. `ape version` reports the module version: `go install …@vX.Y.Z` prints that release, and `@latest` the newest one. A build from a source checkout reports a Go pseudo-version instead, which `ape doctor`'s `framework.ape_version` row warns it cannot compare with a framework's `min_ape_version`.

## Option 4 — Build from source

```bash
git clone https://github.com/exoport/apex_process_ape.git
cd apex_process_ape
make install        # → /usr/local/bin/ape
```

`make install` runs `go build` and copies the binary. Override the destination with `make install INSTALL_DIR=/opt/local/bin` if you don't have write access to `/usr/local/bin`.

## Verifying the install ran

```bash
ape version
```

Should print something like:

```
ape 1.3.0
  build date: ...
  git commit: ...
```

 If `ape: command not found`, the install location isn't on your `$PATH` — check `echo $PATH` and either move the binary or extend `PATH` in your shell rc.

## Verifying release authenticity (optional)

Every release tarball is signed with cosign. To confirm the tarball you downloaded was actually built and uploaded by this repo's release workflow (rather than a tampered substitute), see [How to verify a release artifact](verify.md). Required for hardened CI / regulated environments; optional for everyday installs.

## Uninstalling

```bash
sudo rm /usr/local/bin/ape
```

(or wherever you installed it). ape is a single binary with no system-wide config files.

## Next steps

ape is the CLI only. A project also needs the framework's skills and its own APEX v1 layout. Order of operations for a new project:

1. **[Install the framework into the project](framework-setup.md)**: `ape framework setup` installs the newest framework release and commits it. Run it once per project.
2. **`ape project init`**: writes `_apex/config.yaml`, the project skeleton and the hooks. `ape project init --help` lists what it creates.
3. **[Your first APEX v1 project](../tutorials/first-v1-project.md)**: the tutorial walks from there to a first milestone.

Later, [refresh the framework](framework-update.md) with `ape framework update` when it publishes a release, and [update ape itself](update.md) with `ape update`. An APEX v0 project is upgraded with `ape framework update`: see [Upgrading a v0 project to v1](framework-update.md#upgrading-a-v0-project-to-v1).

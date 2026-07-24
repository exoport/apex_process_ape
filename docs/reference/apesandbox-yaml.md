# `.apesandbox.yaml` — the project sandbox descriptor

One committed file per project describes the whole sandbox workspace: the repos to
mount, extra mounts, the egress domains to request, and the toolchain to
materialize. It lives at the **main repo root** and is versioned with the code, so
"how this project's workspace is built" travels with the project instead of living
in someone's shell history.

```yaml
# .apesandbox.yaml
version: 1

# --- repos: each mounted at /workspace/<name>; exactly one main (sets the cwd)
repos:
  - { source: ., name: app, main: true }
  - { source: ../shared-libs, name: shared-libs }

# --- extra non-repo mounts: data, fixtures, host caches
mounts:
  - { source: /srv/data/fixtures, dest: /data/fixtures }          # read-only by default
  - { source: ./.cache/build, dest: /data/build-cache, readonly: false }

# --- egress: deny-by-default; still intersected with the node's policy
egress:
  authorized_domains: ["github.com", "*.githubusercontent.com", "proxy.golang.org"]

# --- toolchain: reference the native files rather than duplicating versions
toolchain:
  tool_versions: .tool-versions
  bingo: true
```

## Everything here is a request, never a grant

This file is **committed and therefore attacker-reachable** in any repo you
review, so nothing in it is trusted:

| What it asks for | What actually decides |
| --- | --- |
| a mount source | aped re-canonicalizes it and re-checks it against `mount_roots` in its own `policy.yaml`; a path outside them is denied |
| a mount destination | reserved destinations (`/workspace`, `/opt/apex-framework`, `/sandbox/home`) are refused outright — a project cannot redirect, shadow, or make-writable a system mount |
| a writable mount | a source under a `mount_roots_ro` root is denied write |
| how many mounts | `limits.max_mounts` caps the resolved list |
| egress domains | aped intersects them with `egress.allowed_domains`; a project can **narrow** what the node permits, never widen it |
| the framework version | aped resolves `<its own framework root>/<ref>`; the ref selects a *version*, never a *path* |

The client resolves relative paths and fails fast with the file named; **aped
re-checks everything regardless**. A hand-crafted wire request gets the same
treatment as a committed file.

## Sections

### `version` (required)

Schema version. Currently `1`. Unknown **top-level** keys are ignored on purpose,
so a project file carrying a section your `ape` predates still works — that is what
lets new sections be added without breaking older clients.

### `repos`

Each entry is a repository mounted at **`/workspace/<name>`** — always, even for a
single repo, so adding a second repo never moves the first.

| Key | Default | Meaning |
| --- | --- | --- |
| `source` | required | Path to the repo. Relative paths resolve against this file's directory. |
| `name` | basename of the resolved source | Mount name → `/workspace/<name>`. Must be a single path segment. |
| `main` | `false` | Exactly one entry is main: it sets the working directory (`attach`/`exec` open there) and is the target for framework setup and boundary commits. A lone repo is implicitly main. |
| `readonly` | `false` | Repos are writable — you work in them. |

With no `repos:` section, the workspace gets one repo derived from `--cwd`.

### `mounts`

Additive, non-repo binds. They can only ever **add**.

| Key | Default | Meaning |
| --- | --- | --- |
| `source` | required | Host path; relative resolves against this file's directory. `~` expands. |
| `dest` | `/mnt/<basename>` | Absolute guest path. |
| `readonly` | **`true`** | Safe default. Set `readonly: false` deliberately. |

`--mount-path <source>[:<dest>][:ro|:rw]` (repeatable) merges on top of this list,
CLI winning by destination:

```bash
ape sandbox up dev \
  --mount-path ../shared-protos:/workspace-extra/protos \
  --mount-path /srv/data/fixtures:/data/fixtures:rw
# --sandbox-config <path>    point at a non-default descriptor
# --no-sandbox-config        ignore any descriptor
```

> The flag is `--mount-path`, not `--mount`: `--mount` already selects the mount
> *mode* (`host-fs | volume | ephemeral`).

### `egress`

```yaml
egress:
  authorized_domains: ["github.com", "*.githubusercontent.com"]
```

Domains are exact hostnames or a single leading wildcard. They are requested
through the node's CONNECT proxy on 443, deny-by-default and audited per
connection. `--egress-domain` adds to the list from the CLI. See
[how-to/run-aped.md](../how-to/run-aped.md) for the node-side half (bridge,
netns helper, `policy.yaml` `egress:`).

### `toolchain`

```yaml
toolchain:
  tool_versions: .tool-versions   # asdf runtimes, repo-relative
  bingo: true                     # install the repo's .bingo-pinned Go tools
  tools: ["golang 1.23.4"]        # inline alternative to .tool-versions
```

Prefer referencing the native files so there is one source of truth for versions.

## The framework mount

The framework is **not** declared here — it is a system mount aped applies on its
own authority, read-only at `/opt/apex-framework`, present with or without a
descriptor. A project selects only the *version*:

```bash
ape sandbox framework materialize v0.3.1   # host-side, your git credentials
ape sandbox framework ls
ape sandbox up dev --framework-ref v0.3.1
```

aped never fetches: if a ref is not materialized on the node, `up` fails with the
exact command to run. Inside the workspace, consume it read-only:

```bash
ape framework setup --no-fetch --repo /opt/apex-framework
```

## Mounting a project under `/home`

aped runs with `ProtectHome=yes`, so paths under `/home` and `/root` are invisible
to it and cannot be policy-checked. To use a project there, expose that one
directory to **both** aped units and list it in `mount_roots` — see
`deploy/systemd/aped.service.d/mount-root.conf.example` and
`deploy/dev-host.sh`, which does both.

The drop-in being read-only only bounds aped's own `lstat` view; the **guest still
gets rw** where the mount says rw, because Kata's virtiofsd (a separate service)
does the real I/O.

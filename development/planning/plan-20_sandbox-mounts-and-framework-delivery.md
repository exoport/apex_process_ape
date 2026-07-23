---
plan_id: PLAN-20
created_at: 2026-07-23
status: proposed
tags:
  - sandbox
  - aped
  - workspace
  - mounts
  - framework
summary: >
  Replace the sandbox's single host-fs project mount with a general,
  policy-checked mount model — a list of {source, dest, readonly} entries — and
  make the APEX framework a built-in read-only entry rather than a baked image
  layer. Mounts are declared by a committable `.apemount.yaml` in the project
  and/or repeatable `--mount` CLI flags (both merge). These are additive USER
  mounts; the framework, composed ~/.claude, and primary project are always-on
  SYSTEM mounts that aped applies independently of — and that user input can
  never override or remove. aped authoritatively re-checks every user source
  against the policy mount-root allow-list, so a project-committed file can
  never mount an unauthorized host path nor touch the framework. The
  ape-sandbox image becomes PUBLIC + framework-free (published in the exoport
  org, same as ape); the private framework lives in a host-side git checkout the
  user keeps current with their own credentials, and aped mounts the pinned ref
  read-only at /opt/apex-framework — verified present-or-clear-error, and usable
  fully networkless. Supersedes the private-baked-image route.
origin:
  - 2026-07-23 design conversation — the private-baked-image route (exoar/ape-sandbox)
    created a distribution/credential burden that is uncomfortable team-wide, because
    aped runs on users' machines too (not just central nodes), so "per-node" == "per-dev".
    Decision — the image should be framework-free + public so anyone/any node pulls it
    with zero credentials, and the private framework is supplied at runtime.
  - 2026-07-23 constraint — workspaces (and aped's root executor) are networkless today
    (see the Networking note in docs/how-to/sandbox-workspaces.md and PLAN-18 "barrier 2"),
    so the framework cannot be fetched inside the workspace. It is instead supplied from a
    host-side checkout and mounted in — which also works offline. Network egress is a
    separate, larger workstream (see Related).
  - 2026-07-23 user requirements — framework-free image (update framework without rebuilding
    the image); mount the framework read-only at /opt; the host user's own credentials fetch
    the framework host-side; pin the framework version and STOP with a clear error if the pin
    is absent from the local repo; works on user machines and nodes; declare mounts via a
    committable file (`.apemount.yaml`) and/or CLI flags; per-mount read-only vs read-write.
  - Assumptions marked inline were made at authoring time; flag at review.
---

# PLAN-20: Sandbox mounts (general model) + framework delivery

## Goal

A workspace's host mounts are a single, uniform, **policy-checked list** of
`{source, dest, readonly}` entries. The primary project, the composed
`~/.claude`, and the **APEX framework** are all just entries in that list —
the framework a **built-in, read-only** one — instead of the framework being a
baked image layer. A developer declares extra mounts in a **committable
`.apemount.yaml`** in their project and/or with repeatable `--mount` flags; the
two merge. `aped` re-checks every resolved source against its mount-root
allow-list, so a committed file can never escalate to an unauthorized host
path. The `ape-sandbox` image becomes **public and framework-free**, so any
`aped` (node *or* laptop) pulls it with no credentials, and the private
framework is supplied from a host-side checkout — working fully **networkless**.

## Why (the pivot)

The shipped private-baked-image route (PLAN-16 D6 → exoar/ape-sandbox) forces
every consumer to hold a registry pull credential. Because `aped` also runs on
**developers' machines**, "per-node" is "per-developer" — the exact friction we
want to avoid. Making the image **framework-free + public** removes the
credential entirely from image distribution; the private framework is only ever
touched host-side (fetched with the developer's own creds) and mounted in. This
plan **supersedes** the private-baked-image approach (see Migration).

## Design

### The mount model (system mounts + user mounts)

A workspace's binds come from **two distinct categories** that share one
`MountSpec` shape and one bind layer, but have **different authority**:

```
MountSpec = { Source, Dest string; ReadOnly bool }
```

- **Source** — a canonical host path. Relative paths in `.apemount.yaml` resolve
  against the **project root on the client** and are canonicalized to an
  absolute path *before* they hit the wire (aped never sees, nor trusts, a
  relative path).
- **Dest** — the guest mount point. If omitted, defaults to `/mnt/<basename>`.
- **ReadOnly** — the guest-side bind option. **Default `true`** for
  user-declared entries (safe default; opt into `readonly: false` for write);
  the primary project entry is `rw`, the framework entry is `ro`.

**1. System mounts — aped-owned, always applied, INDEPENDENT of user input.**
The **framework** (`/opt/apex-framework`, ro), the composed **`~/.claude`**
(`/sandbox/home`), and the **project repos** (see "Multi-repo" below — each at
`/workspace/<name>`, rw) are injected by aped/the profile itself. They are
**not** declared in, affected by, or removable via `.apemount.yaml` or
`--mount`. In particular the **framework mount is present by default regardless
of what the user requests**, and its *source* (the pinned host-repo ref) is
resolved server-side/by the profile — a project can never set, omit, redirect,
or make-writable the framework. Only a **server-side profile/policy** may alter
a system mount (e.g. a deliberately framework-less profile); the client/project
cannot.

**Multi-repo: `/workspace` is a root, not a single mount.** A project may span
several repos. Each repo is mounted at **`/workspace/<name>`** (always — even a
single repo mounts at `/workspace/<name>`, not bare `/workspace`), and exactly
one is flagged **`main`**. The main repo sets the workspace's **default working
directory** (`WORKDIR` / where `attach`/`exec`/claude open) and the default
target for APE operations (`ape framework setup`, boundary commits). The repo
set + `main` is declared in `.apemount.yaml` (`repos:`), or degenerates to a
single main repo from `--cwd`. Repos are project mounts (rw by default, per-repo
`readonly` allowed); `/workspace` itself is a reserved root a user mount cannot
occupy or shadow.

**2. User mounts — additive requests via `.apemount.yaml` and `--mount`.**
These only ever *add* extra binds. They are merged with each other (CLI wins by
`Dest`), then policy-checked, and can never collide with or override a system
mount (see Security → reserved dests). Absence of any user mount changes nothing
about the system mounts.

So the bind layer is one uniform list, but it is assembled as
`system_mounts ++ validated(user_mounts)` — the framework/home/project are not
user-tunable knobs. The OCI-spec layer already supports many binds and per-bind
ro/rw (`internal/sandbox/spec.go` `Comp.Binds[].ReadOnly`, `ExtraRW`); this plan
surfaces that through the contract rather than inventing new spec plumbing.

### Where user-mount entries come from (merge order)

System mounts (above) are applied unconditionally. **User** mounts merge, later
overriding earlier **by `Dest`** (duplicate dest = last wins); none may target a
reserved dest (see Security):

1. **Profile-declared user mounts** (server-side, aped-resolved) — optional extra
   binds a profile wants for every workspace of its kind. *(These are still
   policy-checked; distinct from the always-on system mounts.)*
2. **`.apemount.yaml`** committed in the project (client-read).
3. **`--mount` CLI flags** (client, repeatable): ad-hoc additions/overrides.

### `.apemount.yaml` (committable project config)

Lives at the project root; committed with the repo so a project's expected
mounts travel with it.

```yaml
# .apemount.yaml — the repos + extra host folders for the sandbox workspace.
# Sources are host paths; RELATIVE paths resolve against this file's directory
# on the CLIENT and are canonicalized before being sent. aped re-checks every
# resolved path against its mount-root allow-list (a request here is NOT trust).
version: 1

# Project repos — each mounted at /workspace/<name>. Exactly one is `main`
# (sets the default cwd + the default target for `ape framework setup`/commits).
repos:
  - source: .                       # the repo holding this file
    name: app                       # → /workspace/app
    main: true
  - source: ../shared-libs
    name: shared-libs               # → /workspace/shared-libs
    readonly: false

# Extra non-repo mounts (data, host toolchain caches — see PLAN-22).
mounts:
  - source: /srv/data/fixtures      # absolute host path
    dest: /data/fixtures
    readonly: true                  # default; opt into `readonly: false` for write
```

CLI equivalent / override, repeatable:

```
ape sandbox up dev \
  --mount ../shared-protos:/workspace/shared-protos:ro \
  --mount /srv/data/fixtures:/data/fixtures:rw
# --mount-file <path>   # point at a non-default file
# --no-mount-file       # ignore any .apemount.yaml
```

Flags and file **both** apply and merge (CLI wins by dest). Syntax:
`--mount <source>[:<dest>][:ro|:rw]`.

### Security / trust boundary (load-bearing)

`.apemount.yaml` and `--mount` are **requests, not grants**. The client
canonicalizes sources; **`aped` (the executor) authoritatively re-checks every
entry** against the policy mount-root allow-list — the existing
`internal/aped/policy.go` `checkMount` / `MountRoots`, applied **per entry**. A
committed file asking for `/etc`, `/`, or another user's home is **denied**
because it is not under an allowed root. Additions:

- **Reserved dests** — `/opt/apex-framework`, `/sandbox/home`, and the
  `/workspace` **root** (including any `/workspace/<name>` a `repos:` entry
  already claims) are **system mounts**: a user `mounts:` entry (profile/file/CLI)
  targeting them is **rejected**, never merged. A project cannot shadow, redirect,
  remove, or make-writable the framework, the composed home, or a project repo.
  The framework mount and its (pinned host-repo) source are resolved server-side
  and applied **independently of any user request** — the default with or without an
  `.apemount.yaml`.
- **`max_mounts`** policy ceiling (default e.g. 16) to bound fan-out.
- **`mount_roots` may mark a root ro-only** — a `readonly: false` request under
  an ro-only root is denied (lets an operator export a shared dir read-only to
  all workspaces).
- **`/home` prerequisite (documented):** sources under `/home` or `/root` are
  invisible to aped under `ProtectHome=yes` and are denied with the existing
  hint. To allow one, add a `BindReadOnlyPaths=<dir>` drop-in to **both** aped
  units + list the dir in `mount_roots`
  (`deploy/systemd/aped.service.d/mount-root.conf.example`). Note: the drop-in
  being *read-only* only bounds aped's own `lstat` view — the **guest still gets
  rw** (Kata's virtiofsd does the real I/O), so guest ro/rw stays controlled
  per-entry by `ReadOnly`.

### Framework delivery (the built-in RO entry)

- **Image:** `ape-sandbox` becomes **public + framework-free**, published in the
  **exoport** org (same home as `ape`). No framework layer ⇒ nothing private to
  leak ⇒ zero-credential pull for every node/laptop. `ape` (public releases),
  node, claude, git, sshd, chromium/Playwright stay baked.
- **Host framework repo:** a git checkout the developer keeps current using
  **their own** credentials (host-side; the client may offer `ape sandbox
  framework fetch`, but the credentialed fetch is a host action, never in the
  guest). Its location must fall under a `mount_roots` entry (or `/home` + the
  drop-in).
- **Pinned version:** the framework ref (a tag) is resolved at `up`
  (profile default, overridable). `ape sandbox up` **verifies the pinned ref
  exists in the local repo**; if absent → **stop with a clear, actionable error**
  ("framework <ref> not found in <path>; fetch it: git -C <path> fetch --tags").
  It does **not** silently fetch.
- **Materialize + mount:** the pinned ref is materialized at a stable,
  aped-mountable path (a `git worktree` for the ref, on a local `main` branch so
  the `ape framework setup` branch check is satisfied) and applied by aped as a
  **system mount** — the built-in **`/opt/apex-framework` ro** entry, present by
  default and independent of `.apemount.yaml`/`--mount` (see the mount model).
- **Consume in-guest:** `ape framework setup --no-fetch` reads the RO mount and
  installs into the project (rw). `--no-fetch` is required (a fetch would write
  to the repo, which RO forbids — and no network is available anyway). Guard the
  RO-repo git quirk with `GIT_INDEX_FILE=<tmp>` / `safe.directory`.
- **Networkless:** because the framework is on-disk before boot, none of this
  needs workspace egress — it works offline.

## Deliverables

- [ ] **D1 — Contract.** `internal/workspace`: replace the single
  `Mount`/`MountSource` with `Mounts []MountSpec` (`{Source, Dest, ReadOnly}`);
  keep `--cwd`/mode as sugar that injects the primary project entry. Update the
  `Workspace` record + `inspect`.
- [ ] **D2 — Client resolve + merge.** `ape sandbox up`: assemble the list from
  built-ins + profile + `.apemount.yaml` + `--mount` flags; canonicalize
  relative sources against the project root; dedupe by dest with the documented
  precedence; enforce reserved dests client-side (fail fast, aped re-checks).
- [ ] **D3 — `.apemount.yaml` + flags.** Schema (`version`, `mounts[]`) + client
  parser; `--mount src[:dest][:ro|:rw]` (repeatable), `--mount-file`,
  `--no-mount-file`.
- [ ] **D4 — aped policy.** Per-entry `checkMount` against `mount_roots`;
  reserved dests; `max_mounts`; optional ro-only roots; honor `ReadOnly` in the
  OCI bind. Extend `deploy/policy.yaml` + docs.
- [ ] **D5 — Framework delivery.** Make the image public + framework-free in
  exoport; host-repo pinned-ref materialize + verify-or-error + RO
  `/opt/apex-framework` built-in mount; `ape framework setup --no-fetch` glue
  (branch/RO-repo guards). Optional `ape sandbox framework fetch` convenience.
- [ ] **D6 — Docs.** `.apemount.yaml` reference; the `/home` `BindReadOnlyPaths`
  prerequisite; the framework update workflow (host-side fetch + pin);
  regenerate `cli.md`; reconcile PLAN-16 D6 + `sandbox-workspaces.md`.
- [ ] **D7 — Migration (see below).**

## Migration — supersede the private-baked-image route

The private route we just built is reverted/repurposed as part of D5:

- **Image:** publish a **public, framework-free** `ape-sandbox` in exoport (fold
  the Dockerfile back into `apex_process_ape/images/ape-sandbox/`, public, no
  build secret; or a public `exoport/ape-sandbox` repo). Retire the private
  `exoar/ape-sandbox` build (or keep it dormant) — it is no longer the source of
  truth. Delete/mark-private-obsolete the `ghcr.io/exoar/ape-sandbox` package.
- **apex_process_ape:** revert `sandbox.DefaultImage` (`internal/sandbox/kata.go`)
  and `deploy/policy.yaml` from the private `ghcr.io/exoar/...` ref (commit
  `7dd21e2`) to the **public** ref. No aped pull credential is needed anymore.
- The `internal/sandbox` CONNECT-proxy/composer layers are unaffected.

## Non-goals

- **Network egress** for workspaces — a separate, larger (M–L) workstream. See
  Related; this plan's framework delivery is deliberately network-free.
- Changes to `volume` / `ephemeral` mount modes (host-fs is the focus).
- Netbird / private overlay (PLAN-18 Phase 4 / platform repo).
- Auto-fetching the framework (the pin must be present locally; we error, not fetch).

## Effort

**S–M.** Most of the spec layer exists (multi-bind + per-bind ro/rw); the work
is the contract change (D1), the client resolve/merge + file/flags (D2/D3), and
the per-entry policy check + reserved dests (D4). D5 is mostly the image pivot
(public/framework-free) + the host-repo materialize/verify glue.

## Related

- **Sandbox network egress** — scoped 2026-07-23 (a separate future plan). The
  CONNECT proxy + allowlist + `PlanEgress` are code-complete but unwired; the
  blocker is that aped's hardened executor can't set up container networking
  ("barrier 2"). Minimal enforced egress needs a narrow **privileged netns/nft
  helper** (the executor stays hardened) — ~multi-week (L); a non-enforced
  "honest boundary" variant (shared bridge + proxy env, no nft wall) is ~M.
  Recommended before real workspace work, since a networkless workspace can't
  clone deps / do research. Track as PLAN-21.
- **PLAN-16** (Kata VM workspaces) — this refines D6 (image + framework).
- **PLAN-18** (`ape`/`aped` split) — the mount + policy + ProtectHome model.

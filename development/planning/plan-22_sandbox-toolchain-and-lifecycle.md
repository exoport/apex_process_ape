---
plan_id: PLAN-22
created_at: 2026-07-23
status: proposed
tags:
  - sandbox
  - toolchain
  - devcontainer
  - lifecycle
  - persistence
summary: >
  Turn the sandbox into a real per-project devcontainer: a project declares its
  toolchain (language/runtime versions via asdf's `.tool-versions`, Go CLI tools
  via the already-used bingo), and the workspace materializes exactly those
  versions — reproducibly, durable across workspace reuse/rebuild, and usable
  offline once warmed. Today there is one fixed image (node/claude/ape/git only),
  no toolchain declaration, no runtime layer composition, and no runtime install
  (networkless). The model: a lean base image carrying asdf (the Go rewrite) +
  bingo; per-project toolchain state (asdf installs, ~/go incl. bingo tools,
  ~/.cargo, build caches) lives in DURABLE mounts (host caches and/or a
  per-project persistent volume), never the ephemeral rootfs; the initial fetch
  needs egress (PLAN-21), everything after is offline. Also closes the node
  lifecycle gaps: EXPOSE the already-implemented `Stop`/`Start` (backend/contract/
  client done — only the CLI verb is missing) so RAM can be freed while keeping
  the rootfs+state, add an optional idle reaper / TTL, and reconcile the registry
  to containerd reality after a restart. Workspaces are node-pinned and reusable
  across many sessions.
origin:
  - 2026-07-23 design conversation — projects need different toolchains (a pinned Go
    version + Go tools via bingo, sometimes Flutter, sometimes Rust). "Do we support
    layers?" No runtime layer composition exists; the only mechanism today is the
    `image:` override. Decision — declared per-project toolchains via asdf (runtimes)
    + bingo (Go tools), a hybrid of a lean base image + version manager + durable
    cached mounts + egress.
  - 2026-07-24 lifecycle finding — workspaces are long-lived/reusable and node-pinned;
    the VM rootfs is destroyed on `down`. `Stop`/`Start` (free RAM, keep rootfs+state)
    are ALREADY implemented in the driver/`Backend`/`ape.vmm` contract/`vmmclient` — only
    the `ape sandbox stop`/`start` CLI verb is missing. `freeze` keeps RAM resident and
    does not survive a reboot; `suspend` is UNSUPPORTED. There is no idle reaper/TTL and
    no restart reconciliation. So: externalize toolchain state to durable storage, expose
    stop/start, and add node lifecycle management (reaper/TTL + reconcile-on-startup).
  - Assumptions marked inline were made at authoring time; flag at review.
---

# PLAN-22: Sandbox toolchain / devcontainer model + workspace lifecycle

## Goal

A workspace is a **per-project devcontainer**: the project **declares** its
toolchain, and the workspace materializes exactly those language/runtime + tool
versions — reproducible, **durable across reuse/rebuild**, and **offline after a
first warm-up**. Backbone: **asdf** (the Go rewrite) for languages/runtimes,
**bingo** for Go CLI tools (already used in this repo).

## Background — where we are

- One fixed image (node/claude/ape/git/build-essential/chromium) — **no Go,
  Rust, or Flutter** baked.
- The profile can override the **`image:`** and declare `extra_rw` mounts
  (`internal/sandbox/profile.go`), but there is **no toolchain/version field**.
- **No runtime layer composition** — OCI layers are built, not stacked at `up`
  time. And workspaces are **networkless** today, so nothing installs at boot.
- Workspace lifecycle: long-lived/named/reusable; `freeze`/`unfreeze` =
  cgroup-freeze (RAM resident); `suspend` = UNSUPPORTED; `down` = destroy the VM
  + rootfs (`docs/how-to/sandbox-workspaces.md`).

## Design

### 1. Declared toolchain (per project)

- **Languages/runtimes → asdf** `.tool-versions` (committed in the project /
  main repo):

  ```
  golang 1.23.4
  nodejs 20.18.0
  flutter 3.24.0
  rust 1.81.0
  ```

- **Go CLI tools → bingo** (unchanged; `.bingo/` already pins go-installable
  tools to a version-stamped `$GOBIN`).
- Both are **committed with the repo** — the toolchain travels with the project.
  The **`.apesandbox.yaml`** descriptor (PLAN-20) carries a **`toolchain:`**
  section that either inlines the versions or (preferred) **references the native
  `.tool-versions` + `.bingo/`** so there is no duplicate source of truth:

  ```yaml
  toolchain:
    tool_versions: .tool-versions   # asdf
    bingo: true                     # install the repo's pinned Go tools
  ```

### 2. Lean base image + version manager (no baked languages, no runtime layers)

- The public, framework-free `ape-sandbox` base (PLAN-20) gains **asdf (Go
  binary)** + **bingo**, plus the minimal build deps asdf plugins need
  (`build-essential`, already present). It stays **language-agnostic** — no Go/
  Rust/Flutter baked, so it does not fan out into a matrix of images.
- **"Layers" clarified:** we do **not** compose OCI layers at runtime. Per-project
  toolchains come from asdf/bingo installing into **durable state** (below), not
  from stacking image layers. Pre-built image *variants* for heavy/common stacks
  (e.g. a Flutter variant) remain an *optional* build-time optimization via the
  existing `image:` override — not the primary mechanism.

### 3. Install step

- A first-boot / on-demand step (`ape sandbox setup`, or an entrypoint hook)
  runs **`asdf install`** (materialize the `.tool-versions` set) and **`bingo
  get`** (the pinned Go tools). Idempotent; a no-op when the durable caches are
  already warm (fully **offline** in that case).
- The **initial** fetch needs **egress (PLAN-21)**; thereafter the cached tools
  serve offline.

### 4. Durable state — the lifecycle crux

The VM rootfs is destroyed on `down`, so **toolchain + dependency state must not
live only in the rootfs.** It goes to **durable storage**, reusing PLAN-20's
mount model:

- **Host-cache mounts (default):** the **asdf** install dir (`~/.asdf` / the
  asdf data dir), **`~/go`** (modules + bingo's `~/go/bin`), `~/.cargo`,
  `~/.pub-cache`/Flutter SDK, and language build caches — mounted from the host
  so they persist, are shared, are pre-warmable, and survive `down`/`up`.
  (Same-OS/arch host↔guest required — a Linux aped node ↔ Linux guest; a
  macOS/Windows client's caches live on the aped host, not the client.)
- **Per-project persistent `volume` (opt-in):** for state you want isolated in
  the VM rather than shared on the host (a project's own `~/go`, `node_modules`,
  build artifacts). Survives `down` unless `--remove-volume`.
- **Default:** shared host caches + **asdf per-project versions** (which
  neutralize cross-project version drift); per-project volume as an opt-in for
  isolation.

### 5. Workspace lifecycle — reuse, rebuild, and node management

**States available today** (all node-local; a workspace is pinned to its
`ape.vmm.<node>` node — no migration):

| State | Verb | RAM | Rootfs/state | Notes |
| --- | --- | --- | --- | --- |
| running | `up` | in use | live | reused across sessions via `exec`/`attach` |
| frozen | `freeze`/`unfreeze` | **resident** | kept | cgroup pause; instant resume; **does not survive a reboot** |
| stopped | `Stop`/`Start` | **freed** | kept | task killed, container+snapshot retained; **survives reboot**; `start` revives |
| suspended | `suspend`/`resume` | — | — | **UNSUPPORTED** on Kata-via-containerd |
| destroyed | `down` | freed | **deleted** | drops the registry entry; `--remove-volume` also deletes a `volume` |

**Correction (2026-07-24):** `Stop`/`Start` are **already implemented** end-to-end
— `internal/sandbox/containerd_driver_linux.go` (`Stop` kills+deletes the task,
keeps container+snapshot; `Start` revives), the `workspace.Backend` interface,
the `ape.vmm` contract (`internal/workspace/vmmwire.go`), and `vmmclient`
(`Start`/`Stop`). The **only gap is CLI exposure** — no `ape sandbox
stop`/`start` subcommand (`internal/apecmd/sandbox.go` registers up/ls/inspect/
attach/ssh/exec/freeze/unfreeze/suspend/down only). So this is *expose*, not
*build*.

**Reuse modes:** keep warm (`freeze`), free RAM but keep state (`stop`, best on
busy nodes/laptops), or rebuild (`down`/`up`, cheap because state is durable).

**Node lifecycle gaps to close (there is no automatic management today):**
- **No idle reaper / TTL.** Workspaces live until an explicit `down`; nothing
  reclaims RAM or disk. Add an optional per-node policy: auto-`stop` after N idle
  (reclaim RAM — use `stop`, not `freeze`, since freeze holds RAM), auto-`down`
  after M (reclaim disk), and/or a per-workspace TTL. Ephemeral/preview
  workspaces should default to a short TTL; a developer's project workspace to
  none.
- **No restart reconciliation.** After an aped/host reboot the container+snapshot
  persist but any running/frozen task is gone; nothing reconciles the registry to
  reality or restarts "keep-alive" workspaces. Add a reconcile-on-startup that
  marks vanished tasks `stopped` and optionally auto-`start`s flagged ones.
- **Node affinity is implicit** (per-node contract, node-local state); no
  scheduler/placement. Document it; cross-node is out of scope (Phase 4).

## Deliverables

- [ ] **D1 — Toolchain config resolution.** Read `.tool-versions` (+ `.bingo/`)
  from the main repo; surface it to the workspace.
- [ ] **D2 — Base image.** Add asdf (Go) + bingo to the lean base (PLAN-20);
  keep it language-agnostic.
- [ ] **D3 — Install step.** `ape sandbox setup` / entrypoint hook →
  `asdf install` + `bingo get`; idempotent; offline when caches are warm.
- [ ] **D4 — Durable state mounts.** Standard host-cache mount presets (asdf dir,
  `~/go`, `~/.cargo`, …) via the PLAN-20 mount model; per-project `volume`
  option; docs on shared-vs-isolated.
- [ ] **D5 — Lifecycle.** (a) **Expose** the existing `Stop`/`Start` as `ape
  sandbox stop`/`start` CLI verbs (backend/contract/client already done — CLI
  only). (b) **Idle reaper / TTL** — optional per-node policy: auto-`stop` after
  N idle, auto-`down` after M, per-workspace TTL (short default for
  ephemeral/preview, none for dev project workspaces). (c) **Restart
  reconciliation** — on aped startup, reconcile the registry to containerd
  reality (vanished task → `stopped`) and optionally auto-`start` flagged
  keep-alive workspaces. Keep `freeze`/`stop`/`down` semantics distinct + documented.
- [ ] **D6 — Optional image variants.** Document building heavy-stack variants
  (e.g. Flutter) via `image:` for teams that want them pre-baked.
- [ ] **D7 — Docs.** Devcontainer how-to; toolchain config reference; caching /
  offline / pre-warm workflow; lifecycle (freeze vs stop vs down).

## Non-goals

- Baking every language into one image, or a combinatorial image matrix as the
  primary mechanism (variants are an optional optimization).
- Runtime OCI-layer composition (not a thing).
- A bespoke toolchain format when `.tool-versions` + `.bingo/` already exist.
- Nix/devbox (a different philosophy; can be revisited).

## Dependencies

- **PLAN-21 (egress)** — required for the *initial* toolchain/dependency fetch.
  Offline after warm-up.
- **PLAN-20 (mounts)** — provides the durable host-cache mounts + multi-repo
  root the toolchain state relies on.

## Effort

**M–L.** D2/D3 (image + install step) and D4 (cache mount presets) are the bulk;
D5 (`stop`/`start`) is small (the driver already supports a stopped task).
Gated on PLAN-21 for the online path.

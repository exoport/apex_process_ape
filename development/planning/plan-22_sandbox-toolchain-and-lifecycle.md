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
  needs egress (PLAN-21), everything after is offline. Also fixes the workspace
  lifecycle gap — the only RAM-freeing verb today is destructive `down` — by
  adding a `stop`/`start` that frees guest RAM while keeping the rootfs + state.
origin:
  - 2026-07-23 design conversation — projects need different toolchains (a pinned Go
    version + Go tools via bingo, sometimes Flutter, sometimes Rust). "Do we support
    layers?" No runtime layer composition exists; the only mechanism today is the
    `image:` override. Decision — declared per-project toolchains via asdf (runtimes)
    + bingo (Go tools), a hybrid of a lean base image + version manager + durable
    cached mounts + egress.
  - 2026-07-23 lifecycle finding — workspaces are long-lived/reusable, but the VM
    rootfs is destroyed on `down`, and the only RAM-freeing option is `down` (`freeze`
    keeps RAM resident; `suspend` is UNSUPPORTED on Kata-via-containerd). So toolchain
    state must be externalized to durable storage, and a non-destructive `stop` is
    worth adding.
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

### 5. Workspace lifecycle — reuse, rebuild, and a `stop`/`start` verb

- Workspaces stay **long-lived/reusable per project**. Two cheap reuse modes:
  **keep warm** (`freeze`/`unfreeze`) or **rebuild** (`down`/`up`, cheap because
  state is durable).
- **Gap to fix:** the only way to free a workspace's RAM today is destructive
  `down` (`freeze` keeps RAM resident; `suspend` is UNSUPPORTED). Propose a
  **`stop`/`start`** pair: stop the guest task (free guest RAM) while **retaining
  the container + snapshot rootfs + durable state**, and `start` to bring it back
  (the containerd driver already has a `Start` for a stopped task —
  `internal/sandbox/containerd_driver_linux.go`). This gives a third,
  non-destructive, RAM-freeing state — important on RAM-constrained laptops
  running several project workspaces.

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
- [ ] **D5 — Lifecycle `stop`/`start`.** Non-destructive RAM-freeing stop +
  start; reuse/rebuild docs; keep `freeze`/`down` semantics distinct.
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

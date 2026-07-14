---
plan_id: PLAN-16
created_at: 2026-07-02
updated_at: 2026-07-14
status: partially-implemented
tags:
  - sandbox
  - kata
  - microvm
  - isolation
  - workspaces
  - platform
summary: >
  `ape sandbox` provisions long-lived, hardware-isolated **Kata microVM
  workspaces** for local development: a persistent VM (own guest kernel, KVM)
  from an official `ape-sandbox` OCI image (or a custom image), with the project
  made available inside (host-fs share / persistent volume / ephemeral clone), a
  per-workspace composed `~/.claude` (credential modes A/B, curated
  skills/hooks/agents, git modes), deny-by-default public egress via a CONNECT
  proxy + allowlist + audit, and lifecycle `up → attach/ssh/exec → freeze/unfreeze
  → down`. Inside the VM you run Claude Code, APEX pipelines, or Playwright; you
  attach via SSH / VS Code Remote. Backend is **kata-only** (kata-clh default,
  kata-qemu for GPU/USB device workspaces). **As shipped (PLAN-18), `aped` — not
  `ape` — drives Kata via the containerd Go client (`--driver containerd`); the
  daemonless `nerdctl`/`ctr` shell-out with no Go dep described below is
  retired.** This is **Phase 1 of the APEX Process
  Platform** (north-star: `../../../exoar/apex_process_platform/draft/`);
  Phases 2–4 (in-VM NATS worker, Netbird overlay networking, preview/staging
  environments, the device tier) live in that repo.
origin:
  - 2026-07-02 user request — sandbox pipeline/task/command/script sessions with something stronger than Docker (gVisor), mounting folders, no host access; per-session `.claude` skills/hooks. (Original scope; superseded below.)
  - 2026-07-02..05 — see `development/research/sandbox-isolation-20260702.md` (isolation-tech research) and this plan's earlier "gVisor-sandboxed sessions" revision.
  - 2026-07-05 spike (this repo) — live rootless-runsc findings that blocked the gVisor-job design and drove the reframe (see "Spike findings").
  - 2026-07-06/07 — deep research (`development/research/sandbox-qemu-vs-kata-20260706.md`) + user decisions → **kata-only, long-lived VM workspaces, Phase 1 of a broader self-hostable platform.** North-star docs authored in `../../../exoar/apex_process_platform/draft/` (00–05).
  - 2026-07-07 user decisions — kata-only; VMM kata-clh default / kata-qemu for GPU/USB; GPU **exclusive passthrough** (no consumer-GPU sharing) in v1; official ape images + custom; project-mount supports all three modes; ape stays the CLI operator; Phase 1 ships in this repo.
---

# PLAN-16: Kata VM workspaces for local development (Phase 1 of the APEX Process Platform)

> **Reframe note (2026-07-07).** This plan began as *"gVisor-sandboxed sessions"*
> — wrap each `ape <job> --no-tui` in an ephemeral gVisor sandbox. A live spike
> (findings below) plus deep research changed the direction to **long-lived Kata
> microVM *workspaces*** — the near-term slice of the broader **APEX Process
> Platform** (north-star lives in the separate `apex_process_platform` repo,
> `draft/00-05`). **gVisor is dropped as the backend.** This plan now scopes
> **only Phase 1** (local-dev Kata workspaces, shippable on one Linux box with
> KVM). Phases 2–4 (in-VM NATS worker, Netbird two-overlay networking,
> preview/staging environments, the GPU/USB device tier) are the platform repo's.

> **Reconciliation (2026-07-14).** This Phase-1 plan describes a **daemonless
> `ape sandbox` runner** — an unprivileged `ape` that shells out to
> `nerdctl`/`ctr` itself and exposes a `pause`/`resume` lifecycle. **That is not
> what shipped.** PLAN-18 (`plan-18_ape-aped-split.md`) split the work into an
> unprivileged **`ape`** client and a single rootful daemon, **`aped`**. Read the
> design/spike narrative below through this note; the operational truth lives in
> `docs/how-to/sandbox-workspaces.md` and `docs/how-to/run-aped.md`.
>
> - **Control plane, not a local runner.** `ape sandbox` is a thin `aped` client
>   speaking the `ape.vmm.<node>.>` NATS contract; `aped` owns provisioning,
>   `~/.claude` composition, per-VM credential minting, egress, and the workspace
>   registry, and is the only root component. `ape` never touches containerd. The
>   daemonless `ape`→containerd path described below is **retired**.
> - **Verbs (`internal/apecmd/sandbox.go`, `internal/workspace.Backend`):**
>   `up | ls | inspect | exec | attach | ssh | freeze | unfreeze | suspend | down`.
>   There is **no `pause`/`resume`.** `freeze`/`unfreeze` are a guest
>   **cgroup-freeze** (RAM stays resident, `unfreeze` thaws instantly), **not** a
>   VM suspend; the distinct `suspend` verb returns `UNSUPPORTED` because
>   Kata-via-containerd can't save guest RAM today (PLAN-18 D7). `ssh` is a
>   placeholder that defers to `aped` networking (Tier-2).
> - **Driver.** The live-validated provisioning path is the containerd **Go
>   client** (`aped run --driver containerd`), which builds the OCI spec as a
>   typed object with no client-side `mount(2)`. The `nerdctl`/`ctr` shell-out
>   described below is the default `shellDriver`, which **cannot** run through
>   `aped`'s hardened root executor (its client-side rootfs mount is denied) — see
>   PLAN-18's Risks and `run-aped.md`.
>
> Everything below is kept as the Phase-1 historical record; inline
> `*(PLAN-18: …)*` markers flag the specific spots a reader would otherwise be
> misled.

## Status / task checklist

### Phase 1 — Kata VM workspaces, local dev (this repo)

- [x] **`ape sandbox` command surface:** *(PLAN-18)* shipped as `up | ls | inspect | exec | attach | ssh | freeze | unfreeze | suspend | down <name>` (`internal/apecmd/sandbox.go`) — **not** the `pause | resume` originally listed here. `freeze`/`unfreeze` are a cgroup-freeze; `suspend` returns `UNSUPPORTED` on Kata; `ssh` defers to `aped` networking (Tier-2).
- [x] **D1 workspace runner** — `internal/sandbox` (`kata.go` pure nerdctl command construction + on-disk registry; `kata_linux.go` exec methods; `kata_other.go` Windows stub); **kata-clh default** VMM via `io.containerd.kata-<vmm>.v2` runtime handler. Live provision/exec/pause validated only under Tier-2 (no KVM in CI / on this box yet). *(PLAN-18: refactored into the `shellDriver` behind `internal/workspace.Backend`; `ape` no longer runs it — `aped` does, and the live-validated driver is the containerd-Go-client `containerdDriver` (`--driver containerd`), which the `shellDriver` cannot match through the hardened root executor. `pause` → `freeze`.)*
- [x] **D6 official `ape-sandbox` image** — `images/ape-sandbox/` (Dockerfile `FROM` agent-infra/sandbox + claude/node/`ape`/git/framework/sshd/chromium+playwright; entrypoint; README with build/pin/publish + versioning). `sandbox.DefaultImage` is the wiring point; `image:` override supported. **Base pinned (step 9):** `BASE_IMAGE` = `ghcr.io/agent-infra/sandbox:1.11.0@sha256:6328d7fd…f906e7` (verified multi-arch amd64+arm64 manifest-list digest). **Offline framework (step 9):** `ENV APEX_FRAMEWORK_REPO=/opt/apex-framework` so `ape framework setup --no-fetch` installs from the baked-in checkout. *Remaining: the actual build/publish needs the container toolchain (docker/nerdctl), not yet installed here.*
- [x] **project-mount modes** — host-fs (default) · volume · ephemeral wired in `WorkspaceSpec.RunArgs` + `sandbox up`
- [x] **D2 per-workspace `~/.claude` composition** — reused composer, invoked per-workspace at `up` into a per-workspace staging home
- [x] **D5 git-credential composition** — token / deploy-key / agent (reused composer, unchanged)
- [x] **D4 public egress** — `ProxyEnv` wires `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` into the guest; `sandbox up --proxy host:port` points it at an externally-run proxy. **Supervisor (done, step 7):** a profile declaring `network.authorized_domains` makes `up` start a **detached** host-side CONNECT proxy (`internal/sandbox/proxysup.go` + `proxysup_linux.go`; a hidden `ape sandbox _proxyd` re-exec, `setsid`, fd-3 readiness handshake), dial-check it (**fail-closed** — `up` aborts if it can't come up), wire `HTTPS_PROXY`, and record its `pid`/`addr`/audit-log path in the registry; `down` SIGTERMs it. `--proxy` still wins; no allowlist ⇒ open (default) egress. `PlanEgress` picks the mode. Live guest→proxy reachability (bridge routing / `--network none` enforcement) is Tier-2.
- [x] **D7 access** — sshd is in the image (key-auth-only, over the forwarded `--ssh-port` loopback); `sandbox ssh` builds the `ssh` invocation. **authorized_keys (done, step 8):** the composer stages the guest `~/.ssh/authorized_keys` from the profile's `access.authorized_keys` (public-key literals or `~/.ssh/*.pub` paths; `composeSSHAccess`); the image sets the `ape` user's home to `/sandbox/home` (so sshd reads the composed keys and `HOME` matches `exec`) with `StrictModes no` (virtio-fs bind ownership needn't match `ape`). *Image change needs a rebuild to take effect; end-to-end key-auth is Tier-2/3.*
- [x] **D8 `ape doctor` checks** — `kvm.available` (+ `kvm` group remediation), `containerd.running`, `kata.runtime`, `sandbox.image` (`doctor_checks.go`, registered in `doctor.go`). Non-required; degrade to INFO off-Linux / when the toolchain is absent.
- [x] **D3 profile** — extended the loader with `backend`, `vmm`, `image`, `mount` (+ defaults + validation)
- [x] **Tests (Tier-1)** — composer, profile (incl. new fields), proxy, OCI-config builder, **nerdctl command construction + registry** (`kata_test.go`), doctor sandbox checks, **egress-proxy supervisor lifecycle** (`proxysup_test.go`: `PlanEgress`, daemon args, `ProxyState`/registry round-trip, in-process `RunProxyDaemon` deny+audit). Green under `-race`; Windows cross-compile + lint clean.
- [x] **Tests (Tier-2 scaffolding, step 7)** — `internal/sandbox/integration_linux_test.go` (`//go:build linux`), gated on `APE_SANDBOX_IT=1` + `/dev/kvm` + `nerdctl`, **skipped by default** (this box has no toolchain yet). Covers the acceptance matrix: host-fs mount writable both ways, real `~/.claude` not readable in-guest (mode B), exec/non-zero-exit, pause/resume state, down leaves no container, and egress allow+deny+audit (needs `APE_SANDBOX_IT_HOST` = guest-reachable host IP).
- [x] **Code cleanup** — retired `runner.go` / `runner_linux.go` / `runner_other.go` / `runner_linux_test.go`; pure layers kept + extended.
- [x] **Docs** — `docs/how-to/sandbox-workspaces.md`, `docs/reference/sandbox-profile.md`, sandbox section added to `docs/reference/bridge-security.md` (the security doc is under `reference/`, not `explanation/`), README index updated, `docs/reference/cli.md` regenerated with the `ape sandbox` tree. Link-check green. North-star referenced by name (separate repo — not a relative link, to keep the docs link-checker green).

> **Session note (2026-07-07, steps 1–6).** Accuracy notes for the next session:
> 1. **Masking vs. Kata rootfs.** `spec.go`'s tmpfs-shadow masking of `/home` etc. is a host-`/`-rootfs (runsc) concern. With Kata + an OCI image the guest rootfs is the *image*, so the host fs isn't in the VM at all — isolation on the nerdctl path comes from the image rootfs + explicit virtio-fs binds, not masking. `BuildSpec` is retained + unit-tested as the reusable OCI-config builder for the `ctr`/OCI-bundle driver path; the primary Phase-1 driver is `RunArgs` (nerdctl flags).
> 2. **Egress proxy lifecycle** was the one D4 gap — now closed (step 7, below).

> **Session note (2026-07-07, step 7 — egress supervisor + Tier-2 scaffolding).**
> 1. **Egress-proxy supervisor (D4) built + verified.** `up` supervises a detached CONNECT proxy when the profile declares `network.authorized_domains`; `down` stops it. Fail-closed (`up` aborts if the proxy can't start; the container never comes up). Verified live *without Kata* (the supervisor needs only the `ape` binary): `ProxySupervisor.Start` spawns `ape sandbox _proxyd` via `setsid`, reads the ephemeral loopback addr back over fd 3, dial-checks it; `Stop` SIGTERMs it and the listener stops accepting. The daemon itself was smoke-tested end-to-end (denies unauthorized host → 403 + audit row → clean SIGTERM exit).
> 2. **Reaping.** `up` `Release()`s the child and never `Wait`s; the daemon is reaped by init once `up` exits. `down` is a separate process (not the parent), so SIGTERM + no-wait is correct there too. (A harness that keeps the parent alive will see the killed daemon as a `Z` zombie — expected, not a leak.)
> 3. **Honest boundary (unchanged, documented).** The supervisor guarantees a *host-side* deny-by-default proxy + audit. It does **not** yet force the guest through it (a guest process ignoring `HTTPS_PROXY` and using default bridge networking can still egress). Kernel-level enforcement (`--network none` + proxy-only reachability) is a Tier-2/live networking task.
> 4. **Tier-2 tests are a gated scaffold**, skipped by default (no KVM/containerd/Kata on this box). They are correct-by-inspection, not yet executed. `provisionIT` carries a `mount` param + `MountEphemeral` case for future volume/ephemeral coverage (`//nolint:unparam` until those callers land).
> 5. **Still open (items 3–4):** `authorized_keys` composition for `ape sandbox ssh` (D7 follow-up), and image finalization (pin `BASE_IMAGE` digest / resolve `agent-infra/sandbox` coords / offline `/opt/apex-framework`).

> **Session note (2026-07-07, step 8 — authorized_keys / D7).**
> 1. **Composer + profile.** New `access.authorized_keys` profile field (list of public-key literals or `~/.ssh/*.pub` paths); `composeSSHAccess` writes `<staging>/.ssh/authorized_keys` (0600). Empty ⇒ no file (key auth unconfigured; use attach/exec). Validated (non-empty entries); missing key *files* fail at provision, not load. Tier-1 tested.
> 2. **Image.** `ape`'s home is now `/sandbox/home` (was `/home/ape`), so sshd's default `AuthorizedKeysFile` resolves to the composed `~/.ssh/authorized_keys` and `HOME` is consistent across `ssh`/`exec`; `StrictModes no` so the virtio-fs bind's in-guest ownership needn't match `ape`. **Source-only** — the published image must be rebuilt for this to take effect; end-to-end key-auth is Tier-2/3.
> 3. **Still open (item 4):** image finalization — resolve/pin `agent-infra/sandbox` `BASE_IMAGE` to a digest, optionally prefer the offline `/opt/apex-framework`. The build/publish needs the container toolchain (docker/nerdctl) not yet installed on this box.

> **Session note (2026-07-07, step 9 — image finalization / D6).**
> 1. **Base pinned.** Confirmed the upstream coordinates via the agent-infra/sandbox README: `ghcr.io/agent-infra/sandbox` (current release 1.11.0; CN mirror also exists). Resolved the `1.11.0` manifest-list digest over the public GHCR API (anon token → manifest HEAD, no docker needed): `sha256:6328d7fd2f0ff0b4c147c3d05b3df1ce331f4a482eb6e550ecd64ed1fcf906e7`, a multi-arch list covering `linux/amd64` + `linux/arm64` (matches the Dockerfile's buildx `TARGETARCH`). Pinned `ARG BASE_IMAGE=ghcr.io/agent-infra/sandbox:1.11.0@sha256:6328d7fd…`. README documents a docker-free re-resolve recipe.
> 2. **Offline framework.** No Go change needed — `ape framework setup` already resolves the repo from `$APEX_FRAMEWORK_REPO`. Added `ENV APEX_FRAMEWORK_REPO=/opt/apex-framework` to the image; `ape framework setup --no-fetch` installs offline.
> 3. **Blocked here:** building/publishing the image needs docker or nerdctl (none installed on this box; installing them + containerd/Kata is the outstanding host-toolchain step — needs the user + sudo). Once built, tag to the ape release and bump `sandbox.DefaultImage`.
> 4. **Phase-1 status:** all in-repo Phase-1 deliverables (D1–D8) are code-complete; what remains is host-toolchain provisioning + Tier-2/3 live validation, and the Phase-2–4 platform-repo work.

### Reuse — already built + unit-tested in this repo (`internal/sandbox`)

- [x] Profile loader + validation (`profile.go`) — extend, don't rewrite
- [x] `~/.claude` + git-credential composer (`compose.go`, `gitcred.go`) — reuse per-workspace
- [x] OCI spec builder (`spec.go`) incl. tmpfs-shadow masking — reuse for the kata bundle/OCI config
- [x] CONNECT proxy + wildcard matcher + egress audit (`proxy.go`, `match.go`) — reuse unchanged

### Deferred to the platform repo (Phases 2–4)

- [ ] **Phase 2** — in-VM `ape` **NATS worker** (folds PLAN-13/14 into the workspace); infra-network enrollment
- [ ] **Phase 3** — **Netbird** two-overlay networking (infra + per-project, setup keys, default-deny policy); SSH/VS Code Remote over the overlay
- [ ] **Phase 4** — preview/demo/staging environments (ephemeral per branch, auto-idle-stop, shareable); fleet scheduling; BYOC control/data-plane packaging
- [ ] **Device tier** — GPU (exclusive VFIO passthrough via `kata-qemu`) + USB passthrough; host IOMMU/`vfio-pci` setup; profile `devices:`

## Goal

`ape sandbox up dev` provisions a long-lived Kata microVM workspace: hardware
boundary (own guest kernel + KVM), the project available inside (host-fs by
default), a `~/.claude` containing exactly what the profile says, and
deny-by-default public egress. You then `ape sandbox ssh dev` (or VS Code
Remote) and work inside — run Claude Code, APEX pipelines, or Playwright —
across many sessions; `ape sandbox freeze dev` cgroup-freezes it (guest RAM
stays resident; `unfreeze` thaws instantly — a real VM suspend isn't available on
Kata-via-containerd, so the distinct `suspend` verb returns `UNSUPPORTED`, PLAN-18
D7); `ape sandbox down dev` tears it down. The workspace cannot touch the rest of
the host even though
every ape/claude session inside runs with `--dangerously-skip-permissions`.

## Why (Kata workspaces, not gVisor jobs)

- **The unit of work is a durable workspace, not a one-shot job.** Most work
  happens in long-lived sessions (an agent worker, a dev box you SSH into, a
  test runner), so per-task VM boot time is irrelevant and a persistent,
  hardware-isolated environment is the right primitive.
- **Hardware boundary.** A Kata microVM has its own guest kernel enforced by
  KVM — a guest-kernel compromise still can't reach the host. This is the
  boundary the "run claude with permissions off" model needs.
- **kata-only is simpler** than a two-backend design and matches how the
  leading platforms build this (Northflank runs Kata + Cloud-Hypervisor at
  scale; Daytona offers optional Kata for hardware isolation).
- **The gVisor path hit a hard blocker** (below): raw `runsc run` rootless
  can't serve the writable project mount — the core of the feature.

## Non-goals (Phase 1)

- **No gVisor backend.** Dropped (see spike findings). gVisor + nvproxy is noted
  only as a *future* GPU-sharing option in the platform north-star.
- **No NATS worker, no Netbird overlays, no preview/staging** — Phases 2–4,
  platform repo.
- **No device tier (GPU/USB) in Phase 1** — needs host IOMMU + `vfio-pci`
  binding; lands with the platform. Design leaves room (`vmm: kata-qemu`,
  `devices:`).
- **No macOS-hosted VMs** — Kata/KVM is Linux-only; Macs join as SSH/VS Code
  Remote clients (overlay in Phase 3). Apple `container`/Virtualization.framework
  is a separate future backend.
- **containerd is a host prerequisite, not a Go dependency** — ape shells out to
  `nerdctl`/`ctr`, keeping the CLI a single binary. *(PLAN-18 superseded this: the
  live-validated path is `aped` driving the containerd **Go client** (`--driver
  containerd`); the `nerdctl` shell-out is the retired default `shellDriver`, which
  can't run through `aped`'s hardened root executor. `ape` stays a single binary
  and no longer touches containerd — `aped`, as root, does.)*

## Backend decision: kata-only

- **Kata** is the OCI/containerd orchestration bridge; the VMM does the
  virtualization. **Default VMM: Cloud-Hypervisor (`kata-clh`)** — fast,
  low-overhead, production-proven. **`kata-qemu`** for the device tier (GPU/USB
  passthrough) later. VMM is per-workspace via the profile.
- ape drives Kata through **containerd** (`nerdctl run --runtime
  io.containerd.kata.v2 …` / `ctr`), shelling out — no containerd Go client in
  `go.mod`. Docker Engine 23+ is a fallback (`--runtime` works there too).
  > **[2026-07-14 — superseded by PLAN-18].** `ape` no longer drives containerd;
  > `aped` does, as root. The live-validated driver is the containerd **Go
  > client** (`aped run --driver containerd`), which builds the OCI spec as a
  > typed object with no client-side `mount(2)`. The `nerdctl`/`ctr` shell-out
  > became the default `shellDriver`, which the hardened root executor forbids
  > (its client-side rootfs mount is denied).
- Requires **KVM / nested virt** on the host (see doctor + risks). This gates
  deployability: many cloud VMs don't expose nested virt (a platform-phase
  concern; local bare-metal dev boxes have it).

## Design

### D1: Workspace runner (`internal/sandbox`, containerd-driven)

- **Provision:** `ape sandbox up <name>` starts a **detached** Kata container
  from the resolved image with the kata runtime class, the composed `~/.claude`
  and project mounted, `HTTPS_PROXY` set, sshd running. Long-lived; not tied to
  a single job.
- **Interact:** `attach`/`exec`/`ssh` (and VS Code Remote) into the running
  workspace; run Claude Code / pipelines / Playwright inside — the in-guest
  `ape`/`claude` allocate their own PTY + bridge IPC exactly as on the host.
- **Lifecycle:** `pause`/`resume` (suspend idle), `down` (teardown with a
  persistence policy per mount mode); a small on-disk registry tracks running
  workspaces (name → container id, profile, mount). *(Shipped as `freeze`/`unfreeze`
  — a guest cgroup-freeze, RAM resident, not a VM suspend; a distinct `suspend`
  returns `UNSUPPORTED` on Kata, PLAN-18 D7. The registry now lives in `aped`.)*
- **Driver:** shell out to `nerdctl`/`ctr`; the OCI config is built by the
  existing spec builder (Kata consumes an OCI bundle). The gVisor/runsc rootless
  runner from the spike is **retired**. *(PLAN-18: the shipped, live-validated
  driver is the containerd **Go client** — `aped run --driver containerd` —
  building the OCI spec as a typed object with no client-side `mount(2)`; the
  `nerdctl`/`ctr` shell-out became the default `shellDriver`, which can't run
  through `aped`'s hardened executor.)*

### D2: Per-workspace `~/.claude` composition (reuse)

The existing composer (`compose.go`) assembles a staging home mounted as the
guest `$HOME`, **once per workspace at provision** (not per job):

```
<staging>/
  .claude.json               # generated: onboarding-complete + prefs
  .claude/
    .credentials.json        # mode A only: bind of the host's real file
    settings.json            # generated from the profile (hooks/preferences)
    skills/<name>/…           # hand-picked (profile skill list)
    agents/<name>.md          # hand-picked (profile agent list)
```

- **Mode A — `credentials: oauth`.** Bind the host's real OAuth material into
  the guest home (`~/.claude/.credentials.json`; the minimal working file set
  incl. token refresh is a spike task — Tier-3 manual). Nothing else from the
  real home.
- **Mode B — `credentials: api-key`.** No credential files; `ANTHROPIC_API_KEY`
  injected via env from the profile's source. Scoped, low-limit key per
  workspace.
- **Skills/agents/hooks:** project-first resolution; curated user-level set
  copied by name/path (default empty — nothing leaks by omission); bridge hooks
  still injected via `--settings` and must survive every profile (Stop-hook
  completion depends on them). (Reused; unchanged from the composer already
  built + tested.)

### D3: Profile (extend the existing loader)

`_apex/sandbox/<profile>.yaml` gains `backend` / `vmm` / `image` / `mount`
alongside the existing credential/skills/git/network fields:

```yaml
name: dev
backend: kata                   # kata (only)
vmm: clh                        # clh (default) | qemu (device tier)
image: ""                       # "" → official ape-sandbox; or a custom OCI ref
mount: host-fs                  # host-fs | volume | ephemeral
credentials: oauth              # oauth | api-key
api_key_source: env:APE_JOB_ANTHROPIC_KEY   # api-key mode
skills: []                      # guest ~/.claude/skills (name or /abs/path)
agents: []
project_skills_overlay: ""
ignore_project_settings: true
preferences: {}                 # → settings.json (may carry a hooks block)
network:
  authorized_domains:           # public egress allowlist (D4) — CONNECT 443
    - api.anthropic.com
    - "*.githubusercontent.com"
git:
  mode: none                    # none | token | deploy-key | agent
  token_source: env:APE_JOB_GITHUB_TOKEN
  deploy_key: ""
# devices: []                   # (device tier, later) VFIO GPU/USB ids
```

CLI: `ape sandbox <verb> [name] [--profile <p>]` (Linux-only). No `--sandbox`
flag on the run commands anymore — the workspace *is* the environment; you run
jobs *inside* it.

### D4: Public egress (reuse)

The existing CONNECT proxy (`proxy.go`) runs on the host outside the workspace;
the guest gets `HTTPS_PROXY`. Deny-by-default; `network.authorized_domains`
(exact + leading-wildcard); every CONNECT (allowed + denied) recorded to
`egress-audit.jsonl` (per-connection metadata; hostnames only, never payloads).
Private/overlay reachability (NATS, project peers) is a **Phase-3 Netbird**
concern and composes with this proxy (overlay = private, proxy = public).

### D5: Git credentials (reuse)

`git.mode`: `token` (recommended — generated `.gitconfig` + credential helper
serving an env token over HTTPS, `url.insteadOf` rewrite) · `deploy-key`
(read-only key bind + pinned `known_hosts`) · `agent` (ssh-agent socket bind —
live signing capability, see D9) · `none`. Reused from the built composer.

### D6: Image pipeline (official + custom)

- **Official `ape-sandbox` image:** pinned, audited, versioned with ape +
  framework releases. Contains claude, node, `ape`, git, the APEX framework,
  sshd, chromium + Playwright, build tools, and the `~/.claude` composition
  entrypoint. Base/reference: **`agent-infra/sandbox`** (Apache-2.0; browser
  VNC+CDP, VSCode Server, terminal, MCP) — build `FROM` it (pinned) or
  cherry-pick; note it expects `seccomp=unconfined` (acceptable inside a Kata
  VM — the VM is the boundary), and don't track its `:latest`.
- **Custom images:** profile `image:` = any OCI ref; the composer overlays
  `~/.claude` + credentials at provision.
- Snapshot/template a golden workspace for fast provisioning (platform phase).

### D7: Access (SSH / VS Code Remote)

sshd runs in the image. Phase 1: connect over host loopback (a forwarded port).
Phase 3: connect over the project Netbird overlay (no public inbound port). VS
Code Remote-SSH installs its server into the guest; Dev Containers can attach to
the running Kata container too.

### D8: `ape doctor` checks

- `kvm.available` — `/dev/kvm` present **and accessible** (operator in the `kvm`
  group); FAIL with the `usermod -aG kvm` remediation.
- `containerd.running` — daemon reachable; `nerdctl`/`ctr` on PATH.
- `kata.runtime` — `io.containerd.kata.v2` installed; `kata-runtime`/kata
  version.
- `sandbox.image` — the official `ape-sandbox` image (or the profile's `image:`)
  is pulled.

### D9: Honest boundaries (documented, not solved)

- The project mount (host-fs share) is inside the boundary: an in-VM session can
  write `.git/hooks`, Makefiles, direnv files that the *host* may later execute.
  `mount: ephemeral` (clone-in-VM) avoids this for untrusted work.
- Mode A places the full OAuth token inside the boundary; egress allowlisting
  doesn't stop exfiltration *to Anthropic* via prompt content. Mode B + scoped
  key is the recommendation for untrusted work.
- `git.mode: agent` bind-mounts a live signing capability (any key in the agent)
  for the workspace's lifetime; prefer `token` for untrusted work.
- Device passthrough (later) widens the boundary (DMA-capable hardware in the
  guest) and needs root + IOMMU — scope it to the workspaces that need it.

## Spike findings (2026-07-05, live rootless `runsc` — why we pivoted to Kata)

Ran against `runsc release-20260622.0`, rootless, no sudo. These blocked the
gVisor-job design and drove the reframe; kept here as rationale.

1. **Rootless runsc works with no sudo** for fs/process isolation (unprivileged
   userns on). ✅
2. **`maskedPaths` is NOT honored by rootless runsc over a host-`/` rootfs** —
   the guest read the real `~/.claude/.credentials.json`. **Fix found + verified:
   shadow sensitive dirs (`/home`, `/root`) with an empty tmpfs mount** — carried
   into the OCI spec builder and reusable for Kata. ✅
3. **Rootless runsc can't use the isolated netstack** (falls back to host
   networking) — no kernel-hard egress rootless.
4. **BLOCKER: raw `runsc run` rootless serves bind mounts empty** (gofer
   `aname=/`; a mountpoint absent from the ro rootfs crashes the sandbox). This
   makes the **writable project mount** — the core of the feature —
   unattainable via raw rootless runsc. Kata sidesteps this: containerd + the
   Kata shim set up virtio-fs project mounts as a first-class, supported path.

Net: the fs-masking learning and all the pure layers carry over; the gVisor
*runner* does not. Kata gives the writable project mount + a hardware boundary
without the rootless-gofer limitations.

## Testing tiers

- **Tier 1 — GitHub CI (existing `ubuntu-latest` + `windows-latest`).** Unit
  tests for the pure logic: profile loader/validation, `~/.claude`/git composer
  fixtures, wildcard matcher, CONNECT proxy via `httptest` (allow/deny + audit),
  OCI-config builder (struct→JSON). No KVM, no containerd needed. Kata-touching
  code behind `//go:build linux` with a portable stub so the Windows leg
  compiles.
- **Tier 2 — local / self-hosted only (KVM + containerd + Kata).** Provision a
  real Kata workspace and assert: project mount is writable (edits reflect per
  mount mode), the real `~/.claude` is not readable in-guest, public egress is
  allowlisted + audited, sshd/exec works. **GitHub-hosted runners lack nested
  virt**, so this never runs there; gate on `APE_SANDBOX_IT=1` + KVM presence.
- **Tier 3 — manual checklist.** Real OAuth (mode A), real scoped key (mode B),
  real `git push`, VS Code Remote into a workspace, Playwright run inside — real
  credentials/services/API spend; never a CI gate.

## Steps

> **[2026-07-14].** These are the Phase-1 steps as authored. Shipped reality
> (PLAN-18): the verbs are `freeze`/`unfreeze` (not `pause`/`resume`) plus
> `inspect`/`suspend`; `ape` is a thin `aped` client, not a local runner; and the
> live driver is the containerd Go client (`--driver containerd`), not a
> client-side `nerdctl` shell-out. See the reconciliation banner and PLAN-18.

1. **Retire the gVisor runner** from the spike; keep the pure layers (profile,
   composer, git-cred, OCI-config builder, proxy).
2. **`internal/sandbox` kata runner** — containerd/`nerdctl` shell-out:
   provision/exec/attach/pause/down + workspace registry. Linux build tags +
   Windows stub. Tier-1 unit tests on the OCI-config + command construction.
3. **Extend the profile** (`backend`/`vmm`/`image`/`mount`); wire per-workspace
   composition + `HTTPS_PROXY` egress.
4. **`ape sandbox` command** (`up/ls/attach/ssh/exec/pause/resume/down`) +
   `ape doctor` checks (KVM/containerd/kata/image).
5. **Official `ape-sandbox` image** (base off `agent-infra/sandbox`, pinned) +
   `image:` override; publish/versioning.
6. **Tier-2 local integration tests** (gated) + **docs**
   (`how-to/sandbox-workspaces.md`, `reference/sandbox-profile.md`,
   `explanation/bridge-security.md`), linking the north-star.

## Acceptance

### Tier 1 (CI)
- Profile with `backend/vmm/image/mount` loads + validates; bad combos rejected.
- Composer output matches the profile (mode A binds only `.credentials.json`;
  mode B injects the key, no cred files; skills/agents match exactly; git files
  correct) — the existing fixtures, now including the new fields.
- OCI config builds with the read-only-root + tmpfs-shadow masking, the project
  + home mounts, and the expected env for each mode/mount.
- Proxy allow/deny + `egress-audit.jsonl` rows (per-connection byte counts) via
  `httptest`.

### Tier 2 (local, KVM+containerd+Kata)
- `ape sandbox up dev` provisions a Kata workspace; `exec`/`ssh` works.
- `mount: host-fs` — a file written in-guest at `/workspace` appears on the
  host; the real `~/.claude` is **not** readable in-guest.
- Public egress: an allowlisted host succeeds + is audited `allowed`; a
  non-allowlisted host is refused + audited `denied`.
- `pause`/`resume` preserve in-guest state; `down` leaves no leaked container /
  staging / registry entry. *(Shipped as `freeze`/`unfreeze` — cgroup-freeze, RAM
  resident; `down` drops the `aped` registry entry.)*

### Tier 3 (manual)
- Mode A OAuth session authenticates and sees only the profile's skills.
- Mode B session works via the injected key.
- VS Code Remote / SSH into a workspace; run Claude Code and a Playwright test
  inside; `git push` with a token.

## Risks

- **KVM / nested-virt availability** gates the whole feature (Kata needs it).
  Fine on local bare-metal; a real constraint for future cloud hosting
  (platform phase) — many cloud VMs don't expose nested virt. `ape doctor`
  surfaces it; there is **no gVisor fallback** by decision.
- **Kata/containerd host setup** is heavier than a single binary — mitigated by
  clear doctor checks + docs; containerd stays a host prerequisite, not a Go
  dep.
- **Official image maintenance** — the `ape-sandbox` image must track claude /
  framework / ape versions; pin + version it, don't float `:latest`.
- **Mode A OAuth file set** is undocumented and may shift with Claude Code
  versions — pin via a Tier-3 manual check; composer fails loudly when it moves.
- **virtio-fs I/O overhead** on the project mount — acceptable for
  agent/dev sessions; measure if it bites.

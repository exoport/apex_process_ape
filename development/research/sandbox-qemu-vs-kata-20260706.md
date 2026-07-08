---
created_at: 2026-07-06
status: open
tags:
  - sandbox
  - isolation
  - kata
  - qemu
  - kvm
  - gvisor
  - security
summary: >
  Deep comparison of hardware-VM isolation backends for ape sandboxed jobs
  (PLAN-16), driven by a hard requirement for GPU + USB/PCI passthrough / a
  true hardware boundary — which rules gVisor out for those workloads. Compares
  raw QEMU/KVM vs Kata Containers (and the Firecracker / Cloud-Hypervisor / QEMU
  VMM choice underneath Kata) across security, setup, local vs server use,
  filesystem mounting, VS Code Remote / SSH, snapshots / layers, and headless
  browsing. Verdict: Kata Containers with the QEMU VMM (kata-qemu) is the sweet
  spot — hardware boundary + mature GPU/USB passthrough + OCI ergonomics
  (virtio-fs project mount, image layers) + standalone-or-service + swappable
  VMM; raw QEMU only for maximum device control at the cost of building all
  orchestration yourself. Neither is truly rootless once passthrough is
  involved — GPU/USB/VFIO forces a root component + host IOMMU setup. gVisor +
  RootlessKit remains the best lightweight/no-device/no-sudo tier. Recommend a
  multi-backend runner interface: gVisor (light) + Kata-QEMU (VM/devices),
  profile-selected.
---

# Sandbox backends: QEMU/KVM vs Kata Containers (2026-07-06 research)

> **MAJOR REFRAME (2026-07-06, late): kata-only + durable VM *workspaces*, not
> ephemeral per-task sandboxes.** User decision: the model is long-lived VMs
> you do lots of work inside, not a sandbox wrapped around each short job. This
> makes VM boot time irrelevant, moots gVisor's "light/fast/no-image" edge, and
> makes **kata-only** the choice (one backend, guest image central). This is
> the *persistent-workspace* pattern (Daytona, Fly.io Sprites, Northflank —
> the last runs it on Kata at 2M workloads/mo), not the ephemeral pattern
> (E2B). The three target workflows:
> 1. **NATS worker VM** — VM boots an in-guest `ape` that connects to NATS
>    (PLAN-13/14) and runs received requests inside the VM. The VM *is* a
>    worker node; PLAN-14's service runs *inside* the sandbox.
> 2. **Dev-environment VM** — VM + VS Code Remote / SSH in; run/debug code and
>    **run Claude Code inside** the VM interactively.
> 3. **Playwright/test VM** — VM runs browser tests inside.
>
> Consequences (supersede the two-tier framing below, which was the
> ephemeral-job design):
> - **Drop gVisor** as a backend. One backend: **kata-qemu** (device tiers) —
>   optionally kata-clh for non-device VMs later.
> - New CLI surface is **VM lifecycle**, not job-wrapping:
>   `ape sandbox up/ls/attach/ssh/exec/pause/resume/down <name>` (names TBD).
> - The **guest OCI image** (`ape-sandbox`: claude/node/ape/git/framework/
>   chromium+playwright/sshd/build tools) is now a first-class, versioned,
>   published-or-user-provided artifact.
> - `~/.claude` composition (D2) happens **per-VM at provision**, not per-job;
>   the VM has a persistent home + `/workspace` (virtio-fs share of the host
>   project, or a persistent block volume).
> - **Persistence/idle:** pause/resume + snapshot/template a golden VM so new
>   workspaces provision fast and idle ones stop consuming resources
>   (Daytona-style auto-stop/archive policies).
> - **PLAN-16 heavily merges with PLAN-14** — the sandbox VM is the execution
>   substrate for NATS workers, plus a standalone dev-VM feature.
> - Reusable from the code already built: profile loader, `~/.claude`/git
>   composer, CONNECT proxy + audit, OCI-spec builder. Not reused: the
>   gVisor/runsc rootless runner (kata drives via containerd/nerdctl).
>
> The rest of this doc (two-tier gVisor+kata) is retained as the analysis that
> led here; read it through the lens of this reframe.

Follow-up to `sandbox-isolation-20260702.md` and PLAN-16. Requirement that
drove this: **sandboxed ape jobs may need GPU + USB/PCI passthrough or a
true hardware-enforced VM boundary.** That rules gVisor out _for those
workloads_ (gVisor has no VFIO/USB passthrough; only NVIDIA CUDA via
`nvproxy`). So the real question is **raw QEMU/KVM vs Kata Containers**.

## TL;DR

- **Recommended: Kata Containers on the QEMU VMM (`kata-qemu`).** It gives the
  hardware boundary _and_ the most mature GPU/USB (VFIO) passthrough, while
  keeping OCI ergonomics: mount the project via virtio-fs, get image layers
  for free, run it **standalone** (nerdctl/ctr/Docker) locally or **as a
  service** (containerd/k8s), and **swap the VMM** (Cloud-Hypervisor /
  Firecracker) per workload from one integration.
- **Raw QEMU/KVM** only if you need device control beyond what Kata exposes,
  or want zero container stack — but you then build images, virtiofsd,
  networking, lifecycle, and snapshots yourself. Heavier than containerd,
  against ape's lean charter.
- **Hard truth:** the moment you require **GPU/USB/PCI passthrough**, you
  accept a **root component + host IOMMU/BIOS setup** (bind device to
  `vfio-pci`, no host driver on it). Kata's "rootless VMM" mode _disables_
  device passthrough. There is no rootless GPU-passthrough path.
- **Keep gVisor + RootlessKit** as the light tier (no devices, no sudo, no
  guest image) for the common coding-agent job. The two are complementary,
  not either/or — see "Mapping to ape".

## First: "Kata" always rides a VMM — pick one

Kata is an OCI runtime that launches each sandbox as a lightweight VM using
**one of three VMMs**. The VMM choice decides passthrough + speed, so it's
the real fork:

| VMM                  | Boot                                     | Mem/VM     | GPU/PCI passthrough                                                     | USB                                | Notes                                                               |
| -------------------- | ---------------------------------------- | ---------- | ----------------------------------------------------------------------- | ---------------------------------- | ------------------------------------------------------------------- |
| **Firecracker**      | ~125 ms (≈28 ms from snapshot)           | <5 MiB     | **Not yet** — active community PCIe roadmap (`feature/pcie`), but only virtio-pci in milestone 1; no GPU/device passthrough in mainline as of 2026 | No                                 | 5 devices only; fastest/densest; snapshot-restore is its superpower |
| **Cloud-Hypervisor** | ~100–200 ms                              | low        | **Yes (VFIO)** but _less mature_ — documented IOMMU-group gaps in-guest | limited                            | ~16 modern virtio devices; hotplug, live-migration, REST API; Rust  |
| **QEMU**             | ~2–5 s (or `microvm` machine ~4× faster) | 100s of MB | **Yes — most mature** (NVIDIA GPU Operator is built around `kata-qemu`) | **Yes** (`usb-host`/usbredir/VFIO) | 40+ devices, widest compat; ~2M LoC C = larger attack surface       |

**For your GPU + USB requirement → QEMU is the reliable VMM.** Cloud-Hypervisor
GPU passthrough exists but has open IOMMU-in-guest issues; Firecracker has
none. So "Kata for devices" concretely means **`kata-qemu`**.

## The big comparison (raw QEMU/KVM vs Kata-QEMU)

| Dimension                            | Raw QEMU/KVM                                                    | Kata Containers (kata-qemu)                                                                                      |
| ------------------------------------ | --------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| Isolation boundary                   | hardware VM                                                     | hardware VM (same KVM boundary)                                                                                  |
| Guest OS image                       | **you build + maintain** (kernel + rootfs w/ claude/node/ape)   | Kata ships a minimal guest kernel+initrd; **your workload comes from an OCI image** (layers)                     |
| Mount project (rw)                   | virtio-fs via `virtiofsd` you launch                            | virtio-fs **built-in** (default since Kata 2.0), one `virtiofsd` per VM                                          |
| GPU passthrough                      | ✅ most mature (VFIO)                                            | ✅ via `kata-qemu` (NVIDIA GPU Operator target)                                                                   |
| USB passthrough                      | ✅ `usb-host`/usbredir                                           | ✅ via VFIO/QEMU annotations                                                                                      |
| Image layers                         | ❌ (raw disk / qcow2 backing files)                              | ✅ OCI overlay snapshotter                                                                                        |
| VM snapshot/restore                  | ✅ qcow2 + `savevm`/`loadvm`, live external snapshots            | limited — VM _templating_/factory for fast start; not a general savevm workflow                                  |
| Networking                           | you wire tap/bridge or user-net (passt)                         | CNI auto (nerdctl), or your CNI                                                                                  |
| Egress allowlist                     | host nft on the tap, or in-guest                                | host nft on the veth/CNI, or in-guest; our CONNECT proxy works either way                                        |
| Local dev                            | bespoke scripts                                                 | `nerdctl --runtime io.containerd.kata.v2 …` (Docker-like)                                                        |
| As a server service                  | you orchestrate                                                 | native containerd / Kubernetes runtime class                                                                     |
| Rootless                             | non-root VM possible in `kvm` group; **passthrough needs root** | "rootless VMM" runs _QEMU_ as non-root, but **shim + virtiofsd still root**, and **passthrough breaks** under it |
| Setup effort                         | high (DIY everything)                                           | moderate (install kata + containerd/nerdctl)                                                                     |
| Reuses ape's OCI spec/composer/proxy | ❌ (no OCI)                                                      | ✅ (OCI bundle + containerd)                                                                                      |
| Startup                              | 2–5 s (or microvm)                                              | ~1–3 s typical (qemu); faster with CH/FC or VM templating                                                        |
| Attack surface                       | large VMM (mitigate w/ seccomp/microvm)                         | same QEMU surface + the Kata shim/agent                                                                          |

## Your questions, answered

### Most secure?

All three (raw QEMU, Kata, gVisor) beat plain containers. Ranking depends on
what you weigh:

- **Boundary strength:** VM (QEMU/Kata) > gVisor. A VM has a separate guest
  kernel, hardware-enforced (VT-x/EPT) — a guest-kernel compromise still
  can't reach the host. gVisor's boundary is a user-space kernel (software).
- **Host-facing attack surface:** gVisor (small Go sentry) < Cloud-Hypervisor/
  Firecracker (~50k LoC Rust) < QEMU (~2M LoC C, decades of device-emulation
  CVEs — but battle-tested + auditable; shrink it with the `microvm` machine
  type + seccomp).
- **Passthrough is a security _cost_:** exposing DMA-capable hardware (GPU/USB
  via VFIO) to the guest widens the boundary (device/driver/DMA bugs) and
  needs root + IOMMU. "Max isolation" and "USB passthrough" pull opposite
  ways. If a job needs passthrough, it is inherently less contained than one
  that doesn't — scope passthrough to only the jobs that require it.
- **Net:** for a _hardware boundary with devices_, **Kata-QEMU** is the
  pragmatic best; the residual risk is the QEMU surface, mitigated by KVM's
  hardware boundary underneath.

### Easy to setup?

- **Kata:** moderate — `kata-deploy` or distro packages install the runtime +
  guest kernel/initrd; drive it with `nerdctl --runtime io.containerd.kata.v2`
  (Docker-like) or `ctr`/Docker 23+. Needs `/dev/kvm` (present on this host).
- **Raw QEMU:** high — you build/maintain a guest image, write launch +
  `virtiofsd` + networking + lifecycle scripts, and own snapshots yourself.
- **Passthrough adds host setup either way:** enable IOMMU (`intel_iommu=on`/
  AMD), ACS, bind the device to `vfio-pci` (GPU must have **no host driver**
  loaded), udev rules for USB. This is BIOS + kernel-cmdline + root work,
  independent of Kata vs raw.

### Could it be used on a local machine?

Yes, both — need `/dev/kvm`. Kata is far easier locally via `nerdctl`. Raw
QEMU works but you script it. (Caveat: if the _host_ is itself a VM, you need
nested virt; `/dev/kvm` present here suggests bare-metal or nested-enabled.)

### Could it be used as a service on a server?

Yes. **Kata is the natural fit** — it's a containerd/Kubernetes runtime class,
so PLAN-14's service can spawn Kata sandboxes through the same containerd it
would use for any OCI job (`RuntimeClass`/`--runtime`). Raw QEMU as a service
means building your own VM pool/scheduler.

### Could we mount the fs? How do we mount the local folder?

Yes — **virtio-fs** in both.

- Kata: automatic. The runtime starts a `virtiofsd` per VM with
  `--shared-dir` = the container's bundle; your bind-mounted project appears
  in-guest under `/run/kata-containers/shared/...` and is presented as the
  container's mounts. With `nerdctl` you just `-v /host/project:/workspace`.
- Raw QEMU (manual): `virtiofsd --socket-path /run/x.sock --shared-dir
/host/project`, launch QEMU with the matching `vhost-user-fs` device +
  shared memory, then in-guest `mount -t virtiofs <tag> /workspace`.
- Trust note: Kata docs _recommend against_ host↔guest FS sharing for hostile
  workloads (prefer block volumes / `virtio-blk`). For our case (mount the
  project rw) virtio-fs is the right tool; just know it's a shared surface.

### VS Code Remote? SSH?

Yes, via **Remote-SSH** — the same mechanism whether QEMU or Kata:

- Run an **sshd inside the guest** (or the Kata container), forward a port
  (QEMU user-net `hostfwd`, or the CNI/tap IP). VS Code Remote-SSH installs
  its server into the guest and gives a full local-quality editor over SSH.
- Bonus with Kata: because it's OCI/containerd, you can also use **Dev
  Containers** (Remote-SSH → "Reopen in Container"), and attach to a running
  Kata container. Note VS Code's default _local_ bind-mount devcontainer flow
  doesn't apply to a remote/VM host — use Remote-SSH into the guest, or a
  socket-based devcontainer. (`AllowStreamLocalForwarding yes` in sshd if you
  tunnel the Docker socket.)
- This is a real ergonomic win over gVisor for interactive "sit inside the
  sandbox and code" sessions.

### Snapshots? Layers?

Two different things:

- **Layers (image/rootfs):** Kata ✅ via OCI overlay snapshotter — build a
  base image (claude/node/ape/framework) once, layer per-project on top; fast,
  dedup'd, cached. Raw QEMU: only qcow2 backing-file chains (manual).
- **VM snapshot/restore (freeze+resume):** raw QEMU ✅ (`savevm`/`loadvm`
  internal, external live snapshots with RAM). **Firecracker** ✅ and is the
  star here — resume-from-snapshot in ~28 ms (skips kernel boot), the trick
  behind sub-100ms agent sandboxes; but Firecracker has no passthrough, so
  you can't combine "snapshot-fast" and "GPU" in one VMM. Kata-QEMU/CH: VM
  _templating_ (a pre-booted template cloned per job) for fast start, but not
  a general savevm/restore workflow.
- If "instant warm sandboxes" matters more than devices for _some_ jobs,
  that's a Firecracker-snapshot use case — another reason to keep the VMM
  swappable.

### Could we do web browsing (headless Chromium / Playwright)?

Yes — and a **VM is actually better than gVisor** for this. Chromium/Playwright
(ape already uses Playwright for Excalidraw rendering) exercise a wide syscall
surface; a VM has a real guest kernel so there are no user-space-kernel compat
gaps, whereas gVisor occasionally needs workarounds. In a VM you install
Chromium in the guest image and control egress via the proxy/nft. So
"agent browses the web" is a point in favour of the VM tiers.

### Could it do "one or the other" (flexibility)?

Yes — this is a Kata strength. **One Kata integration, swappable VMM**:
`kata-qemu` for GPU/USB jobs, `kata-clh` for lighter isolated jobs,
`kata-fc` (Firecracker) for fast snapshot-restore jobs — selected per job via
runtime class / annotation. Raw QEMU is just QEMU. And at the **ape** level we
can go further (below): pick gVisor _or_ Kata per profile.

### containerd vs Docker (which container stack for the Kata tier)?

Decided acceptable to depend on containerd. How it compares to Docker:

| | containerd | Docker (Engine) |
| --- | --- | --- |
| Layer | low-level industry-standard runtime daemon (what Docker *and* k8s use underneath) | high-level: `dockerd` **on top of** containerd + BuildKit, Compose, volumes, Docker API |
| Kata integration | **native** (`--runtime io.containerd.kata.v2`) | works too (Engine 23+: `docker run --runtime …`) but via an extra daemon layer |
| CLI | `ctr` (low-level) or **`nerdctl`** (Docker-compatible) | `docker` (most familiar) |
| Weight / surface | leaner (one daemon) | heavier (dockerd + containerd + buildkit); `docker.sock` is root-equivalent |
| Service fit (PLAN-14) | **best** — same runtime k8s uses, direct Go client, no redundant layer | redundant layer over containerd |
| Rootless | yes (nerdctl rootless + RootlessKit) | yes (rootless Docker + RootlessKit) |
| Build images | needs BuildKit/nerdctl build | built-in |

**Recommendation:** target **containerd**, and have ape **shell out to `nerdctl`/`ctr`** rather than importing the (large) containerd Go client — keeps `go.mod` lean and matches how ape already shells to `runsc`/`git`/`claude`. Docker only as a fallback if a user already runs it (Engine 23+ accepts the same `--runtime`). This keeps the "single binary" spirit: no new Go dep, just an external daemon the device tier requires.

> Host note (2026-07-06): on this dev box `/dev/kvm` is `root:kvm` 660 and the
> user is **not in the `kvm` group**, so *no* KVM VMM (Firecracker/QEMU/Kata)
> can run as the user yet. One-time: `sudo usermod -aG kvm $USER` + re-login.
> This gates any live VM-tier verification here.

### GPU: kata-qemu passthrough (exclusive) vs gVisor nvproxy (shared)

Both back GPU, differently — the fork is *sharing*, not capability:

- **kata-qemu = VFIO passthrough = exclusive (1 GPU : 1 VM).** Whole card
  dedicated to the sandbox VM while it runs; hardware boundary. **Best when a
  dedicated/discrete GPU can be handed to the VM.** Bonus: the same kata-qemu
  tier also does **USB** — so *one* device tier covers GPU + USB.
- **gVisor nvproxy = shared.** Card stays usable by host/others; CUDA/LLM
  workloads work on consumer cards; software boundary; driver-version
  sensitive. **Only needed to share a single GPU** (e.g. a one-GPU
  workstation also driving the display, where passthrough would steal it).
- Shared-GPU-*in-a-VM* (vGPU/MIG) needs licensing + pro/datacenter cards — not
  an option for consumer GPUs.

**Decision:** make **kata-qemu the unified device tier (GPU + USB)**. Treat
nvproxy as an *optional* add-on to the gVisor light tier for the narrow
single-shared-GPU case, added only if a target machine needs it. Deciding
input = the device-tier machines' GPU profile (dedicated discrete → kata
passthrough; single shared → nvproxy).

> Host note (2026-07-06): this dev box has **no NVIDIA GPU** (Intel UHD 620
> iGPU only), so GPU — passthrough or nvproxy — is **not testable here**; it
> depends on the target machines. IOMMU groups are present (passthrough-capable
> in principle).

## Platform vision (2026-07-06): control/data plane + two overlay networks

User direction: beyond sandboxed workspaces, ape grows toward a **self-hosted,
hardware-isolated agent-dev platform** (Northflank/Daytona-class, tailored to
APEX + NATS), later covering **previews/demos/staging**. Official ape images
plus custom; project-mount supports all three modes; networking via
**WireGuard / Netbird**.

### Reference: how Northflank does it (the pattern to mirror)
- Runs on Kubernetes; **Kata is the orchestration bridge**, **Cloud-Hypervisor
  is the primary VMM** (Firecracker + gVisor applied per workload); GPU via
  Kata+CH where nested-virt exists, else gVisor fallback.
- **BYOC = control-plane / data-plane split:** managed control plane
  (orchestration, scheduling, lifecycle), data plane (the VMs) runs in *your*
  VPC/infra. Data never leaves your network.
- **Preview environments:** ephemeral full-stack per-PR/branch, snapshot/cache
  for fast recreate, **auto-shutdown when idle**, shareable URLs, teardown by
  tags. Staging = persistent/shared; preview = ephemeral/isolated.
- Hard dependency: **KVM/nested-virt** for microVMs; gVisor fallback where absent.

> VMM nuance vs. earlier: Northflank runs **Cloud-Hypervisor** in production
> (incl. GPU at scale), so CH is viable, not just QEMU. Practical call for ape:
> **kata-clh as the default VMM** (fast, production-proven), **kata-qemu for
> GPU/USB device VMs** (most-documented passthrough). Same "swap VMM per
> workload" flexibility, now with a concrete default.

### Networking: two Netbird (WireGuard) overlays
Netbird = WireGuard overlay with a **control plane** (Management + Signal +
Relay/Coturn) and **data plane** (direct P2P WireGuard). Key fits for ape:
- **Setup keys** → headless/automated peer enrollment: a VM joins networks at
  boot with a key, no interactive SSO.
- **Group-based, default-deny ACLs** applied via nftables on each peer → clean
  per-project isolation; peers connect **outbound-only** (no inbound ports —
  great for NAT'd VMs and dev laptops).
- Self-hostable end-to-end, OIDC IdP.

ape's two networks map onto Netbird groups + policies:
- **Infra/control network** — NATS cluster, VM provisioning, metrics,
  monitoring, the ape control plane. Members: control-plane nodes + every
  workspace VM's *management* side. Policy: VMs may reach NATS/metrics
  endpoints only.
- **Per-project working network** — one group per project: the project's
  workspace VM(s) + the developer's machine (+ the project's services). Policy:
  default-deny across projects; a VM sees only its project peers.
- A workspace VM enrolls into **both** (its project group + the infra group)
  via setup keys at provision. Cross-project traffic is denied by policy, not
  topology. This is the same control/data split Northflank uses.

Egress note: Netbird governs *overlay* (private) reachability (NATS, project
peers). Public egress (Anthropic API, web for Playwright, github) still rides
the D4 CONNECT proxy + allowlist. The two compose: overlay = private mesh,
proxy = public allowlist + audit.

### Project-mount: all three, context-selected (profile `mount:`)
- **host-fs (virtio-fs share)** — default for **local development**: edits
  live-reflect both ways; the developer works on the real project.
- **volume (persistent block)** — default for **server** long-lived
  workspaces: survives pause/resume, VM-owned, no host coupling.
- **ephemeral + clone** — for **server** untrusted/preview jobs: clone the repo
  into the VM, throw away on teardown (safest; matches preview environments).

### Suggested phasing (north-star → shippable)
1. **Kata VM workspace primitive** (kata-clh default / kata-qemu device):
   `ape sandbox up/attach/exec/pause/down`, official image, 3 mount modes,
   `~/.claude` composition per-VM, D4 proxy egress. (Rewrite of PLAN-16.)
2. **In-VM NATS worker** — the VM runs `ape` ↔ NATS (folds PLAN-13/14 into the
   VM); infra-network enrollment.
3. **Netbird two-overlay networking** — infra + per-project groups, setup-key
   enrollment, default-deny ACLs.
4. **Platform layer** — preview/demo/staging environments (ephemeral per
   branch, auto-idle-stop, shareable), control/data-plane BYOC split.

macOS caveat (unchanged): Kata/KVM is Linux. Mac participants join as Netbird
peers / VS Code Remote clients to Linux-hosted VMs; local Mac VMs would need
Apple `container`/Virtualization.framework — a separate backend, later.

## Mapping to ape

- **Most of what's already built is reusable for Kata.** The profile loader,
  synthetic-home + git-cred composer, CONNECT proxy + audit, and the **OCI
  spec builder** all apply to Kata (it consumes an OCI bundle via containerd).
  Only the _runner_ differs: instead of `runsc run`, a Kata runner talks to
  **containerd** with `--runtime io.containerd.kata.v2` (or shells `nerdctl`).
  PLAN-16 D1 already frames the runner as an interface and lists a hardware-VM
  backend as a follow-up — this fits.
- **Backend selection belongs in the profile** (`_apex/sandbox/<p>.yaml`):
  add `backend: gvisor | kata` (+ `vmm: qemu|clh|firecracker`, `devices: [...]`
  for VFIO). Then:
  - `gvisor` (+RootlessKit) = light, no-sudo, no devices, no guest image —
    the default coding-agent tier.
  - `kata` (qemu) = hardware boundary + GPU/USB, OCI images/layers, VS Code
    Remote — the heavy tier, needs containerd + root + host IOMMU setup.
- **Cost of Kata vs ape's charter:** Kata needs a **containerd daemon** and a
  guest image pipeline — a real departure from "single binary, no
  infrastructure services." Acceptable _as an opt-in heavy tier_, but it
  shouldn't be the default. gVisor/RootlessKit stays the lean default.

## Requirements summary (Kata-QEMU path)

Host (one-time, root):

- `/dev/kvm` + KVM (present here).
- containerd ≥ 1.7 (+ nerdctl for local UX).
- Kata ≥ 3.x (`kata-deploy` or packages) → installs guest kernel/initrd,
  `virtiofsd`, the shim, `configuration-qemu.toml`.
- For passthrough: IOMMU on (`intel_iommu=on`/AMD), ACS in BIOS, device bound
  to `vfio-pci` (GPU driver-free on host), udev/permissions for USB.
  Per job: an OCI image with claude/node/ape/framework; project bind (`-v`);
  egress via CNI + our proxy/nft; optional sshd for VS Code Remote.

## Final recommendation

1. **Keep gVisor + RootlessKit as the default lightweight tier** (no sudo, no
   devices, no image) — covers the majority of coding-agent jobs.
2. **Add Kata-QEMU as the opt-in "VM / devices" tier** for jobs needing a
   hardware boundary, GPU/USB passthrough, VS Code Remote, or heavier browser
   work. Reuse the OCI spec + composer + proxy; new runner = containerd/kata.
3. **Do not adopt raw QEMU/KVM** unless a device need exceeds what Kata
   exposes — the DIY image/network/snapshot burden is worse than containerd
   for our shape.
4. **Accept and document** that the device tier requires root + host IOMMU
   setup and is inherently less contained than the gVisor tier — scope
   passthrough to the jobs that truly need it.
5. Make **backend + VMM profile-selectable** so one integration serves light
   (gVisor), isolated (kata-clh/fc), and device (kata-qemu) jobs.

Decisions locked (2026-07-06):
- (a) **containerd dependency is acceptable** for the device tier — ape shells
  to `nerdctl`/`ctr` (no Go client), Docker as fallback. It is *optional*:
  only the device tier needs it; the gVisor light tier stays daemon-free.
- (b) **OCI image: both** — ape publishes a base `ape-sandbox` image
  (claude/node/ape/framework) *and* a profile may point to a user-provided
  image (`image:` field).
- (c) **Device tier is first-class, not niche: ~25% of projects** need it —
  GPU for local models, USB for hardware development. So Kata-QEMU is a
  supported tier, resourced accordingly (not a "someday" follow-up).

Still open:
- Is Firecracker-snapshot "instant (~28 ms) warm sandbox" worth a *third* VMM
  option for the no-device fast path, or does gVisor already cover "fast +
  light" well enough? (Firecracker still has no GPU/USB, so it's orthogonal to
  the device tier.)
- On this host, the `kvm`-group gap blocks live VM-tier testing until
  `usermod -aG kvm` is done.

## Sources

- Kata rootless VMM: <https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-run-rootless-vmm.md>
- Kata + containerd/nerdctl standalone: <https://kata-containers.github.io/kata-containers/how-to/containerd-kata/>
- Kata NVIDIA GPU passthrough (QEMU): <https://kata-containers.github.io/kata-containers/use-cases/NVIDIA-GPU-passthrough-and-Kata-QEMU/>
- Cloud-Hypervisor GPU IOMMU gap: <https://github.com/kata-containers/kata-containers/issues/11687>
- Kata virtio-fs: <https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-use-virtio-fs-with-kata.md>
- virtio-fs project: <https://virtio-fs.gitlab.io/>
- Firecracker vs Cloud-Hypervisor vs QEMU: <https://northflank.com/blog/firecracker-vs-cloud-hypervisor>, <https://northflank.com/blog/firecracker-vs-qemu>
- Firecracker snapshot restore (~28 ms): <https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md>
- QEMU snapshots (qcow2/backing/savevm/live): <https://wiki.qemu.org/Documentation/CreateSnapshot>
- VS Code Remote-SSH + Dev Containers: <https://code.visualstudio.com/docs/remote/ssh>, <https://code.visualstudio.com/remote/advancedcontainers/develop-remote-host>
- passt/pasta rootless networking: <https://passt.top/>

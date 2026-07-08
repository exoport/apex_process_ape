---
created_at: 2026-07-08
status: open
kind: research-prompt
tags:
  - sandbox
  - aped
  - kata
  - qemu
  - vfio
  - nats
  - nex
  - research-brief
summary: >
  A self-contained research/investigation prompt to deepen and ADJUST the
  `ape`/`aped` split design. Feed it to a multi-agent research run (or a fresh
  session) to validate + refine the design doc and produce a concrete
  implementation plan. Encodes the LOCKED decisions so the research refines
  within them rather than relitigating.
---

# Research prompt — refine the `ape` + `aped` design (Kata-QEMU rootful, GPU/USB passthrough, NATS everywhere)

## How to use this
Run this as a **complete, multi-track investigation** (parallel research agents or
a deep-research pass), then apply the findings as **adjustments** to the design
doc. **Read first, in order:**
1. `development/research/ape-aped-split-20260707.md` — the design doc to refine (it
   already synthesizes an initial 3-agent research pass: security/prior-art,
   NATS/NEX, VM-mechanics/VFIO).
2. `development/planning/plan-16_kata-vm-workspaces.md` — Phase-1 (Kata VM
   workspaces) as-built; **keep it intact** — the `ape`/`aped` split is *separate*,
   future work (a prospective **PLAN-18**; **PLAN-17 is already taken** — reporting
   CLI + NATS identity), not a rewrite of PLAN-16. Also read the NATS foundation
   plans **PLAN-13** (`nats-events-and-transcript-blobs` — eventing/blobstore +
   subject taxonomy), **PLAN-14** (`ape-service-nats-micro` — the NATS-micro service
   pattern), and **PLAN-17** (`reporting-cli-and-nats-identity` — identity from the
   `.creds` user JWT + server-enforced `ape.*.<token>.>` subjects + the
   agent-self-reporting mode). The `aped` NATS layer **builds on these; it does not
   reinvent them.**
3. `development/research/sandbox-qemu-vs-kata-20260706.md` — backend comparison
   (note: its "kata rootless" premise was disproven — see below).
4. `_output/2026-07-07-sandbox-host-changes-and-cleanup.md` — what the rootless
   experiments did to the box and why they failed.
5. The current code: `internal/sandbox/` (`kata.go` `Runner`/`RunArgs`/`Registry`,
   `kata_linux.go`, `compose.go`, `gitcred.go`, `proxy.go`, `profile.go`,
   `spec.go`) — the layers `aped` must absorb/reuse.

**Validate against CURRENT upstream versions** (Kata Containers 3.32.x, containerd
2.x, nerdctl 2.3.x, NATS server + nats.go/micro, NEX). Prefer primary sources
(project docs, source, GitHub issues) and cite everything.

## LOCKED decisions — do NOT relitigate; refine WITHIN these
1. **Split:** `ape` = unprivileged CLI (runs AI-agent workloads + orchestration);
   `aped` = the only rootful component, a narrow, audited VM-management daemon.
   `ape` never runs as root.
2. **Backend:** **Kata Containers + QEMU VMM (`kata-qemu`), rootful.** Hard KVM
   boundary + mature GPU/USB VFIO passthrough. `kata-clh` is a non-device option;
   Firecracker is a *future* dense/no-device tier.
3. **Rootless is off the table** for device/hard-boundary workspaces — VFIO/IOMMU
   passthrough is physically rootful (proven this session; Kata's rootless-VMM
   disables passthrough). Do not re-explore rootless Kata.
4. **GPU + USB passthrough is a hard requirement** for local device workspaces.
5. **Transport: embedded NATS everywhere.** `aped` embeds a NATS server; one
   **NATS-micro** contract serves host `ape` (management subjects), the in-VM
   `ape` agent (per-VM telemetry-only subjects, denied management), and the future
   company NATS cluster. Security = per-credential subject authz + per-VM-unique
   creds. (This is a firm product decision; the team uses embedded NATS broadly.)
6. **NEX = borrow patterns, own the control plane.** NEX is Firecracker-only (no
   passthrough); reuse its *shapes* over Kata-QEMU, don't fork it.
7. **Primary threat model = VM→host escape.** Defenses: the Kata KVM boundary +
   credential-scoped control plane. Host-side allowlists are defense-in-depth.
8. **`ape` stays single-binary / dependency-light;** `aped` (a dedicated daemon)
   MAY carry heavier deps (e.g. the containerd Go client).
9. **Keep PLAN-16** as the Phase-1 record; this work is additive.
10. **Build the NATS layer on PLAN-13/14/17 — do not reinvent it.** `aped`'s control
    API follows **PLAN-14**'s NATS-micro pattern; per-credential identity + the
    server-enforced `ape.*.<token>.>` subjects come from **PLAN-17**'s identity
    model; the in-VM `ape` agent's telemetry/metrics/transcripts = PLAN-17's
    "report from a session with only creds" mode over **per-VM creds** `aped` mints
    at VM create (scoped to that VM's telemetry subjects only); eventing +
    transcript blobs = **PLAN-13**. **`aped` ≈ PLAN-14's `ape service` daemon** —
    reuse its `internal/service` NATS-micro shape (`micro.AddService`, `$SRV`,
    keyed admission, graceful shutdown, submitter-vs-daemon identity nuance),
    elevated to **rootful VM management** instead of spawning CLI child processes.
11. **Composition — the "NATS worker VM":** `aped` (host, PLAN-18) provisions a
    Kata VM; *inside* it the in-VM `ape` runs **PLAN-14 `ape service`** (accept
    jobs) / **PLAN-15 `ape script`** (yaegi orchestration) / **PLAN-17** reporting.
    Host `aped` = VM lifecycle; in-VM `ape` = workload + telemetry. Design PLAN-18
    to compose with these, not replace them.

## Deliverables (what the investigation must produce)
- **D1. Adjusted design doc** — concrete edits/additions to
  `ape-aped-split-20260707.md` (corrections, gaps filled, decisions firmed).
- **D2. A validated GPU/USB passthrough recipe** (Track B) — the riskiest, most
  load-bearing requirement; step-by-step + the exact OCI annotations `aped` must
  inject + the guest-image delta. Where possible, note what was actually tested
  vs. inferred.
- **D3. A draft PLAN-18 (ape/aped split)** — phased, actionable steps with
  acceptance criteria, that leaves PLAN-16 intact and reuses PLAN-13/14/17.
- **D4. A "borrow-from-NEX" table** — each NEX pattern → keep/adapt/reject for
  the Kata-QEMU implementation, with source pointers.

## Investigation tracks

### Track A — NEX pattern extraction (read the source)
Study `synadia-io/nex` + docs. Extract concretely: node/embedded-NATS bootstrap;
the control-API surface + subject scheme (`$NEX.…`); the **agent-in-VM protocol**
(how the guest agent authenticates + what subjects it uses); **Xkey** secret
encryption in deploy requests; node auto-clustering + **auction placement**;
workload lifecycle + state machine. For each: **keep / adapt / reject** for a
Kata-QEMU implementation, and *why*. Explicitly flag what does NOT transfer
because NEX is Firecracker/no-passthrough.

### Track B — Kata-QEMU rootful + GPU/USB VFIO passthrough (end-to-end, current)
The load-bearing risk. Produce a **validated, step-by-step recipe** for Kata 3.32:
- Host prereqs: IOMMU (`intel_iommu=on`/`amd_iommu=on`, `iommu=pt`), `vfio-pci`
  module + device binding, IOMMU-group isolation checks, `/dev/vfio` perms,
  `RLIMIT_MEMLOCK`, no-host-NVIDIA-driver, single-GPU constraints.
- **How passthrough is expressed to Kata-QEMU:** hot-plug (`--device
  /dev/vfio/<group>`) vs **cold-plug via annotations** (`io.katacontainers.*`,
  `cdi.k8s.io/vfio*`, `cold_plug_vfio=root-port`, `enable_iommu=true`,
  `pcie_root_port=N`); which containerd config (`pod_annotations`,
  `privileged_without_host_devices`, `enable_annotations`) is required; the
  **exact annotation set `aped` must inject**. Confirm nerdctl can't emit these →
  quantify the need for `ctr --annotation` or the containerd Go client.
- **USB**: xHCI-controller VFIO vs QEMU `usb-host`; what Kata actually supports.
- **Guest image delta**: VFIO drivers in-guest, virtual IOMMU, kernel config.
- Current failure modes + open Kata issues; what's reliable vs fragile in 3.32.
- Whether Cloud-Hypervisor is a viable passthrough alt (vs QEMU) in current Kata.

### Track C — `aped` daemon: privilege, hardening, process architecture
- Concrete systemd **system** unit: `CapabilityBoundingSet` (the minimal set for
  KVM + VFIO + tap/netns + device-node chown + cgroup), `NoNewPrivileges`,
  `SystemCallFilter`, `ProtectSystem`, socket/entry activation; target
  `systemd-analyze security` score.
- **Per-VM QEMU de-privilege** for the Kata path: is Kata's `rootless=true`
  (rootless-VMM: VMM runs as `kata-NNN`, shim/virtiofsd stay root) the right lever
  here, or a custom jail? How does it compose with rootful containerd + passthrough
  (recall: passthrough needs the privileged parent to chown `/dev/vfio/*`)?
- **De-privileged VM-facing front-end vs root executor:** how to split the process
  so a `nats-server`/telemetry exploit reachable from a guest does NOT yield host
  root — separate processes + minimal internal IPC? capability drop? Recommend a
  concrete architecture.
- Audit logging of every privileged op (+ `auditd` on `/dev/kvm`, `/dev/vfio/*`).

### Track D — NATS control plane (concrete)
- The **NATS-micro `vmm` service** definition (endpoints, subjects, versioning,
  `$SRV` discovery) refined from the design doc.
- **Per-credential subject authz** config: host-operator account vs per-VM
  telemetry account; deny management to guests; `allow_responses`. Provide sample
  server auth config.
- **Per-VM creds lifecycle:** how `aped` mints unique nkey/JWT (or token) per VM at
  create, injects them into the guest, scopes + revokes on destroy.
- **The private host↔guest transport:** how the in-VM agent reaches `aped`'s NATS
  without using public egress — bridge-gateway reachability vs a **vsock↔NATS
  bridge** (NATS has no native vsock). Recommend one, with the concrete wiring.
- **Interactive exec/attach over NATS:** per-session subjects + chunking +
  app-level flow control (dodge slow-consumer disconnect) vs an opportunistic
  side-channel; confirm the hybrid and specify the session protocol
  (stdin/out/ctl/exit + resize).
- Local↔cluster: leaf-node topology, per-tenant accounts, what stays identical.

### Track E — `Backend` interface + migration from PLAN-16 code
- Refine the Go `Backend` interface (op set, request/response schemas that double
  as the NATS wire contract, the `Stream` attach abstraction).
- **Map existing code onto it:** `Runner`/`RunArgs`/`Registry`/`compose`/`gitcred`/
  `proxy`/`profile`/`spec` → reused as-is / adapted / replaced. Define the
  `shellDriver` (nerdctl, non-device) vs `containerdDriver` (Go client, device
  tier + task events + PTY) split.
- **Pause vs Suspend vs Snapshot:** correct the PLAN-16 pause mislabel; specify
  real VM suspend via VMM (QEMU QMP `savevm`/managedsave; CH pause+snapshot) and
  templating (Kata `[factory] enable_template` / VMCache) — the mechanics `aped`
  drives beyond containerd task ops.

### Track F — Guest agent (the in-VM `ape`)
- Responsibilities: telemetry/metrics/**transcripts** + running workloads;
  explicitly NOT VM management.
- How it's delivered (baked in the `ape-sandbox` image? injected at create?),
  started, and given its per-VM creds; what subjects it may publish/subscribe;
  what it must be unable to do even if the guest is fully compromised.

### Track G — Firecracker future tier + platform/controller mapping
- firecracker-containerd fit behind the same `Backend` (devmapper snapshotter,
  no host-fs, no passthrough) as the dense/CI/preview tier.
- The **controller** above per-node agents: scheduling/placement from
  `Capabilities()`, multi-tenant accounts, image distribution, overlays — what's
  new vs. what the per-node `aped`/agent already does. Keep scheduling OUT of the
  per-node API.

## Guardrails
- Refine within the LOCKED decisions; if evidence contradicts a locked decision,
  flag it explicitly as a **finding that challenges a decision** (don't silently
  redesign).
- Distinguish **tested** from **inferred** — especially in Track B (note: the
  original dev box has an Intel iGPU / no discrete GPU, so GPU passthrough is not
  testable there; call out what needs a discrete-GPU box).
- Cite primary sources. Prefer current versions. Keep `ape` lean; `aped` may not.
- Output should be directly applicable as edits to the design doc + a PLAN-18
  draft — not an essay.

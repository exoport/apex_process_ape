---
plan_id: PLAN-18
created_at: 2026-07-08
status: proposed
tags:
  - sandbox
  - kata
  - qemu
  - vfio
  - passthrough
  - daemon
  - aped
  - nats
  - security
  - platform
summary: >
  Split `ape` into an unprivileged CLI (`ape`, runs AI-agent workloads +
  orchestration, never root) plus **`aped`** — the only rootful component, a
  narrow, audited VM-management daemon that drives **Kata-QEMU** microVMs with
  GPU/USB (VFIO) passthrough. Transport is **embedded NATS everywhere**: one
  NATS-micro `vmm` contract serves host `ape` (management), the in-VM `ape` agent
  (per-VM telemetry-only, management-denied), and a future company cluster.
  `aped` is built on the repo's NATS foundations — **PLAN-14** (`ape service`
  micro shape) elevated to rootful VM management, **PLAN-17** identity
  (server-enforced `ape.*.<token>.>`), **PLAN-13** eventing/blobs — and reuses
  PLAN-16's pure layers (compose/gitcred/proxy/match/secret/profile). This plan
  is **additive**: PLAN-16 stays the Phase-1 record; PLAN-18 is the prospective
  split. It maps to the four phases in the design doc §11 (extract `Backend` →
  local `aped` → device tier → remote/controller) and **front-loads a Phase 0**
  because PLAN-13/14/17 have no code yet (no `nats` dependency in `go.mod`, no
  `internal/{natsconn,eventing,reporting,service,blobstore}`). Key refinements
  from the 2026-07-08 investigation: a **network-less root executor** behind a
  de-privileged NATS surface (AF_UNIX `SO_PEERCRED` boundary), a **minimized
  capability set** (empty non-device / at most `CAP_CHOWN` device), a corrected
  **VFIO cold-plug recipe** (baked per-tier handler, `enable_annotations=[]`,
  drop `enable_iommu` from the GPU set, whole-IOMMU-group passthrough), and
  **Freeze vs Suspend** reconciled with what Kata-via-containerd actually
  supports today.
origin:
  - 2026-07-08 — deliverable **D3** of the multi-track investigation refining
    `development/research/ape-aped-split-20260707.md` (the design doc). Tracks:
    A (NEX extraction), B (Kata-QEMU VFIO passthrough), C (daemon hardening),
    D (NATS control plane), E (`Backend` + PLAN-16 migration), F (guest agent),
    G (Firecracker + controller). Findings adversarially verified; version
    currency checked against Kata 3.32.0 / containerd 2.3.2 / nerdctl 2.3.4 /
    nats-server 2.14.2 / nats.go 1.52.0.
  - 2026-07-07 — live rootless-Kata bring-up hit an unbreakable cgroup wall
    (Kata Go shim mkdir's cgroups at the host root); combined with the hard
    GPU/USB passthrough requirement this proved rootless is off the table for
    the device tier and forced the rootful `ape`/`aped` split (see the design
    doc origin + `_output/2026-07-07-sandbox-host-changes-and-cleanup.md`).
  - This work is **additive to PLAN-16** (Kata VM workspaces, Phase 1) and
    **reuses PLAN-13/14/17** (NATS) rather than reinventing them (LOCKED 9/10).
---

# PLAN-18: `ape` + `aped` — a rootful VM-management daemon for Kata-QEMU workspaces

> **Scope note.** This plan is the prospective **Phase-2+** split that the design
> doc `development/research/ape-aped-split-20260707.md` researches. **PLAN-16 stays
> intact as the Phase-1 record** — its pure layers (`compose.go`, `gitcred.go`,
> `proxy.go`, `match.go`, `secret.go`, `profile.go`, `spec.go`) are reused
> as-is; only `Runner`/`Registry`/`proxysup` are refactored behind a `Backend`
> interface. **PLAN-16's daemonless `ape sandbox` runner path is retired** when
> PLAN-18 lands — `ape` is always a thin `aped` client (no unprivileged
> `ape`→containerd path; that was the socket-≈-root smell). PLAN-16 stays the
> record and its pure layers are reused. **PLAN-17 is already taken** (reporting CLI +
> NATS identity); this is **PLAN-18**. The NATS layer **builds on PLAN-13/14/17,
> it does not reinvent them** — which is why **Phase 0 implements those first**.

## Status / task checklist

> Nothing here is built yet. PLAN-13/14/17 (the NATS foundation `aped` sits on)
> are `proposed` with **zero code**: no `nats` dependency in `go.mod`, and none
> of `internal/{natsconn,eventing,reporting,service,blobstore}` exist. Phase 0
> is a hard gate on everything else.

### Phase 0 — NATS foundation (prerequisite; PLAN-13 → PLAN-17 → PLAN-14)
- [ ] Implement **PLAN-13** (`internal/natsconn` + `Identity()`, `internal/eventing`, `internal/blobstore`; the `ape.{evt,log,metrics}.<user>.<project>.…` taxonomy; `ape.blob.uri-request`) — adds the `nats.go` dependency.
- [ ] Implement **PLAN-17** identity amendments inside PLAN-13's PRs (user token in subjects, `session` kind, `session_id` in payloads) + the reporting commands.
- [ ] Implement **PLAN-14** (`internal/service` NATS-micro shape: `micro.AddService`, `$SRV`, keyed admission, `req.Error` codes, graceful drain).

### Phase 1 — Extract the `Backend` interface (pure refactor of PLAN-16; no daemon)
- [ ] Define `internal/workspace.Backend` (D3) + JSON request/response types that double as the NATS wire contract; `WireVersion` const + sentinel errors mapping to PLAN-14 `req.Error` codes.
- [ ] `shellDriver` = today's `Runner`/`RunArgs`/`ExecArgs`/`AttachArgs` satisfying `Backend`; non-device happy path, fully Tier-1 testable.
- [ ] `Suspend`/`Resume`/`Snapshot` declared but return `ErrUnsupported` on the Kata path (D7).
- [ ] **Correct the pause mislabel** (D7): `kata_linux.go:35`, `kata.go:196-197`, `apecmd/sandbox.go:37` & `:353` — relabel `pause` as a **freeze** (guest cgroup-freeze, RAM resident).

### Phase 2 — `aped` local (rootful daemon, non-device Kata-QEMU)
- [ ] Three-process architecture (D1): network-less **root executor** + de-privileged **NATS surface** joined by an `SO_PEERCRED`-guarded typed-command AF_UNIX boundary.
- [ ] Embedded NATS + the **`vmm` NATS-micro service** on `ape.vmm.<node>.>` (D2), built on PLAN-14's shape.
- [ ] Per-credential subject authz (HOST_OPS vs TELEMETRY), per-VM cred minting at Create (D2).
- [ ] Policy engine + default-deny allowlists + structured audit (D9); minimal-cap hardened systemd unit(s) (D4).
- [ ] `ape` becomes a thin NATS client speaking `Backend` over the `vmm` contract.

### Phase 3 — Device tier (GPU/USB VFIO; **needs a discrete-GPU box**)
- [ ] `containerdDriver` (containerd 2.x Go client) for the task-event stream + PTY fidelity + typed OCI spec (D3/D5).
- [ ] VFIO orchestration (D5): `vfio-pci` bind, IOMMU-group enumeration/isolation check, baked per-tier `kata-qemu-gpu` handler, single-injection cold-plug, destroy+rebind+device reset.
- [ ] GPU guest-image build/signing workstream (D5): NVIDIA modules signed against the pinned Kata guest kernel + NVRC.
- [ ] Profile `devices:` (whole-IOMMU-group PCI; per-device USB via QEMU `usb-host`, aped-synthesised from a vendor:product allowlist).

### Phase 4 — Remote agent + controller + Firecracker tier (platform repo)
- [ ] Leaf-node topology to the company hub; per-tenant accounts; the same `Backend`/subjects/schemas (D8).
- [ ] Controller: scheduling/placement from `Capabilities()`, drain, image distribution, overlays — scheduling stays **out** of the per-node API.
- [ ] Firecracker dense/no-device tier as a **separate** node-local `firecracker-containerd` 1.7.x stack behind a third `firecrackerDriver` (D8).

## Execution order & session handoffs (PLAN-18 is the coordinator)

This plan **coordinates** the whole `ape`/`aped` line of work. PLAN-13/14/17 stay
separate plans — each independently valuable and shippable — but they are the NATS
foundation this plan sits on, so **build them in order, one focused session each,
and return here.** Do not start a session before the previous exit gate is green.

| Session | Open plan | Deliverable | Exit gate (green before the next session) |
| ------- | --------- | ----------- | ------------------------------------------ |
| 1 | **PLAN-13** (+ **PLAN-17** identity amendments, folded into PLAN-13's PRs) | `internal/natsconn`+`Identity()`, `internal/eventing`, `internal/blobstore`; the `ape.{evt,log,metrics}` taxonomy; `nats.go` in `go.mod` | PLAN-13 acceptance passes on an embedded `nats-server`; the subject taxonomy is captured in `docs/reference/events.md` (the single contract) |
| 2 | **PLAN-17** (reporting commands) | `ape event`/`log`/`metrics`/`transcript`; `internal/reporting`, `internal/sessionref` | PLAN-17 acceptance passes; a standalone `ape metrics` and a supervised run publish schema-identical payloads |
| 3 | **PLAN-14** | `internal/service` NATS-micro daemon (`ape service`) | PLAN-14 acceptance passes; the admission matrix + `$SRV` + graceful drain work |
| 4 | **PLAN-18 Phase 1** | Extract `internal/workspace.Backend`; `shellDriver`; fix the pause mislabel | Phase-1 acceptance (CI): `shellDriver` satisfies `Backend`, byte-identical nerdctl args |
| 5 | **PLAN-18 Phase 2** | `aped` local (network-less executor + de-privileged front-end + `vmm` service + per-VM creds + policy + audit); `ape` becomes a thin client | Phase-2 acceptance (Tier 2: KVM+containerd+Kata) |
| 6 | **PLAN-18 Phase 3** | `containerdDriver` + VFIO recipe + GPU guest image + profile `devices:` | Phase-3 acceptance (**Tier 3, discrete-GPU box — not available today**) |
| 7 | **PLAN-18 Phase 4** (platform repo) | Remote agent + leaf nodes + per-tenant accounts + controller + Firecracker tier | Phase-4 acceptance |

**Handoff protocol.** At the end of a session, verify its exit gate, then open the
next plan **in a fresh session** by its `plan_id` — a clean context per plan
matches the repo's one-plan-per-session convention. Sessions 1–3 are independently
useful (build them for their own sake if desired); Sessions 4–7 require 1–3 as a
hard prerequisite (**Phase 0**). **Session 6 (Phase 3) is blocked** until a
discrete-GPU + IOMMU box exists (see Testing tiers → Hardware availability).

### Folding in PLAN-10 (telemetry v2) remainder

**PLAN-10** is mostly shipped (v0.0.35–v0.0.37); its deferred remainder is a
cost/telemetry **data-quality** workstream that does **not** gate building `aped`,
but feeds the metrics/transcripts the in-VM `ape` agent publishes (PLAN-13/17). Do
each piece at the session where the master plan first benefits — not before:

- **With Session 1 (PLAN-13) — the one hard tie.** Extract
  `internal/cost.SessionFiles` (PLAN-10 D2 remainder): PLAN-13 blob-upload and
  PLAN-17 `ape metrics`/`ape transcript` both need to enumerate the main +
  sub-agent transcript set. The logic exists but is coupled inside
  `internal/apecmd/pipeline_interactive.go`; PLAN-10 D2 already says "extract when
  PLAN-13/17 need it" — that moment is here.
- **With Session 2 (PLAN-17) — accuracy before it goes on the wire.** Land PLAN-10
  D1 (per-turn `TurnRecord` + H6 requestId/stop_reason dedup) and D3 (date-aware
  Sonnet-5 pricing) **iff** `ape metrics` numbers are to be load-bearing once
  published over NATS / on the cluster (Session 7 / Phase 4 at the latest). Both
  are deferred today with a conservative fallback; promote them here, not earlier.
- **Anytime, independent.** The `cost.NewTailer` dead-code cleanup is unrelated to
  PLAN-18 — do it opportunistically.

If a phase doesn't need a given PLAN-10 item, it stays deferred; none of it blocks
`aped`. (The cost-discrepancy investigation that motivated PLAN-10 is already
resolved — `development/research/cost-discrepancy-20260521.md`.)

### Folding in PLAN-16 (Phase-1 Kata workspaces) remainder

**PLAN-16** is **code-complete in-repo** (D1–D8 shipped, Tier-1 green, Windows
cross-compile clean); it is `proposed` only because it lacks live validation + the
published image. It is **not a from-scratch session** — its code is the *starting
point* for this plan: **Session 4 (Phase 1)** extracts the `Backend` from PLAN-16's
`Runner`/`Registry`/`proxysup` and reuses its pure layers
(compose/gitcred/proxy/match/secret/profile/spec). Its remaining work is a
**host-provisioning + de-risking** workstream, **not gated by Phase 0 (NATS)**:

- **Host toolchain** — install containerd + Kata + nerdctl on a Linux box with KVM
  (needs sudo); `ape doctor` surfaces the gaps.
- **Official `ape-sandbox` image** — build + publish (D6), pin the digest, bump
  `sandbox.DefaultImage`.
- **Tier-2/3 live validation** — run the gated
  `internal/sandbox/integration_linux_test.go` (`APE_SANDBOX_IT=1` + `/dev/kvm` +
  nerdctl) + the Tier-3 manual checklist.

**When:** anytime a Linux+KVM box exists — ideally **early and in parallel with
the Phase-0 NATS sessions**, because it de-risks the whole Kata/containerd/nerdctl
+ compose/proxy stack that **PLAN-18 Phase 2 (Session 5) reuses inside `aped`**,
and it is a hard prerequisite for Session 5's Tier-2 validation. Note: PLAN-18
retires PLAN-16's *daemonless CLI path* (`ape` always goes through `aped`), but the
underlying Kata mechanics PLAN-16 validates are exactly what `aped` drives — so the
validation still pays off.

## Goal

`ape sandbox up dev` (unprivileged) talks to a rootful `aped` over embedded
NATS; `aped` provisions a Kata-QEMU microVM, and — for device workspaces —
binds a GPU / USB controller through VFIO into the guest. Inside the VM the same
`ape` binary runs in a locked-down **agent** role: it publishes telemetry on its
own per-VM credential and (optionally) accepts jobs as PLAN-14 `ape service`,
but it **cannot issue any VM-management command** even if the guest is fully
compromised. `ape` never runs as root; the only privileged surface is `aped`,
and `aped` is structurally incapable of being a generic "do X with root"
executor. Local and the future company cluster speak the identical `vmm`
contract — only credentials and topology differ.

## Why now

- The design doc is researched and the load-bearing risks are characterized;
  the split is a firm product decision (LOCKED 1–11). This plan turns the
  design into phased, testable work.
- The NATS foundation is the critical path: `aped` **is** PLAN-14's `ape
  service` elevated to rootful VM management, and its identity/telemetry are
  PLAN-17/13. Those are unbuilt, so Phase 0 gates the whole plan — surfacing it
  now prevents planning Phase 2 against vapor.
- Upstream moved: Kata is at 3.32.0 (QEMU is the runtime-rs default hypervisor
  and the only NVIDIA-reference GPU path), and current NEX has been rewritten
  (Firecracker removed) — both change the design doc's stated rationale (not its
  decisions), and this plan records the corrections.

## Non-goals

- **No rewrite of PLAN-16.** PLAN-16 stays the Phase-1 record and its pure layers
  are reused; only its daemonless `ape sandbox` runner path is superseded by `aped`
  (DECIDED — `ape` is always an `aped` client). PLAN-18 is additive.
- **No rootless device tier.** VFIO/IOMMU is physically rootful; Kata
  rootless-VMM breaks passthrough without a root-parent device-node chown
  (LOCKED 3). A rootless *non-device* tier (libkrun/gVisor) is out of scope.
- **No reinvention of the NATS layer.** Reuse PLAN-13/14/17 (LOCKED 10).
- **No forking of NEX.** Borrow shapes; own the control plane (LOCKED 6).
- **No confidential-computing GPU (SEV-SNP/TDX), driver-in-guest UVM/NVRC
  attestation, or multi-GPU NVSwitch** in the device tier — those are
  GPU-Operator/CDI/k8s-centric and separately budgeted.
- **No VM-level suspend/snapshot on the Kata tier** — unreachable through
  containerd/Kata today (see D7); `Suspend`/`Snapshot` return `ErrUnsupported`
  until a future VMM-owning driver or the Firecracker tier.
- **No scheduling in the per-node API** — that is the controller's job (D8).

## Locked decisions (encoded)

1. **Split:** `ape` = unprivileged CLI; `aped` = the only rootful component, a
   narrow audited VM-management daemon. `ape` never runs as root.
2. **Backend:** Kata Containers + **QEMU VMM (`kata-qemu`), rootful.** `kata-clh`
   is a non-device option (its GPU/VFIO integration is only now landing, not
   broken); Firecracker is the future dense/no-device tier.
3. **Rootless is off the table** for device/hard-boundary workspaces; do not
   re-explore it. (Rootless-VMM's VFIO breakage is a device-node permission gap,
   not a categorical disable — but fully-rootless Kata remains impossible.)
4. **GPU + USB passthrough is a hard requirement** — USB via per-device QEMU
   `usb-host` (aped-synthesised from a vendor:product allowlist), **not**
   whole-controller VFIO (which would leak the system keyboard/mouse); see D5.
5. **Transport: embedded NATS everywhere** — one NATS-micro `vmm` contract, per-
   credential subject authz, per-VM-unique creds.
6. **NEX = borrow patterns, own the control plane.**
7. **Primary threat = VM→host escape.** Kata KVM boundary + credential-scoped
   control plane; host-side allowlists are defense-in-depth. `aped` must never be
   a generic do-X-with-root executor.
8. **`ape` stays single-binary / dependency-light;** `aped` may carry the
   containerd Go client.
9. **PLAN-16 stays the Phase-1 record;** this is additive.
10. **Build the NATS layer on PLAN-13/14/17.**
11. **Composition — the "NATS worker VM":** `aped` (host) provisions the VM;
    inside it the in-VM `ape` runs PLAN-14 `ape service` / PLAN-15 `ape script` /
    PLAN-17 reporting.

## Design

### D1: Process architecture — a network-less root executor behind a de-privileged NATS surface

The VM→host-escape threat means the guest-reachable NATS front-end is attack
surface. Split `aped` so a `nats-server` exploit from a hostile guest can never
reach host root:

- **`aped-exec` (root executor) — the only privileged process.** Holds **no
  network address family except `AF_UNIX`** (`RestrictAddressFamilies=AF_UNIX`).
  Drives containerd + VFIO, re-validates every command against policy (D9), and
  performs the narrow privileged acts. Listens only on
  `/run/aped/priv.sock` (`AF_UNIX`, `SOCK_SEQPACKET`, `0660 root:ape`), accepts a
  **closed enum of typed, fully-resolved commands** (image digest, canonical
  mount path, resolved PCI BDF — never a free-form request or a caller host
  path), and verifies `getsockopt(SO_PEERCRED)` peer uid before acting. This
  relocates `SO_PEERCRED` from the (impossible) NATS leg to a real local socket
  where it is authoritative.
- **De-privileged NATS surface (`User=aped`, no caps, no `/dev`, no containerd
  socket).** Embeds `nats-server` + telemetry ingestion + subject authz +
  policy pre-check, and forwards typed commands to `aped-exec` over the AF_UNIX
  boundary. Management NATS binds `127.0.0.1` (host `ape`, guest-unreachable);
  guest telemetry NATS binds the bridge/gateway IP and is **TELEMETRY-account-
  scoped**, so a guest-facing RCE can never name a management subject.

A guest that pops `nats-server` lands in a capability-less, device-less,
containerd-socket-less process, scoped to the TELEMETRY account, that cannot
satisfy the root executor's `SO_PEERCRED` check — three independent barriers.

> **Sub-choice (open):** the guest telemetry listener may be a *second listener
> in the same de-privileged process* (simpler) or a *separate leaf gateway
> process* leaf-connected to the management server bound to TELEMETRY (stronger
> blast-radius isolation). Both keep `aped-exec` network-less and guests
> account-scoped. Recommend the separate gateway once the device tier ships.

**Correction vs the design doc:** Track D's "management NATS #1 runs in root
`aped`" is superseded — the root executor holds **no** NATS listener; management
NATS runs in the de-privileged front-end.

### D2: NATS control plane — the `vmm` micro service (built on PLAN-14/17/13)

**Service.** `micro.AddService(nc, micro.Config{Name:"ape-vmm", Version:<aped
semver>, Metadata:{node, hostname, kata_version}})`, one
`AddGroup("ape.vmm."+node)`, one `AddEndpoint` per `Backend` verb:
`capabilities | create | start | stop | exec | attach.open | freeze | unfreeze |
suspend | resume | snapshot | list | inspect | destroy`. `$SRV.{PING,INFO,STATS}`
discovery is free; multiple `aped` instances queue-subscribe; errors use
`req.Error` with the PLAN-14 code set (`BUSY`, `VALIDATION`, `NOT_FOUND`,
`UNSUPPORTED`, `DEVICE_UNAVAILABLE`, `DENIED`). Versioning = the micro `Version`
field + payload `"v":1` (PLAN-13 discipline); subjects are additive-only.

**Telemetry subjects = PLAN-17 model, not an ad-hoc root.** Per-VM telemetry
uses `ape.*.<vmtoken>.>` where `<vmtoken>` is the per-VM credential's subject
token — `aped` mints the JWT with `name=vm-<id>`, so telemetry flows on the
existing PLAN-13/17 roots `ape.evt.vm-<id>.…`, `ape.log.vm-<id>.…`,
`ape.metrics.vm-<id>.…`, plus `ape.blob.uri-request` — byte-compatible with
existing consumers. Management is the new additive root `ape.vmm.<node>.>`. This
replaces the design doc's `ape.vm.<id>.telemetry.>`.

**Two accounts (DECIDED).** HOST_OPS (management: host `ape` + the `aped` service
identity, `ape.vmm.<node>.>` + `$SRV.>`) and TELEMETRY (per-VM users). Account
isolation means a guest cannot even *name* a management subject — a stronger
guarantee than a deny rule. **`aped` holds a credential in *both* accounts** (it is
the server + minter + operator): a HOST_OPS cred for management and a TELEMETRY
subscriber cred for ingestion, so **no cross-account stream export/import is
required**. The **guest-facing de-privileged front-end holds ONLY the TELEMETRY
ingestion cred — never HOST_OPS**, so a front-end compromise cannot act as an
operator. `aped` runs the embedded server in **operator/JWT mode with a memory
resolver** (required to hot-mint per-VM users without a reload). deny-wins +
`allow_responses` cover the request/reply leg.

**Per-VM creds lifecycle.** Mint at Create with `jwt/v2` + `nkeys`
(`NewUserClaims` + `IssuerAccount` + `Encode(TELEMETRY signing key)` +
`FormatUserConfig`, or scoped signing keys with `{{name()}}` templating). Grant:
pub-allow `ape.{evt,log,metrics}.vm-<id>.>` (+ `ape.blob.uri-request` if
transcript offload is on) + `allow_responses`; sub-allow only `ape.svc.vm-<id>.>`
(the PLAN-14 job-intake) + a scoped `_INBOX_vm-<id>.>`; deny-sub the default
`_INBOX.>`; explicit deny `ape.vmm.>` and any other-VM `ape.*.vm-*.>`. **Inject**
via the PLAN-16 staging home as a **read-only bind-mounted `.creds` file**
(`~/.config/ape/vm.creds`, `0600`) + env `APE_NATS_URL`/`APE_NATS_CREDS`
(PLAN-13 D1 resolution — no new plumbing). Prefer the file over env: `.creds`
embeds the nkey seed, which env leaks via `/proc/<pid>/environ` + child
inheritance. **Invalidate** at Destroy primarily by the VM connection dropping +
a short JWT `exp` (re-minted while the VM lives); `AccountClaims.Revoke` + a
resolver push is break-glass (the open-connection caveat is moot — the VM is
gone). CreateRequest secrets are **xkey-sealed** (`nkeys` CurveKeys; pin
`nkeys ≥ 0.4.6` for CVE-2023-46129) for the remote tier.

**Private host↔guest transport.** Baseline: the **container-bridge gateway IP
over plain TCP** — the NEX model; `nats.go` dials it natively; set
`APE_NATS_URL=nats://<gateway-ip>:<port>` in the guest. This is a distinct
destination from public egress and must bypass the deny-by-default CONNECT proxy
(extend `NO_PROXY`) while staying off it. Recommend **TLS** on this leg
(`nats-server` is plaintext by default). A `vsock↔NATS` bridge is **future
hardening only**: NATS has no native vsock/UDS (`nats-server` discussion #7677
closed), so it needs a byte-relay on both ends + per-VM Kata guest-CID discovery
+ only works on kata-qemu's real vhost-vsock. It becomes attractive when the
hardened `--network none` + nft wall lands (the gateway IP disappears there), so
sequence it with that work.

**Interactive exec/attach.** Core NATS **drops slow consumers by closing the
connection**, so bulk stdio must not ride request/reply. Control via micro
`attach.open` (returns a session id + subject prefix), then explicit session
subjects `ape.vmm.<node>.exec.<sid>.{stdin,stdout,stderr,resize,control,exit}`
with **≤32 KiB frames + credit-based flow control**. Locally the operator is
on-host: the `Stream` (D3) is implemented directly over the containerd task-exec
PTY, so bulk stdio **never touches NATS** — same signature, two impls.

### D3: The transport-agnostic `Backend` interface + PLAN-16 migration

One interface (new `internal/workspace`, generalizing today's `Runner`) that a
local driver and a remote NATS client both implement, so `ape` and a future
controller code identically against either. Request/response types are
JSON-serializable and double as the NATS wire contract.

```go
package workspace

const WireVersion = 1

// Sentinel errors → PLAN-14 req.Error codes.
var (
    ErrUnsupported       = errors.New("workspace: unsupported on this backend") // UNSUPPORTED
    ErrNotFound          = errors.New("workspace: no such workspace")           // NOT_FOUND
    ErrBusy              = errors.New("workspace: busy")                         // BUSY
    ErrValidation        = errors.New("workspace: invalid request")             // VALIDATION
    ErrDeviceUnavailable = errors.New("workspace: device unavailable")          // DEVICE_UNAVAILABLE
    ErrPolicyDenied      = errors.New("workspace: denied by policy")            // DENIED
)

type Backend interface {
    Capabilities(ctx context.Context) (Capabilities, error) // scheduler input (§10/D8)

    Create(ctx context.Context, req CreateRequest) (Workspace, error) // detached
    Start(ctx context.Context, id string) error
    Stop(ctx context.Context, id string) error
    Destroy(ctx context.Context, id string, req DestroyRequest) error

    Exec(ctx context.Context, id string, req ExecRequest) (ExitStatus, error)
    Attach(ctx context.Context, id string, req AttachRequest, s Stream) (ExitStatus, error)

    // Freeze/Unfreeze: containerd cgroup-freeze — guest RAM stays resident. REAL today.
    Freeze(ctx context.Context, id string) error
    Unfreeze(ctx context.Context, id string) error

    // Suspend/Resume/Snapshot: VMM save/restore. ErrUnsupported on Kata-via-containerd
    // (shim Checkpoint unimplemented; the VMM control socket is Kata-owned). Only a
    // future VMM-owning driver / the Firecracker tier implements these.
    Suspend(ctx context.Context, id string) error
    Resume(ctx context.Context, id string) error
    Snapshot(ctx context.Context, id string, req SnapshotRequest) (SnapshotRef, error)

    Logs(ctx context.Context, id string, req LogsRequest) (io.ReadCloser, error)
    Events(ctx context.Context) (<-chan Event, error) // TaskExit/TaskPaused/TaskOOM …

    List(ctx context.Context) ([]Workspace, error)
    Inspect(ctx context.Context, id string) (Status, error)
}

type Stream interface { // interactive attach channel, transport-agnostic
    Stdin() io.Reader
    Stdout() io.Writer
    Stderr() io.Writer // explicit stderr sink (design-doc sketch had none)
    Resize(cols, rows uint16) error
    CloseWrite() error // half-close stdin
}

type CreateRequest struct {
    Name    string   `json:"name"`
    Image   string   `json:"image,omitempty"`   // "" → pinned default (digest-store only)
    Runtime string   `json:"runtime,omitempty"` // kata-qemu | kata-clh | firecracker
    Mount   string   `json:"mount,omitempty"`   // host-fs | volume | ephemeral
    Profile string   `json:"profile,omitempty"`
    Devices []Device `json:"devices,omitempty"`
    From    string   `json:"from,omitempty"`    // Kata factory template only on the Kata tier
}

type Device struct {
    // PCI: BDF → IOMMU group → vfio-pci — GPUs and whole PCI controllers.
    PCI string `json:"pci,omitempty"`
    // USB: a single device by "vendor:product" (ESP-32, barcode reader, dongle),
    // forwarded via QEMU usb-host — NOT whole-controller VFIO (that would leak the
    // system keyboard/mouse). Only aped synthesises the usb-host string, from a
    // per-caller vendor:product allowlist; the caller never sends raw QEMU args (D5).
    USB string `json:"usb,omitempty"`
}

type Capabilities struct {
    KVM      bool            `json:"kvm"`
    Runtimes []RuntimeInfo   `json:"runtimes"`
    HostFS   bool            `json:"host_fs"` // false on Firecracker nodes
    GPUs     []GPU           `json:"gpus"`
    USB      []USBDevice     `json:"usb"`      // passable USB devices (vendor:product) for usb-host
    IOMMU    IOMMUState      `json:"iommu"`
    Mem      MemInfo         `json:"mem"`
    Factory  FactoryState    `json:"factory"`
}
type GPU struct {
    BDF, VendorID, DeviceID, Model, Driver string
    IOMMUGroup   int
    GroupIsolated bool
    GroupMembers []string
}
type USBDevice   struct { VendorID, ProductID, Description string } // usb-host, not VFIO
type IOMMUState  struct { Enabled bool; Mode string; VfioReady bool }
type FactoryState struct { Templating, VMCache bool } // Templating shares RAM RO → KSM side-channel
```

**Migration map (from PLAN-16 `internal/sandbox`):**

| PLAN-16 symbol | PLAN-18 fate |
| --- | --- |
| `Runner` + `RunArgs`/`ExecArgs`/`AttachArgs`/`Pause|Resume|DownArgs`/`ProxyEnv`/`runtimeHandler` | `shellDriver` implementing `Backend` (verbatim builders) |
| `WorkspaceSpec` | `CreateRequest` (wire type) |
| `Registry` + `Workspace` | `aped` **server-side authoritative state** (source of truth for `List`/`Inspect`) + `Workspace`/`Status` wire types |
| `BuildSpec` (`spec.go`) | adapted into the `containerdDriver` OCI-spec builder (injects CDI + Kata hypervisor annotations) |
| `proxysup*.go` | moves **inside `aped`**, egress proxy tied to VM lifecycle, run de-privileged |
| `compose.go`, `gitcred.go`, `proxy.go`, `match.go`, `secret.go`, `profile.go` | **reused as-is** (client-side, pure) |

**Three drivers** (LOCKED 8 permits `aped`'s heavier deps): `shellDriver`
(nerdctl/ctr, non-device parity), `containerdDriver` (containerd 2.x Go client:
`v2/client`, `v2/pkg/cio`, `v2/pkg/oci`, `api/events` — for the task-event
stream + PTY fidelity + a typed auditable OCI spec), and `firecrackerDriver`
(D8, separate `firecracker-containerd` 1.7.x endpoint). One `aped` binary cannot
link both a containerd 2.x in-process client and fc-containerd's 1.7.x
compiled-in plugin; the drivers target different sockets, hidden by `Backend`.

> **Correction:** the containerd driver is **not** forced by "nerdctl can't emit
> annotations" (false — `nerdctl run --annotation` and `ctr --annotation` both
> exist). Its justification is the programmatic task-event stream, PTY/stdio
> fidelity over NATS, and owning the OCI spec as a typed object.

### D4: Privilege & hardening

- **Minimized capability set.** Because `aped-exec` is a containerd **client**
  (the heavy privileged work — cgroup/mount/netns/memlock/device-node prep — is
  containerd's / the shim's / QEMU's), the design doc's 7-cap set collapses:
  drop `CAP_SYS_ADMIN`, `CAP_NET_ADMIN`, `CAP_SYS_RESOURCE`, `CAP_IPC_LOCK`,
  `CAP_DAC_OVERRIDE`, `CAP_MKNOD`. Net: `CapabilityBoundingSet=` **(empty)** for
  the non-device tier; **`{CAP_CHOWN}`** for the device tier *only if* `aped`
  chowns `/dev/vfio/<group>` to a de-privileged VMM uid. (uid-0 writes the
  root-owned containerd socket and `0200/0644` root-owned `vfio-pci` sysfs bind
  files as *owner* — no `CAP_DAC_OVERRIDE` needed.) *Inferred from DAC + the
  chosen architecture; confirm against a running `aped`.*
- **Per-VM QEMU de-privilege = Kata rootless-VMM, not an aped-built jailer.**
  QEMU is a child of `containerd-shim-kata-v2`, not `aped`, so `aped` does **not**
  reimplement the Firecracker jailer. The lever is Kata `hypervisor.rootless=true`
  (or the `io.katacontainers.hypervisor.rootless` annotation), executed by the
  shim; shimv2 + virtiofsd stay root. For device VMs, rootless-VMM's VFIO needs
  the root parent to chown `/dev/vfio/<group>` to the random `kata-NNN` uid the
  shim picks at create (a uid handshake — see Risks).
- **QEMU host seccomp.** QEMU is the **only** Kata VMM with host-facing seccomp
  **off** by default. `aped`'s Kata-config management must set
  `seccompsandbox="on,obsolete=deny,spawn=deny,resourcecontrol=deny"` in the
  QEMU hypervisor config — the highest-value hardening the design currently
  omits.
- **systemd units.** `NoNewPrivileges=yes` (explicit — it's root),
  `ProtectSystem=strict` + minimal `ReadWritePaths=`, `ProtectProc=invisible`,
  allowlisted `RestrictNamespaces=`, `SystemCallErrorNumber=EPERM`. The
  `@system-service` filter already includes the KVM/VFIO `ioctl` and the
  chown/namespace syscalls, so **no `SystemCallFilter` additions are needed** and
  `~@privileged` is safe alongside `CAP_CHOWN` (verify `@chown ∉ @privileged` on
  the deployment's systemd). **`ProtectKernelTunables=yes` is incompatible with
  the `vfio-pci` sysfs bind** (it makes `/sys` read-only) — the device-tier
  executor omits it or delegates the bind to a separate-namespace oneshot helper.
  The `0660`/`SocketGroup=ape` gate belongs on the internal AF_UNIX `priv.sock`,
  **not** on the loopback-TCP NATS listener (where those bits are inert). Measure
  with `systemd-analyze security --offline=true` (predicted OK band ~2.5–3.8;
  the score credits none of the SO_PEERCRED boundary, subject authz, QEMU
  `-sandbox`, or KVM boundary — necessary, not sufficient).
- **TCB includes the separate rootful containerd + shim + QEMU**, whose units
  `aped` must also configure: `LimitMEMLOCK=infinity` on the containerd unit (VFIO
  pins all guest RAM; this box's default is 8192 KiB), and `seccompsandbox=on` in
  the Kata QEMU config. Verify memlock inheritance to QEMU via
  `/proc/<qemu-pid>/limits` (not `systemctl show`).

### D5: VFIO device tier — the validated passthrough recipe (D2 deliverable)

> **VALIDATION BANNER.** This box is an Intel UHD 620 **iGPU only** (`8086:5917`,
> `i915`), no `intel_iommu` in `/proc/cmdline` (11 IOMMU groups exist by kernel
> default DMAR), `/dev/vfio` holds only the container node. **GPU passthrough is
> NOT testable here.** Every step below is **primary-source / inferred**, not
> hardware-tested. Validate end-to-end on a **discrete-GPU box** (NVIDIA/AMD
> alone in its IOMMU group, `intel_iommu=on iommu=pt`, Kata 3.32 + rootful
> containerd) before finalizing this tier.

Ordered recipe `aped` owns (the work the NVIDIA GPU Operator does in k8s):

1. **Host prereqs.** IOMMU on at boot (`intel_iommu=on`/`amd_iommu=on`,
   `iommu=pt`); `LimitMEMLOCK=infinity` on both `aped` and containerd; no host
   NVIDIA driver bound.
2. **`vfio-pci` bind** of the target BDF: `driver_override` + unbind host driver
   + `drivers_probe` (the standard 3-line sysfs sequence; this is all the
   Operator's "VFIO-Manager" does). Lifecycle across reboots is an open
   decision: persistent initramfs/`driverctl` bind vs `aped` runtime
   unbind-at-create (a GPU driving the host display cannot be runtime-unbound).
3. **IOMMU-group check.** Enumerate `/sys/bus/pci/devices/<BDF>/iommu_group`.
   **Refuse a group only if it contains a device the caller is not authorized
   for, or that the host needs** — a GPU co-grouped with its own audio/USB-C
   function is normal; **pass all group members together**. Passing the whole
   group is necessary but **not sufficient**: place all same-group devices in
   **one guest PCIe address space** (size `pcie_root_port` to the function count;
   avoid an in-guest vIOMMU for multi-function groups) or QEMU fails "group N
   used in multiple address spaces" (Kata #10622).
4. **Baked per-tier handler.** Ship a dedicated `kata-qemu-gpu` containerd
   runtime handler (mirroring upstream `kata-qemu-nvidia-gpu`) whose `ConfigPath`
   **bakes** `cold_plug_vfio=root-port`, `hot_plug_vfio=no-port`,
   `pcie_root_port≥#GPUs`, the guest kernel+image, and memory — never
   caller-supplied. **Do not** put `enable_iommu=true` in the GPU set: Kata's own
   `kata-qemu-nvidia-gpu` template ships `enable_iommu=false`; the native
   `nvidia.ko` binds in guest-kernel mode. `enable_iommu=true` (guest vIOMMU)
   belongs to the separate `vfio_mode=vfio` tier (DPDK/nested — guest runs its
   own VFIO drivers). Cold-plug is required because hot-plug of large-BAR GPUs
   fails — not because of the generic q35 `pcie.0` restriction (solved by a root
   port) but because the root port's 64-bit prefetchable MMIO window is fixed at
   PCI enumeration and can't grow to fit a multi-GB BAR (+ a kata-agent
   PCI-rescan race); so `aped` must also **size the guest 64-bit MMIO
   (`pci-hole64`) window** to the largest BAR.
5. **`enable_annotations=[]`.** Kata's *default* is **not** minimal
   (`["enable_iommu","kernel_params","kernel_verity_params"]` — including the
   powerful `kernel_params` lever), so `aped` must **actively set
   `enable_annotations=[]`** (everything is baked into the handler config). Lock
   **both** gates: do **not** broadly allowlist containerd `pod_annotations`
   **and** set Kata `enable_annotations=[]`. The CRI knobs
   (`pod_annotations`/`container_annotations`/`privileged_without_host_devices`)
   are k8s-only and inert on `aped`'s direct ctr/Go-client path.
6. **Single-injection cold-plug.** On the standalone (non-k8s) path Kata
   cold-plugs a VFIO device passed as a plain OCI `--device /dev/vfio/<group>`
   (or a `cdi.k8s.io/vfio<n>` annotation `aped` mints from its device allowlist)
   when `cold_plug_vfio=root-port`. Inject the device **exactly once at sandbox
   creation** — passing both `io.katacontainers.*` and `cdi.k8s.io/vfio*` triggers
   double CDI injection (fixed upstream by PR #11150, still a footgun). The inner
   `cdi.k8s.io/vfio<N>` annotations are **runtime-generated**, not client-injected.
7. **Guest image delta.** The device tier needs a **separate, larger** guest
   image (+ possibly a custom kernel). NVIDIA GPU needs kernel modules built and
   **signed against the pinned Kata guest kernel** + NVRC + userspace libs (stock
   Kata image has none). Generic in-guest VFIO needs `vfio`/`vfio_pci`/
   `vfio_iommu_type1` + a vIOMMU; non-built-in drivers load via the
   `io.katacontainers.config.agent.kernel_modules` annotation. This is a
   non-trivial CI workstream that owns the host-Kata↔guest-kernel version
   coupling.
8. **Destroy.** `rm -f` + proxy stop + netns/nft teardown + **device reset (FLR)
   + scrub** before VFIO rebind to host (prevent cross-tenant state leakage) +
   registry remove.

**USB — per-device `usb-host` (DECIDED).** Whole-controller VFIO is **rejected**
for USB: an xHCI controller carries *all* its ports, so passing it would hand the
guest the **system keyboard/mouse** (and it is IOMMU-group-constrained). Instead
pass a **single device by `vendor:product`** via QEMU `usb-host` (`-device
usb-host,vendorid=…,productid=…`) — the ESP-32 / barcode-reader / dongle case:
device-level forwarding mediated by QEMU + the host USB stack (**no raw DMA**, no
IOMMU-group constraint, no keyboard/mouse leak). **Only `aped`** builds the
`usb-host` string, from a per-caller `vendor:product` allowlist; the caller sends
the typed `Device{USB}` and never raw QEMU args, so the attack surface is just
"which USB IDs may this caller pass." **Cost/risk:** Kata does not expose
`usb-host` today, so `aped` must add it (a small upstream Kata contribution — a
USB-device annotation — or a narrow aped-controlled qemu-device injection) plus
give the guest an emulated xHCI. Until then USB passthrough is unavailable
(controller VFIO stays rejected).

**The #1 load-bearing unknown:** whether the non-k8s single-phase (ctr/Go-client)
`--device /dev/vfio/<N>` + `cold_plug_vfio=root-port` cold-plugs end-to-end vs
strictly requiring the k8s CDI + Pod-Resources two-phase discovery. #11671 shows
it fragile; **validate on a discrete-GPU box before committing the design.**
Out of scope (Operator/CDI/k8s-centric): SEV-SNP/TDX confidential GPU,
driver-in-guest UVM/NVRC, multi-GPU NVSwitch, attestation.

### D6: Guest agent — the in-VM `ape`

- **Delivery = baked, not injected.** The agent is the **same `ape` binary
  already in the `ape-sandbox` image** (PLAN-16 D6, `Dockerfile` pins
  `ARG APE_VERSION`). Baked beats inject-at-create: reproducible, offline,
  digest-pinned, and it matches NEX (whose agent resides in the rootfs, never
  user-launched).
- **Startup = the OCI entrypoint, not systemd.** A Kata container-image VM has no
  init managing the entrypoint; the kata-agent spawns the image ENTRYPOINT
  directly. Add a best-effort background `ape sandbox-agent` launch to
  `entrypoint.sh` before `exec "$@"` (like sshd), **gated on per-VM creds
  presence** (`APE_NATS_CREDS`+`APE_NATS_URL` set). No creds → agent skipped → a
  workspace booted without per-VM creds (e.g. the image run in a test harness)
  still boots; the agent just doesn't start.
- **Two hats over one per-VM credential.** PLAN-17 reporting (event/log/metrics/
  transcript) + PLAN-14 `ape service` (accept jobs → spawn child `ape`). Because
  both connect with the same `.creds`, `natsconn.Identity().SubjectToken` = the
  per-VM token, so children inherit correct per-VM attribution for free.
- **Structurally management-incapable.** Two belts: (a) server authz denies
  `ape.vmm.>`; (b) the `sandbox-agent` subcommand carries **no vmm-request-builder
  code path** — an agent bug cannot be tricked into issuing management verbs. The
  same binary *can* be a vmm client on the host because capability is
  credential-scoped at the server, not compiled in.
- **`$SRV` discovery.** The in-VM `ape service` runs `--name=vm-<id>` and does
  **not** rely on global `$SRV` broadcast (it collides with the locked-down
  subscribe grant); discovery/liveness is via `aped`'s registry + a per-VM
  heartbeat, or name-scoped `$SRV.*.vm-<id>` grants.
- **Threat table.** A fully-compromised guest CAN poison its own per-VM
  telemetry, run workloads it was already meant to run, and read its own scoped
  `.creds`; it CANNOT issue any `ape.vmm.*` command, address/impersonate another
  VM, sniff another VM's replies, or reach the host operator / other tenants —
  all server-enforced regardless of the guest owning its creds.

### D7: Lifecycle — Freeze vs Suspend vs Snapshot, reconciled with reality

- **Freeze/Unfreeze (real today).** `containerd task Pause` on Kata is a **guest
  freezer-cgroup freeze with RAM fully resident** — chain
  `service.Pause → sandbox.PauseContainer → agent freeze_cgroup(cid,Frozen)`. Not
  a VM suspend. **Correct the PLAN-16 mislabel** at `kata_linux.go:35`,
  `kata.go:196-197`, and `apecmd/sandbox.go:37` & `:353` ("Suspend a workspace
  microVM" → freeze), and add a distinct future `ape sandbox suspend`.
- **Suspend/Resume/Snapshot (unreachable on Kata-via-containerd).** The Kata
  shim's `Checkpoint` returns `ErrNotImplemented`, and the VMM control socket
  (QEMU `qmp.sock`, often fd-passed; CLH `clh-api.sock`) is Kata-owned and QMP is
  single-client — so `aped` cannot drive QEMU `snapshot-save` / CH `snapshot`
  behind Kata's back. These return `ErrUnsupported` on the Kata drivers; real
  save/restore requires a future driver where `aped` **owns** the VMM lifecycle
  (jailer pattern) or the Firecracker tier (native snapshot API).
- **Fast-create.** `CreateRequest.From` on the Kata tier maps **only** to Kata
  factory templates (`[factory] enable_template`, requires `initrd=` — which the
  `image=`-booting `ape-sandbox` image does not use — and shares RAM read-only, a
  KSM-class side channel) or **VMCache** (no shared memory, no savings). Surface
  both in `Capabilities.Factory` with the KSM caveat; never a per-run flag.
- **Durable state + resume.** Adopt NEX's KV-persist + `existing=true`
  rehydration for `aped` restart/resume; map NEX's `warm` state to the factory
  pre-warm tier; use lameduck-style graceful drain for `aped`'s systemd stop.

### D8: Local ↔ remote symmetry, controller, Firecracker tier

**Identical local↔cluster:** the `vmm` endpoint set + schemas, the
`ape.{evt,log,metrics}.vm-<id>.>` telemetry taxonomy, per-VM creds model, exec
session protocol, all `Backend` handlers. **Different:** credentials
(`aped`-generated operator/memory-resolver locally vs company
operator/per-tenant accounts/TLS + a controller remotely) and topology (embedded
server + de-priv gateway locally vs an added outer **leaf** to the hub, `:7422`
outbound-only, per-tenant account).

**Per-node vs controller classification:**

| Concern | Per-node (`aped`, mostly built) | New at the controller |
| --- | --- | --- |
| Scheduling/placement/drain | reports `Capabilities()`; validates its own admission | picks a node, bin-packs, drains |
| Multi-tenant accounts | accounts/leaf/subject-authz/per-VM creds (D2) | mint/rotate per-tenant JWTs, run the hub, quotas, tenant→node policy |
| Image distribution | lazy pull (stargz/eStargz) + air-gap (baked `/opt/apex-framework`) | registry-cred distribution, pre-pull/replication, eStargz build pipeline |
| Networking overlays | per-VM tap/netns/egress-proxy | Netbird/WireGuard fabric + preview-URL ingress |
| Snapshot portability | node-local templates | pin `From:` to the holding node or replicate + normalize FC CPU |

Placement uses NEX's auction primitive **at the controller only** (tags carry
capability facts: `has-gpu`, region, free-IOMMU-group; the `Auctioneer` is the
per-node veto). NEX "auto-clustering" is plain NATS routing over a shared system
— not Raft/gossip — which validates the leaf-node topology.

**Firecracker tier = a separate node-local stack.** `firecracker-containerd` is
pinned to **containerd v1.7.x** (main `go.mod`: v1.7.29; open PR to v1.7.33; no
2.x migration) and needs a **specialized containerd binary** with its control
plugin compiled in (+ matching `firecracker-ctr`). It **cannot share** the
Kata-QEMU node's containerd 2.3.x stack — it runs as a separate containerd 1.7.x
+ `aws.firecracker` shim + devmapper snapshotter, driven by the third
`firecrackerDriver`. FC = ~5 virtio-MMIO devices, **no PCI/VFIO, no host-fs**, so
`Create` rejects `Devices` and `Mount: host-fs` at **admission** (node-side,
against its own `Capabilities()` — admission, not scheduling). Low upstream
velocity + untagged `firecracker-go-sdk`; pin exact SHAs and gate this tier
behind the Kata-QEMU tier shipping first.

### D9: Policy engine + allowlists + audit (the real boundary)

- **Config.** A typed `aped policy.yaml` (the PLAN-14 `service.yaml` analog),
  loaded and validated at startup, binding an authenticated identity → what it
  may request: allowed image digests, mount roots (canonicalized + re-checked
  after symlink resolution), the **device allowlist** (which caller may pass
  which PCI BDF (GPU) / USB `vendor:product` — the highest-value escalation target), and
  vCPU/RAM/count ceilings. Default-deny throughout. The root executor
  **re-validates every fully-resolved command** against policy (the CVE lesson:
  authorize the concrete parsed request, never a summary). A JSONSchema on
  `CreateRequest` is at most an input-shape guard **in front of** — never a
  substitute for — the typed Go allowlist.
- **Audit.** `aped` emits a structured audit event per privileged op — caller
  identity, operation, **resolved** args (canonical paths, device IDs, image
  digest), policy rule + decision, outcome — published on an additive PLAN-13
  subject `ape.audit.<node>.>` (append-only / forwarded). Backed by kernel-level
  `auditd` rules on `/dev/kvm` and `/dev/vfio/*` (path/dir watches, `arch=b64` +
  `arch=b32`, `perm=rwa`, a `-k` key, `-e 2` immutable-until-reboot so a
  compromised root can't silently disable auditing).

### D10: Borrow-from-NEX table (D4 deliverable)

> Current NEX (0.4.1; HEAD 2026-07-02) is **runtime-agnostic** — Firecracker/
> microVM/vsock **removed**; a node is a NATS-micro control plane that auctions
> to pluggable **Nexlets**. It has **no VM runtime, no VFIO/GPU/USB, and (having
> no VM) no host↔guest transport**. Its node↔nexlet trust model is **inverted**
> vs ours: NEX's nexlet is a *privileged executor the node drives*; our in-VM
> `ape` is the *untrusted* party. Correct mapping: **NEX-node → `aped` control
> surface; NEX-nexlet/agent → `aped`'s privileged executor; NEX-workload-creds →
> the in-VM `ape`.**

| NEX pattern | Verdict | For PLAN-18 |
| --- | --- | --- |
| Embedded-or-external NATS in the node (`WithInternalNatsServer`) | **Keep** | `aped` embeds `nats-server` (D1/D2) |
| SVC-control vs FEED-telemetry subject split | **Keep** | already have it: `ape.vmm.*` (PLAN-14) vs `ape.{evt,log,metrics}.*` (PLAN-13) |
| Node-minted **telemetry-only** workload creds (`WorkloadClaims`) | **Keep** | direct proof of LOCKED-5 per-VM creds; mint at Create (D2) |
| Three-tier credential ladder (bootstrap → operational → workload) | **Adapt** | optional enroll leg; per-VM creds minted at Create (D2) |
| NATS-micro control API | **Adapt** | the `vmm` service, **roles remapped** (agent tier → `aped`, not the guest) |
| Per-nexlet JSONSchema on `run_request` | **Adapt** | input-shape guard in front of the typed policy (D9) — shape, not policy |
| Xkey (curve) secret sealing | **Adapt** | REMOTE tier only (seal per-VM creds/secrets so the hub never sees plaintext); low value for the trusted local `aped` |
| KV-backed state + `existing=true` resume; `warm` state; lameduck drain | **Adapt** | `aped` restart/resume + factory pre-warm + systemd drain (D7) |
| Node functional-option seams (Minter/State/SecretStore/Auctioneer) | **Adapt** | mirror as testable SPIs in `aped` |
| Two-phase auction placement (bidder-id scatter-gather) | **Reject locally / Adapt remote** | overhead for the single local `aped`; the controller-tier placement primitive (D8) |
| `FullAccessMinter` / `AllowAllRegistrar` / native no-isolation exec (insecure defaults) | **Reject** | `aped` is default-deny + hard-KVM from line one |
| Adopt NEX's node wholesale via a Kata Nexlet | **Reject** | forks the PLAN-13/14/17 taxonomy, imports insecure defaults, inverts the trust model |
| Host↔guest transport / vsock guest-agent | **N/A** | removed from NEX; entirely our own work (D2) |

## Testing tiers

- **Tier 1 — GitHub CI (`ubuntu-latest` + `windows-latest`).** Pure logic, no
  KVM/containerd/Kata: `Backend` request/response JSON round-trips; the policy
  engine's allowlist decisions (image digest, mount-root canonicalization,
  device BDF); subject-authz config generation + deny-wins; per-VM cred minting +
  the `vmm` micro endpoints against an **embedded `nats-server`**; the AF_UNIX
  typed-command codec + `SO_PEERCRED` gate; migration of the pure PLAN-16 layers.
  Kata-touching code behind `//go:build linux` with a Windows stub.
- **Tier 2 — local / self-hosted (KVM + containerd + Kata, no GPU).** `aped`
  provisions a **non-device** Kata-QEMU workspace over NATS; `ape` drives it as a
  thin client; per-VM telemetry creds land and publish on
  `ape.{evt,log,metrics}.vm-<id>.>`; a guest cannot publish/subscribe `ape.vmm.>`
  (server-rejected); `freeze`/`unfreeze` preserve state; exec/attach over NATS
  works; a compromised-front-end simulation cannot satisfy the root executor's
  `SO_PEERCRED` check. Gated on `APE_APED_IT=1` + `/dev/kvm` + `nerdctl`.
- **Tier 3 — discrete-GPU self-hosted box (manual).** The whole device tier:
  `vfio-pci` bind, IOMMU-group isolation check, cold-plug via the baked
  `kata-qemu-gpu` handler, GPU visible in-guest (`nvidia-smi`), single-injection,
  `enable_annotations=[]` enforced, destroy + FLR-reset + rebind. Real OAuth
  (mode A), real scoped key (mode B), `git push`, Playwright inside.

> **Hardware availability (blocking for Phase 3).** This dev box is Intel UHD 620
> iGPU only (no `intel_iommu` boot param); GitHub-hosted runners have no nested
> virt. **No discrete-GPU + IOMMU box is confirmed available.** Phase 3 cannot be
> validated — and the #1 cold-plug unknown (D5) cannot be resolved — until one
> exists. Track this as a hard prerequisite.

## Steps

1. **Phase 0.** Land PLAN-13 (+ PLAN-17 amendments) then PLAN-14 — the
   `nats.go`/`micro`/`jwt`/`nkeys` dependencies and the `internal/{natsconn,
   eventing,reporting,service,blobstore}` packages `aped` builds on. Target the
   **current** versions (nats-server 2.14.x, nats.go 1.52.x, jwt/v2 2.8.x, nkeys
   0.4.16), not NEX's older pins.
2. **Phase 1.** Extract `internal/workspace.Backend` from `Runner`/`Registry`/
   `proxysup`; `shellDriver`; `ErrUnsupported` stubs for Suspend/Resume/Snapshot;
   fix the pause mislabel (D7). Tier-1 tests. Pure refactor — no daemon.
3. **Phase 2.** Build `aped` (D1/D2/D4/D9): the three-process architecture,
   embedded NATS + the `vmm` micro service, per-credential authz + per-VM cred
   minting, the policy engine + audit, hardened systemd units. Make `ape` a thin
   NATS client. Tier-2 non-device validation.
4. **Phase 3.** `containerdDriver` + the D5 VFIO recipe + the GPU guest-image
   build/signing workstream + profile `devices:`. **Tier-3, discrete-GPU box.**
5. **Phase 4** (platform repo). Remote agent + leaf nodes + per-tenant accounts
   + controller (`Capabilities()` scheduler) + the separate `firecrackerDriver`
   tier (D8).

## Acceptance

### Phase 0
- A fixture pipeline run publishes the PLAN-13 lifecycle sequence; identity token
  from the `.creds` is server-enforceable via `ape.*.<token>.>`; a PLAN-14
  `task.run` is accepted and transitions running→done against an embedded server.

### Phase 1 (CI)
- `shellDriver` satisfies `Backend`; a non-device workspace provisions via the
  extracted interface with byte-identical nerdctl args to today. `Suspend`/
  `Snapshot` return `ErrUnsupported`. Pause is labeled a freeze everywhere.

### Phase 2 (Tier 2)
- `aped` up as a system unit; `ape sandbox up dev` provisions a non-device
  Kata-QEMU workspace over the `vmm` contract; `exec`/`attach`/`freeze`/`unfreeze`
  work. A per-VM `.creds` is minted at Create, injected read-only, and its holder
  can publish `ape.metrics.vm-<id>.…` but is **server-rejected** on `ape.vmm.>`
  (pub and sub) and on another VM's `_INBOX`. The de-privileged front-end cannot
  reach the root executor (SO_PEERCRED mismatch). `systemd-analyze security`
  lands in the OK band for both units.

### Phase 3 (Tier 3, discrete GPU)
- `vfio-pci`-bound GPU alone (with its audio function) in its IOMMU group passes
  through; `nvidia-smi` works in-guest; the VFIO device is injected exactly once;
  `enable_annotations=[]` is enforced; a per-device USB `vendor:product` is
  forwarded via `usb-host` (whole-controller VFIO refused); destroy FLR-resets and
  rebinds the device.

### Phase 4
- The same `Backend`/subjects run over a leaf to a hub; a controller places work
  from `Capabilities()`; the FC tier provisions a no-device VM and rejects
  `Devices`/`Mount:host-fs` at admission.

## Risks

- **Schedule: the NATS foundation is unbuilt.** Phase 0 (PLAN-13/14/17) gates
  everything; the whole plan is impossible until those land.
- **No discrete-GPU box.** The device tier (Phase 3) and the #1 cold-plug unknown
  are unvalidatable on this box and on CI; a self-hosted GPU host is a hard
  prerequisite.
- **USB needs a Kata mechanism.** Per-device USB uses QEMU `usb-host`
  (aped-synthesised from a vendor:product allowlist — safe: no keyboard/mouse leak,
  no IOMMU-group constraint, no raw DMA), but **Kata doesn't expose `usb-host`
  today**, so `aped` must add it (a small upstream Kata contribution or a narrow
  aped-controlled qemu-device injection) + give the guest an emulated xHCI.
  Whole-controller VFIO is rejected (it would leak the system keyboard/mouse).
- **Suspend/Snapshot are blocked upstream** on the Kata path (shim `Checkpoint`
  unimplemented; VMM socket Kata-owned). Real VM save/restore waits for a
  VMM-owning driver or the Firecracker tier.
- **Device-VM QEMU de-privilege fork + `kata-NNN` uid handshake.** Running device
  QEMU rootless-VMM needs `aped` to chown `/dev/vfio/<group>` to the random
  `kata-NNN` uid the shim picks at create — no Kata hook pins it today, so it is a
  post-create race or a patch. Decide after discrete-GPU validation; the fallback
  is running device QEMU as root (relying on QEMU `-sandbox` + the KVM boundary).
- **GPU guest-image version coupling.** NVIDIA modules must be signed against the
  pinned Kata guest kernel; the host-Kata↔guest-kernel coupling is a non-trivial
  CI workstream that needs an owner.
- **Firecracker coupling.** `firecracker-containerd` is committed to containerd
  1.7.x with a compiled-in plugin and low velocity — a separate stack, pinned by
  SHA, kept optional/future.
- **containerd Go-client dep in `aped`.** Acceptable (charter is about `ape`), but
  pin/verify version-coupling with the host containerd + Kata; `MemoryDenyWrite
  Execute=yes` may be incompatible with cgo/plugin paths — verify before enabling.
- **`ape` self-exec inside workspaces** still runs as the guest user in the VM —
  unchanged; the VM is the boundary.
- **`shellDriver` vs the D1 executor sandbox (Phase-2 finding, validated live on
  Ubuntu 26.04 / kernel 7.0).** The Appendix-A executor unit is written for a
  containerd *client* that does no host work (`ProtectSystem=strict`, empty
  `CapabilityBoundingSet`, `RestrictAddressFamilies=AF_UNIX`). But Phase 2 ships
  the **`shellDriver`**, which shells out to `nerdctl`, and `nerdctl` does
  **client-side CNI in the executor's own process** — creating netns + veth,
  running the bridge plugin. That needs exactly what the sandbox denies: writable
  `/var/lib/nerdctl` + CNI state, `CAP_NET_ADMIN`/`CAP_NET_RAW`, `AF_NETLINK`, and
  `@mount` (persistent-netns bind). So `ape sandbox up` **through the deployed
  daemon fails** (`nerdctl run: … read-only file system`, then it would fail on
  networking), even though the lifecycle logic is correct —
  `TestTier2Provision` passes because `go test` runs the executor in-process with
  no systemd sandbox. Widening the unit (`ProtectSystem=full` + net caps +
  `AF_NETLINK`) reintroduces the "root with power" the split exists to avoid, so
  it is **not** the fix. The clean resolutions are architectural: switch the
  deployed executor to the **`containerdDriver`** (Go client, no in-process CNI)
  and/or move networking out of `aped` — run Phase-2 workspaces networkless and
  let the Phase-3 overlay provide connectivity. Tracked as a known gap in
  `docs/how-to/run-aped.md`.

## Appendix A — `aped` systemd units + auditd rules (D4)

Architecture: **two units + one internal boundary.** `aped` is a *client* of a
separate rootful system `containerd` (Kata shim registered); it does **not** parent
QEMU. The guest-reachable NATS/telemetry surface runs de-privileged; the root
executor holds almost no capability and no network address family beyond `AF_UNIX`.

```
                         host (one Linux box, KVM)
 ┌──────────────────────────────────────────────────────────────────────────┐
 │  hostile guest VMs ─NATS TCP (per-VM telemetry-only creds)─┐               │
 │  host `ape` operator ─NATS TCP (mgmt creds)───────────────┐│               │
 │                                              ┌────────────▼▼─────────────┐ │
 │                                              │ aped-front  (User=aped)   │ │
 │                                              │  embedded nats-server     │ │
 │                                              │  telemetry ingestion      │ │
 │                                              │  subject authz (deny-wins)│ │
 │                                              │  policy: who-may-ask-what │ │
 │                                              │  NO caps · NO /dev ·      │ │
 │                                              │  NO containerd socket     │ │
 │                                              └────────────┬──────────────┘ │
 │                    typed, fully-resolved command ENUM     │ AF_UNIX         │
 │                    (no free-form request, no host paths)  │ SEQPACKET       │
 │                                                SO_PEERCRED ▼ checked        │
 │                                              ┌───────────────────────────┐ │
 │                                              │ aped  (root executor)     │ │
 │                                              │  re-validates every cmd   │ │
 │                                              │  vfio-pci bind (sysfs)    │ │
 │                                              │  drives containerd client │ │
 │                                              │  append-only audit log    │ │
 │                                              │  caps: {} (or CAP_CHOWN)  │ │
 │                                              │  AF_UNIX only · no network│ │
 │                                              └───────┬──────────┬────────┘ │
 │                                       unix socket    │          │ sysfs     │
 │                                                       ▼          ▼          │
 │                              /run/containerd/containerd.sock  /sys/bus/pci  │
 │            (separate rootful systemd units, NOT aped children):            │
 │            containerd → containerd-shim-kata-v2 → QEMU/KVM                  │
 │            QEMU de-priv: Kata rootless-VMM (kata-NNN) + QEMU -sandbox       │
 └──────────────────────────────────────────────────────────────────────────┘
```

> **Deploying these units — read first.** The blocks below are the *design*
> form and are annotated with inline `# …` for readability, but **systemd has no
> inline comments**: a trailing `# …` becomes part of the value, so `Group=ape #
> …` fails to resolve the group (`aped.service` exits `216/GROUP`; the front is
> rejected as a bad unit file) and `ProtectKernelTunables=yes # …` is silently
> dropped. The `SystemCallFilter` denylist also needs **one** leading `~` for the
> whole space-separated list (`~@mount @swap @reboot …`) — repeating `~` per group
> makes systemd read `~@swap` as an unknown *syscall name* and silently drop the
> filter, leaving only `@mount` denied. These blocks are also aspirational in
> shape (`Type=notify` + `--socket-activated` await `sd_notify`; the shipped units
> use `Type=exec` with explicit flags). Deploy the corrected, `systemd-analyze
> verify`-clean copies from **`deploy/systemd/`** (installed by
> `deploy/tier2-setup.sh`), not these.

### `/etc/systemd/system/aped-priv.socket` — internal front↔root boundary

```ini
[Unit]
Description=aped privileged-executor internal command socket

[Socket]
# AF_UNIX SEQPACKET: message boundaries for the typed command enum.
ListenSequentialPacket=/run/aped/priv.sock
# The gate the design doc wanted on "aped.socket" belongs HERE (AF_UNIX), not on
# the NATS TCP listener. Only root + members of group `ape` may connect().
SocketMode=0660
SocketUser=root
SocketGroup=ape
RemoveOnStop=yes
Service=aped.service

[Install]
WantedBy=sockets.target
```

### `/etc/systemd/system/aped.service` — the root executor

```ini
[Unit]
Description=aped — rootful Kata-QEMU VM-management executor (privileged, narrow)
After=containerd.service network-pre.target
Requires=aped-priv.socket
Wants=containerd.service

[Service]
Type=notify
ExecStart=/usr/bin/aped run --socket-activated
Restart=on-failure
WatchdogSec=30s

# Identity: root without power.
User=root
Group=ape                     # for the priv.sock group; NOT the containerd group
NoNewPrivileges=yes

# Capabilities: empty (non-device). containerd/shim/QEMU hold every VM capability.
CapabilityBoundingSet=
AmbientCapabilities=
# DEVICE tier (uncomment) — only if aped chowns /dev/vfio/<group> to kata-NNN.
# vfio-pci *binding* needs NO capability (uid-0 owns the 0200/0644 sysfs files).
#CapabilityBoundingSet=CAP_CHOWN

# Filesystem
ProtectSystem=strict
ReadWritePaths=/var/lib/aped /run/aped /var/log/aped
ProtectHome=yes
PrivateTmp=yes
UMask=0077
ProtectProc=invisible
ProcSubset=pid
# NON-DEVICE unit keeps this on; the DEVICE-tier executor OMITS it (vfio-pci bind
# writes /sys) or runs the bind in a separate-namespace oneshot helper.
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
PrivateDevices=yes            # DEVICE-CHOWN tier: PrivateDevices=no + DeviceAllow=/dev/vfio/vfio

# Namespaces: aped creates none (containerd/shim do). Deny all.
RestrictNamespaces=yes

# Network: AF_UNIX only — unreachable over IP.
RestrictAddressFamilies=AF_UNIX
IPAddressDeny=any

# Syscalls: @system-service already allows ioctl (KVM/VFIO) + chown + process.
SystemCallArchitectures=native
SystemCallErrorNumber=EPERM
SystemCallFilter=@system-service
SystemCallFilter=~@mount @swap @reboot @module @raw-io @cpu-emulation @obsolete @debug @privileged
#  One leading ~ denies the whole list (see the caveat above). @chown is NOT in
#  @privileged on systemd 259, so device-node chown survives.

RestrictSUIDSGID=yes
RestrictRealtime=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes    # Go daemon (no JIT); verify with the containerd-client build
RemoveIPC=yes
KeyringMode=private

[Install]
WantedBy=multi-user.target
```

### `/etc/systemd/system/aped-front.service` — de-privileged NATS/telemetry front-end

```ini
[Unit]
Description=aped-front — de-privileged embedded NATS + telemetry ingestion
Requires=aped-priv.socket
After=aped.service aped-priv.socket

[Service]
Type=notify
ExecStart=/usr/bin/aped front
Restart=on-failure

# Identity — the guest-reachable attack surface: NOT root, no privilege.
User=aped
Group=ape                     # so it may connect() to /run/aped/priv.sock (0660 root:ape)
DynamicUser=no                # stable uid for the executor's SO_PEERCRED check
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=

ProtectSystem=strict
ReadWritePaths=/var/lib/aped/nats /run/aped
ReadOnlyPaths=/var/lib/aped/creds   # per-VM .creds the executor mints live here
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectProc=invisible
ProcSubset=pid
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
UMask=0077

# Embeds nats-server: loopback (host ape) + the private host↔guest bridge subnet.
RestrictNamespaces=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
IPAddressDeny=any
IPAddressAllow=localhost
IPAddressAllow=169.254.0.0/16       # example host↔guest link-local; set to the real subnet

SystemCallArchitectures=native
SystemCallErrorNumber=EPERM
SystemCallFilter=@system-service
SystemCallFilter=~@mount @swap @reboot @module @raw-io @cpu-emulation @obsolete @debug @privileged @chown
RestrictSUIDSGID=yes
RestrictRealtime=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RemoveIPC=yes
KeyringMode=private

[Install]
WantedBy=multi-user.target
```

### `/etc/audit/rules.d/50-aped.rules` — kernel-level device auditing

```
## Path/dir watches fire on path-referencing syscalls (open/openat/chown/chmod),
## NOT on fd-level ioctl — low-volume (one hit per VM boot / per bind), and do NOT
## capture QEMU's hot KVM_RUN ioctl loop.
-a always,exit -F arch=b64 -F path=/dev/kvm -F perm=rwa -k aped_kvm
-a always,exit -F arch=b32 -F path=/dev/kvm -F perm=rwa -k aped_kvm
-a always,exit -F arch=b64 -F dir=/dev/vfio -F perm=rwa -k aped_vfio
-a always,exit -F arch=b32 -F dir=/dev/vfio -F perm=rwa -k aped_vfio
-a always,exit -F arch=b64 -F dir=/sys/bus/pci/drivers/vfio-pci -F perm=wa -k aped_vfio_bind
## Immutable until reboot (LAST line): a compromised root can't silently disable it.
-e 2
```

### Application-level audit (in the root executor)

Every command accepted over `priv.sock` is appended (before + after execution) to
`/var/log/aped/audit.jsonl` (`O_APPEND`; file `chattr +a`) **and** forwarded over
NATS (PLAN-13 eventing, `ape.audit.<node>.>`). Record the **resolved** request
(the CVE-2024-41110 / CVE-2026-34040 lesson — log what will actually run):

```json
{ "ts":"…", "boundary_peer":{"uid":…,"pid":…},        // SO_PEERCRED of priv.sock
  "caller":"<SubjectToken from NATS creds, PLAN-17>",  // front-end-attested identity
  "op":"CreateVM",
  "resolved":{ "image_digest":"sha256:…", "mount":"/canonical/abs/path",
               "pci_bdf":"0000:01:00.0", "usb":"303a:1001", "vmm_uid":"kata-1734" },
  "policy":{ "rule":"profile:dev/devices", "decision":"allow" },
  "outcome":{ "ok":true, "vm_id":"…" } }
```

Three-layer attribution: this app log (resolved args + policy decision) + the NATS
server's own auth log (who *published* — authoritative per subject authz) + auditd
(kernel device access).

### Host-config dependencies (separate root units, NOT `aped.service` directives)

- `containerd.service`: `LimitMEMLOCK=infinity` (VFIO pins all guest RAM) + a
  raised `LimitNOFILE`. The shim/QEMU inherit these; `aped` does not set them.
- Kata QEMU config (`configuration-qemu.toml`):
  `seccompsandbox = "on,obsolete=deny,spawn=deny,resourcecontrol=deny"` — **QEMU is
  the only Kata VMM with host seccomp OFF by default.**
- Non-device VMs: `rootless = true` (kata-NNN VMM). Device VMs: root QEMU +
  `-sandbox` (no chown) **or** rootless-VMM + `aped` chowning `/dev/vfio/<group>` to
  kata-NNN (needs `CAP_CHOWN`) — validate on discrete-GPU hardware.

### Predicted `systemd-analyze security` (verify offline: `--offline=true`)

`aped.service` (root, empty caps, AF_UNIX-only, no network): **~3.0–3.8 OK**.
`aped-front.service` (User=aped, AF_INET-facing): **~2.5–3.5 OK**. The score credits
none of the real controls (SO_PEERCRED boundary, subject authz, QEMU `-sandbox`,
KVM boundary) — necessary, not sufficient.

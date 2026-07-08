---
created_at: 2026-07-07
status: open
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
  Design research for splitting `ape` into an unprivileged CLI (`ape`) plus a
  small **rootful VM-management daemon** (`aped`) that drives Kata-QEMU microVMs
  with GPU/USB (VFIO) passthrough. Forced by a hard finding: **rootless + device
  passthrough is physically impossible** (VFIO/IOMMU needs root; Kata's own
  rootless-VMM mode disables passthrough), so the privilege must be isolated in
  a narrow, audited daemon rather than run the workload-executing CLI as root.
  Covers: the security model (avoiding the "docker-group = root" trap), the
  privilege/de-privileging design (jailer / Kata rootless-VMM pattern + systemd
  hardening), the `ape`↔`aped` transport (**decided: embedded NATS everywhere** — host `ape`,
  the in-VM `ape` agent, and the future cluster share one NATS-micro contract, the
  NEX model — unified by a transport-agnostic `Backend` interface + a
  per-credential subject-authz policy layer), the VM-lifecycle op set
  and driver choice (shell-out vs containerd Go client), VFIO prerequisites, and
  how the local daemon generalizes to a remote hypervisor agent (NEX is the
  reference implementation). Includes a Go `Backend` interface sketch and a
  phased plan.
origin:
  - 2026-07-07 — live rootless-Kata bring-up on this box failed at an unbreakable
    cgroup wall (Kata Go shim mkdir's cgroups at the host root; nerdctl punts
    rootless-Kata as external/kata #135). Combined with the hard requirement for
    GPU/USB passthrough, this proved rootless is off the table for the device
    tier. User decision: rootful `kata-qemu` + an `ape`/`aped` privilege split.
  - Supersedes the "kata rootless" assumption in
    `sandbox-qemu-vs-kata-20260706.md` and PLAN-16's rootless framing.
  - Host cleanup for the failed experiments: `_output/2026-07-07-sandbox-host-changes-and-cleanup.md`.
---

# `ape` + `aped`: a rootful VM-management daemon for Kata-QEMU workspaces

> **Why this exists.** GPU/USB passthrough + a hard KVM boundary is **inherently
> rootful** — binding devices to `vfio-pci`, programming IOMMU groups, opening
> `/dev/vfio`, and DMA-pinning guest RAM all require root, and Kata's own
> rootless-VMM mode *explicitly disables* device passthrough. Rootless and
> passthrough are mutually exclusive. So we do **not** run `ape` (which executes
> AI-agent workloads) as root; we isolate the unavoidable privilege in a small,
> auditable daemon `aped`, and `ape` drives it. This is exactly how
> libvirtd / dockerd / containerd are built.

## 1. Decision recap

- **Backend:** Kata Containers + **QEMU VMM (`kata-qemu`), rootful.** Hard KVM
  boundary + the most mature GPU/USB VFIO passthrough + OCI ergonomics
  (virtio-fs project mount, image layers). `kata-clh` remains a non-device
  option; **Firecracker** is the future dense/no-device tier.
- **Privilege split:** `ape` = unprivileged CLI (all current functionality +
  workload execution). `aped` = rootful daemon; the *only* privileged surface,
  narrow and audited. `ape` never runs as root; `sudo`-the-whole-CLI is the
  security smell we are removing.
- **Rootless** is dropped for device/hard-boundary workspaces (impossible). A
  rootless *non-device* tier (libkrun/gVisor) is a possible future option, out
  of scope here.

## 2. Security model

**Primary threat: VM→host escape.** Untrusted code runs *inside* the VMs; the top
priority is that it can never reach the host. Two layers:
1. **The Kata hardware boundary** — a separate guest kernel enforced by KVM. This
   is the main anti-escape control and the whole reason for Kata.
2. **Credential-scoped control plane** — the in-VM `ape` gets **per-VM credentials
   that permit telemetry only and cannot issue any VM-management command** (§4,
   enforced by NATS subject authz on `aped`). Even a fully-compromised guest
   cannot drive `aped`. Use **per-VM-unique** creds so one compromised VM can't
   impersonate/replay another.
   - *Caveat:* **VFIO passthrough widens this boundary** — a passed-through device
     is DMA-capable hardware in the guest; IOMMU mitigates but doesn't eliminate
     the risk. Scope passthrough to workspaces that need it.
   - *Hardening:* the VM-facing NATS front-end is attack surface reachable from
     hostile guests — **run NATS + telemetry ingestion de-privileged**, with
     privileged VM ops behind a narrow internal boundary, so a `nats-server`
     exploit from a guest doesn't yield host root (§4).

**Secondary (defense-in-depth): host-side access must not become root.** Even
though the host `ape` is the operator, `aped` must not be a generic "do X with
root" executor — the **"docker-group = root" trap**: a root
daemon that faithfully executes whatever a client asks is root-equivalent,
because the client can ask it to erase the boundary — `docker run -v /:/mnt …`,
`--privileged`, arbitrary device passthrough. Same footgun just hit Lima's guest
agent (**CVE-2026-53657**: a root gRPC daemon behind a `0777` socket = "every
account has a direct, unauthenticated line to a root service"). Docker's own
AuthZ-plugin bypasses (**CVE-2024-41110**, its 2026 regression **CVE-2026-34040**)
show the second lesson: authorize the *fully-decoded* request, not a summary.

**`aped` must therefore never be a generic "do X with root" executor.** Its
defenses, in order of importance:

1. **Constrained operation vocabulary.** `aped` exposes *typed VM-lifecycle verbs
   on constrained objects* (create/start/stop/attach a VM described by a schema)
   — **never** a free-form "run with these host mounts/devices/flags." There is
   no `--privileged`, no arbitrary host-path mount, no arbitrary device string.
   This structural narrowing is the difference from the Docker API.
2. **Default-deny, per-caller allowlists** for every host resource a request can
   touch:
   - **VM assets** (images/kernels/rootfs) only from an `aped`-controlled,
     **digest-pinned** store — never a caller-supplied host path. (Extends the
     `ape-sandbox` base-digest discipline already in the repo.)
   - **virtio-fs shares**: only inside a per-caller allowlisted root;
     **canonicalize (resolve symlinks, strip `..`) and re-check *after*
     resolution** — the confused-deputy defense. Never share host `/`, `/dev`,
     `/proc`, `/sys`, or `aped`'s own state.
   - **VFIO/USB devices**: an explicit allowlist mapping *which caller* may pass
     *which specific* PCI BDF / USB id. Deny by default. This is the
     highest-value escalation target — security-critical.
3. **Per-caller policy** binding an authenticated identity → what it may request
   (profiles, device allowlist, vCPU/RAM/count ceilings, mount roots). AuthN
   answers *who*; policy answers *what they may ask*.
4. **One validated path to execution.** Authorize the concrete, fully-parsed
   request object; no oversized/empty-body side doors (the CVE lesson).
5. **Audit every privileged op** — caller identity, operation, *resolved* args
   (canonical paths, device IDs, image digest), policy rule + decision, outcome;
   append-only / forwarded; backed by `auditd` rules on `/dev/kvm`,
   `/dev/vfio/*`.

Net: even a caller who fully owns `ape` + the transport can only spin up VMs from
allowlisted assets, mounting allowlisted paths, passing allowlisted devices —
which is **not** root, by construction.

## 3. Privilege & de-privileging model

`aped` genuinely needs privilege a userns can't grant (VFIO binding, DMA pinning,
`/dev/kvm`, tap/netns, device-node chown, cgroup setup). So the model is **run as
root, tightly hardened, and de-privilege the per-VM VMM** — the Firecracker
**jailer** / Kata **rootless-VMM** pattern, not a rootless daemon.

- **Scope the caps** (grant only these via `CapabilityBoundingSet=`):
  `CAP_SYS_ADMIN` (ns/mount/cgroup setup), `CAP_NET_ADMIN` (tap/netns),
  `CAP_SYS_RESOURCE`+`CAP_IPC_LOCK` (VFIO DMA memlock), `CAP_CHOWN`,
  `CAP_DAC_OVERRIDE`, `CAP_MKNOD` (per-VM device-node prep). Nothing else.
- **systemd hardening** for `aped.service`: `NoNewPrivileges=yes` (set explicitly
  — it's root), `ProtectSystem=strict` + minimal `ReadWritePaths=`,
  `SystemCallFilter=@system-service` (+ KVM/VFIO ioctls), `ProtectProc=invisible`,
  `RestrictAddressFamilies=AF_UNIX AF_NETLINK AF_INET AF_INET6`, allowlisted
  `RestrictNamespaces=` (we *do* create namespaces), `SystemCallErrorNumber=EPERM`.
  Ship an `aped.socket` (socket activation) that owns the listener with
  `SocketMode=0660`/`SocketGroup=ape`. Measure with `systemd-analyze security aped`.
  (User units can't be hardened — `aped` is a **system** unit.)
- **De-privilege QEMU per VM** (jailer pattern): `aped` does the privileged setup
  (build per-VM jail/chroot with only that VM's assets; create tap; `chown`
  `/dev/kvm` + `/dev/vfio/<group>` + `/dev/net/tun` to a per-VM `kata-NNN` uid:gid;
  set cgroup + memlock), then QEMU runs as that unprivileged per-VM user in
  pid/net namespaces + a per-thread seccomp profile. **Caveat (Kata's own):** in
  rootless-VMM only the VMM de-privileges; **shimv2 + virtiofsd stay root**, and
  device chown is done by the privileged parent. So `aped` (like the Kata shim)
  stays root and does the device prep; the *QEMU process* is the de-privileged
  thing. `virtiofsd` runs one-per-VM in `namespace` sandbox mode scoped to the
  VM's allowlisted share.

## 4. Transport & auth — DECIDED: embedded NATS everywhere

`aped` embeds a NATS server (TCP) and exposes the VM API as a **NATS micro**
service. The **same NATS contract** serves three clients, distinguished by
credential:
1. **host `ape`** — operator creds scoped to **VM-management** subjects
   (`ape.vmm.<node>.>`).
2. **in-VM `ape`** (one per workspace) — **per-VM** creds scoped to
   **telemetry/metrics/transcripts only** (`ape.vm.<id>.telemetry.>`), and
   **explicitly denied** every management subject.
3. **future company NATS cluster** — the identical micro contract, so local and
   platform are the same API.

**Why this is the right call (and supersedes the earlier "unix-socket local"
lean):** the in-VM agent *must* cross the VM boundary to reach the host, which
rules out a unix socket for that leg — so NATS is mandatory **anyway**; using it
for the host `ape`↔`aped` leg too buys one uniform contract at no extra cost. It's
exactly the **NEX** model, and matches how the team already deploys embedded NATS.
The guest reaches `aped`'s NATS over a **private host↔guest link** (the container
bridge gateway, or a vsock↔NATS bridge), separate from the deny-by-default public
egress.

**Security is enforced by NATS subject authorization, per credential** (deny wins;
`allow_responses` for the reply leg): the in-VM account publishes only its own
telemetry subjects and has **no** access to `ape.vmm.>`, so a fully-compromised VM
cannot issue any management command over the control plane. Gate the **host** leg
with a **`root:ape 0640` creds file** + loopback/host-only bind; use **Operator→
Account→User JWT/nkey + TLS + per-tenant accounts + leaf nodes** for the cluster.

### Reuse the repo's NATS/daemon foundations — `aped` is not greenfield
- **Identity + subject taxonomy = PLAN-13/17.** Decode the `.creds` user JWT to a
  subject token, server-enforced `ape.*.<token>.>` (PLAN-13 `internal/natsconn`
  `Identity()`; PLAN-17 D1). The in-VM `ape` agent's telemetry/metrics/transcripts
  = **PLAN-17**'s "self-report with only creds" mode over **per-VM creds** `aped`
  mints at create; eventing + transcript blobs = **PLAN-13**
  (`natsconn`/`eventing`/`blobstore`).
- **`aped` ≈ PLAN-14's `ape service`, elevated to rootful VM management.** PLAN-14
  is *already* a NATS-micro daemon (`micro.AddService`, `$SRV` discovery, keyed
  admission, project allowlist, graceful shutdown, the submitter-vs-daemon
  identity nuance, "whoever can publish is trusted"). `aped` reuses that
  `internal/service` shape — the difference is it manages **Kata-QEMU VMs**
  (rootful, VFIO) instead of spawning CLI child processes, which is exactly why
  the rootful hardening (§2–3) matters *more* here than in PLAN-14.
- **Composition — the "NATS worker VM."** `aped` (host, this plan) provisions a
  Kata VM; *inside* it the in-VM `ape` runs **PLAN-14 `ape service`** (accept
  jobs) / **PLAN-15 `ape script`** (yaegi orchestration) / **PLAN-17** reporting.
  Host `aped` = VM lifecycle; in-VM `ape` = workload + telemetry. Two NATS-micro
  daemons, composed — this is the platform's worker-VM vision.

### On the earlier `SO_PEERCRED` objection (why it doesn't override this)
`nats-server` does **not** support a unix-domain-socket client transport (it's a
proposal-only community prototype, `nats-server` Discussion #7677; maintainers
hesitant). "Embedded NATS locally" therefore means **loopback TCP + a bootstrap
credential file** — which *loses* the two things a rootful local daemon most
wants:
- **kernel-authoritative caller identity** via `SO_PEERCRED` (uid/gid/pid) — over
  loopback every client is `127.0.0.1`;
- **filesystem gating** (a `root:ape 0660` socket).

`InProcessServer`/`DontListen` (the zero-copy embedded mode) only connects clients
*inside the same process* — `ape` is a separate binary, so it can't use it.

This is **accepted, not fatal**: peer-cred identity would only matter for
defending against *another local host process*, which is not the threat model
(§2) — the host `ape` is the operator on their own box, and the dangerous client
(the in-VM agent) can't use a unix socket anyway. The host leg is gated by the
`root:ape 0640` creds file + loopback/host-only bind; the in-VM leg is gated by
per-VM subject-scoped creds. So the local gate is **credential-file perms +
subject authz**, not a socket's `root:group` mode — a deliberate, acceptable
trade for one uniform contract.

### Remote/platform tier (additive, same handlers)
The **same `Backend` handlers + schemas** are exposed to the cluster with
decentralized **Operator→Account→User JWT/nkey + TLS**, one **account per tenant**
(automatic subject namespacing, no cross-tenant traffic), and **leaf nodes** so a
per-node agent makes an **outbound-only** connection to the hub (port 7422, no
inbound firewall) and the hub transparently routes `ape.vmm.<node>.>` to it —
exactly how **NEX** (§7) ships. Local↔remote differ only in credential/topology,
not in subjects, schemas, or `ape` client code.

### Hardening: de-privilege the VM-facing NATS front-end
Because `aped`'s embedded NATS is reachable *from the guests*, the `nats-server`
+ telemetry ingestion is attack surface exposed to hostile VMs — run **that part
de-privileged** and keep the privileged VM-management executor behind a narrow
internal boundary (separate root helper / minimal internal IPC) that only the
authorized management path reaches. A `nats-server` exploit from a guest then
yields the de-privileged front-end, not host root.

## 5. Driving Kata-QEMU: shell-out vs containerd Go client

Kata's integration is **containerd Runtime v2 (shim-v2)**: `io.containerd.kata-qemu.v2`
→ one `containerd-shim-kata-v2` per sandbox → QEMU/KVM + a `kata-agent` in-guest
over VSOCK; rootfs/volumes shared via **virtio-fs**. `aped` needs **system
(rootful) containerd** with the Kata shim registered + `/dev/kvm`, regardless of
driver.

**The charter clarified:** "single binary / shell out / no heavy dep" is a
constraint on **`ape`**, not `aped`. A dedicated rootful daemon may carry a
`containerd` Go-client dep.

**Recommendation — a `Backend` interface with two drivers inside `aped`:**
- `shellDriver` (nerdctl/ctr) — reuses the existing pure `RunArgs`/`ExecArgs`
  builders; Phase-1 non-device happy path; fully testable.
- `containerdDriver` (containerd Go client, using the split-out
  `github.com/containerd/containerd/api` where possible) — for the two things
  shelling out can't do well:
  1. **Task event stream** (exit/OOM/state) for a real daemon state machine.
  2. **PTY/stdio fidelity** for interactive exec/attach.
  3. **Annotation-based VFIO cold-plug** — NVIDIA GPU passthrough needs
     `io.katacontainers.*` + `cdi.k8s.io/vfio*` annotations injected into the OCI
     spec *before* create. **nerdctl doesn't emit these**; you need `ctr
     --annotation` or the Go client's annotation map. This is a concrete
     capability gap that *forces* something beyond pure-nerdctl for the device
     tier.

Gate the containerd driver behind the device tier + event-stream needs; keep the
shell driver for parity.

## 6. The transport-agnostic `Backend` interface

One interface (new `internal/workspace`, generalizing today's `Runner`) that a
local containerd/nerdctl driver **and** a remote NATS client both implement, so
`ape` (and a future controller) code identically against either. Request/response
types are JSON-serializable — they double as the NATS wire contract.

```go
package workspace

type Backend interface {
	// Capabilities: what this node can satisfy (KVM, runtimes/VMMs, GPU/USB
	// inventory + IOMMU-group state, host-fs support, free RAM). A controller's
	// scheduler consumes this across nodes before placing work.
	Capabilities(ctx context.Context) (Capabilities, error)

	Create(ctx context.Context, req CreateRequest) (Workspace, error) // provision (detached)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error

	Exec(ctx context.Context, id string, req ExecRequest) (ExitStatus, error)
	// Attach: interactive PTY. Stream abstracts the bidi channel — locally
	// os.Stdin/out over the unix socket; over NATS a per-session inbox.
	Attach(ctx context.Context, id string, req AttachRequest, io Stream) error

	Pause(ctx context.Context, id string) error   // cgroup-freeze (RAM resident)
	Resume(ctx context.Context, id string) error
	Suspend(ctx context.Context, id string) error // VMM save/evict (QEMU savevm / CH snapshot) — distinct op

	Snapshot(ctx context.Context, id string, req SnapshotRequest) (SnapshotRef, error)
	List(ctx context.Context) ([]Workspace, error)
	Inspect(ctx context.Context, id string) (Status, error)
	Destroy(ctx context.Context, id string, req DestroyRequest) error
}

type Stream interface {          // interactive attach channel, transport-agnostic
	io.ReadWriteCloser           // Read = client stdin, Write = client stdout
	Resize(cols, rows uint16) error
}

type CreateRequest struct {
	Name    string   `json:"name"`
	Image   string   `json:"image,omitempty"`   // "" → pinned default
	Runtime string   `json:"runtime,omitempty"` // kata-qemu | kata-clh | firecracker
	Mount   string   `json:"mount,omitempty"`   // host-fs | volume | ephemeral
	Profile string   `json:"profile,omitempty"`
	Devices []Device `json:"devices,omitempty"` // {PCI:"<BDF>"} | {USB:"vendor:product"}
	From    string   `json:"from,omitempty"`    // SnapshotRef to boot from
	// egress, git, credential, env, ssh-port … reuse the existing Profile shape
}
type Device struct {
	PCI string `json:"pci,omitempty"` // BDF → IOMMU group → vfio-pci
	USB string `json:"usb,omitempty"` // vendor:product → QEMU usb-host
}
type Capabilities struct {
	KVM       bool     `json:"kvm"`
	Runtimes  []string `json:"runtimes"`  // io.containerd.kata-qemu.v2, …, firecracker
	HostFS    bool     `json:"host_fs"`   // false on Firecracker nodes
	GPUs      []GPU    `json:"gpus"`      // BDF, model, iommu_group, bound_driver
	FreeMemMB int64    `json:"free_mem_mb"`
}
```

- **Subsumes today's `Runner`** — the shell driver keeps building `RunArgs` and
  satisfies `Backend`; the containerd driver adds events + annotations.
- **Scheduling stays out** — a controller consumes `Capabilities()` across agents
  then calls `Create()` on the chosen one; the per-node API is unchanged
  local-vs-fleet.
- `Runtime` + `Devices` + `Mount` + `Capabilities.HostFS` let one API span
  Kata-QEMU (passthrough/host-fs) and Firecracker (dense/block-only) without
  branching in `ape`.

## 7. Prior art to build on: NEX (NATS Execution Engine)

Synadia's **NEX** is almost exactly this design, shipped: a rootful **node**
embeds a NATS server, exposes a control API, and supervises **Firecracker
microVMs**, with an **agent inside each VM** over NATS; nodes **auto-cluster** and
workloads are **auction-placed**. Subject namespace `$NEX.…` with a tenant token;
per-node **Xkey** encryption of secrets in deploy requests.

**DECIDED: borrow the patterns, own the control plane.** We do **not** fork or
build on NEX — it is **Firecracker-centric and cannot do GPU/USB VFIO
passthrough**, a hard requirement for local device workspaces (**Kata-QEMU/KVM**).
We take NEX's *shapes* — embedded NATS in the node, a NATS-micro control API, an
agent-in-VM over NATS with scoped per-VM creds, node subject namespacing,
Xkey-encrypted secrets in deploy requests, and (later) auction placement — and
implement them over **Kata-QEMU**. Study NEX's source as the reference for those
patterns.

## 8. VFIO passthrough — host prerequisites `aped` owns (all genuinely rootful)

1. **IOMMU on at boot** (`intel_iommu=on`/`amd_iommu=on`, `iommu=pt`); verify via
   populated `/sys/kernel/iommu_groups/`.
2. **VFIO modules** (`vfio`, `vfio_pci`, `vfio_iommu_type1`) loaded early (GPUs
   bound in initramfs before `nouveau`/`nvidia`/`amdgpu`).
3. **`vfio-pci` binding** of the target BDF (unbind host driver + `driver_override`).
4. **IOMMU-group isolation** — read `/sys/bus/pci/devices/<BDF>/iommu_group`,
   enumerate, and **refuse** groups containing devices you can't hand over.
5. **`/dev/vfio` access** (`/dev/vfio/vfio` + `/dev/vfio/<group>`; group-granular).
6. **`RLIMIT_MEMLOCK`** ≥ VM RAM (VFIO pins all guest RAM) — needs `CAP_SYS_RESOURCE`.
7. **GPU specifics** — no host NVIDIA driver; GPU+HDA-audio alone in the group;
   single-GPU passthrough; on a bare box `aped` does the binding (the NVIDIA GPU
   Operator's VFIO Manager does it in k8s).

**Expressing it to Kata:** **hot-plug via `--device /dev/vfio/<group>`** works for
many devices but is unreliable for NVIDIA GPUs; **NVIDIA needs cold-plug via
annotations** (`cold_plug_vfio=root-port`, `io.katacontainers.*` + `cdi.k8s.io/vfio*`,
`enable_iommu=true` in the guest, `pcie_root_port=N`) injected **before** create —
which nerdctl can't emit (→ §5 containerd driver). USB = either VFIO of the whole
xHCI PCI controller, or QEMU per-device `usb-host` (thin Kata support). The device
tier **forces `vmm: qemu`**. The profile `devices:` list should distinguish
`pci: <BDF>` from `usb: <vendor:product>`.

## 9. Lifecycle op set (nerdctl-call vs daemon-logic)

The "just a nerdctl call" surface is thin (run/exec/pause/rm + `-v`/`--device`);
the **daemon's real work is orchestration**. Highlights:

- **create** = one `run`, wrapped in: profile resolution, `~/.claude` compose +
  staging home, egress-proxy supervise (fail-closed, already built), netns+nft
  wall (deferred D4), **VFIO bind + IOMMU-group check + annotation build**,
  memlock, registry write.
- **pause is currently mislabeled.** `nerdctl pause` on Kata is a **guest
  cgroup-freeze (RAM stays resident)**, *not* a microVM suspend. Split `Pause`
  (freeze) from a VMM-backed `Suspend` (QEMU `savevm` / CH snapshot) — the code
  comment in `kata_linux.go` should be corrected.
- **attach** ≠ `nerdctl attach` (binds PID 1's stdio); model as `exec -it <shell> -l`
  (already done) or client `Task.Exec` + PTY + resize.
- **snapshot/template** = image `commit` and/or **Kata VM templating**
  (`[factory] enable_template`) / **VMCache** — host-config features, daemon-managed,
  not per-run flags (KSM side-channel caveat for templating).
- **list/status** = prefer the **daemon registry** (already the source of truth,
  carries proxy pid/mount/devices) over parsing `nerdctl ps`.
- **destroy** = `rm -f` + proxy stop + netns/nft teardown + **VFIO rebind to host**
  + staging/volume policy + registry remove.
- **mount** = virtio-fs (host-fs) / block (volume) / in-guest clone (ephemeral);
  **Firecracker can't host-fs** → force volume/ephemeral on FC.

## 10. Local ↔ remote symmetry

**Identical:** the lifecycle verb set + request/response schemas; OCI-image guest
+ containerd + Kata shim-v2 mechanics; `~/.claude` composition + credential/git
modes; the egress-proxy + audit model; profile schema + mount modes. The remote
"hypervisor agent" does the *exact same* containerd+Kata work a local `aped` does.

**Different (and genuinely new for remote):**
- **Scheduling/placement** — a **controller** above the agent API picks a node
  from `Capabilities()` (GPU inventory, KVM, RAM, IOMMU-group availability),
  bin-packs, drains. The per-node agent stays a pure executor.
- **Multi-tenant auth** — per-tenant NATS accounts/JWT, namespace isolation,
  quotas (the biggest new surface).
- **Image distribution** — registry auth, pre-pull/replication, lazy pull
  (eStargz), air-gap (the image already bakes `/opt/apex-framework`).
- **Networking overlays** — Phase-3 Netbird/WireGuard, preview URLs (Phase 4).
- **Snapshot portability** — Kata factory templates / VMM snapshots are
  node-local unless on shared storage → controller pins or replicates.

**Firecracker vs Kata-QEMU:** FC = dense/fast, **5 devices only, no passthrough,
no host-fs** (ephemeral/CI/preview tier via firecracker-containerd + devmapper
snapshotter). Kata-QEMU = passthrough + host-fs. The `create` API carries a
`runtime` selector + requirements and rejects incompatible combos early
(**FC ⇒ no `devices:`, no `mount: host-fs`**).

## 11. Recommendation & phased plan

**Recommended architecture:**
- `ape` (unprivileged) ↔ `aped` (rootful) over an **embedded NATS server** — one
  **NATS-micro** contract for host `ape`, the in-VM `ape` agent, and the future
  cluster (the NEX model) — behind a transport-agnostic **`Backend` interface** +
  a **default-deny policy layer** (per-credential **subject authz** + per-caller
  asset/mount/device allowlists + full audit).
- **Per-credential scoping:** host `ape` = management subjects; in-VM `ape` =
  per-VM telemetry-only subjects, denied management. The **VM-facing NATS +
  telemetry front-end runs de-privileged**; the privileged VM-management executor
  sits behind a narrow internal boundary.
- `aped` de-privileges QEMU per VM (jailer/rootless-VMM pattern) and runs as a
  hardened systemd system service with a scoped cap set.
- Drivers: `shellDriver` (nerdctl, non-device parity) + `containerdDriver`
  (Go client; device-tier cold-plug annotations + task events + PTY).
- **Remote tier** adds JWT/nkey + TLS + leaf nodes + per-tenant accounts over the
  **same** handlers/subjects/schemas. Study/align with **NEX**.

**Phasing:**
1. **Extract the `Backend` interface** from today's `Runner`/`Registry`/proxy;
   keep the shell driver. Pure refactor, no daemon yet.
2. **`aped` (local):** rootful systemd unit + **embedded NATS** (`root:ape 0640`
   creds, loopback/host-only bind) + a **NATS-micro** `vmm` service; per-credential
   subject authz; the policy engine (allowlists + audit); de-privileged NATS/
   telemetry front-end vs. root executor. `ape` becomes a thin NATS client.
   Non-device Kata-QEMU workspaces, rootful.
3. **Device tier:** `containerdDriver` + VFIO orchestration (IOMMU-group checks,
   `vfio-pci` bind/rebind, cold-plug annotations) + profile `devices:`. Validate
   on a box with a discrete GPU (this dev box has Intel iGPU only — not testable
   here).
4. **Remote agent + NATS front-end + controller** (platform repo): same `Backend`
   over NATS, scheduler consuming `Capabilities()`, multi-tenant accounts. Align
   with NEX. Firecracker as the dense no-device tier.

## 12. Open questions / risks
- **Transport: DECIDED — embedded NATS everywhere** (host `ape` + in-VM `ape` +
  future cluster; NEX model). Residual work: (a) run the VM-facing NATS/telemetry
  front-end **de-privileged** so a guest-reachable `nats-server` exploit doesn't
  yield host root; (b) design the **private host↔guest link** the in-VM agent uses
  to reach `aped`'s NATS (bridge gateway or vsock↔NATS bridge), separate from
  public egress; (c) **per-VM-unique** creds so a compromised VM can't replay.
- **NEX: DECIDED — borrow patterns, own the control plane** (NEX is
  Firecracker-only, no VFIO passthrough; we need Kata-QEMU for local GPU/USB). Take
  its shapes (embedded NATS, NATS-micro API, agent-in-VM with scoped creds, node
  namespacing, Xkey secrets, auction placement) implemented over Kata-QEMU.
- **containerd Go-client dep in `aped`** — acceptable (charter is about `ape`),
  but pin/verify the version-coupling with the host containerd + Kata.
- **Pause vs Suspend** semantics must be corrected in code + docs (PLAN-16 D1).
- **Policy language** — start with a typed Go allowlist per caller; consider
  OPA/Rego only if external policy authoring becomes a requirement.
- **`ape` self-exec inside workspaces** still runs as the guest user in the VM —
  unchanged; the VM is the boundary.

## 13. Sources
See the three source-linked research appendices captured this session (security /
prior-art, NATS/NEX, VM-mechanics/VFIO/platform). Key anchors:
- Prior-art daemons & the socket-≈-root trap: libvirt auth/polkit ACL; Docker
  socket + AuthZ CVEs (2024-41110, 2026-34040); Lima CVE-2026-53657; Firecracker
  jailer; polkit/machined.
- NATS: embedding `nats-server`; `nats.go/micro` (ADR-32); UDS Discussion #7677;
  decentralized JWT + leaf nodes; **NEX** (synadia-io/nex, docs.nats.io/using-nats/nex).
- Kata/VFIO: Kata architecture + containerd-kata how-to; NVIDIA-GPU-passthrough-and-Kata-QEMU;
  kernel VFIO docs; ArchWiki PCI passthrough; firecracker-containerd (devmapper snapshotter).

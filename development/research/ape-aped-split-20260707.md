---
created_at: 2026-07-07
updated_at: 2026-07-08
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
  rootless-VMM mode breaks passthrough without a root-parent device-node chown),
  so the privilege is isolated in a narrow, audited daemon rather than run the
  workload-executing CLI as root. Covers: the security model (avoiding the
  "docker-group = root" trap), the privilege/de-privileging design (a
  **network-less root executor** behind a **de-privileged NATS front-end**, joined
  by an `SO_PEERCRED`-guarded AF_UNIX boundary; Kata rootless-VMM for the per-VM
  QEMU), the `ape`↔`aped` transport (**decided: embedded NATS everywhere** — host
  `ape`, the in-VM `ape` agent, and the future cluster share one NATS-micro
  contract, the NEX model — unified by a transport-agnostic `Backend` interface +
  a per-credential subject-authz policy layer), the VM-lifecycle op set and
  driver choice (shell-out vs containerd Go client), VFIO prerequisites, and how
  the local daemon generalizes to a remote hypervisor agent (NEX is a pattern
  reference). Includes a Go `Backend` interface sketch and a phased plan. **The
  actionable, phased spec is PLAN-18** (`development/planning/plan-18_ape-aped-split.md`);
  the device-tier recipe is `ape-aped-passthrough-recipe-20260708.md`.
origin:
  - 2026-07-07 — live rootless-Kata bring-up on this box failed at an unbreakable
    cgroup wall (Kata Go shim mkdir's cgroups at the host root; nerdctl punts
    rootless-Kata as external/kata #135). Combined with the hard requirement for
    GPU/USB passthrough, this proved rootless is off the table for the device
    tier. User decision: rootful `kata-qemu` + an `ape`/`aped` privilege split.
  - Supersedes the "kata rootless" assumption in
    `sandbox-qemu-vs-kata-20260706.md` and PLAN-16's rootless framing.
  - Host cleanup for the failed experiments: `_output/2026-07-07-sandbox-host-changes-and-cleanup.md`.
  - 2026-07-08 — refined by a multi-track investigation (tracks A–G, findings
    adversarially verified; version currency checked against Kata 3.32.0 /
    containerd 2.3.2 / nerdctl 2.3.4 / nats-server 2.14.2 / nats.go 1.52.0). The
    edits below (§1–§13) fold in its corrections; see the research brief
    `ape-aped-research-prompt-20260708.md`, and the two produced deliverables:
    **PLAN-18** (the phased spec) and the **passthrough recipe**.
---

# `ape` + `aped`: a rootful VM-management daemon for Kata-QEMU workspaces

> **Revision note (2026-07-08).** This doc has been refined by a 7-track
> investigation (NEX / VFIO passthrough / daemon hardening / NATS plane / Backend
> + migration / guest agent / Firecracker + controller). Notable corrections
> folded in below: current **NEX is no longer Firecracker-only** (it was rewritten
> — runtime-agnostic, no VM runtime, no passthrough), so §7/§12's *rationale* is
> updated (the decision stands); the **containerd Go client is not forced by an
> annotation gap** (`nerdctl`/`ctr` can emit the annotations) — it is justified by
> task events + PTY fidelity + a typed OCI spec; **`enable_iommu` is dropped from
> the GPU set**; the **cold-plug cause** is the fixed 64-bit MMIO window, not the
> `pcie.0` restriction; **"refuse mixed IOMMU groups" is wrong** (a GPU + its own
> audio is normal — authorize group members, place them in one guest address
> space); **USB is per-device via QEMU `usb-host`** (aped-synthesised from a
> vendor:product allowlist — not whole-controller VFIO, which would leak the
> keyboard/mouse); and the daemon splits into a **network-less root executor + a
> de-privileged NATS
> front-end**. The actionable, phased plan is **PLAN-18**.

> **Why this exists.** GPU/USB passthrough + a hard KVM boundary is **inherently
> rootful** — binding devices to `vfio-pci`, programming IOMMU groups, opening
> `/dev/vfio`, and DMA-pinning guest RAM all require root, and Kata's own
> rootless-VMM mode does not grant the de-privileged VMM access to `/dev/vfio`
> unless a root parent chowns the group node to it (so fully-rootless Kata for
> device work is impossible). Rootless and passthrough are mutually exclusive
> without a root parent. So we do **not** run `ape` (which executes AI-agent
> workloads) as root; we isolate the unavoidable privilege in a small, auditable
> daemon `aped`, and `ape` drives it. This is exactly how libvirtd / dockerd /
> containerd are built.

## 1. Decision recap

- **Backend:** Kata Containers + **QEMU VMM (`kata-qemu`), rootful.** Hard KVM
  boundary + the most mature GPU/USB VFIO passthrough + OCI ergonomics
  (virtio-fs project mount, image layers). **QEMU is the mature, NVIDIA-reference
  GPU/VFIO cold-plug path** and the device-tier choice. *Corrected wording:* do
  **not** say "QEMU is the only VFIO backend" — Kata's design doc marks QEMU,
  Cloud-Hypervisor, **and** Dragonball VFIO-capable (only Firecracker is not); and
  do **not** say "CLH GPU is broken" — it was historically unimplemented and is
  now landing (PR #12679 merged 2026-03-27; further CLH VFIO in 3.32.0), and CLH
  supports non-GPU VFIO. Treat **"device tier = kata-qemu only" as a
  current-maturity decision, not a permanent limit.** `aped` hard-rejects
  `devices:` on any runtime ≠ `kata-qemu` (and on Firecracker). `kata-clh` remains
  the non-device default; **Firecracker** is the future dense/no-device tier.
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
     the risk. Scope passthrough to workspaces that need it, and prefer giving
     device workspaces **more** isolation (a dedicated node), not less. On
     **Destroy**, a passed-through GPU must be **FLR-reset + scrubbed** before
     rebind/reallocation, or state leaks across tenants (or the device wedges).
   - *Hardening:* the VM-facing NATS front-end is attack surface reachable from
     hostile guests — **run NATS + telemetry ingestion de-privileged**, with
     privileged VM ops behind a narrow internal boundary, so a `nats-server`
     exploit from a guest doesn't yield host root (§3, §4).

**Secondary (defense-in-depth): host-side access must not become root.** Even
though the host `ape` is the operator, `aped` must not be a generic "do X with
root" executor — the **"docker-group = root" trap**: a root daemon that faithfully
executes whatever a client asks is root-equivalent, because the client can ask it
to erase the boundary — `docker run -v /:/mnt …`, `--privileged`, arbitrary device
passthrough.

- **CVE-2026-34040** (Docker AuthZ-plugin bypass, the 2026 regression of
  **CVE-2024-41110**; fixed Docker Engine 29.3.1 / Desktop 4.66.1, CVSS 8.8) is
  *this project's exact threat model*: an AI coding agent in a container sandbox,
  prompt-injected via a crafted repo, sends a `>1MB`-padded Docker API request
  that bypasses the AuthZ plugin and creates a privileged host-mounting container.
  It is the strongest justification for defenses #1 (constrained vocabulary) and
  #4 (authorize the *fully-parsed* request, not a summary or an oversized/empty
  body).
- **Lima CVE-2026-53657** (world-writable `0777` guest-agent socket; fixed Lima
  v2.1.3, CVSS 8.2) is the **"socket ≈ root" analogy** — but note it is a
  *guest-local* root privesc, **not** a demonstrated host escape. Keep it as the
  analogy, not as evidence of a host break.

**`aped` must therefore never be a generic "do X with root" executor.** Its
defenses, in order of importance:

1. **Constrained operation vocabulary.** `aped` exposes *typed VM-lifecycle verbs
   on constrained objects* (create/start/stop/attach a VM described by a schema)
   — **never** a free-form "run with these host mounts/devices/flags." No
   `--privileged`, no arbitrary host-path mount, no arbitrary device string. This
   structural narrowing is the difference from the Docker API.
2. **Default-deny, per-caller allowlists** for every host resource a request can
   touch:
   - **VM assets** (images/kernels/rootfs) only from an `aped`-controlled,
     **digest-pinned** store — never a caller-supplied host path.
   - **virtio-fs shares**: only inside a per-caller allowlisted root;
     **canonicalize (resolve symlinks, strip `..`) and re-check *after*
     resolution** — the confused-deputy defense. Never share host `/`, `/dev`,
     `/proc`, `/sys`, or `aped`'s own state.
   - **VFIO/USB devices**: an explicit allowlist mapping *which caller* may pass
     *which specific* PCI BDF (GPU) / USB `vendor:product`. Deny by default. The
     highest-value escalation target — security-critical.
3. **Per-caller policy** binding an authenticated identity → what it may request
   (profiles, device allowlist, vCPU/RAM/count ceilings, mount roots). AuthN
   answers *who*; policy answers *what they may ask*. This is the real boundary —
   a **typed Go allowlist** re-checked on the fully-resolved request, with a
   JSONSchema on the request as at most an input-shape guard *in front of* it
   (§4, PLAN-18 D9). Config: an `aped policy.yaml` (the PLAN-14 `service.yaml`
   analog), loaded + validated at startup.
4. **One validated path to execution.** Authorize the concrete, fully-parsed
   request object; no oversized/empty-body side doors (the CVE lesson).
5. **Audit every privileged op** — caller identity, operation, *resolved* args
   (canonical paths, device IDs, image digest), policy rule + decision, outcome;
   append-only / forwarded on an additive PLAN-13 subject `ape.audit.<node>.>`;
   backed by `auditd` rules on `/dev/kvm`, `/dev/vfio/*` (path/dir watches,
   `arch=b64`+`b32`, `perm=rwa`, a `-k` key, `-e 2` immutable-until-reboot so a
   compromised root can't silently disable auditing).

Net: even a caller who fully owns `ape` + the transport can only spin up VMs from
allowlisted assets, mounting allowlisted paths, passing allowlisted devices —
which is **not** root, by construction.

**Guest-agent threat table (the in-VM `ape`, per-VM cred, server-enforced
regardless of the guest owning its creds).** A fully-compromised guest **CAN**:
poison its own per-VM telemetry, run workloads it was already meant to run, read
its own scoped `.creds`. It **CANNOT**: issue any `ape.vmm.*` command,
address/impersonate another VM's `ape.*.vm-*.>`, sniff another VM's replies
(`_INBOX`), or reach host-operator / other-tenant subjects.

## 3. Privilege & de-privileging model

`aped` genuinely needs privilege a userns can't grant (VFIO binding, DMA pinning,
`/dev/kvm`, device-node chown). But it is a **containerd client**, not the parent
of QEMU — so most of the heavy privileged work (cgroup/mount/netns/memlock/
device-node prep) is **containerd's / the Kata shim's / QEMU's**, not `aped`'s.
The model is **run the executor as root, stripped to almost no capability, and
de-privilege the per-VM VMM via Kata itself.**

- **Minimized capability set** *(supersedes the earlier 7-cap list; `[INFERRED]`
  from DAC + the chosen architecture, not yet executed against a running `aped`).*
  Because `aped-exec` is a containerd client, drop `CAP_SYS_ADMIN`, `CAP_NET_ADMIN`,
  `CAP_SYS_RESOURCE`, `CAP_IPC_LOCK`, `CAP_DAC_OVERRIDE`, `CAP_MKNOD`:
  - uid-0 reaches the **root-owned containerd socket** and writes the `0200`/`0644`
    **root-owned `vfio-pci` sysfs bind files** as *owner* — no `CAP_DAC_OVERRIDE`.
  - memlock/cgroup/mount/netns are containerd's/shim's/QEMU's job — not `aped`'s.
  - Net: **`CapabilityBoundingSet=` (empty)** for the non-device tier; **`{CAP_CHOWN}`**
    for the device tier *only if* `aped` chowns `/dev/vfio/<group>` to a
    de-privileged VMM uid.
- **De-privilege QEMU per VM = Kata rootless-VMM, not an `aped`-built jailer.**
  QEMU is a child of **`containerd-shim-kata-v2`**, not `aped`, so `aped` does
  **not** build a jail/chroot, create the tap, or set cgroup+memlock for QEMU. The
  lever is Kata **`hypervisor.rootless=true`** (or the
  `io.katacontainers.hypervisor.rootless` annotation), executed by the shim;
  shimv2 + virtiofsd stay root. For **device** VMs, rootless-VMM's VFIO needs the
  root parent to **chown `/dev/vfio/<group>`** to the random `kata-NNN` uid the
  shim picks at create (a uid handshake — see §12 / PLAN-18 Risks). *Corrected
  LOCKED-3 wording:* rootless-VMM breaks VFIO **unless the root parent chowns the
  group node to the VMM uid**; fully-rootless Kata (no root anywhere) remains
  impossible.
- **QEMU host seccomp — the highest-value hardening the earlier draft omitted.**
  QEMU is the **only** Kata VMM with host-facing seccomp **off** by default.
  `aped`'s Kata-config management must set
  `seccompsandbox="on,obsolete=deny,spawn=deny,resourcecontrol=deny"` in the QEMU
  hypervisor config. (`elevateprivileges=deny` is intentionally absent — it
  conflicts with QEMU daemonize and rootless-VMM's own uid drop.)
- **systemd hardening.** `NoNewPrivileges=yes` (set explicitly — it's root),
  `ProtectSystem=strict` + minimal `ReadWritePaths=`, `ProtectProc=invisible`,
  allowlisted `RestrictNamespaces=`, `SystemCallErrorNumber=EPERM`. The
  `@system-service` filter **already includes** the KVM/VFIO `ioctl` and the
  chown/namespace syscalls, so **no `SystemCallFilter` additions are needed** and
  `~@privileged` is safe alongside `CAP_CHOWN` (verify `@chown ∉ @privileged` on
  the deployment's systemd). **Caveat: `ProtectKernelTunables=yes` is incompatible
  with the `vfio-pci` sysfs bind** (it makes `/sys` read-only) — the device-tier
  executor omits it or delegates the bind to a separate-namespace oneshot helper.
  Measure with `systemd-analyze security --offline=true` (predicted OK band
  ~2.5–3.8; the score credits none of the `SO_PEERCRED` boundary, subject authz,
  QEMU `-sandbox`, or KVM boundary — necessary, not sufficient). The concrete
  `aped.service` / `aped-front.service` / `aped-priv.socket` unit drafts + auditd
  rules are in **PLAN-18 Appendix A**.
- **TCB includes the separate rootful containerd + shim + QEMU**, whose units
  `aped` must also configure: `LimitMEMLOCK=infinity` on the containerd unit (VFIO
  pins all guest RAM; this box's default is 8192 KiB), and `seccompsandbox=on` in
  the Kata QEMU config. Verify memlock inheritance to QEMU via
  `/proc/<qemu-pid>/limits` (not `systemctl show`).

## 4. Transport & auth — DECIDED: embedded NATS everywhere

`aped` embeds a NATS server (TCP) and exposes the VM API as a **NATS micro**
service. The **same NATS contract** serves three clients, distinguished by
credential:
1. **host `ape`** — operator creds scoped to **VM-management** subjects
   (`ape.vmm.<node>.>`).
2. **in-VM `ape`** (one per workspace) — **per-VM** creds scoped to
   **telemetry/metrics/transcripts only**, and **explicitly denied** every
   management subject.
3. **future company NATS cluster** — the identical micro contract, so local and
   platform are the same API.

**Why this is the right call (and supersedes the earlier "unix-socket local"
lean):** the in-VM agent *must* cross the VM boundary to reach the host, which
rules out a unix socket for that leg — so NATS is mandatory **anyway**; using it
for the host `ape`↔`aped` leg too buys one uniform contract at no extra cost. It's
exactly the **NEX** model. The guest reaches `aped`'s NATS over a **private
host↔guest link** (the container bridge gateway), separate from the deny-by-default
public egress.

### Process architecture — a network-less root executor behind a de-privileged NATS surface
*(Reconciles the two candidate layouts into one; the root executor holds no NATS
listener — the earlier "management NATS #1 in root `aped`" idea is superseded.)*

- **`aped-exec` (root executor) — the only privileged process.** Holds **no
  network address family but `AF_UNIX`** (`RestrictAddressFamilies=AF_UNIX`).
  Drives containerd + VFIO, re-validates every command against policy (§2 #3), and
  performs the narrow privileged acts. Listens only on `/run/aped/priv.sock`
  (`AF_UNIX`, `SOCK_SEQPACKET`, `0660 root:ape`), accepts a **closed enum of typed,
  fully-resolved commands** (image digest, canonical mount path, resolved PCI BDF —
  never a free-form request or a caller host path), and verifies
  `getsockopt(SO_PEERCRED)` peer uid before acting. This **relocates `SO_PEERCRED`
  from the (impossible) NATS leg to a real local socket** where it is authoritative.
- **De-privileged NATS surface (`User=aped`, no caps, no `/dev`, no containerd
  socket).** Embeds `nats-server` + telemetry ingestion + subject authz + a policy
  pre-check, and forwards typed commands to `aped-exec` over the AF_UNIX boundary.
  **Management NATS binds `127.0.0.1`** (host `ape`, guest-unreachable); **guest
  telemetry NATS binds the bridge/gateway IP** and is TELEMETRY-account-scoped, so
  a guest-facing RCE can never name a management subject. A guest that pops
  `nats-server` lands in a capability-less, device-less, containerd-socket-less
  process, TELEMETRY-scoped, that cannot satisfy the executor's `SO_PEERCRED`
  check — three independent barriers.
- *(Sub-choice, open.)* the guest telemetry listener may be a **second listener in
  the same de-privileged process** (simpler) or a **separate leaf gateway process**
  bound to TELEMETRY (stronger blast-radius). Both keep `aped-exec` network-less
  and guests account-scoped. Recommend the separate gateway once the device tier
  ships.
- **Socket-activation fix:** `SocketMode=0660`/`SocketGroup=ape` apply only to
  `AF_UNIX` listeners and are **inert for the loopback-TCP NATS listener** (which
  is gated instead by `.creds` perms + subject authz). Move that `0660`/
  `SocketGroup=ape` gate onto the **internal AF_UNIX `priv.sock`** (the
  front-end↔executor boundary).

### The `vmm` NATS-micro service
`micro.AddService(nc, micro.Config{Name:"ape-vmm", Version:<aped semver>,
Metadata:{node, hostname, kata_version}})`, one `AddGroup("ape.vmm."+node)`, one
`AddEndpoint` per `Backend` verb: `capabilities | create | start | stop | exec |
attach.open | freeze | unfreeze | suspend | resume | snapshot | list | inspect |
destroy`. `$SRV.{PING,INFO,STATS}` discovery is free; multiple `aped` instances
queue-subscribe; errors use `req.Error` with stable codes (reuse PLAN-14 `BUSY`/
`VALIDATION`/`NOT_FOUND` + add `UNSUPPORTED`/`DEVICE_UNAVAILABLE`/`DENIED`).
Versioning = the micro `Version` field + payload `"v":1` (PLAN-13 discipline);
subjects are additive-only.

### Telemetry subjects = the PLAN-17 model, not an ad-hoc root
*(Supersedes the earlier `ape.vm.<id>.telemetry.>`.)* Per-VM telemetry uses
`ape.*.<vmtoken>.>` where `<vmtoken>` is the per-VM credential's subject token —
`aped` mints the JWT with `name=vm-<id>`, so telemetry flows on the existing
PLAN-13/17 roots `ape.evt.vm-<id>.…`, `ape.log.vm-<id>.…`, `ape.metrics.vm-<id>.…`
(+ `ape.blob.uri-request` if transcript offload is on) — **byte-compatible with
existing consumers.** Management is the new additive root `ape.vmm.<node>.>`.
`ape.*.vm-<id>.>` cannot match `ape.vmm.<node>.>`, and an explicit deny
`ape.vmm.>` makes the barrier structural.

### Per-credential subject authz + two accounts (DECIDED)
Security is enforced by NATS subject authorization, per credential (deny wins;
`allow_responses` for the reply leg). **Two accounts:** **HOST_OPS** (management:
host `ape` + the `aped` service identity, `ape.vmm.<node>.>` + `$SRV.>`) and
**TELEMETRY** (per-VM users). Account isolation means a guest cannot even *name* a
management subject — a stronger guarantee than a deny rule. **`aped` holds a
credential in *both* accounts** (it is the server + minter + operator): a HOST_OPS
cred for management and a **TELEMETRY subscriber cred for ingestion**, so **no
cross-account stream export/import is required**. Crucially, the **guest-facing
de-privileged front-end holds ONLY the TELEMETRY ingestion cred — never HOST_OPS**
— so a front-end compromise cannot act as an operator. `aped` runs the embedded
server in **operator/JWT mode with a memory resolver** (required to hot-mint
per-VM users without a reload). Gate the **host** leg with a **`root:ape 0640`
creds file** + loopback bind; use Operator→Account→User JWT/nkey + TLS +
per-tenant accounts + leaf nodes for the cluster.

### Per-VM creds lifecycle
Mint at Create with `jwt/v2` + `nkeys` (`NewUserClaims` + `IssuerAccount` +
`Encode(TELEMETRY signing key)` + `FormatUserConfig`, or scoped signing keys with
`{{name()}}` templating). Grant: pub-allow `ape.{evt,log,metrics}.vm-<id>.>` (+
`ape.blob.uri-request` if offload is on) + `allow_responses`; sub-allow only
`ape.svc.vm-<id>.>` (the PLAN-14 job-intake) + a scoped `_INBOX_vm-<id>.>`;
**deny-sub the default `_INBOX.>`**; explicit deny `ape.vmm.>` and any other-VM
`ape.*.vm-*.>`. **Inject** via the PLAN-16 staging home as a **read-only
bind-mounted `.creds` file** (`~/.config/ape/vm.creds`, `0600`) + env
`APE_NATS_URL`/`APE_NATS_CREDS` (PLAN-13 D1 resolution — no new plumbing). **Prefer
the file over env** — a `.creds` embeds the nkey seed, which env leaks via
`/proc/<pid>/environ` + child inheritance. **Invalidate** at Destroy primarily by
the VM connection dropping + a short JWT `exp` (re-minted while the VM lives);
`AccountClaims.Revoke` + a resolver push is break-glass. CreateRequest secrets are
**xkey-sealed** (`nkeys` CurveKeys; pin `nkeys ≥ 0.4.6` for CVE-2023-46129 —
current is 0.4.16) for the remote tier.

### Private host↔guest transport
Baseline: the **container-bridge gateway IP over plain TCP** — the NEX model;
`nats.go` dials it natively; set `APE_NATS_URL=nats://<gateway-ip>:<port>` in the
guest. This is a **distinct destination from public egress** and must bypass the
deny-by-default CONNECT proxy (extend `NO_PROXY`) while staying off it; run **TLS**
on this leg (`nats-server` is plaintext by default), and run the egress CONNECT
proxy **de-privileged alongside the NATS gateway** (it is another guest-reachable
surface). A `vsock↔NATS` bridge is **future hardening only**: NATS has no native
vsock/UDS (`nats-server` discussion #7677 closed), so it needs a byte-relay on both
ends + per-VM Kata guest-CID discovery + only works on kata-qemu's real
vhost-vsock. **The baseline depends on bridge networking** — under the hardened
`--network none` + nft-wall direction the gateway IP disappears, which is exactly
when the vsock bridge becomes attractive; **sequence it with that work.**

### Interactive exec/attach over NATS
Core NATS **drops slow consumers by closing the connection**, so bulk stdio must
not ride request/reply (`allow_responses` is single-message and insufficient).
Control via micro `attach.open` (returns a session id + subject prefix), then
explicit session subjects `ape.vmm.<node>.exec.<sid>.{stdin,stdout,stderr,resize,
control,exit}` with **≤32 KiB frames + credit-based flow control**. Locally the
operator is on-host: the `Stream` (§6) is implemented directly over the containerd
task-exec PTY, so bulk stdio **never touches NATS** — same signature, two impls.

### Guest agent (the in-VM `ape`)
The in-VM agent is the **same baked `ape` binary** (PLAN-16 D6), started from
`images/ape-sandbox/entrypoint.sh` **before `exec "$@"`** (like sshd — a Kata
container-image VM has no init managing the entrypoint; the kata-agent spawns the
ENTRYPOINT directly, so this is **not** a guest systemd unit), **gated on per-VM
creds presence** (`APE_NATS_CREDS`+`APE_NATS_URL` set) — no creds → agent skipped →
PLAN-16 standalone workspaces boot unchanged. It is **structurally unable** to
issue `ape.vmm.*`: two belts — (a) server authz denies it; (b) the
`sandbox-agent` subcommand carries **no vmm-request-builder code path**. The in-VM
`ape service` runs `--name=vm-<id>` and does **not** rely on global `$SRV`
discovery (it collides with the locked-down subscribe grant); liveness is via
`aped`'s registry + a per-VM heartbeat. Full details in §6/§4 and PLAN-18 D6.

### Reuse the repo's NATS/daemon foundations — `aped` is not greenfield
> **Prerequisite reality (verified 2026-07-08):** PLAN-13/14/17 have **zero code**
> — no `nats` dependency in `go.mod`, and none of
> `internal/{natsconn,eventing,reporting,service,blobstore,sessionref}` exist. So
> the NATS layer is a **hard Phase-0 gate** (PLAN-18): implement PLAN-13 (+
> PLAN-17 amendments) then PLAN-14 first, targeting **current** versions
> (nats-server 2.14.x, nats.go 1.52.x, jwt/v2 2.8.x, nkeys 0.4.16), before any
> `aped` work.

- **Identity + subject taxonomy = PLAN-13/17.** Decode the `.creds` user JWT to a
  subject token, server-enforced `ape.*.<token>.>` (PLAN-13 `internal/natsconn`
  `Identity()`; PLAN-17 D1). The in-VM `ape` agent's telemetry/metrics/transcripts
  = **PLAN-17**'s "self-report with only creds" mode over **per-VM creds** `aped`
  mints at create; eventing + transcript blobs = **PLAN-13**.
- **`aped` ≈ PLAN-14's `ape service`, elevated to rootful VM management.** PLAN-14
  is *already* a NATS-micro daemon (`micro.AddService`, `$SRV` discovery, keyed
  admission, project allowlist, graceful shutdown, the submitter-vs-daemon
  identity nuance). `aped` reuses that `internal/service` shape — the difference
  is it manages **Kata-QEMU VMs** (rootful, VFIO) instead of spawning CLI child
  processes, which is exactly why the rootful hardening (§2–3) matters *more* here.
- **Composition — the "NATS worker VM."** `aped` (host, this plan) provisions a
  Kata VM; *inside* it the in-VM `ape` runs **PLAN-14 `ape service`** / **PLAN-15
  `ape script`** / **PLAN-17** reporting. Host `aped` = VM lifecycle; in-VM `ape` =
  workload + telemetry. Two NATS-micro daemons, composed — the platform's
  worker-VM vision.

## 5. Driving Kata-QEMU: shell-out vs containerd Go client

Kata's integration is **containerd Runtime v2 (shim-v2)**: `io.containerd.kata-qemu.v2`
→ one `containerd-shim-kata-v2` per sandbox → QEMU/KVM + a `kata-agent` in-guest
over VSOCK; rootfs/volumes shared via **virtio-fs**. `aped` needs **system
(rootful) containerd** with the Kata shim registered + `/dev/kvm`, regardless of
driver.

**The charter clarified:** "single binary / shell out / no heavy dep" is a
constraint on **`ape`**, not `aped`. A dedicated rootful daemon may carry a
`containerd` Go-client dep.

**Recommendation — a `Backend` interface with drivers inside `aped`:**
- `shellDriver` (nerdctl/ctr) — reuses the existing pure `RunArgs`/`ExecArgs`
  builders; Phase-1 non-device happy path; fully testable.
- `containerdDriver` (containerd 2.x Go client: `v2/client`, `v2/pkg/cio`,
  `v2/pkg/oci`, `api/events`).
- `firecrackerDriver` (§10) — a **separate** `firecracker-containerd` 1.7.x stack.

> **Correction (verified 2026-07-08):** the containerd driver is **not** forced by
> "nerdctl can't emit the `io.katacontainers.*`/`cdi.k8s.io/vfio*` annotations" —
> that claim is **false**. `nerdctl run --annotation k=v` (passed through to the
> OCI runtime) and `ctr --annotation` both emit them (proven: a nerdctl-emitted
> `io.katacontainers.*` annotation reaches the Kata shim and errors "not enabled"
> — kata#1533); `internal/sandbox/kata.go:99-101` already appends `--label` the
> same way. The genuine forcing functions for the Go client are:
> 1. a **programmatic task-event stream** (`client.EventService().Subscribe`;
>    `TaskExit`/`TaskPaused`/`TaskOOM`) for a real daemon state machine;
> 2. **PTY/stdio fidelity** over NATS (`Task.Exec` + `cio.WithTerminal` +
>    `ResizePty` + `Process.Wait`→`ExitStatus`);
> 3. **owning the OCI spec as a typed, auditable object** (§2 "authorize the
>    fully-decoded request").

Gate the containerd driver behind the device tier + event-stream needs; keep the
shell driver for parity. **Note:** one `aped` binary **cannot** link both a
containerd 2.x in-process client and fc-containerd's compiled-in 1.7.x plugin
(§10); the three drivers target different sockets, hidden behind `Backend`.

## 6. The transport-agnostic `Backend` interface

One interface (new `internal/workspace`, generalizing today's `Runner`) that a
local containerd/nerdctl driver **and** a remote NATS client both implement, so
`ape` (and a future controller) code identically against either. Request/response
types are JSON-serializable — they double as the NATS wire contract.

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
	// Capabilities: what this node can satisfy (KVM, runtimes/VMMs, GPU/USB
	// inventory + IOMMU-group state, host-fs support, free RAM). A controller's
	// scheduler consumes this across nodes before placing work.
	Capabilities(ctx context.Context) (Capabilities, error)

	Create(ctx context.Context, req CreateRequest) (Workspace, error) // provision (detached)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Destroy(ctx context.Context, id string, req DestroyRequest) error

	Exec(ctx context.Context, id string, req ExecRequest) (ExitStatus, error)
	// Attach: interactive PTY. Stream abstracts the bidi channel — locally the
	// containerd task-exec PTY; over NATS a per-session inbox.
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
	Stderr() io.Writer            // explicit stderr sink (the earlier sketch had none)
	Resize(cols, rows uint16) error
	CloseWrite() error            // half-close stdin
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
	// USB: a single device by "vendor:product" (an ESP-32, a barcode reader, a
	// serial dongle), forwarded via QEMU `usb-host` — NOT whole-controller VFIO,
	// which would drag in the system keyboard/mouse. Only aped synthesises the
	// usb-host device string, from a per-caller vendor:product allowlist; the
	// caller sends this typed field, never raw QEMU args (§8).
	USB string `json:"usb,omitempty"`
}

type Capabilities struct {
	KVM      bool            `json:"kvm"`
	Runtimes []RuntimeInfo   `json:"runtimes"` // handler, VMM, version, host_fs, passthrough
	HostFS   bool            `json:"host_fs"`  // false on Firecracker nodes
	GPUs     []GPU           `json:"gpus"`
	USB      []USBDevice     `json:"usb"`      // passable USB devices (vendor:product) for usb-host
	IOMMU    IOMMUState      `json:"iommu"`
	Mem      MemInfo         `json:"mem"`
	Factory  FactoryState    `json:"factory"` // Templating shares RAM RO → KSM side-channel
}
type GPU struct {
	BDF, VendorID, DeviceID, Model, Driver string
	IOMMUGroup    int
	GroupIsolated bool
	GroupMembers  []string
}
type USBDevice   struct { VendorID, ProductID, Description string } // usb-host forwarding, not VFIO
type IOMMUState  struct { Enabled bool; Mode string; VfioReady bool }
type FactoryState struct { Templating, VMCache bool }
```

- **Subsumes today's `Runner`** — the shell driver keeps building `RunArgs` and
  satisfies `Backend`; the containerd driver adds events + a typed OCI spec.
- **`Create()` device-resolution contract:** resolve BDF→IOMMU group→(CDI spec or
  `/dev/vfio/<N>`), **enumerate + refuse groups with unauthorized/host-needed
  members** server-side (default-deny; §8), inject the VFIO device **exactly once**
  at sandbox creation (double-injection footgun — kata#11125 / PR #11150), and log
  the resolved group/BDFs/image-digest for audit.
- **Scheduling stays out** — a controller consumes `Capabilities()` across agents
  then calls `Create()` on the chosen one; the per-node API is unchanged
  local-vs-fleet.

## 7. Prior art to build on: NEX (NATS Execution Engine)

> **Rewritten (2026-07-08).** Current **NEX** (0.4.1; HEAD 2026-07-02) has been
> **rewritten and is runtime-agnostic** — **Firecracker / microVM / vsock removed**
> from the tree. A node is a NATS-micro control plane (embedded **or** external
> NATS via `WithInternalNatsServer`) that auctions workloads to **Nexlets**
> (pluggable runtime adapters, an `agent.Agent` SPI). The built-in nexlet runs
> **native subprocesses (no isolation)**; containers/VMs/WASM are custom nexlets.
> **NEX has no built-in VM runtime, no device passthrough, and — having no VM — no
> host↔guest transport to borrow.** So NEX gives **zero reusable code** for Track
> B (passthrough) or the private host↔guest link (§4) — both are wholly ours. We
> borrow its **shapes** over Kata-QEMU on our PLAN-13/14/17 taxonomy.

**DECIDED: borrow the patterns, own the control plane.** The decision stands (and
is *reinforced* — NEX's default runtime has no isolation and no passthrough); only
its stated rationale is updated from the now-false "Firecracker-only."

**Rejected alternative — adopt NEX's node wholesale via a Kata Nexlet.** It forks
the subject taxonomy onto `$NEX.*` and abandons PLAN-13/14/17 identity, imports
insecure defaults (`FullAccessMinter`, `AllowAllRegistrar`, native no-isolation
exec), and — critically — **inverts the trust model.** NEX's nexlet/agent is a
*privileged executor the node drives* (its `AgentClaims` subscribe to
`STARTWORKLOAD`/`STOPWORKLOAD`); our in-VM `ape` is the *untrusted* party. **Correct
role mapping:** NEX node → `aped`'s control surface; NEX nexlet/agent → `aped`'s
privileged executor; NEX **workload** creds (sub `$NEX.FEED…logs` + inbox only, no
control) → the in-VM `ape`. Copying NEX's *agent* cred set onto the in-VM `ape`
would hand a hostile guest the management verbs the design forbids.

**Borrow-from-NEX (keep / adapt / reject):**

| NEX pattern                                                                                                         | Verdict                           | For this design                                                                                                              |
| ------------------------------------------------------------------------------------------------------------------- | --------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| Embedded-or-external NATS in the node (`WithInternalNatsServer`)                                                    | **Keep**                          | `aped` embeds `nats-server` (§4)                                                                                             |
| SVC-control vs FEED-telemetry subject split                                                                         | **Keep**                          | already have it: `ape.vmm.*`/`ape.svc.*` (control) vs `ape.{evt,log,metrics}.*` (feeds) — keep our `ape.` prefix, not `$NEX` |
| Node-minted **telemetry-only** workload creds (`WorkloadClaims`: pub inbox only, sub `FEED…logs`+inbox, no control) | **Keep**                          | direct proof of the per-VM "telemetry-only, management-denied" cred; mint at Create (§4)                                     |
| Three-tier credential ladder (bootstrap → operational → workload)                                                   | **Adapt**                         | optional enroll leg; per-VM creds minted at Create                                                                           |
| NATS-micro control API                                                                                              | **Adapt**                         | the `vmm` service, **roles remapped** (agent tier → `aped`, not the guest)                                                   |
| Per-nexlet JSONSchema on the run request                                                                            | **Adapt**                         | input-shape guard *in front of* the typed policy (§2 #3) — shape, not policy                                                 |
| Xkey (curve) secret sealing                                                                                         | **Adapt**                         | REMOTE tier only (seal per-VM creds/secrets so the hub never sees plaintext); low value for the trusted local `aped`         |
| KV-backed state + `existing=true` resume; `warm` state; lameduck drain                                              | **Adapt**                         | `aped` restart/resume + factory pre-warm (§9) + systemd drain                                                                |
| Node functional-option seams (Minter/State/SecretStore/Auctioneer)                                                  | **Adapt**                         | mirror as testable SPIs in `aped`                                                                                            |
| Two-phase auction placement                                                                                         | **Reject locally / Adapt remote** | overhead for a single directly-addressed `aped`; the controller-tier placement primitive (§10)                               |
| `FullAccessMinter` / `AllowAllRegistrar` / native no-isolation exec (insecure defaults)                             | **Reject**                        | `aped` is default-deny + hard-KVM from line one                                                                              |
| Adopt NEX's node wholesale via a Kata Nexlet                                                                        | **Reject**                        | forks PLAN-13/14/17, imports insecure defaults, inverts the trust model                                                      |
| Host↔guest transport / vsock guest-agent                                                                            | **N/A**                           | removed from NEX; entirely our own work (§4)                                                                                 |

## 8. VFIO passthrough — host prerequisites `aped` owns (all genuinely rootful)

> The full, verified, step-by-step recipe with tested-vs-inferred markers is
> **`ape-aped-passthrough-recipe-20260708.md`** (deliverable D2). Summary + the
> corrections to the earlier draft below.

1. **IOMMU on at boot** (`intel_iommu=on`/`amd_iommu=on`, `iommu=pt`); verify via
   a populated `/sys/kernel/iommu_groups/`.
2. **VFIO modules** (`vfio`, `vfio_pci`, `vfio_iommu_type1`) loaded early (GPUs
   bound in initramfs before `nouveau`/`nvidia`/`amdgpu`).
3. **`vfio-pci` binding** of the target BDF (`driver_override` + unbind +
   `drivers_probe`). Bind lifecycle across reboots is an open decision (persistent
   initramfs/`driverctl` vs runtime unbind-at-create; a GPU driving the host
   display can't be runtime-unbound; intersects the `ProtectKernelTunables` caveat,
   §3).
4. **IOMMU-group isolation** — read `/sys/bus/pci/devices/<BDF>/iommu_group`,
   enumerate members, and **refuse a group only if it contains a device the caller
   is not authorized for, or that the host needs.** *(Correction: do NOT "refuse
   mixed groups" — a GPU co-grouped with its own audio/USB-C function is normal;
   pass ALL members together.)* Passing the whole group is **necessary but not
   sufficient** — place all same-group devices in **one guest PCIe address space**
   (size `pcie_root_port` to the function count; avoid an in-guest vIOMMU for
   multi-function groups) or QEMU fails "group N used in multiple address spaces"
   (kata#10622 — which is a guest-topology *config* problem, not an ACS/BIOS wall;
   and the "…depends on ACS/BIOS…" line often attributed to it is **not in the
   issue**).
5. **`/dev/vfio` access** (`/dev/vfio/vfio` + `/dev/vfio/<group>`; group-granular).
6. **`RLIMIT_MEMLOCK`** — set `LimitMEMLOCK=infinity` on **both** the `aped` unit
   **and** the containerd unit `aped` drives (VFIO pins all guest RAM + IO space;
   this box's default is 8192 KiB); verify inheritance to QEMU via
   `/proc/<qemu-pid>/limits`, not `systemctl show`.
7. **GPU specifics** — no host NVIDIA driver; GPU + its own HDA-audio passed
   together in the group; single-GPU passthrough; on a bare box `aped` does the
   binding (the 3-line sysfs sequence the NVIDIA GPU Operator's VFIO-Manager does
   in k8s).

**Expressing it to Kata (corrected).**
- **Cold-plug is required for NVIDIA GPUs** (`cold_plug_vfio=root-port`,
  `hot_plug_vfio=no-port`) — but *not* because "a device can't be hot-plugged onto
  q35's `pcie.0`" (that generic restriction is solved by a root port). The
  GPU-specific failure persists on a root port too: the **64-bit prefetchable MMIO
  window is fixed at PCI enumeration and can't grow** to a multi-GB BAR (e.g. a
  V100's 32 GB BAR — kata#835), plus a kata-agent PCI-rescan race. So `aped` must
  also set `pcie_root_port ≥ #GPUs` and **size the guest 64-bit MMIO (`pci-hole64`)
  window**.
- **Drop `enable_iommu=true` from the NVIDIA cold-plug set.** Kata's own
  `configuration-qemu-nvidia-gpu.toml` ships `enable_iommu=false`; the native
  `nvidia.ko` binds in **guest-kernel** mode. `enable_iommu=true` (guest vIOMMU)
  belongs to the separate **`vfio_mode="vfio"`** tier (DPDK/nested — the guest runs
  its own VFIO drivers), which is *not* the typical GPU/USB workspace.
- **Bake a per-tier handler, don't accept caller annotations.** Ship an
  `aped`-owned `kata-qemu-gpu` containerd handler whose `ConfigPath` **bakes**
  `cold_plug_vfio=root-port`, `pcie_root_port`, the GPU guest kernel+image, and
  memory. **Actively set `enable_annotations=[]`** — Kata's *default* is **not**
  minimal (`["enable_iommu","kernel_params","kernel_verity_params"]`, including the
  powerful `kernel_params` lever). Lock **both** gates: don't broadly allowlist
  containerd `pod_annotations` **and** set `enable_annotations=[]`. The device
  *identity* is still one per-create CDI/VFIO request `aped` mints from its
  allowlist (a containerd annotation, not a hypervisor one, so not gated by
  `enable_annotations` — an empty allowlist does not block passthrough).
- **CRI framing fix:** `pod_annotations`/`container_annotations`/
  `privileged_without_host_devices` are **CRI-plugin (Kubernetes) filters, inert
  on `aped`'s direct `ctr`/Go-client path** (containerd docs: the CRI section is
  "not recognized by ctr, nerdctl, and Docker/Moby"). On that path Kata's
  `enable_annotations` is the primary gate (path-type annotations also need a
  `valid_*_paths` value-check; CDI is entirely outside it). Move the CRI knobs to a
  "future k8s deployment only" note.
- **The bare-metal non-k8s path is documented and works** (correction): Kata
  cold-plugs a VFIO device passed via OCI `--device /dev/vfio/<group>` when
  `cold_plug_vfio=root-port`, no k8s/GPU-Operator/Pod-Resources-API in that code
  path. `aped`'s host work is the standard bind + IOMMU-group check + optionally a
  static host CDI spec (`nvidia-ctk cdi generate`); it does **not** reimplement the
  Operator's k8s controllers. (kata#11671 is a *closed question* on 3.19.1 — root
  cause a hand-built guest image, resolved "Success!" — not an unresolved 3.32
  bug; kata#11125 is closed, fixed by PR #11150, with a working standalone demo.)
  **⚑ The #1 load-bearing unknown** is whether that non-k8s single-phase cold-plug
  works end-to-end vs requiring the k8s two-phase — **validate on a discrete-GPU
  box** before finalizing.
- **Guest-image delta:** the device tier needs a **separate, larger** guest image
  (+ possibly a custom kernel) — NVIDIA modules **built+signed against the pinned
  Kata guest kernel** + NVRC + userspace libs (stock image has none); generic
  in-guest VFIO needs `vfio`/`vfio_pci`/`vfio_iommu_type1` + a vIOMMU; load
  non-built-in drivers via `io.katacontainers.config.agent.kernel_modules`. Plan a
  GPU guest-image build/signing workstream (needs an owner) that carries the
  host-Kata↔guest-kernel version coupling.

**USB — per-device `usb-host`, synthesised only by `aped` (DECIDED).**
Whole-controller VFIO is **rejected** for USB: an xHCI controller carries *all* its
ports, so passing it would hand the guest the **system keyboard/mouse** (and it is
IOMMU-group-constrained). Instead, pass a **single device by `vendor:product`** via
QEMU **`usb-host`** (`-device usb-host,vendorid=…,productid=…`) — the ESP-32 /
barcode-reader / dongle case. This is device-level USB forwarding mediated by QEMU
+ the host USB stack (**no raw DMA**, unlike VFIO-PCI) and carries **no
IOMMU-group constraint** and **no keyboard/mouse leak** — so it is both safer and
finer-grained than controller VFIO. **Only `aped`** constructs the `usb-host`
device string, from a **per-caller `vendor:product` allowlist**; the caller sends
the typed `Device{USB:"vendor:product"}` and **never** raw QEMU args, so the attack
surface collapses to "which USB IDs may this caller pass" — the same default-deny
model as PCI BDFs. **Implementation cost (device-tier task/risk):** Kata does
**not** expose `usb-host` today, so `aped` must add it — a small **upstream Kata
contribution** (a USB-device annotation) or a narrow `aped`-controlled qemu-device
injection — plus ensure the guest has an emulated xHCI. Until that lands, USB
passthrough is unavailable (controller VFIO stays rejected).

**Cloud-Hypervisor** is not a device-tier alternative today — see §1.

## 9. Lifecycle op set (nerdctl-call vs daemon-logic)

The "just a nerdctl call" surface is thin (run/exec/pause/rm + `-v`/`--device`);
the **daemon's real work is orchestration**. Highlights:

- **create** = one `run`, wrapped in: profile resolution, `~/.claude` compose +
  staging home, egress-proxy supervise (fail-closed, already built), netns+nft
  wall (deferred), **VFIO bind + IOMMU-group check + baked-handler selection +
  single CDI/device injection**, memlock, registry write.
- **pause is currently mislabeled — corrected.** `containerd task Pause` →
  Kata `service.Pause` → `sandbox.PauseContainer` → agent `freeze_cgroup(cid,
  Frozen)` = a **guest freezer-cgroup freeze (RAM stays resident)**, *not* a
  microVM suspend. Split **`Freeze`/`Unfreeze`** (real today) from a VMM-backed
  **`Suspend`** (future). **Fix the exact comments:**
  `internal/sandbox/kata_linux.go:35`, `internal/sandbox/kata.go:196-197`, and
  `internal/apecmd/sandbox.go:37` & `:353` ("Suspend a workspace microVM" →
  freeze). Relabel `ape sandbox pause` as a freeze and add a distinct future
  `ape sandbox suspend`. *(The earlier draft named only `kata_linux.go`.)*
- **Suspend/Snapshot are unreachable on Kata-via-containerd.** The Kata shim's
  `Checkpoint` returns `ErrNotImplemented`, and the VMM control socket (QEMU
  `qmp.sock`, often fd-passed; CLH `clh-api.sock`) is Kata-owned + single-client —
  so `aped` cannot drive QEMU `snapshot-save` / CH `snapshot` behind Kata's back.
  These return `ErrUnsupported` on the Kata drivers; real save/restore needs a
  future driver where `aped` **owns** the VMM lifecycle, or the Firecracker tier.
- **snapshot/template = two mechanisms, different availability:** (a) VM
  suspend/snapshot via the VMM (QEMU QMP `snapshot-save/load` since 6.0; CH
  pause→snapshot→restore) — future, VMM-owning driver only; (b) Kata factory
  fast-**create** via `[factory] enable_template` (shares RAM read-only → KSM
  side-channel; requires `initrd=`, which the `image=`-booting `ape-sandbox` image
  does **not** use) or **VMCache** (no shared memory, no savings) — a host-config
  capability surfaced in `Capabilities.Factory`, never a per-run flag.
- **`CreateRequest.From`** on the Kata tier maps **only** to Kata factory
  templates (and is otherwise dead until the FC tier). Adopt NEX's KV-persist +
  `existing=true` rehydration for `aped` restart/resume, and lameduck-style drain
  for `aped`'s systemd stop.
- **list/status** = the **daemon registry** (source of truth; carries proxy
  pid/mount/devices) over parsing `nerdctl ps`.
- **destroy** = `rm -f` + proxy stop + netns/nft teardown + **device FLR-reset +
  scrub + VFIO rebind to host** + staging/volume policy + registry remove.
- **mount** = virtio-fs (host-fs) / block (volume) / in-guest clone (ephemeral);
  **Firecracker can't host-fs** → force volume/ephemeral on FC.
- **audit** = `aped`'s own structured event per privileged op (§2 #5) on
  `ape.audit.<node>.>`, backed by `auditd` on `/dev/kvm` + `/dev/vfio/*`.

## 10. Local ↔ remote symmetry

**Identical:** the lifecycle verb set + request/response schemas; the
`ape.{evt,log,metrics}.vm-<id>.>` telemetry taxonomy; per-VM creds model; the exec
session protocol; OCI-image guest + containerd + Kata shim-v2 mechanics;
`~/.claude` composition + credential/git modes; the egress-proxy + audit model;
profile schema + mount modes. All `Backend` handlers are the same; local↔cluster
differ only in credential + topology.

**Per-node vs controller classification (genuinely new for remote):**

| Concern                    | Per-node (`aped`, mostly built)                                    | New at the controller                                                    |
| -------------------------- | ------------------------------------------------------------------ | ------------------------------------------------------------------------ |
| Scheduling/placement/drain | reports `Capabilities()`; validates its own admission              | picks a node, bin-packs, drains                                          |
| Multi-tenant auth          | accounts/leaf/subject-authz/per-VM creds (§4)                      | mint/rotate per-tenant JWTs, run the hub, quotas, tenant→node policy     |
| Image distribution         | lazy pull (stargz/eStargz) + air-gap (baked `/opt/apex-framework`) | registry-cred distribution, pre-pull/replication, eStargz build pipeline |
| Networking overlays        | per-VM tap/netns/egress-proxy                                      | Netbird/WireGuard fabric + preview-URL ingress                           |
| Snapshot portability       | node-local templates                                               | pin `From:` to the holding node, or replicate + normalize FC CPU         |

**Placement** uses NEX's auction primitive **at the controller only** (tags carry
capability facts: `has-gpu`, region, free-IOMMU-group; the `Auctioneer` is the
per-node veto). NEX "auto-clustering" is plain NATS routing over a shared system —
not Raft/gossip — which validates the leaf-node topology (outbound-only `:7422`,
per-tenant accounts).

**Snapshot portability** (with sources): Kata factory templates + VMCache are
host-local (`template_path=/run/vc/vm/template`, templating needs `initrd`+QEMU≥4.1
+ shares RO memory → KSM side-channel); Firecracker snapshots are
CPU-vendor/model-validated on restore (need CPU templates to cross hosts), arm64
GIC-version is a hard blocker, and fc-containerd cross-host container-snapshot
restore corrupts the guest disk (#759) — so the controller must pin
`From:<SnapshotRef>` to the holding node or replicate + normalize.

**Firecracker vs Kata-QEMU (corrected).** `firecracker-containerd` is pinned to
**containerd 1.7.x** (main `go.mod`: v1.7.29; open PR to 1.7.33; **no** 2.x
migration) and needs a **specialized containerd binary** with its control plugin
compiled in (+ matching `firecracker-ctr`). It **cannot share** the Kata-QEMU
node's containerd 2.3.x stack — it runs as a **separate** node-local containerd
1.7.x + `aws.firecracker` shim + devmapper snapshotter, driven by a third
`firecrackerDriver` behind the same `Backend`. FC = ~5 virtio-MMIO devices, **no
PCI/VFIO, no host-fs**, so `Create` rejects `Devices` + `Mount:host-fs` at
**admission** (node-side, against its own `Capabilities()` — admission, not
scheduling). Low upstream velocity + untagged `firecracker-go-sdk`; pin exact SHAs
and gate this tier behind the Kata-QEMU tier shipping first.

## 11. Recommendation & phased plan

**Recommended architecture:**
- `ape` (unprivileged) ↔ `aped` (rootful) over an **embedded NATS server** — one
  **NATS-micro `vmm`** contract for host `ape`, the in-VM `ape` agent, and the
  future cluster (the NEX model) — behind a transport-agnostic **`Backend`
  interface** + a **default-deny policy layer** (per-credential **subject authz** +
  per-caller asset/mount/device allowlists + full audit).
- `aped` = a **network-less root executor** (`AF_UNIX` only; empty
  `CapabilityBoundingSet`, or `{CAP_CHOWN}` for the device tier) + a **de-privileged
  NATS front-end**, joined by an `SO_PEERCRED`-guarded typed-command AF_UNIX
  boundary. Per-VM QEMU is de-privileged via **Kata rootless-VMM + QEMU `-sandbox`**
  (which `aped`'s Kata-config management must enable). The TCB includes the
  separate rootful containerd+shim+QEMU whose units `aped` also configures
  (`LimitMEMLOCK=infinity`, `seccompsandbox=on`).
- Drivers: `shellDriver` (nerdctl, non-device parity) + `containerdDriver`
  (containerd 2.x Go client; device-tier cold-plug + task events + PTY) +
  `firecrackerDriver` (fc-containerd 1.7.x, §10) — **three drivers across two
  containerd versions.** `Suspend/Resume/Snapshot` are declared from day 1 but
  return `ErrUnsupported` until the VMM-owning driver.
- **Remote tier** adds JWT/nkey + TLS + leaf nodes + per-tenant accounts + a
  controller over the **same** handlers/subjects/schemas.

**Phasing** *(actionable spec: PLAN-18)*:
0. **Phase 0 — NATS foundation (hard prerequisite).** PLAN-13/14/17 have **zero
   code**; §4 and all NATS work depend on them. Implement PLAN-13 (+ PLAN-17
   amendments) then PLAN-14 first, at current versions.
1. **Extract the `Backend` interface** from today's `Runner`/`Registry`/proxy;
   keep the shell driver; fix the pause mislabel; `ErrUnsupported` stubs for
   Suspend/Resume/Snapshot. Pure refactor, no daemon yet.
2. **`aped` (local):** the network-less-executor + de-privileged-front-end
   architecture; embedded NATS + the `vmm` micro service; per-credential subject
   authz + per-VM cred minting; the policy engine (allowlists + audit); hardened
   systemd units. `ape` becomes a thin NATS client. Non-device Kata-QEMU
   workspaces, rootful.
3. **Device tier:** `containerdDriver` + VFIO orchestration (IOMMU-group checks,
   `vfio-pci` bind/rebind, baked `kata-qemu-gpu` handler, single cold-plug
   injection) + the GPU guest-image build/signing workstream + profile `devices:`.
   **Validate on a discrete-GPU box** (this dev box has Intel iGPU only — not
   testable here).
4. **Remote agent + controller + Firecracker tier** (platform repo): same
   `Backend` over NATS, scheduler consuming `Capabilities()`, multi-tenant
   accounts. Firecracker as the dense no-device tier (separate 1.7.x stack).

## 12. Open questions / risks
- **Schedule: the NATS foundation is unbuilt** (verified — no `nats` dep in
  `go.mod`; none of `internal/{natsconn,eventing,reporting,service,blobstore}`).
  Phase 0 gates everything.
- **Transport: DECIDED — embedded NATS everywhere.** Residual: (a) the
  network-less-executor + de-privileged-front-end split (done, §4); (b) the private
  host↔guest link is the **bridge gateway IP** baseline (depends on bridge
  networking; the **vsock↔NATS bridge** is future hardening, sequenced with the
  `--network none` + nft wall); (c) **per-VM-unique** short-`exp` creds; (d)
  **two accounts** (HOST_OPS + TELEMETRY) — DECIDED; `aped` holds a cred in each
  (the guest-facing front-end telemetry-only), so no cross-account export/import is
  needed.
- **NEX: DECIDED — borrow patterns, own the control plane.** Current NEX (0.4.1) is
  **no longer Firecracker-only** — runtime-agnostic, no built-in VM runtime, no
  VFIO/GPU/USB, no host↔guest transport, so it gives no reusable code for the
  passthrough or host↔guest work. What transfers: the NATS-micro API, the SVC/FEED
  split, node-minted telemetry-only workload creds, Xkey (remote), KV-resume,
  lameduck, auction (controller tier only). What must be **inverted**: NEX's
  node-trusts-nexlet model — our in-VM `ape` maps to NEX's *workload* tier, not its
  *agent* tier. "Adopt NEX's node via a Kata Nexlet" is explicitly rejected (§7).
- **`aped` = three drivers across two containerd versions** (shellDriver/nerdctl,
  containerdDriver/2.x-Kata, firecrackerDriver/1.7.x). Pin/verify version-coupling
  with the host containerd + Kata; `MemoryDenyWriteExecute=yes` may be incompatible
  with cgo/plugin paths — verify before enabling.
- **USB challenges LOCKED-4 on granularity.** Kata has no per-device `usb-host`;
  whole-xHCI-controller VFIO needs a dedicated controller alone in its IOMMU group.
  The `vendor:product` API is dropped; fine-grained USB would need a QEMU-cmdline
  escape that widens attack surface. **User decision needed** (accept coarser USB
  or budget the escape).
- **No discrete-GPU box.** Phase 3 and the #1 cold-plug unknown are unvalidatable
  on this box (Intel iGPU, no `intel_iommu=on`) and on CI (no nested virt); a
  self-hosted GPU host is a hard prerequisite, none confirmed available.
- **Device-VM QEMU de-privilege + `kata-NNN` uid handshake.** Running device QEMU
  rootless-VMM needs `aped` to chown `/dev/vfio/<group>` to the random `kata-NNN`
  uid the shim picks at create — no Kata hook pins it today (post-create race or a
  patch). Decide after discrete-GPU validation; fallback = device QEMU as root
  (QEMU `-sandbox` + the KVM boundary).
- **Suspend/Snapshot blocked upstream** on the Kata path (shim `Checkpoint`
  unimplemented; VMM socket Kata-owned/single-client). `From:` on the Kata tier
  maps only to factory templates. Decide whether to accept `ErrUnsupported` until a
  VMM-owning driver / the FC tier, or pursue an upstream Kata checkpoint/restore.
- **GPU guest-image version coupling** — NVIDIA modules signed against the pinned
  Kata guest kernel; a non-trivial, currently-unowned CI workstream.
- **Policy config** — start with a typed Go allowlist per caller (`aped
  policy.yaml`, the "real boundary"); consider OPA/Rego only if external policy
  authoring becomes a requirement.
- **Placement model (Phase 4):** NEX-style peer-to-peer auction vs a central
  scheduler reading `Capabilities()` — both keep the per-node API a pure executor;
  pick per operational preference before the controller phase.
- **`ape` self-exec inside workspaces** still runs as the guest user in the VM —
  unchanged; the VM is the boundary.
- **Host-side daemonless `ape sandbox` is retired (DECIDED).** `ape` is **always**
  a thin `aped` client — no unprivileged-`ape`-drives-containerd-directly path
  (that was the socket-≈-root smell). PLAN-16 stays the *record* and its pure
  layers are reused; only its daemonless runner path is superseded when PLAN-18
  lands. The `shellDriver` lives **inside `aped`** (non-device parity), never in
  `ape`.

## 13. Sources
Primary sources, with versions/dates (verified 2026-07-08). Target **current**
versions — nats-server 2.14.x, nats.go 1.52.x, jwt/v2 2.8.2, nkeys 0.4.16, Kata
3.32.0, containerd 2.3.2, nerdctl 2.3.4; **NEX's pins** (nats-server 2.12.6,
nats.go 1.49.0, jwt/v2 2.8.1) are **reference-only**.

- **Prior-art daemons & the socket-≈-root trap:** libvirt auth/polkit ACL; Docker
  AuthZ CVE-2024-41110 (docker.com advisory; fixed docker-ce 27.1.1, 2024-07-23)
  + its 2026 regression **CVE-2026-34040** (fixed Engine 29.3.1 / Desktop 4.66.1;
  CVSS 8.8); Lima **CVE-2026-53657** (fixed Lima v2.1.3; CVSS 8.2; guest-local
  privesc); Firecracker jailer; polkit/machined; systemd.exec(5)/socket(5) +
  systemd-analyze; auditd `audit.rules(7)`.
- **NATS:** embedding `nats-server`; `nats.go/micro` (ADR-32); subject-based auth
  (deny-wins + `allow_responses`); accounts; leaf nodes (`:7422`); the UDS
  discussion #7677 (closed); `jwt/v2` 2.8.2 + `nkeys` 0.4.16 + GHSA-mr45-rx8q-wcm9
  (**CVE-2023-46129**, fixed nkeys 0.4.6 / nats-server 2.10.4); **NEX**
  synadia-io/nex (0.4.1 tag + HEAD 2026-07-02; `internal/credentials/vendor.go`,
  `models/subjects.go`).
- **Kata/VFIO (3.32.0, 2026-06-22):** `configuration-qemu.toml.in` +
  `configuration-qemu-nvidia-gpu.toml.in` @ tag 3.32.0;
  `NVIDIA-GPU-passthrough-and-Kata.md` (standalone `ctr`) +
  `-Kata-QEMU.md` (k8s); `how-to-set-sandbox-config-kata.md`;
  `how-to-run-rootless-vmm.md`; the virtualization design doc; `src/runtime/Makefile`
  (`DEFENABLEANNOTATIONS`); `annotations.go`; issues #11687/#11671/#11125 (+PR
  #11150)/#10622/#9614/#835/#2938/#1533/#693; PR #12679; the Kata `Checkpoint`
  `ErrNotImplemented` path. containerd 2.3.2 CRI `config.md` + `v2/client`/`cio`/
  `oci` + `api/events`; nerdctl 2.3.4 command reference; kernel VFIO docs; ArchWiki
  PCI passthrough; NVIDIA GPU Operator (Kata) docs.
- **Firecracker/platform:** firecracker-containerd main `go.mod` (containerd
  v1.7.29) + PR to 1.7.33 + #759 (cross-host restore); QEMU QMP `snapshot-save`
  (6.0); stargz/eStargz snapshotter; CNCF CDI spec.

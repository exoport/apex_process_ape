---
created_at: 2026-07-08
status: open
kind: passthrough-recipe
tags:
  - sandbox
  - aped
  - kata
  - qemu
  - vfio
  - gpu
  - usb
  - passthrough
summary: >
  Deliverable **D2** of the `ape`/`aped` investigation: a validated, step-by-step
  recipe for GPU/USB **VFIO passthrough into Kata-QEMU microVMs** — the device
  tier's load-bearing risk. It is what `aped` must own on a bare box (the work the
  NVIDIA GPU Operator does in Kubernetes): host IOMMU + `vfio-pci` bind +
  IOMMU-group isolation check + the exact Kata annotation/handler set + a GPU
  guest image. Every Kata-specific step is marked **TESTED / PRIMARY-SOURCE /
  INFERRED**; **nothing here is hardware-tested** (this dev box has an Intel iGPU
  only), so the whole device tier needs a **discrete-GPU box** to validate
  end-to-end. Anchored to **Kata Containers 3.32.0** (2026-06-22), containerd
  2.3.2, nerdctl 2.3.4. Corrects the design doc §8 in several places (drop
  `enable_iommu` from the GPU set; the real cold-plug cause is the fixed 64-bit
  MMIO window, not the `pcie.0` restriction; "refuse mixed groups" → authorize
  group members + place them in one guest address space; the non-k8s bare-metal
  path is documented and *works*; `nerdctl`/`ctr` *can* emit the annotations).
  Findings adversarially verified.
origin:
  - 2026-07-08 — Track B of the multi-track investigation refining
    `development/research/ape-aped-split-20260707.md`; findings adversarially
    verified against Kata 3.32.0 primary sources. Feeds **PLAN-18 Phase 3**
    (device tier) — see `development/planning/plan-18_ape-aped-split.md` §D5.
  - This box (i7-8550U, Intel UHD 620 iGPU `8086:5917` / `i915`, no discrete GPU,
    no `intel_iommu=on` in `/proc/cmdline`, Kata/containerd not installed) can
    validate only host-inspection steps; GPU/USB passthrough is not testable here.
---

# D2 — Validated recipe: Kata-QEMU rootful GPU/USB VFIO passthrough (the `aped` device tier)

> ## ⚠ REQUIRES A DISCRETE-GPU BOX TO VALIDATE END-TO-END
> This recipe was researched against **primary sources** and validated **only for
> host-inspection steps** on the dev box (Intel UHD 620 iGPU, no discrete GPU,
> Kata/containerd not installed). **Every Kata-specific step is `[PRIMARY-SOURCE]`
> or `[INFERRED]`, never `[TESTED]`.** The unvalidated steps and the single
> most load-bearing unknown are listed in §K. Validate on a discrete-GPU host
> (NVIDIA/AMD alone in its IOMMU group, `intel_iommu=on iommu=pt`, Kata 3.32 +
> rootful containerd 2.x, `LimitMEMLOCK=infinity`) before finalizing the tier.

**Version anchor.** Kata Containers **3.32.0** (latest, 2026-06-22; QEMU is the
runtime-rs default hypervisor since 3.30) · containerd **2.3.2** · nerdctl
**2.3.4** · Linux kernel with `CONFIG_VFIO_DEVICE_CDEV=y` (iommufd backend). All
sources cited inline.

## This box's state — `[TESTED]`, for context on what a device host must look like

Read-only host inspection only (no host mutation was performed):

- **CPU** Intel i7-8550U; VT-x present (`vmx`, `ept`, `vpid`) → Intel VT-d path.
- **GPU** `00:02.0 Intel UHD Graphics 620 [8086:5917]`, driver `i915`. **No
  discrete GPU → GPU passthrough not testable here.**
- **IOMMU** `/sys/kernel/iommu_groups/` already holds **11 groups** even though
  `/proc/cmdline` has **no** `intel_iommu=on` (modern kernels enable Intel DMAR
  by default). `/etc/default/grub` has no IOMMU args.
- **USB** `00:14.0 xHCI [8086:9d2f]` sits in **IOMMU group 1 together with
  `00:14.2` Thermal subsystem [8086:9d31]** → **cannot be passed cleanly** (see
  §F). A concrete instance of the isolation rule in §C.
- `ulimit -l` = **8192 KiB** (default) — far below any VM RAM; must be raised (§A4).
- `CONFIG_VFIO=m`, `CONFIG_VFIO_PCI_CORE=m`, `CONFIG_VFIO_DEVICE_CDEV=y`,
  `CONFIG_VFIO_IOMMU_TYPE1=m`; `lsmod | grep vfio` empty (not loaded);
  `/dev/vfio/vfio` present, mode `0666`.

---

## A. Host prerequisites — all genuinely rootful; `aped` owns these

**A1. Enable IOMMU at boot. `[PRIMARY-SOURCE]`**
Kernel cmdline: Intel `intel_iommu=on iommu=pt`; AMD `amd_iommu=on iommu=pt`.
`iommu=pt` (pass-through) keeps unassigned devices on the host DMA fast path.
Verify `ls /sys/kernel/iommu_groups` is populated. On current kernels groups may
exist without the flag (as here), but set it explicitly for determinism.
→ https://kata-containers.github.io/kata-containers/use-cases/NVIDIA-GPU-passthrough-and-Kata-QEMU/

**A2. Load VFIO modules early, before the GPU's native driver. `[PRIMARY-SOURCE / INFERRED]`**
`vfio`, `vfio_pci`, `vfio_iommu_type1` must load in the initramfs **before**
`nouveau`/`nvidia`/`amdgpu` can claim the target. Where VFIO is a module (this
box: `=m`), add them to `/etc/modules-load.d/` + regenerate initramfs and either
blacklist the GPU's native driver or use `driver_override` (§B2). Current
Kata+QEMU uses the **iommufd** backend, so the host kernel needs the iommufd/cdev
path (`CONFIG_VFIO_DEVICE_CDEV=y`, present here).
→ kernel VFIO docs + ArchWiki PCI passthrough.

**A3. No host GPU driver bound to the target. `[PRIMARY-SOURCE]`**
For an NVIDIA GPU the host must have **no nvidia/nouveau** bound to the target
BDF. In k8s this is the GPU Operator's `nvidia-vfio-manager`; **on a bare box
`aped` does the bind itself** (§B2) — which is just the standard 3-line sysfs
sequence, not a controller to reimplement.
→ https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/deploy-kata-containers.html

**A4. `RLIMIT_MEMLOCK` ≥ VM RAM (effectively infinity). `[TESTED (limit) / PRIMARY-SOURCE (requirement)]`**
VFIO **pins all guest RAM** + device IO space. This box's default `ulimit -l` =
8192 KiB is unusable. Set `LimitMEMLOCK=infinity` on **both** the `aped` unit
**and** the `containerd` unit it drives; confirm inheritance to QEMU via
`/proc/<qemu-pid>/limits` (`systemctl show` can misreport). The shim/QEMU inherit
from containerd — `aped` does not parent QEMU, so it does not set QEMU's rlimit
directly.
→ libvirt devel thread on memlock-vs-VFIO; intel/gvt-linux#78.

---

## B. Bind the target BDF to `vfio-pci` — `aped`'s per-VM privileged setup

**B1. Resolve device + IOMMU group. `[PRIMARY-SOURCE]`**
```
readlink /sys/bus/pci/devices/0000:BB:DD.F/iommu_group   # → …/iommu_groups/<N>
ls      /sys/kernel/iommu_groups/<N>/devices/            # every device in the group
```

**B2. Bind via `driver_override` (preferred over a raw unbind). `[PRIMARY-SOURCE]`**
```
echo vfio-pci        > /sys/bus/pci/devices/0000:BB:DD.F/driver_override
echo 0000:BB:DD.F    > /sys/bus/pci/devices/0000:BB:DD.F/driver/unbind   # if bound
echo 0000:BB:DD.F    > /sys/bus/pci/drivers/vfio-pci/bind
```
Do this for **every** BDF the group requires (§C). After binding, `/dev/vfio/<N>`
appears. `vfio-pci` *binding itself needs no capability* — uid-0 writes the
`0200`/`0644` root-owned sysfs files as **owner** (relevant to `aped`'s cap set;
see the design doc §2/§3).

**B3. On destroy, rebind to the host driver** (clear `driver_override` +
`drivers_probe`) and **FLR-reset + scrub the device first** to prevent
cross-tenant state leakage (design doc §9 "VFIO rebind to host").
→ kernel VFIO / ArchWiki; group-node model confirmed by the `ctr --device
/dev/vfio/<N>` example in kata#11671.

**Bind lifecycle across reboots is an open decision** (record in PLAN-18): a
persistent initramfs/`driverctl` bind vs `aped` runtime unbind-at-create. A GPU
actively driving the host display **cannot** be runtime-unbound; and the runtime
path intersects the `ProtectKernelTunables=`/`/sys`-read-only caveat (design doc
§3 / Track C).

---

## C. IOMMU-group isolation safety — SECURITY-CRITICAL (design doc §2/§8)

The highest-value escalation target. A passed-through device drags its **entire**
IOMMU group into the guest.

**C1. Authorize the whole group; do NOT "refuse mixed groups." `[PRIMARY-SOURCE]`**
> *Correction to the design doc's "refuse groups containing devices you can't
> hand over" and to the initial Track-B framing.* A GPU co-grouped with its own
> **audio/USB-C function is the normal case** — the audio device is part of the
> GPU (a multi-function device) and must be passed **together**. Refusing every
> multi-device group would reject virtually every consumer/workstation NVIDIA GPU
> and any GPU behind a PCIe bridge.

`aped` must enumerate `/sys/kernel/iommu_groups/<N>/devices/` and **refuse a group
only if it contains a device the caller is not authorized for, or that the host
needs** (a shared bridge, the boot NIC, a storage controller). Default-deny; the
caller's device allowlist authorizes the group **as a unit**.

**C2. Passing the whole group is necessary but NOT sufficient. `[PRIMARY-SOURCE]`**
All same-group devices must land in **one guest PCIe address space**, or QEMU
fails **"group N used in multiple address spaces."** Achieve this by sizing
`pcie_root_port` to the function count, using `cold_plug_vfio=root-port`, and
**avoiding an in-guest vIOMMU for multi-function groups** (an in-guest vIOMMU
gives each same-group device its own guest AddressSpace, breaking VFIO group
granularity). This is what kata#10622 actually is — a **guest-topology config
problem on a normal GPU+audio group** (the reporter had already correctly bound
the whole, well-isolated group), **not** an unfixable ACS/BIOS wall.

> **Evidence-integrity note.** The line often quoted as being from kata#10622 —
> *"IOMMU group separation depends on hardware ACS support and BIOS
> configuration—not Kata configuration alone"* — **does not appear anywhere in
> that issue** (verified: 0 hits for `acs`/`separation`/`depends on` across the
> body + all 9 comments). It is a synthesized paraphrase; do not cite it as a
> primary quote. IOMMU-group *formation* genuinely is a hardware/PCIe-ACS/BIOS
> property (kernel VFIO docs), but that is the correct source, not #10622.

**C3. Isolation is hardware/ACS/BIOS/topology, not Kata config.** `aped` can only
enumerate and refuse; it logs the resolved group + all BDFs + guest-image digest
(design doc §2 defense #5; `auditd` on `/dev/vfio/*`).
→ kernel VFIO: https://docs.kernel.org/driver-api/vfio.html ·
  kata#10622: https://github.com/kata-containers/kata-containers/issues/10622 ·
  kata#2938 (group = 3 bridges + the 3D controller): https://github.com/kata-containers/kata-containers/issues/2938

---

## D. Expressing passthrough to Kata-QEMU

Two axes: **how the device reaches the OCI spec** (`--device` or CDI) and **how
Kata attaches it** (hot- vs cold-plug, set in config or a per-sandbox annotation).

### D1. Config knobs (verbatim from `configuration-qemu.toml.in` @ tag 3.32.0) `[PRIMARY-SOURCE]`

| Option | Default | Meaning |
|---|---|---|
| `vfio_mode` | `"guest-kernel"` (GPU path) | `guest-kernel` = the guest's **native** driver binds it (GPU→`nvidia.ko`, NIC→`ethX`). `vfio` = device appears as a `/dev/vfio` char dev **inside** the guest (guest runs its own VFIO/DPDK/nested-VM stack). |
| `hot_plug_vfio` | `"no-port"` | Attach after boot to a bridge/root/switch-port. |
| `cold_plug_vfio` | `"no-port"` | Attach at VM launch to a port. **`"root-port"` is the only supported NVIDIA GPU mode.** |
| `pcie_root_port` | `0` | Reserve N PCIe root ports (needed for large-BAR devices on q35; set ≥ #GPUs). |
| `enable_iommu` | `false` | Guest **vIOMMU** (adds `intel_iommu=on,iommu=pt` to the **guest** cmdline). Needed only for `vfio_mode="vfio"`; **NOT** for guest-kernel GPU (§D3). |
| `machine_type` | `q35` (x86_64) | q35 required for large PCI BARs. |
| `guest_hook_path` | `""` | In-guest OCI hook binaries (NVIDIA uses this for GPU setup). |

→ https://raw.githubusercontent.com/kata-containers/kata-containers/3.32.0/src/runtime/config/configuration-qemu.toml.in

### D2. Cold-plug vs hot-plug verdict `[PRIMARY-SOURCE]`

- **Generic PCI (NIC, TPU, non-GPU):** hot-plug via `--device /dev/vfio/<N>` often
  works.
- **NVIDIA GPU (large BAR):** **cold-plug mandatory** —
  `cold_plug_vfio="root-port"`, `hot_plug_vfio="no-port"`. Kata's own doc:
  *"Cold-plug is by design the only supported mode for NVIDIA GPU passthrough of
  the NVIDIA reference stack."*

> **Correction to the design doc's stated *reason* for cold-plug.** Hot-plug is
> not unreliable "because a PCIe device cannot be hot-plugged onto q35's `pcie.0`
> root bus." That `pcie.0` restriction is **generic to every PCIe device** and is
> **solved by adding a root port**. The **GPU-specific** failure persists *even
> when hot-plugging onto a root port*: the root port's **64-bit prefetchable MMIO
> window is fixed at PCI enumeration and cannot grow** to fit a multi-GB BAR
> (e.g. a V100's 32 GB BAR → `BAR 1: no space for [mem size 0x800000000 64bit
> pref]`), compounded by a kata-agent PCI-rescan race. Cold-plug presents the
> device **at boot** so guest firmware sizes the bridge window correctly.
> Therefore `aped` must, beyond choosing cold-plug, also (i) set
> `pcie_root_port ≥ #GPUs` and (ii) **size the guest 64-bit MMIO (`pci-hole64`)
> window** to the largest prefetchable BAR.
> → kata#835 (V100 BAR): https://github.com/kata-containers/kata-containers/issues/835 ·
>   kata/runtime#2664 · LXD#15872 / Proxmox `pci-hole64` threads.

### D3. The exact annotation set for a GPU cold-plug (guest-kernel mode) `[PRIMARY-SOURCE keys / INFERRED values]`

> **`enable_iommu` is `false` for GPU passthrough.** *(Correction to design doc
> §8, which listed `enable_iommu=true`.)* Kata's own
> `configuration-qemu-nvidia-gpu.toml.in` @ 3.32.0 ships **`enable_iommu =
> false`** with `cold_plug_vfio=root-port`, `hot_plug_vfio=no-port`; the native
> `nvidia.ko` binds the GPU in **guest-kernel** mode. `enable_iommu=true` (guest
> vIOMMU) belongs to the separate **`vfio_mode="vfio"`** tier — where the *guest*
> runs its own VFIO drivers (DPDK poll-mode, nested passthrough) — which is **not**
> the typical GPU/USB workspace. (`enable_iommu` and `vfio_mode` are independent
> knobs; the dependency is design guidance, not an enforced coupling.)
> → https://raw.githubusercontent.com/kata-containers/kata-containers/3.32.0/src/runtime/config/configuration-qemu-nvidia-gpu.toml.in

Prefer to **bake these into a per-tier handler config** (§D6), not inject them as
caller annotations. Where an annotation *is* used, the OCI-spec `annotations`
block looks like:

```json
"annotations": {
  "io.katacontainers.config.hypervisor.cold_plug_vfio": "root-port",
  "io.katacontainers.config.hypervisor.hot_plug_vfio":  "no-port",
  "io.katacontainers.config.hypervisor.pcie_root_port": "1",
  "io.katacontainers.config.hypervisor.machine_type":   "q35",
  "io.katacontainers.config.hypervisor.default_vcpus":  "4",
  "io.katacontainers.config.hypervisor.default_memory": "8192",
  "cdi.k8s.io/vfio0": "<vendor>.com/pgpu=0"
}
```
plus a `linux.devices` entry (or the CDI injection) for `/dev/vfio/<N>`.

> **Two ways to name the device — do not use both.** (a) **Plain node:** put
> `/dev/vfio/<N>` in `linux.devices` (what `--device` does); Kata cold-plugs it
> because `cold_plug_vfio` is set. (b) **CDI:** register a host CDI spec (e.g.
> `nvidia-ctk cdi generate`) and reference it via `cdi.k8s.io/vfioN`. Passing
> **both** `io.katacontainers.*` **and** `cdi.k8s.io/vfio*` for the same device
> triggers **double injection** (kata#11125; fixed upstream by PR #11150, still a
> footgun). The **inner** `cdi.k8s.io/vfio<N>` annotations are
> **runtime-generated** by Kata (`annotateContainerWithVFIOMetadata`), consumed
> by the kata-agent — **no client emits them.**

### D4. Equivalent `ctr run` invocation (shellDriver fallback) `[PRIMARY-SOURCE flags / INFERRED result]`

```
sudo ctr run --rm -t \
  --runtime io.containerd.kata-qemu.v2 \
  --device /dev/vfio/<N> \
  --annotation io.katacontainers.config.hypervisor.cold_plug_vfio=root-port \
  --annotation io.katacontainers.config.hypervisor.hot_plug_vfio=no-port \
  --annotation io.katacontainers.config.hypervisor.pcie_root_port=1 \
  --annotation io.katacontainers.config.hypervisor.default_vcpus=4 \
  --annotation io.katacontainers.config.hypervisor.default_memory=8192 \
  <gpu-guest-image> gpu-vm bash
```
`ctr` supports `--annotation key=val` and `--device`. **These annotations are
honored only if Kata's `enable_annotations` allowlist lists those keys** (§D5) —
so prefer baking them into the handler config (§D6) and setting
`enable_annotations=[]`.
→ ctr(8); `--device /dev/vfio/<N>` example: https://github.com/kata-containers/kata-containers/issues/11671

### D5. What containerd config is actually required `[PRIMARY-SOURCE]`

> **Correction to design doc §8.** The CRI knobs
> `pod_annotations`/`container_annotations`/`privileged_without_host_devices` are
> **CRI-plugin (Kubernetes) filters and are INERT on `aped`'s direct
> `ctr`/Go-client path** — containerd's docs: *"the
> `[plugins."io.containerd.grpc.v1.cri"]` section is … not recognized by other
> containerd clients such as ctr, nerdctl, and Docker/Moby."*

- **`aped`'s path (direct containerd, non-k8s):** the only annotation gate is
  **Kata's own `enable_annotations`** in `configuration.toml` — but it is the
  **primary, not the sole** gate: (a) path-type hypervisor annotations
  additionally must pass a `valid_*_paths` glob value-check; (b) CDI
  (`cdi.k8s.io/*`) is entirely **outside** `enable_annotations` (a containerd
  concern; on the direct path via `ctr --device <cdi-name>`). Ship a dedicated
  `configuration-qemu-gpu.toml` with **`enable_annotations=[]`** (everything baked
  — §D6); this *is* the design doc §2 constrained-vocabulary control.
- **Only for a future k8s/CRI deployment:** under
  `[plugins."io.containerd.grpc.v1.cri"…runtimes.kata-qemu-gpu]` set
  `pod_annotations = ["io.katacontainers.*","cdi.k8s.io/vfio*"]`,
  `privileged_without_host_devices = true`, `runtime_type =
  "io.containerd.kata-qemu.v2"`, `ConfigPath = …/configuration-qemu-gpu.toml`.
→ containerd CRI config: https://github.com/containerd/containerd/blob/main/docs/cri/config.md ·
  Kata sandbox-config how-to: https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-set-sandbox-config-kata.md

### D6. Recommended `aped` design — a baked per-tier handler, not caller annotations `[PRIMARY-SOURCE mechanism / INFERRED end-to-end]`

Ship an `aped`-owned `kata-qemu-gpu` containerd runtime handler (mirroring
upstream `kata-qemu-nvidia-gpu`) whose `ConfigPath` **bakes** `cold_plug_vfio=
root-port`, `hot_plug_vfio=no-port`, `pcie_root_port`, the GPU guest kernel+image,
and memory — **never caller-supplied.** `aped` selects the handler by tier; the
caller supplies no raw annotations.

Two precisions that matter for the implementation:

1. **`aped` must ACTIVELY set `enable_annotations=[]`.** Kata's **default is NOT
   minimal** — `["enable_iommu","kernel_params","kernel_verity_params"]`,
   including the powerful **`kernel_params`** lever. Inheriting the default would
   leave that lever open. Lock **both** gates: do not broadly allowlist containerd
   `pod_annotations` **and** set Kata `enable_annotations=[]`.
2. **Baking a handler does not eliminate all per-VM annotations.** The plug-mode,
   kernel/image, root ports and memory are baked; the **device identity** is still
   selected per-create as **one CDI/VFIO device request that `aped` itself mints**
   from its device allowlist (never accepted from the untrusted caller). Because
   that is a containerd/CRI annotation, not an `io.katacontainers.*` hypervisor
   annotation, it is **not** gated by `enable_annotations`, so an empty
   `enable_annotations` does not block passthrough.

→ handler pattern + `DEFENABLEANNOTATIONS`:
  https://raw.githubusercontent.com/kata-containers/kata-containers/main/src/runtime/Makefile ·
  annotation constants:
  https://raw.githubusercontent.com/kata-containers/kata-containers/main/src/runtime/virtcontainers/pkg/annotations/annotations.go

---

## E. `nerdctl` claim test — the design doc §5/§8 claim is WRONG `[PRIMARY-SOURCE]`

The design doc states *"nerdctl doesn't emit these [annotations]."* **False for
current nerdctl.** `nerdctl run --annotation k=v` is documented as *"Add an
annotation to the container (passed through to the OCI runtime)"* — no key
restriction; the flag predates v2.0. Proof it reaches Kata specifically: `nerdctl
run --runtime io.containerd.kata.v2 --annotation
io.katacontainers.config.hypervisor.default_vcpus=2.0 …` reaches the shim and
errors *"annotation … is not enabled"* (kata#1533) — dispositive that the
nerdctl-emitted annotation lands in the OCI spec and reaches the shim.
`internal/sandbox/kata.go:99-101` already appends `--label ape.managed=true`;
`--annotation k=v` is the identical arg-append.

**So the annotation capability does NOT force the containerd Go client.** The
genuine reasons `aped` uses the Go client on the device tier are:
1. a **programmatic task/event stream** (`TaskExit`/`TaskPaused`/`TaskOOM` via
   `client.EventService().Subscribe`) for a real daemon state machine;
2. **PTY/stdio fidelity + resize** over NATS (`Task.Exec` + `cio.WithTerminal` +
   `ResizePty` + `Process.Wait`→`ExitStatus`);
3. **owning the OCI spec as a typed, audited object** (design doc §2 "authorize
   the fully-decoded request") instead of assembling/parsing CLI text.

→ nerdctl command reference: https://github.com/containerd/nerdctl/blob/main/docs/command-reference.md ·
  kata#1533: https://github.com/kata-containers/kata-containers/issues/1533

---

## F. USB passthrough — per-device `usb-host`, NOT VFIO `[PRIMARY-SOURCE / TESTED (this box's group)]`

> **DECISION (2026-07-08).** USB does **not** use VFIO. Whole-controller VFIO is
> **rejected**; USB uses per-device QEMU `usb-host`, synthesised only by `aped`.

- **Whole-controller VFIO is rejected.** Kata's *VFIO* USB path is binding the
  **entire xHCI PCI controller** — coarse (all ports at once) and
  IOMMU-group-constrained. It would hand the guest the **system keyboard/mouse**,
  and on this box it is impossible anyway (xHCI `00:14.0` shares IOMMU **group 1**
  with thermal `00:14.2`, TESTED). Do not use it.
- **Use per-device `usb-host`.** Pass a **single device by `vendor:product`** (an
  ESP-32, a barcode reader, a serial dongle) with QEMU
  `-device usb-host,vendorid=0x…,productid=0x…` attached to an emulated guest xHCI
  (`-device qemu-xhci`). This is device-level forwarding **mediated by QEMU + the
  host USB stack** — **no raw DMA** (unlike VFIO-PCI), **no IOMMU-group
  constraint**, and **no keyboard/mouse leak**. Fine for low-bandwidth peripherals.
- **Only `aped` builds the `usb-host` string**, from a **per-caller
  `vendor:product` allowlist**. The caller sends the typed
  `Device{USB:"vendor:product"}` and **never** raw QEMU args — so the attack
  surface is just "which USB IDs may this caller pass," the same default-deny model
  as PCI BDFs (design doc §2).
- **Cost/risk — Kata does not expose `usb-host` today `[PRIMARY-SOURCE]`.** Kata's
  config/annotations have **no `usb-host` knob** (only VFIO-PCI). So `aped` must add
  it: preferably a **small upstream Kata contribution** (a USB-device annotation
  that emits `-device usb-host` + an xHCI), or a narrow `aped`-controlled
  qemu-device injection. **Until that lands, USB passthrough is unavailable.**

→ Kata config template (no `usb-host` option): https://raw.githubusercontent.com/kata-containers/kata-containers/3.32.0/src/runtime/config/configuration-qemu.toml.in ·
  QEMU `usb-host`: https://qemu-project.gitlab.io/qemu/system/devices/usb.html · TESTED host IOMMU group (this box).

---

## G. Guest-image delta `[PRIMARY-SOURCE]`

| Case | Guest needs |
|---|---|
| **NVIDIA GPU (guest-kernel mode)** | A separate, larger rootfs/initrd: NVIDIA modules **built + signed against the pinned Kata guest kernel**, NVRC init, NVIDIA userspace libs. The stock Kata image has none. In k8s the NVIDIA **Kata Manager** builds it; **`aped` must supply/build a GPU guest image** (possibly a custom guest kernel). |
| **Generic in-guest VFIO (`vfio_mode="vfio"`)** | Guest kernel with `vfio`, `vfio_pci`, `vfio_iommu_type1` (+ `vfio_virqfd` on older kernels) + a **vIOMMU** (`enable_iommu=true`). |
| **Generic guest-kernel device (NIC)** | The native driver in the guest; if not built-in, load via the `io.katacontainers.config.agent.kernel_modules` annotation. |

**Implication for `images/ape-sandbox/` (PLAN-16 D6):** the device tier = a
**second, larger guest image** (and possibly a custom kernel), separate from the
base workspace image — a non-trivial CI workstream that **owns the
host-Kata↔guest-kernel version coupling and needs an owner** before Phase 3.
→ https://kata-containers.github.io/kata-containers/use-cases/NVIDIA-GPU-passthrough-and-Kata-QEMU/ · kata#693

---

## H. Failure modes & open issues in the 3.3x line `[PRIMARY-SOURCE]`

| Symptom | Root cause / status | Source |
|---|---|---|
| Non-k8s `ctr` GPU: *"failed to inject devices after CDI timeout of 100s"* | **NOT a 3.32 bug.** kata#11671 is a **CLOSED question on Kata 3.19.1**; root cause was a hand-built initrd/rootfs missing the NVIDIA/CDI packages; maintainer advised using prebuilt artifacts + default config; reporter's final comment: *"I rolled back to the default configuration toml. Success!"* → the bare-metal `ctr` path **works**. | https://github.com/kata-containers/kata-containers/issues/11671 |
| CDI injects the device twice (sandbox + container) | Both `io.katacontainers.*` and `cdi.k8s.io/vfio*` passed for one device. **CLOSED**, fixed by PR #11150; still a footgun (§D3). The same issue contains a **working standalone `nerdctl … nvidia-smi` demo** on an H800. | https://github.com/kata-containers/kata-containers/issues/11125 · PR #11150 |
| *"group N used in multiple address spaces"* | Same-group functions placed in separate guest address spaces (§C2) — a guest-topology **config** problem, not ACS/BIOS. | https://github.com/kata-containers/kata-containers/issues/10622 |
| `vfio_mode="guest-kernel"` StartContainer: *"Unable to translate host PCI address"* | `vfio-pci-gk` device skipped in the agent pcimap. Guest-kernel mode is fragile. | https://github.com/kata-containers/kata-containers/issues/9614 |
| *"Bus 'pcie.0' does not support hotplugging"* | Large-BAR PCIe hot-plug onto q35 root bus; use cold-plug + a root port (§D2). | config comments + kata/runtime#2664 |
| CLH: *"0000:00:05.0 has no IOMMU group"* | CLH GPU support historically **unimplemented** (now landing — §I). QEMU works. | https://github.com/kata-containers/kata-containers/issues/11687 |

**Reliability grade (current line):** generic PCI VFIO on QEMU = **usable**;
NVIDIA GPU on QEMU (both the **standalone `ctr`/`nerdctl`** path and the k8s
GPU-Operator/CDI flow) = **documented + maintained**, with the non-k8s
single-phase cold-plug still to be validated end-to-end (§K); guest-kernel mode =
**fragile** (#9614); USB = **per-device `usb-host`** (needs a Kata mechanism — not
exposed today; whole-controller VFIO rejected, §F); **CLH GPU = immature/just
landing** (§I).

---

## I. Cloud-Hypervisor vs QEMU verdict `[PRIMARY-SOURCE]`

**QEMU for the device tier** — but the design doc's wording should be corrected:

- Do **not** say "QEMU is the only VFIO backend": Kata's own design doc marks
  **QEMU, Cloud-Hypervisor, and Dragonball** VFIO-capable (only Firecracker is
  not). QEMU is the **most mature** backend and the **only NVIDIA-reference** GPU
  path (`kata-qemu-nvidia-gpu`, `cold_plug_vfio=root-port`).
- Do **not** say "CLH GPU is broken": it was historically **unimplemented** (not a
  regression) and is now **actively landing** — PR #12679 *"clh: Add VFIO device
  cold-plug support"* merged 2026-03-27; further CLH VFIO work in 3.32.0 (#13103,
  #13196, #13160). CLH already supports **non-GPU** VFIO (SR-IOV NICs, etc.).

**Net:** keep `kata-clh` **out of the GPU device tier** as a **current-maturity**
decision (not a permanent architectural limit). `aped` hard-rejects `devices:` on
any runtime ≠ `kata-qemu` (and on Firecracker). Cite the #11687 maintainer
statement + the Kata NVIDIA GPU doc + the VFIO capability table — **not** the
runtime-rs-scoped 3.30 default-hypervisor note.
→ kata#11687 (maintainer: *"I haven't done any work on CLH to enable proper GPU
support"*) · PR #12679 · Kata virtualization design doc.

---

## J. Net — what `aped` must own for the device tier (synthesis)

1. **Host:** cmdline IOMMU (§A1), early VFIO modules (§A2), `LimitMEMLOCK=infinity`
   on `aped` **and** containerd (§A4), no host GPU driver (§A3).
2. **Per-VM privileged setup:** resolve BDF→group; **enumerate + authorize the
   whole group against the caller allowlist, refusing only unauthorized/host-needed
   members** (§C1); bind **every** required group member to `vfio-pci` (rebind +
   FLR-reset on destroy — §B); if de-privileging QEMU via rootless-VMM, chown
   `/dev/vfio/<group>` to the per-VM `kata-NNN` uid (design doc §3; open uid-handshake
   risk).
3. **Build the OCI sandbox spec** (Go client): the device (`/dev/vfio/<N>` **or** a
   CDI request `aped` mints — not both), a **baked per-tier `kata-qemu-gpu`
   handler** with `cold_plug_vfio=root-port` + GPU guest image (§D6),
   `enable_annotations=[]`, `pcie_root_port ≥ #GPUs`, a sized `pci-hole64` window
   (§D2); inject the VFIO device **exactly once** at sandbox creation.
4. **Supply a GPU guest image** (NVIDIA modules signed vs the Kata guest kernel) —
   a real image-build workstream (§G).
5. **Audit** the resolved group/BDFs/image-digest; `auditd` on `/dev/vfio/*`,
   `/dev/kvm` (design doc §2 defense #5 / Track C).
6. **Reject:** CLH+devices, Firecracker+devices, unauthorized/host-needed group
   members, whole-controller USB VFIO (use per-device `usb-host` instead — §F).

---

## K. UNVALIDATED here — needs a discrete-GPU box

- **All of §A2–A4, §B, §D2–D6, §E-result, §G** as they concern a *real* GPU — no
  discrete GPU on this box.
- **⚑ The #1 load-bearing unknown:** does Kata cold-plug a VFIO device from a
  **plain `--device /dev/vfio/<N>` + `cold_plug_vfio="root-port"` in the non-k8s
  single-phase (`ctr`/Go-client) case**, or does it strictly require the k8s CDI +
  Pod-Resources two-phase discovery? **`[INFERRED]`** likely yes (the runtime's
  `coldOrHotPlugVFIO()` marks a device `ColdPlug=true` from the OCI `DeviceInfos`
  with no Pod-Resources-API call), but this must be **validated on a discrete-GPU
  box before finalizing the device tier.**
- Whether `enable_iommu=false` suffices for guest-kernel-mode GPU end-to-end.
- Per-device USB via QEMU `usb-host` (needs the Kata `usb-host` mechanism `aped`
  must add — §F; and a spare host USB device to forward).
- iommufd-backend behavior under Kata on the target kernel.

**Validation box spec:** a discrete GPU (ideally NVIDIA) alone in its IOMMU group
+ a spare USB controller alone in its group; `intel_iommu=on iommu=pt`; Kata 3.32
+ rootful containerd 2.x; `LimitMEMLOCK=infinity`. Run the §D4 `ctr` invocation
first (fastest signal), then the Go-client path.

---

## Scope-out (Operator/CDI/k8s-centric — separately budgeted, not this tier)

SEV-SNP/TDX **confidential-computing GPU**, **driver-in-guest UVM/NVRC**
attestation, **multi-GPU NVSwitch**. These are GPU-Operator/CDI/Kubernetes-centric
and are **not** reachable via the bare-metal `ctr` path; they are out of scope for
PLAN-18's device tier unless separately budgeted.

## Sources (primary, with versions)

Kata Containers **3.32.0** (2026-06-22): `configuration-qemu.toml.in` +
`configuration-qemu-nvidia-gpu.toml.in` @ tag 3.32.0; `NVIDIA-GPU-passthrough-and-Kata.md`
(standalone `ctr`) + `NVIDIA-GPU-passthrough-and-Kata-QEMU.md` (k8s);
`how-to-set-sandbox-config-kata.md`; the virtualization design doc;
`src/runtime/Makefile` (`DEFENABLEANNOTATIONS`); `annotations.go`; issues
#11687/#11671/#11125 (+PR #11150)/#10622/#9614/#835/#693/#2938/#1533; PR #12679. ·
containerd **2.3.2** CRI `config.md`. · nerdctl **2.3.4** command reference. ·
NVIDIA GPU Operator (Kata) docs. · Linux kernel VFIO docs; ArchWiki PCI
passthrough. · Host inspection of this box (read-only, 2026-07-08).

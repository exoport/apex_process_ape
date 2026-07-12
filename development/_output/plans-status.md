# Plans status — implemented vs pending (2026-07-12)

Snapshot of every plan's real implementation state, cross-checked against the
code on `feat/plan-18-phase2-aped` (72 commits ahead of `main`/v0.0.41; the
branch work targets CHANGELOG **v0.0.42**, unreleased). Authoritative per-plan
`status:` fields and the index table in
[`../planning/index.md`](../planning/index.md) were updated to match.

Legend: **done** = all deliverables coded + tested (may be released or on the
branch); **partially-implemented** = some deliverables shipped, others pending;
**proposed** = not started.

---

## 1. Not implemented yet (proposed — zero code)

### PLAN-12 — `ape command` (prompt/handoff-driven claude session)
Entire plan not started. Absent in the code: the `ape command` cobra verb; the
`--prompt`/`--agent`/`--model`/`--workflow`/`--ultracode`/`--idle-timeout`
surface; the `sessiondriver` refactor; `_output/ape/commands/<id>/` records + the
rollup `Commands` bucket + `ape costs` row; exit-code 4. The `ape service`
`command.run` endpoint is registered but rejects with `ErrKindUnavailable`
(`internal/service/spawn.go`).
- Note: only the **handoff-by-reference** idea shipped, and on **`ape task
  --handoff`** (PLAN-11) — not as `ape command`. PLAN-12's skill-less, commit-less
  session with `--workflow`/`--ultracode` does not exist.

### PLAN-15 — `ape script` (yaegi-interpreted orchestration scripts)
Entire plan not started. No `yaegi` dependency (absent from `go.mod`/`go.sum`), no
`apescript` package, no `ape script` verb, no stdin mode, no `--sandbox`
interpreter mode. The `ape service` `script.run` endpoint is registered but
rejects with `ErrKindUnavailable`.

---

## 2. Partially-implemented plans — pending items

### PLAN-14 — `ape service` (NATS-micro job daemon)
Core daemon is **done + tested**: `micro.AddService`, the 9-endpoint group, the
project allowlist (`_apex/service.yaml`), keyed-exclusivity admission, job spawn
(typed argv, process group, `APE_JOB_ID` + NATS env), lifecycle events, and
graceful drain — `pipeline.run` and `task.run` fully execute. Pending:
- **`command.run` executes nothing** — returns `VALIDATION`; blocked on PLAN-12
  (`ape command`).
- **`script.run` executes nothing** — blocked on PLAN-15 (`ape script`); its
  security gates (`allow_script_source`, `force_script_sandbox`) are parsed +
  validated but unenforced because there is no script-job path.
- **`docs/reference/service-api.md` not written** (the how-to
  `docs/how-to/run-ape-as-a-service.md` exists; the endpoint-contract reference
  does not).
- (stretch, uncommitted) `last_event_at` on `job.status` — not implemented.

### PLAN-16 — Kata VM workspaces (Phase 1 of the APEX Process Platform)
All in-repo Phase-1 **code** deliverables (D1–D8 + the reuse layers) are merged:
runner/registry, `~/.claude` composition, profile, CONNECT egress proxy,
git-credential composition, SSH access, and the `ape doctor` checks. Pending **in
this repo**:
- **Official `ape-sandbox` OCI image not built/published** — `sandbox.DefaultImage`
  is still the `ghcr.io/exoport/ape-sandbox:v0` placeholder tag (the `Dockerfile`
  exists but was never built/pushed; needs a container toolchain).
- **Never live-validated** — the Tier-2 (KVM+containerd+Kata) and Tier-3 manual
  checklists were not run; the integration tests are build-gated and skipped.
- **CLI/runner narrative superseded by PLAN-18** — the doc still describes a
  daemonless `ape sandbox` runner with `pause|resume`; the code is now an
  `ape`(client)/`aped`(daemon) split with `freeze|unfreeze|suspend` + `inspect`.
  (Doc-accuracy follow-up, not a code gap — the pure layers survive and are reused
  by PLAN-18.)

Explicitly **out of scope for this repo** (deferred to the separate
`apex_process_platform` repo): Phase 2 (in-VM `ape` NATS worker), Phase 3 (Netbird
two-overlay networking), Phase 4 (preview/demo/staging). The **device tier**
(GPU/USB VFIO) is deferred and now tracked under PLAN-18 (below).

### PLAN-18 — `ape` + `aped` split (rootful Kata-QEMU VM daemon)
Phases 0–2 are **done + live-validated (2026-07-11/12)**, plus the non-device
`containerdDriver` (Phase 3): the NATS foundation (PLAN-13/14/17), the `Backend`
refactor, the two-process daemon (root executor + de-privileged NATS front over
the SO_PEERCRED priv socket), the `ape.vmm` contract, per-credential authz +
per-VM cred minting, the default-deny policy + audit forwarding, the hardened
systemd units, and interactive exec/attach streaming all run end-to-end through
the deployed hardened units. Pending:

- **Phase 3 — device tier (VFIO/GPU) — BLOCKED on a discrete-GPU box** (dev box is
  Intel iGPU only):
  - VFIO orchestration (D5): `vfio-pci` bind, IOMMU-group enumeration + isolation
    check, a baked per-tier `kata-qemu-gpu` handler, single-injection cold-plug,
    and destroy → rebind → device-reset.
  - GPU guest-image build/signing (D5): NVIDIA modules signed against the pinned
    Kata guest kernel + NVRC.
  - Profile `devices:` (whole-IOMMU-group PCI; per-device USB via QEMU `usb-host`,
    aped-synthesised from a vendor:product allowlist).
- **Phase 4 — remote agent + controller + Firecracker tier** (platform-repo work,
  not started):
  - Leaf-node topology to the company hub; per-tenant accounts; same
    `Backend`/subjects/schemas (D8).
  - Controller: scheduling/placement from `Capabilities()`, drain, image
    distribution, overlays (scheduling stays out of the per-node API).
  - Firecracker dense/no-device tier behind a third `firecrackerDriver` (D8).

---

## 3. Minor / optional follow-ups on otherwise-done plans

Both follow-ups were closed on `feat/plan-18-phase2-aped` (v0.0.42) — nothing
open here:

- **PLAN-10** (done): the optional `cost.NewTailer` dead-code deletion shipped —
  the unused live-file poller was deleted; `AssistantLine`, the one symbol
  `scanTurns` still parses, was relocated to `internal/cost/line.go`.
- **PLAN-17** (done): the `APE_SESSION_ID` design-vs-behaviour mismatch was
  reconciled in the plan doc (D4 refinement) and the report-from-a-session
  how-to — the runner intentionally exports only the NATS env and lets the
  child's session auto-resolve; ape can't know the child's session id up front.

---

## 4. Done — for reference

- **Released (≤ v0.0.41):** PLAN-1 … PLAN-9, PLAN-11.
- **Feature-complete on the branch (v0.0.42, unreleased):** PLAN-10, PLAN-13,
  PLAN-17; PLAN-18 Phases 0–2 + the non-device containerd driver; PLAN-16 Phase-1
  code (image build + live validation pending — see §2).

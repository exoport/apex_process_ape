# Planning Index

| ID      | Title                                                   | Status                | Created    |
| ------- | ------------------------------------------------------- | --------------------- | ---------- |
| PLAN-25 | Project-data commands — config resolution, registries, stories, memory, deferred work, Python retirement | implemented | 2026-08-21 |
| PLAN-24 | Local sandbox polish — port-forward, reporting, capacity, agent, reaper | implemented | 2026-08-05 |
| PLAN-23 | Runtime `ape` delivery into sandbox workspaces           | done                  | 2026-07-26 |
| PLAN-22 | Sandbox toolchain / devcontainer model + lifecycle      | partially-implemented | 2026-07-23 |
| PLAN-21 | Sandbox network egress (allowlisted, deny-by-default)   | done                  | 2026-07-23 |
| PLAN-20 | Sandbox mounts (general model) + framework delivery     | done                  | 2026-07-23 |
| PLAN-19 | Activity-aware step timeout — progress anchor + hard cap | done                  | 2026-07-14 |
| PLAN-18 | `ape` + `aped` split — rootful Kata-QEMU VM daemon       | partially-implemented | 2026-07-08 |
| PLAN-17 | Reporting CLI — event/log/metrics/transcript + identity | done                  | 2026-07-02 |
| PLAN-16 | Kata VM workspaces (local dev) — Platform Phase 1       | partially-implemented | 2026-07-02 |
| PLAN-15 | `ape script` — yaegi orchestration scripts              | done                  | 2026-07-02 |
| PLAN-14 | `ape service` — NATS micro job daemon                   | done                  | 2026-07-02 |
| PLAN-13 | NATS progress events + transcript blobs                 | done                  | 2026-07-02 |
| PLAN-12 | `ape prompt` — prompt/handoff claude session            | done                  | 2026-07-02 |
| PLAN-11 | `ape task` — single-skill runs without YAML             | done                  | 2026-07-02 |
| PLAN-10 | Telemetry v2 — per-model cost, timestamps, subagents    | done                  | 2026-07-02 |
| PLAN-9  | CLI/docs hygiene + PTY-only consolidation               | implemented           | 2026-07-02 |
| PLAN-8  | Migrate tmux → in-process PTY                           | done                  | 2026-05-22 |
| PLAN-7  | Unified pipeline TUI (interactive ≡ programmatic)       | done                  | 2026-05-21 |
| PLAN-6  | Interactive pipeline exec + orthogonal UI/exec modes    | done                  | 2026-05-19 |
| PLAN-5  | `ape chat` + `ape pipeline` web mode                    | done                  | 2026-05-17 |
| PLAN-4  | Per-step boundary commits                               | done                  | 2026-05-11 |
| PLAN-3  | Pipeline run manifest + per-step metrics                | done                  | 2026-05-11 |
| PLAN-2  | Pipeline UX follow-ups (v0.0.7 carry-out)               | done                  | 2026-05-10 |
| PLAN-1  | Pipeline UX and framework setup separation              | done                  | 2026-05-10 |

> **Status note (2026-07-27, v0.0.50):** the whole sandbox wave has landed on `main` and is
> **live-validated on node `mmq4`** — PLAN-20 (mounts + framework delivery), PLAN-21
> (allowlisted egress), PLAN-22 (toolchain + lifecycle, minus the deliberately-unbuilt idle
> reaper), PLAN-23 (runtime `ape` delivery). PLAN-16/18 remain
> `partially-implemented` for one reason only: the **device (GPU/USB VFIO), overlay
> (Netbird) and fleet/controller tiers**, which need hardware or a second node that does not
> exist yet. What is pending, deduplicated across plans, is inventoried in
> `_output/2026-07-26-plan-review-pending.md`.
>
> **Amended 2026-08-05 (PLAN-24).** "Needs hardware or a second node" was too broad. The
> pending work splits by *what a single host can do*, and the local half is now **PLAN-24**
> in this repo:
>
> - **PLAN-16 Phase 2's local half → PLAN-24 D5–D7.** The guest→host transport, the
>   `ape sandbox-agent` heartbeat and an honest idle reaper are all one-host work. Two of
>   Phase 2's assumptions were superseded: the bridge-IP NATS listener (PLAN-24 D5 tunnels
>   through the existing CONNECT proxy to a loopback NATS in the same process — no new
>   listener, no new firewall hole) and a separate agent binary (PLAN-23 already mounts
>   `ape` into every workspace, so the agent is a subcommand). PLAN-18 D6's
>   entrypoint launch is **reversed** — `aped` execs it, keeping the work in one repo.
> - **PLAN-22 D5b (reaper) and D7 (devcontainer how-to) → PLAN-24 D7 and D1.** Both were
>   the only non-hardware items left on that plan.
> - **PLAN-18's Firecracker tier is CLOSED**, not deferred: no host-fs means the mount
>   model the product became does not port. The answer was already in PLAN-18 D8.
> - **Genuinely deferred** (PLAN-24 F1–F8, each with a written unpark trigger): live
>   telemetry, Netbird overlay (shape pre-decided: host as routing peer), public previews,
>   manual multi-node (**already works** via `ape sandbox --node`, needs only docs +
>   credential distribution), the fleet hub/controller (blocked on a shared-storage
>   decision, not on hardware — workspaces are not portable), the GPU/USB device tier,
>   per-tenant isolation, and image-pin generation + multi-arch.
>
> Decision record: `_output/2026-08-04-unified-roadmap-decided.html`.
>
> The rationale below is the original 2026-07-02 sequencing record.

> **Added 2026-08-21 (PLAN-25).** The first non-sandbox wave since PLAN-19: the
> deterministic project-data commands the APEX framework skills call instead of doing
> mechanical work by hand. Origin is upstream — `apex_process_framework_eval/_output/
> apex-implementation-plan.md` — whose Part B/D is `ape`'s share. It is **not** a sandbox
> plan and has no dependency on any of PLAN-16/18/20–24. Two ordering constraints matter:
> `ape config resolve` (D1) gates every other deliverable, because no `.go` file in this
> tree currently reads `_apex/config.yaml`'s folder variables; and `ape` must be released
> **before** the framework version bump that calls these commands, since C2 gives the
> skills no fallback branch.

## Proposed-wave dependency order (2026-07-02)

**PLAN-11 first** (reordered 2026-07-02: its F0 trust-dialog fix + `ape
task --output-format json` unblock the eval repo's migration off raw
`claude -p`; PLAN-9/10 are not prerequisites). Then PLAN-9 → PLAN-10 →
PLAN-12 → PLAN-13 → PLAN-14; PLAN-15 after PLAN-11/12 (its library wraps
them) and before/with PLAN-14's `script.run`;
PLAN-16 (reframed 2026-07-07): **Kata VM workspaces for local dev — Phase 1
of the APEX Process Platform** (north-star in the separate
`apex_process_platform` repo, `draft/00-05`). Now independent of PLAN-14
(the workspace *is* the environment; you run jobs inside it). kata-only;
reuses the composer/proxy/profile/OCI-spec already built; drops the gVisor
runner. Research: `development/research/sandbox-qemu-vs-kata-20260706.md`
(+ `sandbox-isolation-20260702.md`). Phases 2–4 (in-VM NATS worker, Netbird
overlays, previews/staging, device tier) live in the platform repo.
PLAN-17 after PLAN-10 + PLAN-13 (it consumes their scan/discovery and
natsconn/eventing/blobstore), parallel to PLAN-14 — but its identity
amendments (user token in subjects, `session` kind, payload
`user`/`session_id`) are folded **into PLAN-13's own PRs**, since the
subject taxonomy is an additive-only contract from day one.
PLAN-18 (added 2026-07-08): the prospective **`ape`/`aped` split** — an
unprivileged CLI plus a rootful Kata-QEMU VM-management daemon with GPU/USB
(VFIO) passthrough over embedded NATS. **Additive to PLAN-16** (reuses its pure
layers; refactors only `Runner`/`Registry`/`proxysup` behind a `Backend`
interface) and **built on PLAN-13/14/17** — PLAN-18's **Phase 0** implemented them
first (PLAN-13/17 `done`, PLAN-14 core `done`); Phases 0–2 + the non-device
containerd driver are now live-validated (2026-07-12). Design
research: `development/research/ape-aped-split-20260707.md` (+ the
`ape-aped-research-prompt-20260708.md` brief and the
`ape-aped-passthrough-recipe-20260708.md` device-tier recipe). Phase 3 (device
tier) needs a discrete-GPU box (not available on the dev box — Intel iGPU only).

Review context: `_output/review-20260702/` (project review + CLI and docs
improvement proposals).

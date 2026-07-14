# Planning Index

| ID      | Title                                                   | Status                | Created    |
| ------- | ------------------------------------------------------- | --------------------- | ---------- |
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

> **Status note (2026-07-12):** the proposed wave has largely landed on
> `feat/plan-18-phase2-aped` (targets CHANGELOG v0.0.42). See the table above
> for what's implemented vs pending. The rationale
> below is the original 2026-07-02 sequencing record.

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

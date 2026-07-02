# Planning Index

| ID      | Title                                                   | Status   | Created    |
| ------- | ------------------------------------------------------- | -------- | ---------- |
| PLAN-16 | gVisor-sandboxed sessions + per-job `~/.claude`         | proposed | 2026-07-02 |
| PLAN-15 | `ape script` — yaegi orchestration scripts              | proposed | 2026-07-02 |
| PLAN-14 | `ape service` — NATS micro job daemon                   | proposed | 2026-07-02 |
| PLAN-13 | NATS progress events + transcript blobs                 | proposed | 2026-07-02 |
| PLAN-12 | `ape command` — prompt/handoff claude session           | proposed | 2026-07-02 |
| PLAN-11 | `ape task` — single-skill runs without YAML             | done     | 2026-07-02 |
| PLAN-10 | Telemetry v2 — per-model cost, timestamps, subagents    | proposed | 2026-07-02 |
| PLAN-9  | CLI/docs hygiene + PTY-only consolidation               | proposed | 2026-07-02 |
| PLAN-8  | Migrate tmux → in-process PTY                           | done     | 2026-05-22 |
| PLAN-7  | Unified pipeline TUI (interactive ≡ programmatic)       | done     | 2026-05-21 |
| PLAN-6  | Interactive pipeline exec + orthogonal UI/exec modes    | done     | 2026-05-19 |
| PLAN-5  | `ape chat` + `ape pipeline` web mode                    | done     | 2026-05-17 |
| PLAN-4  | Per-step boundary commits                               | done     | 2026-05-11 |
| PLAN-3  | Pipeline run manifest + per-step metrics                | done     | 2026-05-11 |
| PLAN-2  | Pipeline UX follow-ups (v0.0.7 carry-out)               | done     | 2026-05-10 |
| PLAN-1  | Pipeline UX and framework setup separation              | done     | 2026-05-10 |

## Proposed-wave dependency order (2026-07-02)

**PLAN-11 first** (reordered 2026-07-02: its F0 trust-dialog fix + `ape
task --output-format json` unblock the eval repo's migration off raw
`claude -p`; PLAN-9/10 are not prerequisites). Then PLAN-9 → PLAN-10 →
PLAN-12 → PLAN-13 → PLAN-14; PLAN-15 after PLAN-11/12 (its library wraps
them) and before/with PLAN-14's `script.run`;
PLAN-16 with or immediately after PLAN-14 (its `--isolate` flag also works
for local runs, but the service is the driving consumer — research in
`development/pending/sandbox-isolation-20260702.md`).
Review context: `_output/review-20260702/` (project review + CLI and docs
improvement proposals).

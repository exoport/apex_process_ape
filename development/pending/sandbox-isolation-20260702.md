---
created_at: 2026-07-02
status: open
tags:
  - sandbox
  - isolation
  - security
  - pty
  - skills
summary: Research findings for running ape's pipeline/task/command/script PTY sessions inside an isolation boundary stronger than plain Docker, with project folders mounted and per-session curated .claude/skills + hooks. Verdict — feasible; recommended ladder is gVisor (runsc) as default, Kata Containers (Cloud Hypervisor) when a hardware VM boundary is required, Docker Sandboxes (sbx) as a pragmatic proprietary shortcut; Firecracker and raw QEMU are wrong-shaped for interactive dev-tool sessions. Per-session skills are achievable by mount/staging (project-first resolution), hooks ape already injects per-session via --settings. Candidate future PLAN-16.
---

# Sandboxed sessions — research notes (2026-07-02)

Question: can pipeline/task/command/script interactive sessions run in a
sandbox stronger than Docker (folders mounted, host protected), and can each
session get its own `.claude/skills` / hooks?

Answer: yes to both. ("gizmo" in the original question — no such project
exists; almost certainly gVisor.)

## Isolation ladder (Linux, interactive PTY + RW project mount + Go driver)

| Rank | Option | Boundary | Mounts | PTY | Start | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| 1 (default) | **gVisor / runsc** | user-space kernel (Go), no host-syscall passthrough; defended 96% of tracked kernel CVEs incl. every recent runc escape | OCI bind mounts (directfs; 10–30% I/O overhead) | native (`--runtime=runsc -it`) | ~50–150 ms, +~100 MiB | drive via containerd/go-runc — pure-Go end to end; K8s agent-sandbox SIG's default |
| 2 (hardware boundary) | **Kata Containers + Cloud Hypervisor** | KVM VM per container | virtiofs RW | native exec/attach | ~200–300 ms, ~150 MB+ | containerd shim; needs KVM; guest kernel/rootfs lifecycle |
| 3 (shortcut) | **Docker Sandboxes (`sbx`)** | proprietary microVM (KVM on Linux) | workdir bind or clone-mode | purpose-built for Claude Code TUIs | microVM-class | no Go SDK (shell out); young product; credentials held outside VM |
| 4 (embedded) | **libkrun** (go-microvm / microsandbox SDK) | KVM microVM, VMM in-process | virtiofs first-class | designed for it | <200 ms | experimental wrappers, API churn; go-microvm adds DNS egress firewall |
| avoid | Firecracker | strongest pedigree but **no virtiofs** (rejected upstream), DIY in-guest PTY agent | — | — | — | built for serverless fleets, not dev-tool sessions |
| avoid | raw QEMU + virtiofs | same boundary Kata automates | virtiofs | DIY | — | highest ops burden |
| not stronger | bubblewrap / Anthropic sandbox-runtime (srt) | namespaces+seccomp, shared kernel | native | yes | ~0 | below the "stronger than Docker" bar, but its **out-of-sandbox domain-allowlist egress proxy** is a pattern to copy |

Claude Code's built-in `/sandbox` covers only the Bash tool — whole-session
isolation must come from outside, which is what ape would do anyway.

## ape integration shape

- Run the **whole `ape <kind> … --no-tui` job** inside the sandbox (ape +
  claude + PTY + bridge IPC + `ape mcp-bridge` child all share the guest;
  nothing crosses the boundary except NATS/API egress). The service (PLAN-14)
  spawns the sandbox instead of a bare child process — same admission,
  events, and job model.
- Mount: project root RW (+ allowlisted component repos), nothing else.
- Egress: daemon-owned CONNECT proxy **outside** the sandbox with a domain
  allowlist (`api.anthropic.com`, NATS host); L3 IP filters rot with CDNs.
- Credentials: inject a **scoped, low-limit `ANTHROPIC_API_KEY` per job** via
  env — don't mount the real `~/.claude`. Accept: whatever is inside the
  boundary (key, project contents) is readable by the session; the
  credential-injecting-proxy pattern (proxy holds the real secret) is the
  stronger variant.
- Residual exposure: the RW project mount itself (`.git` hooks, Makefiles
  later executed on the host). Consider clone-then-sync for hostile inputs.

## Per-session skills / hooks / agents

- **Hooks: already solved.** ape injects per-session hooks via inline
  `--settings` today (`internal/bridge/config/settings.go`) — works, is how
  Stop-hook completion detection functions. Caveat: settings *merge*; user/
  project hooks also fire. In a sandbox that's moot (we control both mounts);
  on the plain host, ape's `--ignore-project-settings` handles the project
  layer.
- **Skills/agents: resolution is project-first** (`.claude/skills` beats
  `~/.claude/skills`; same for agents). No CLI flag to add skill dirs, so the
  mechanism is directory control:
  - In a sandbox: mount a curated skills dir at project `.claude/skills`
    (overlay over the real project mount) and a curated/empty guest
    `~/.claude/skills`. Full control, zero host mutation.
  - Without a sandbox: stage a synthetic project overlay (bind mount or
    tmp checkout) — clumsier; the sandbox actually *simplifies* this.
- `CLAUDE_CONFIG_DIR` relocates only `.credentials.json` per current docs —
  not a full config relocation; env-var API key injection is the cleaner
  path in the guest.

## Open items (for a future PLAN-16)

- Choose rung 1 vs 3 (gVisor via go-runc vs shelling to `sbx`) — build-vs-buy.
- Sandbox as `--isolate <profile>` flag on pipeline/task/command/script +
  `service.yaml` per-job enforcement (pairs with `force_script_sandbox`).
- Egress-proxy implementation (tiny CONNECT proxy in the daemon).
- Skill-set profiles: `_apex/config.yaml` naming curated skill dirs per job
  kind.
- macOS later: Apple `container` framework (v1.0, 2026-06) or libkrun/HVF.
- Windows: none of the rung-1/2 options apply; document as unsupported for
  isolation v1.

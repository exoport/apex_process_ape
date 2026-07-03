---
plan_id: PLAN-16
created_at: 2026-07-02
status: proposed
tags:
  - sandbox
  - gvisor
  - isolation
  - security
  - skills
  - credentials
summary: Run ape jobs (pipeline/task/command/script) inside a gVisor (runsc) sandbox — the whole `ape <kind> --no-tui` process tree (ape + claude + PTY + bridge) executes in the guest, with the project mounted read-write, the host root read-only, and a per-job synthetic `~/.claude` assembled by ape. Two credential modes: (A) mount the host's real OAuth credential files into the synthetic home while every other layer (preferences, hooks, skills, agents) is hand-picked; (B) no OAuth at all — a low-limit per-job ANTHROPIC_API_KEY injected via env, with custom hooks/skills. Skills compose from the project's `.claude/skills` (in the project mount, optionally overlaid) plus an optional curated skill set in the synthetic guest `~/.claude/skills`. Bridge hooks continue to be injected via `--settings`; because ape authors every settings layer inside the guest, user-level hooks/skills can be removed, replaced, or hand-picked per job. Egress is deny-by-default through a single, always-global daemon-owned CONNECT proxy whose authorized-domains list is defined per profile (wildcards supported, service-level cap); the proxy never decrypts and doubles as the audit point — every outbound CONNECT, allowed or denied, is recorded to a per-job `egress-audit.jsonl` and optionally published as NATS audit events; git access is composed per job via `git.mode` — scoped token over HTTPS (recommended), read-only deploy key, or ssh-agent socket passthrough — never the real `~/.ssh`/`~/.gitconfig`. Isolation profiles are versioned files under `_apex/sandbox/`; exposed as `--isolate <profile>` on the run commands and enforceable per job kind in the service.
origin:
  - 2026-07-02 user request — sandbox pipeline/task/command/script sessions with something more secure than Docker ("qemu, gizmo, or similar" — gVisor), mounting folders, no access to the rest of the host; per-session mounted `.claude` skills/hooks.
  - 2026-07-02 user requirements (Q&A) — (1) must be able to mount `~/.claude` WITH the OAuth but different preferences/hooks/skills; (2) must be able to mount `~/.claude` WITHOUT the OAuth, using a low-limit per-job ANTHROPIC_API_KEY, with custom hooks/skills; (3) project `.claude/skills` plus optional skills in the mounted custom guest `~/.claude`; (4) keep injecting hooks via `--settings`, with the ability to customize, remove, or hand-pick any user-level hooks/skills.
  - 2026-07-02 user follow-up — (5) authorized domains for the egress proxy must be definable (per profile); (6) need a way to mount GitHub or SSH credentials into the sandbox for git operations.
  - 2026-07-02 user decision — the CONNECT proxy stays global for all egress even when its credential role is GitHub-only (it never decrypts, hostname metadata only, so no privacy/latency reason to bypass it); outbound requests are tracked for audit. A `git-only` proxy scope was considered and dropped.
  - development/pending/sandbox-isolation-20260702.md — isolation-technology research: gVisor recommended (user-space kernel, OCI bind mounts, native PTY, ~50–150 ms start, pure-Go drivable via containerd/go-runc, best escape track record short of a VM); Kata/Docker-Sandboxes as hardware-boundary alternatives; Firecracker/raw-QEMU rejected for this shape; Claude Code's own /sandbox covers only the Bash tool. Skills/agents resolve project-first; no CLI flag adds skill dirs — directory control is the mechanism; CLAUDE_CONFIG_DIR relocates only the credentials file.
---

# PLAN-16: gVisor-sandboxed sessions with per-job `~/.claude` composition

## Goal

`ape task apex-create-prd --isolate ci-profile` (and the same for
pipeline/command/script, locally or via the service) runs the entire job
inside a gVisor sandbox: host root read-only, project read-write, network
limited to the Anthropic API and the NATS cluster, and a `~/.claude` that
contains exactly what the profile says — chosen credentials mode, chosen
skills, chosen hooks, nothing else from the real home. The session cannot
touch the rest of the host even with `--dangerously-skip-permissions`, which
is precisely the flag every ape session runs with today.

## Why now

- Every ape session runs claude with permissions disabled inside the user's
  real environment; PLAN-14 (service) and PLAN-15 (scripts, incl. remote
  `script_source`) widen who can start such sessions. The isolation story
  should land with — or immediately after — the service, not later.
- The synthetic-home mechanism is independently valuable **without** the
  sandbox: hand-picked skills/hooks per job is a repeatability feature
  (same curated skill set every run) as much as a security one.

## Non-goals

- No Kata/Docker-Sandboxes backend in v1 (the runner is an interface; a
  hardware-VM backend is a follow-up if mandated).
- No TUI/web UI *inside* the sandbox: v1 isolates headless jobs
  (`--no-tui` shape — which is what the service spawns anyway). Local
  `--isolate` + `--tui`/`--web` is rejected with a clear error; revisit if
  needed.
- No OCI image management: the guest rootfs is the host filesystem
  read-only, not a built image (see D1). Claude, node, git, and ape come
  from the host, unwritable.
- No credential minting: mode B expects the low-limit API key to be
  provided (config/env/request); creating scoped keys is the operator's
  workflow.
- macOS/Windows isolation (gVisor is Linux-only): `--isolate` errors on
  non-Linux; Apple `container`/libkrun is future work.

## Design

### D1: Sandbox runner (`internal/sandbox`)

OCI-spec construction + `runsc` invocation (via `containerd`'s go-runc
client or direct `runsc run` with a generated bundle — decide in the spike;
go-runc preferred, it exposes console sockets and is pure Go):

- **Rootfs:** host `/` bind-mounted **read-only** (plus masked paths:
  `/home`, `/root`, `/var`, `/etc/ssh`, cloud-credential dirs). No image
  build, no drift — the guest sees the same toolchain the host has.
- **Writable mounts:** project root (rw); allowlisted component repos (rw,
  from PLAN-14's allowlist); job staging dir (the synthetic home, D2);
  `tmpfs` on `/tmp`; a scratch dir for `_output` if the project mount
  doesn't already contain it.
- **Env:** `HOME` pointed at the staging home; `ANTHROPIC_API_KEY` (mode B);
  `HTTPS_PROXY`/`NO_PROXY` (D4); NATS env passthrough (PLAN-13/14) — and
  because `APE_NATS_CREDS` is a *file path*, the `.creds` file itself is
  bind-mounted read-only into the staging home with the env var rewritten
  to the in-guest path; without it, in-guest eventing and PLAN-17 reporting
  (`ape event`/`log`/`metrics`/`transcript`) silently disable.
- **Process:** the job command itself — `ape <kind> … --no-tui --quiet`.
  ape-in-guest allocates its PTYs (`internal/repl`) and its localhost bridge
  IPC exactly as today; nothing about the interactive runner changes.
  PTY support and job control are native in runsc.
- **Lifecycle:** `runsc kill` on job stop (maps onto PLAN-14's `job.stop`);
  sandbox always torn down at job end; staging dir shredded unless
  `--keep-staging` (debug).
- **Preflight:** `ape doctor` gains checks — `runsc` on PATH + version,
  unprivileged-userns/kvm availability as applicable.

### D2: Synthetic `~/.claude` (guest home composition)

Per job, ape assembles a staging directory mounted as the guest `$HOME`:

```
<staging>/
  .claude.json               # generated: minimal prefs (mode A: see below)
  .claude/
    .credentials.json        # mode A only: bind of the host's real file
    settings.json            # generated from the profile: hand-picked hooks,
                             #   preferences; may be empty
    skills/<name>/SKILL.md   # hand-picked copies (profile skill list)
    agents/<name>.md         # hand-picked copies (profile agent list)
```

**Credential modes:**

- **Mode A — `credentials: oauth`.** The host's real OAuth material — and
  only it — is bind-mounted into the synthetic home: `~/.claude/.credentials.json`
  and the OAuth/token portion of `~/.claude.json` (spike task: determine the
  minimal file set for a working authenticated session incl. token refresh —
  these binds are rw because refresh writes; everything else in the
  synthetic home is job-authored). The session uses the subscription; the
  sandbox cannot read anything else from the real home.
- **Mode B — `credentials: api-key`.** No credential files at all.
  `ANTHROPIC_API_KEY` injected via env from the profile's configured source
  (env var name, file path, or service-request field). Intended to be a
  scoped, low-limit key per job. Anything inside the boundary can read the
  key — the limit on the key is the real control.

**Skills composition (requirement 3):** the session resolves skills
project-first, so the effective set is:

1. project `.claude/skills` — ships with the project mount; profile may
   optionally **overlay** it (replace the mount with a curated dir) or pass
   it through untouched;
2. guest `~/.claude/skills` — the profile's hand-picked user-level set
   (copied from the host's real `~/.claude/skills` by name, or from any
   directory path); default **empty** — nothing leaks in by omission.

Agents compose identically.

**Hooks (requirement 4):** three authored layers, all under ape's control
inside the guest — user (`<staging>/.claude/settings.json`: exactly the
hooks the profile picks, or none), project (`.claude/settings.json` inside
the project mount; profile may mask it via the existing
`--ignore-project-settings` or an overlay), and CLI (`--settings` — the
bridge hooks, injected unchanged as today; they must survive every profile
since Stop-hook completion detection depends on them). Net effect: remove,
replace, or hand-pick any user/project hook while the bridge contract stays
intact.

### D3: Isolation profiles

Versioned files `_apex/sandbox/<profile>.yaml` (project-owned, reviewable):

```yaml
name: ci-profile
credentials: api-key            # oauth | api-key
api_key_source: env:APE_JOB_ANTHROPIC_KEY   # mode B only
skills:                         # guest ~/.claude/skills
  - apex-create-prd             # by name → copied from host user skills
  - /abs/path/curated/my-skill  # or by path
agents: []                      # guest ~/.claude/agents (default empty)
hooks: []                       # user-layer hooks (default none)
project_skills_overlay: ""      # optional dir replacing project .claude/skills
ignore_project_settings: true
preferences: {}                 # keys for generated settings.json
network:
  authorized_domains:           # egress proxy allowlist (D4) — CONNECT, port 443
    - api.anthropic.com
    - github.com                # only if git-over-HTTPS is used
    - "*.githubusercontent.com" # leading-wildcard subdomain matching
  direct_allow:                 # non-HTTP endpoints — nftables, fixed hosts only
    - nats.example.com:4222
    # - github.com:22           # only for git.mode: deploy-key / agent
git:                            # see D5
  mode: none                    # none | token | deploy-key | agent
  token_source: env:APE_JOB_GITHUB_TOKEN   # token mode
  deploy_key: ""                # deploy-key mode: host path, mounted ro
mounts:
  extra_rw: []                  # additional allowlisted rw paths
```

CLI: `--isolate <profile>` on pipeline/task/command/script (Linux-only;
mutually exclusive with `--tui`/`--web` in v1). Service (PLAN-14):
`isolation_profile` per request + `service.yaml: force_isolation:
<profile>` per job kind (composes with `force_script_sandbox` — that one
restricts the yaegi interpreter, this one contains the process; a remote
script job can have both).

### D4: Egress control — authorized domains

- **Egress proxy:** daemon/CLI-owned CONNECT proxy running **outside** the
  sandbox; guest gets `HTTPS_PROXY=http://host:port`. The allowlist is the
  profile's `network.authorized_domains` — exact hostnames plus
  leading-wildcard entries (`*.githubusercontent.com`); deny-by-default;
  CONNECT to port 443 only. Domain-level (L7) control — the pattern
  Anthropic's own sandbox-runtime uses. Denied CONNECTs appear in the audit
  trail (below) with `decision: denied`, so a mis-scoped profile is
  diagnosable in one look.
- **The proxy is always global** (user decision 2026-07-02): `HTTPS_PROXY`
  is set in the guest env and every HTTPS egress — Anthropic API and git
  alike — rides the same CONNECT proxy. A git-only scope was considered and
  rejected: the proxy never decrypts (hostname metadata only; TLS stays
  end-to-end with Anthropic), so there is no privacy or latency reason to
  bypass it, and routing the API around it would force CDN-range
  `direct_allow` rules that forfeit domain-level control. GitHub-specific
  credential behavior (the v2 injecting variant) is a per-domain feature of
  this one proxy, not a separate proxy scope.
- **Egress audit trail:** because the proxy is the single egress path, it
  doubles as the audit point. Every outbound CONNECT — allowed and denied —
  is recorded to the job runlog as `egress-audit.jsonl`: `{ts, job_id,
  host, port, decision, duration_ms, bytes_up, bytes_down}`. Hostname-level
  metadata only, never payloads (nothing is decrypted, so nothing more is
  even available). When NATS is configured (PLAN-13), audit entries are
  also published on `ape.evt.<user>.<project>.<kind>.<id>.egress` so a central
  consumer can retain the trail; the `run-end` event carries per-host
  totals.
- **Layering:** the service (`service.yaml`) may define a superset cap —
  profile domains not present in the service-level authorization are
  rejected at admission, so a project profile cannot widen what the daemon
  operator allowed.
- **Non-HTTP endpoints** (`network.direct_allow`): fixed host:port pairs
  allowed directly in the per-job netns via nftables — NATS (nats.go does
  not speak HTTP CONNECT) and, when a profile opts in, `github.com:22` for
  SSH git. These must be stable hosts, not CDNs (IP allowlists rot).
- Everything else: denied. gVisor's netstack runs in a per-job netns wired
  only to the proxy and the direct allows.

### D5: Git and SSH credentials

Git access follows the same philosophy as D2's credential modes: nothing
from the real home by default; per-job, scoped, explicitly composed.
`git.mode` in the profile selects one of:

- **`token` (recommended).** A scoped token — fine-grained GitHub PAT
  limited to the specific repos, or a GitHub App installation token —
  injected via env from `token_source`. The composer writes a generated
  `.gitconfig` into the staging home wiring a credential helper that serves
  the env token for `https://github.com` (plus `url.insteadOf` rewriting
  `git@github.com:` → `https://github.com/` so SSH-style remotes in
  existing checkouts keep working). Traffic rides the D4 proxy —
  `github.com` must be in `authorized_domains`; no port 22, no key
  material. Same trust math as mode B API keys: the guest can read the
  token, so its *scope and expiry* are the control.
- **`deploy-key`.** A dedicated per-project deploy key (host path in the
  profile) mounted **read-only** at `<staging>/.ssh/id_ed25519`, with a
  generated `.ssh/config` and a pinned `known_hosts` (GitHub's published
  host keys — no TOFU prompt inside the guest). Never the real `~/.ssh`.
  Requires `github.com:22` in `direct_allow`. For repos/orgs where PATs are
  unavailable or SSH remotes are mandated.
- **`agent`.** The host's `ssh-agent` socket (`SSH_AUTH_SOCK`) bind-mounted
  into the guest — private keys never enter the sandbox, only signing
  capability while the job runs. Strongest key hygiene, weakest capability
  containment (the guest can sign as you for the job's duration, for any
  key in the agent — the how-to says to run a dedicated agent holding only
  the intended key). Gated on the spike verifying host-UDS bind mounts
  through runsc's gofer.
- **`none` (default).** No git credentials; read-only public clones still
  work through the proxy if the domains are authorized.

v2 (noted, not planned): the credential-injecting proxy — the D4 proxy
holds the GitHub token outside the sandbox and injects the `Authorization`
header, so the guest never sees any secret. Same v2 as the Anthropic-key
variant in D6.

### D6: Honest boundaries (documented, not solved)

- The RW project mount is inside the boundary: the session can write
  `.git/hooks`, Makefiles, direnv files that the *host* may later execute.
  The isolation how-to says this plainly; clone-then-sync is a possible v2.
- Mode A places the full OAuth token inside the boundary; egress
  allowlisting doesn't prevent exfiltration *to Anthropic* via prompt
  content. Mode B with scoped keys is the recommendation for untrusted
  work; the credential-injecting-proxy variant (proxy holds the secret,
  guest never sees it) is noted as v2.

## Steps

1. **Spike (blocks the rest):** runsc + host-ro-rootfs OCI spec running
   `claude` interactively under `internal/repl` inside the guest; verify
   PTY, Stop-hook flow (bridge IPC in-guest), transcript writing to the
   staging `$HOME`, mode A's minimal OAuth file set incl. token refresh,
   and host-UDS bind mounting through the gofer (gates D5 `agent` mode).
   Record findings in the plan before phase 2.
2. `internal/sandbox`: spec builder + runner + teardown; unit tests on spec
   generation, integration tests gated on `runsc` presence (CI job installs
   runsc; skip locally when absent).
3. Synthetic-home composer (D2) + git-credential composer (D5:
   `.gitconfig`/credential helper, `.ssh` staging, pinned known_hosts) +
   profile loader (D3) — pure fs logic, heavily tested (all six requirement
   scenarios as fixtures).
4. Egress proxy (small CONNECT proxy in the daemon/CLI; `authorized_domains`
   with wildcard matching; `egress-audit.jsonl` trail for allowed + denied;
   NATS audit events; service-level cap) + netns/nftables setup for
   `direct_allow`; integration tests asserting a non-allowlisted host is
   unreachable from the guest and both decisions appear in the audit log
   with byte counts.
5. `--isolate` wiring on the four run commands + PLAN-14 `isolation_profile`
   / `force_isolation`; doctor checks.
6. Docs: `how-to/isolate-sessions.md` (profiles, both credential modes,
   what the sandbox does and does not protect), `reference/sandbox-profile.md`
   (schema), update `explanation/bridge-security.md`.

## Acceptance

- Mode A job: session authenticates via OAuth, sees only the hand-picked
  skills (`ls ~/.claude/skills` in-guest matches the profile; a skill
  present on the real host but not in the profile does not resolve), real
  home unreadable from the guest.
- Mode B job: no OAuth material anywhere in the guest fs; session works via
  the injected key; per-model telemetry (PLAN-10) attributes the run.
- Project skills still resolve; profile overlay replaces them when set.
- Bridge hooks fire (step completion works) with an empty user-hooks layer.
- In-guest attempts to read `/home/<user>/.claude` (real), write outside
  the project, or connect to a non-allowlisted host all fail; the denied
  host appears in `egress-audit.jsonl` with `decision: denied`, and every
  Anthropic API request of the run appears with `decision: allowed`.
- `git.mode: token` job: `git push` to an authorized repo succeeds over
  HTTPS through the proxy with a fine-grained PAT; the real `~/.ssh` and
  `~/.gitconfig` are not visible in-guest. `deploy-key` job: push works
  over `github.com:22` with the mounted key only.
- A profile domain outside the service-level cap is rejected at admission.
- `job.stop` tears down the sandbox; no staging dirs or netns leak after
  1000 job cycles (leak test).

## Risks

- **gVisor syscall-compat edge cases** — claude/node exercise a wide
  surface; the spike is the gate. Fallback if a blocker appears: same
  design on Kata/Cloud-Hypervisor (the runner interface keeps this a
  backend swap).
- **Mode A OAuth file set is undocumented** and may shift with Claude Code
  versions — pinned by an integration test; the composer fails loudly when
  the expected files move.
- I/O overhead (~10–30%) on the project mount — acceptable for agent
  sessions (API-latency-dominated); measured in the spike.
- Root-ro-host rootfs means host package changes affect guests — that's a
  feature (no image drift) and a caveat (no pinned toolchain); an OCI-image
  rootfs option can be added later without changing the profile surface.

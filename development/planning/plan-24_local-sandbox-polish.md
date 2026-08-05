---
plan_id: PLAN-24
created_at: 2026-08-05
status: proposed
tags:
  - sandbox
  - aped
  - agent
  - lifecycle
  - reporting
  - docs
summary: >
  Finish the single-node sandbox story. After PLAN-20/21/22/23 shipped and were
  live-validated on `mmq4`, what remains splits cleanly in two: work that lives on
  one host, and work that needs a second node, another network product, or
  hardware. This plan is the first half. Seven deliverables: the devcontainer
  how-to (the only documentation gap in the repo), a private port-forward so a
  human can actually look at the web app running inside a workspace,
  sandbox-aware cost/reporting (the transcripts are already on the host — nothing
  collects them), node capacity in `Capabilities()`, and then the chain that
  PLAN-16 Phase 2 / PLAN-18 D6 designed and never built: a guest→host NATS path,
  the `ape sandbox-agent` heartbeat, and an honest idle reaper. The chain is
  reshaped by two facts found while planning: the transport needs no new listener
  and no new firewall hole (the CONNECT proxy already reaches a loopback NATS in
  the same process), and the agent needs no new binary (PLAN-23 already mounts
  `ape` into every workspace, build-info-verified). Everything stays in this
  repo — no image bump, no digest re-pin, no second release cycle.
origin:
  - 2026-07-26 plan review (`_output/2026-07-26-plan-review-pending.md`): all 23
    plans re-read against v0.0.50. Found very little real work pending and a lot of
    stale bookkeeping; the genuinely-pending items were the GPU/fleet/overlay tier
    plus one deliberate non-decision (the reaper) and one doc gap.
  - 2026-07-27 congruence review (`_output/2026-07-27-unified-roadmap.html`): asked
    what PLAN-20/21/22/23 *changed* about the pending work. Found that PLAN-21 had
    closed the transport PLAN-16 Phase 2 assumed, and filed reaper → agent →
    firewall-hole as a hard three-link chain.
  - 2026-08-05 decision session (`_output/2026-08-04-unified-roadmap-decided.html`):
    eleven decisions taken, scope narrowed to **local sandboxes first**. Host↔guest
    work counts as local; a second node does not. Four claims from the earlier
    reviews did not survive being re-checked against the code — see "Findings that
    reshaped the work" below.
  - Owner's calls during that session: keep the in-guest agent heartbeat rather than
    the cheaper host-CPU signal (precision over cost); private port-forward rather
    than an overlay, with Netbird explicitly kept open; launch the agent from `aped`
    rather than the image entrypoint, to keep the work in one repo.
---

# PLAN-24: Local sandbox polish — port-forward, reporting, capacity, agent, reaper

## Goal

A single-node sandbox you can live in: look at what you are building, know what it
cost, know whether another workspace fits, and have idle VMs stop themselves
honestly.

Explicitly **out of scope**: anything needing a second node, an overlay, a public
URL, or hardware. Those are inventoried in §Deferred with the event that unparks
each one, so this plan is not the place they get re-litigated.

## Background — where we are

The sandbox stack is shipped and live-validated on `mmq4`: allowlisted egress with
every bypass route blocked (PLAN-21), the general mount model with a committed
`.apesandbox.yaml` (PLAN-20), declared toolchains / durable caches / `stop`-`start`
/ reboot recovery (PLAN-22), and runtime `ape` delivery with build-info verification
(PLAN-23). One shared Claude OAuth session converges in all four directions.

What is left on one host:

- **No way to see a running app.** A workspace has *zero* inbound reachability. The
  per-netns ruleset is `policy drop` on `input`, `output` *and* `forward`
  (`egressnet.go:227-246`), and the host bridge table drops forwarding in both
  directions (`dev-host.sh:283`). `exec`/`attach` work only because they ride
  containerd's own shim channel, not the network.
- **Work done inside a workspace is invisible to `ape`'s reporting** — or so the
  earlier reviews recorded. It is not; see Finding 1.
- **`Capabilities()` reports runtimes + `HostFS` and nothing else**
  (`containerd_driver_linux.go:80`), even though `workspace.Capabilities` already
  declares `KVM`, `Mem`, `Factory`, `GPUs`, `USB`, `IOMMU` and leaves them empty.
- **The idle reaper was deliberately not built** (PLAN-22 D5b). `last_used_at`
  exists and `ape sandbox ls --idle` reports it, but it records exec/attach/start,
  so a workspace running a three-hour build with nobody attached looks idle.
- **The in-guest agent has no code at all** — verified: no `sandbox-agent` anywhere
  in `internal/` or `cmd/`. PLAN-18 D6 designed it; PLAN-21, written later, closed
  the transport it assumed.
- **The devcontainer how-to is open** (PLAN-22 D7, PARTIAL) — the only documentation
  gap the 2026-07-26 review found.

## Findings that reshaped the work

Each was found by checking the code rather than the plans, and each changes what
gets built. They are recorded here because they are the reason this plan differs
from what PLAN-16/18 describe.

**1. Sandbox transcripts are already on the host filesystem.** The guest's `$HOME`
(`/sandbox/home`) is a **read-write bind from a per-workspace host staging dir** —
`spec.go:128-133`, commented "Synthetic home: read-write (session state,
transcripts)" — and the workspace registry already records that path
(`containerd_driver_linux.go:161`, `rec.StagingDir`). So cost and reporting do not
need a telemetry wire; they need to know where to look. This is D3, it is
independent of the agent chain, and it demotes live telemetry from a gap to a
refinement (deferred, F1).

**2. The transport needs no new listener and no new firewall hole.** The
per-workspace CONNECT proxies run **in-process inside the de-privileged `aped`
front** (`egress.go:37-52`), and that same process hosts the embedded NATS server on
`MgmtHost`, defaulting to `127.0.0.1` and commented *"guest-unreachable"*
(`front.go:24`). The proxy's upstream dial therefore happens from inside the
process that already holds the NATS listener — so the tunnel's far end is a
**loopback dial the front makes to itself**. Nothing new binds anywhere, and the
guest's single permitted destination remains the proxy it already uses. This
supersedes PLAN-16 Phase 2 / PLAN-18 D6's bridge-IP listener.

**3. The agent needs no new binary.** PLAN-18 D6 already specifies `ape
sandbox-agent`, a subcommand — and PLAN-23 made its delivery free: `aped` mounts the
`ape` beside it read-only at `/opt/ape/bin`, trusted by *reading* build info rather
than executing it, with `github.com/exoport/apex_process_ape/cmd/ape` as the
identity check (`apebin.go:43`). Version skew is impossible by construction.

**4. The reaper did not have to wait for the agent.** `task.Metrics()` is reachable
from a driver that already loads tasks (`containerd_driver_linux.go:531`), and a
Kata guest burning vCPU burns host CPU. That was a reaper with no agent and no
network change. **Recorded because it was viable and was rejected** (owner's call,
2026-08-05): the heartbeat knows *what* is running rather than inferring from a
number. The three-link chain in the 2026-07-27 review was a choice, not a
constraint, and it should read that way to whoever comes next.

**5. SSH is already running inside the workspace.** The image entrypoint starts
`sshd` (PLAN-23 D9 exists because sshd builds a fresh session environment), `.ssh`
is already composed with a pinned `known_hosts` (`gitcred.go:108`), and
`WorkspaceSpec.SSHPort` + the `ssh_port` wire field already exist
(`kata.go:97,358`) — but they are wired **only** in the nerdctl/shell path, which
publishes `-p 127.0.0.1:<port>:22` (`kata.go:248`). The containerd driver that
`aped` actually uses never reads `SSHPort`, and the netns wall would drop the
inbound connection anyway. So D2 supplies the missing half, and the concept, the
naming and the guest side all already exist.

## Design

### D2 — port-forward: why the stream and not a port publish

Two candidate shapes. The nerdctl one (`-p 127.0.0.1:N:22`) is already written for
the shell driver, and is the wrong one here: the containerd driver builds an OCI
spec plus a netns directly rather than shelling out, and the netns `input` chain is
`policy drop` — a published port would need the wall relaxed, which is exactly the
posture PLAN-21 built and live-validated.

The other shape costs nothing from the wall: `internal/vmmstream` already carries
interactive `exec`/`attach` as per-session NATS subjects with ≤32 KiB frames and
channel-tagged credit flow control. A port-forward is that transport with a
different payload — bytes to a guest-local TCP address instead of to a PTY. The
guest end dials `127.0.0.1:<port>` *inside* the netns, where loopback is explicitly
accepted (`egressnet.go:240`, `iifname "lo" accept`). Nothing inbound is ever
required.

Consequence worth stating plainly: this gives **the operator** access, over the
channel `aped` already authenticates and audits. It does not give a public URL, and
it does not give workspace↔workspace. Those are F2/F3.

### D5 — the guest→host NATS path

```
guest: ape sandbox-agent
  │ TCP → <bridge IP> : <this workspace's proxy port, within 3128-3999>
  │      the one hole that already exists in BOTH walls
  ▼
CONNECT proxy   (in-process in the aped front, bound to the bridge IP)
  │ system-route check → audit record → net.Dial
  │ 127.0.0.1 : <MgmtPort>          loopback, inside the front's own netns
  ▼
embedded NATS server   (same process, still bound to 127.0.0.1)
```

Three pieces:

- **A system route in the proxy.** An exact `host:port` pair checked *before* both
  allowlists. Required, not optional: `ProxyConfig.AllowedPorts` defaults to
  `{"443"}` (`proxy.go:90`) and `egress.go` never overrides it, so a plain
  `CONNECT …:4222` dies at `proxy.go:186` before the domain matcher is consulted.
  Widening `AllowedPorts` instead would let the guest reach *any* allowlisted domain
  on that port — a real widening, and the reason this is a separate concept rather
  than a config change.
- **A NATS custom dialer.** `nats.SetCustomDialer` takes a
  `Dial(network, address) (net.Conn, error)`; ours dials the proxy, writes the
  CONNECT request, waits for `200 Connection Established`, and returns the raw conn.
  NATS then runs its own INFO/CONNECT handshake over it. (NATS's protocol also has a
  verb spelled `CONNECT`; no conflict, the HTTP one completes first.)
- **Wire `--guest-nats-url` in the deploy assets.** A non-empty value is what
  enables per-VM credential injection, which is already built and currently dormant
  (`apedcmd/front.go:91` — `"'' disables per-VM creds"`). `aped-front.service:18`
  carries a comment about this and no setting.

**Why `egress set` cannot delete the route.** `egress set` rewrites the workspace's
`domains` list, and `writeStateLocked(name, domains, port)` persists only that. A
system route constructed from `EgressConfig` has no user-facing surface to remove —
the same exemption shape PLAN-23 used for reserved destinations. It still writes an
audit line, with a reason that distinguishes it (`"system route: agent nats"`), so
it is **visible and filterable, never silently exempt**.

**Two traps to design around, not discover:**

- **Use a sentinel hostname, not a loopback literal.** If the guest is handed
  `nats://127.0.0.1:4222`, that string means the *guest's own* loopback to anything
  that dials it directly instead of through the custom dialer — and it will fail
  silently. Hand it `nats://aped.internal:4222` and make that the system route's
  exact match. Note `ProxyEnv` sets `NO_PROXY=localhost,127.0.0.1`
  (`kata.go:277`) to keep sshd off the proxy; the agent must not be caught by that,
  which is automatic since it uses its own dialer rather than proxy env vars — but
  it is the kind of thing someone later "fixes" without knowing.
- **The bridge segment is plaintext.** `nats-server` is plaintext by default;
  proxy→NATS is loopback within one process, but guest→proxy crosses the host
  bridge unencrypted. Sniffing it requires root on the host, which already owns
  everything, so this is **accepted** — per-VM credentials do the authentication.
  Recorded so it is a decision rather than an oversight.

### D6 — the agent: a subcommand, launched by `aped`

**Form.** `ape sandbox-agent`, per PLAN-18 D6, following the repo convention
(`internal/apecmd/<command>.go`, one command per file, `new<Command>Cmd()`). Not a
separate binary: that would need a goreleaser target, a release-archive slot, a
second verification path in `apebin.go` and lockstep releasing, to deliver something
already delivered — and Go static-linking means an agent carrying the NATS client
would land in `ape`'s size class anyway. "Minimal" is a property of the process
here, not the artifact.

**Launch — this reverses PLAN-18 D6.** D6 says add a best-effort background launch
to the image's `entrypoint.sh` before `exec "$@"`. But `entrypoint.sh` lives in the
separate public `exoport/ape-sandbox` repo, so following it literally costs a
cross-repo change, a new image version, a digest re-pin and a policy update before
the agent can be tested once — and the agent gets no supervision.

Instead `aped` launches it via `task.Exec` after start
(`containerd_driver_linux.go:422-440`), gated on per-VM creds being present
(`APE_NATS_CREDS` + `APE_NATS_URL`). No creds → no agent → the workspace still
boots, which is the property D6 wanted. `aped` re-launches it on every `start` and
if it dies. Trade-off accepted: the agent starts *after* the workload rather than
before, and re-launch is `aped`'s bookkeeping. Revisit the entrypoint only if
something needs the agent running before the workload.

**Two independent safety belts, preserved from D6.** (a) Server authz denies
`ape.vmm.>` to the per-VM credential. (b) The subcommand carries **no
vmm-request-builder code path**, so an agent bug cannot be tricked into issuing a
management verb. The same binary can still be a vmm client on the host, because
capability is credential-scoped at the server rather than compiled in. D6's threat
table stands unchanged: a fully-compromised guest can poison its own telemetry and
read its own scoped creds; it cannot issue `ape.vmm.*`, address another VM, sniff
another VM's replies, or reach the operator.

### D7 — the reaper, and the failure mode that decides its shape

Signal: D6's heartbeat. **Never** `last_used_at` — that is the guess PLAN-22
refused to ship, and replacing the signal rather than shipping the guess is the
whole point.

**The case that must be handled explicitly:** the agent is best-effort, so a
workspace whose agent never started emits nothing — and "emits nothing" is
indistinguishable from "idle" unless the reaper is written to distinguish it.
**"Never seen a heartbeat" must mean *unknown, do not reap*, not *idle*.**
Otherwise every workspace with a broken or absent agent gets stopped at the
threshold, which is a worse failure than the one the reaper exists to fix. D6's
`aped`-supervised launch makes this rare; it does not make it impossible, and the
reaper must not depend on it being impossible.

Action: `stop` only — state preserved, `start` returns it. Never destroy: a wrong
stop costs ~30 s, a wrong destroy costs work. Default 2 h, configurable,
per-workspace opt-out in `.apesandbox.yaml`, and every stop logged with the evidence
that triggered it.

## Deliverables

- [ ] **D1 — Devcontainer how-to.** Closes PLAN-22 D7 (PARTIAL). A dedicated
      how-to under `docs/how-to/`: the caching / offline / pre-warm workflow, and
      **freeze vs stop vs down in one place**. Must also cover what did not exist
      when D7 was scoped — the login-shell environment (PLAN-23 D9; image v1.1.1
      gives login shells the image PATH), the bingo-vs-delivered-`ape` rule, and the
      digest-pin ↔ policy pairing. Mention the per-node pre-pull requirement without
      centring it; it is a fleet concern.
- [ ] **D2 — Private port-forward.** `ape sandbox forward <ws> <port>[:<guest>]`
      over `internal/vmmstream`: a new session kind whose payload is bytes to a
      guest-local TCP address rather than a PTY. Guest end dials `127.0.0.1:<port>`
      inside the netns, where loopback is already accepted. **No netns or host
      firewall change.** Reuse the existing credit-flow framing; reuse `SSHPort` /
      `ssh_port` naming where it fits (`kata.go:97,358`). Ship with a `--ssh`
      convenience or documented recipe, since sshd and `.ssh` already exist in the
      guest and VS Code Remote is the payoff. Multiple concurrent forwards per
      workspace; forwards die with the session, not with the workspace.
- [ ] **D3 — Sandbox-aware cost + reporting.** Teach the cost/reporting commands to
      enumerate workspace staging dirs from the registry (`rec.StagingDir`) and
      attribute rollups per workspace, with `--output-format human|json|yaml` per
      the repo convention. No agent, no wire, no network — independent of D5–D7.
      **Verify first** that a real sandbox Claude session's transcripts land under
      the staging dir in the shape the existing scan expects; that check is what
      decides whether this is an afternoon or a day, and it should be the first
      thing done in this deliverable.
- [ ] **D4 — Node capacity in `Capabilities()`.** Fill the fields
      `workspace.Capabilities` already declares and the containerd driver leaves
      empty — `KVM`, `Mem`, `Factory` — plus cores and **free capacity**, and surface
      them as a command. Deliberately **not** scheduler input: the question being
      answered is "does workspace #6 fit on a 30 GB box where every workspace is a
      real VM at 2–4 GB?". Egress support, cache roots, materialized framework refs
      and the delivered `ape` version are the fleet's version of this and stay
      deferred (F5).
- [ ] **D5 — Guest→host NATS path.** The system route in
      `internal/sandbox/proxy.go` (exact `host:port`, checked before both allowlists,
      audited with a distinguishing reason), constructed by the `EgressSupervisor`
      from `EgressConfig` so no user-facing surface can remove it; the NATS custom
      dialer that speaks HTTP CONNECT; `--guest-nats-url` wired in
      `deploy/systemd/aped-front.service` (replacing the comment at line 18) and in
      `deploy/dev-host.sh`. Sentinel hostname, not a loopback literal. Tests:
      allowlist-bypass attempts still denied, the route survives an `egress set`
      that rewrites every domain, and the audit line is present and labelled.
- [ ] **D6 — `ape sandbox-agent` — heartbeat.** New subcommand in
      `internal/apecmd/`, carrying **no vmm-request-builder code path** (assert this
      with a test, not a comment). Connects with the per-VM creds through D5's
      dialer; publishes liveness plus what is running. Launched by `aped` via
      `task.Exec` after start and re-launched on `start` and on death, gated on
      `APE_NATS_CREDS` + `APE_NATS_URL` both being set. A workspace with no creds
      still boots, with no agent.
- [ ] **D7 — Idle reaper (stop only).** Consumes D6's heartbeat. **"Never seen"
      means unknown, not idle** — test this case explicitly, it is the one that
      would otherwise stop healthy workspaces. Default 2 h → `stop`; threshold
      configurable; per-workspace opt-out in `.apesandbox.yaml`; every stop logged
      with its triggering evidence. Destroy is out of scope by decision. Update
      PLAN-22's "Why no reaper (2026-07-25)" note to point here rather than leaving
      it reading as current.

## Non-goals

- **Public preview URLs, and inbound reachability for anyone but the operator.**
  D2 is a private forward over an authenticated channel. Public exposure needs
  wildcard DNS, TLS, a reverse proxy and lifecycle registration — F3.
- **Overlay networking.** F2, with its shape already decided (host as routing peer;
  see Deferred).
- **Live telemetry streaming.** F1. D3 delivers the rollups that matter; live
  streaming is a refinement of something that will already work.
- **Destroying anything automatically.** The reaper stops. Disk is reclaimed by a
  human.
- **Any change to another repository.** No image bump, no digest re-pin, no policy
  version churn. This is a hard constraint on D6's shape, not a preference.
- **Scheduling, placement, or multi-node anything.** D4 is capacity reporting for a
  human, not scheduler input.

## Deferred — with the event that unparks each

Recorded here so they are not re-litigated inside this plan's scope. Designs are
kept where they were already settled.

| ID | Item | Unpark trigger |
| --- | --- | --- |
| F1 | Live telemetry from the agent (`ape.{evt,log,metrics}.vm-<id>.>`, two hats over one per-VM credential — PLAN-18 D6) | D3's after-the-fact rollups prove insufficient. Needs D5+D6. |
| F2 | Netbird overlay — **shape pre-decided:** the *host* joins as a routing peer advertising the workspace subnet, guest runs no agent, gets no UDP and no DNS, keeps its egress wall. Narrow changes only: allow forwarding from the overlay interface across the bridge, accept the overlay source in the netns `input` chain. | A second human needs into a workspace. |
| F3 | Public preview environments — wildcard DNS + TLS + reverse proxy + register/unregister on lifecycle. **Netbird alone does not give public URLs.** | F2 done *and* someone outside needs a URL. Needs D7 for auto-idle-stop. |
| F4 | Manual multi-node — **needs no code**: `ape sandbox --node box2 --nats-url nats://box2:4222` already works (`apecmd/sandbox.go:76`), provision with `deploy/tier2-setup.sh`. What is missing is a how-to and credential distribution, since each `aped` self-mints its own creds. | You want more capacity than one box gives. |
| F5 | Fleet — hub + controller, scheduler-grade `Capabilities()`, per-node digest pre-pull, cross-node `ls`. **The blocker is not hardware:** host-fs mounts pin a workspace to the disk holding its repo, so elasticity needs a shared-storage decision first. | You stop wanting to pick the node by hand. |
| F6 | GPU + USB device tier (PLAN-18 D5) — copy PLAN-20's request-∩-node-table pattern; PLAN-18 flags upstream cold-plug as fragile, so validate on hardware before committing the design. | A discrete-GPU box *and* an `intel_iommu=on` reboot. `mmq4` has neither. |
| F7 | Per-tenant cache + credential isolation — `/cache/*` is node-wide shared and the published OAuth session is single-operator, both by design. | A second tenant exists. |
| F8 | Image-pin generation + multi-arch image — a test already guards pin drift *in this repo*, so exposure is fleet-side; published image is `linux/amd64` only. | Node #2 (pin) · an arm64 **Linux** host (arch). |

## Closed — settled, with the reason, so they are not reopened

| Thing | Why |
| --- | --- |
| Firecracker dense tier (PLAN-18 D8) | **Architecturally incompatible with the product.** PLAN-18 itself records no PCI/VFIO and **no host-fs**, and admission-time rejection of `Mount: host-fs`. The product has since become mounts: framework, `/opt/ape/bin`, `/cache/*`, composed home, `/workspace/<name>`. None of it ports. Not a fact to verify. |
| Bridge-IP NATS listener + a new firewall hole | What PLAN-16 Ph2 / PLAN-18 D6 assumed. Superseded by D5, which reaches the same endpoint through the hole that already exists. |
| A separate agent binary | Would need its own goreleaser target, archive slot, verification path and lockstep release, to deliver something already delivered. |
| Agent launch from the image `entrypoint.sh` | PLAN-18 D6's literal shape. Costs a cross-repo change, a new image version, a digest re-pin and a policy update before one test — and gets no supervision. Revisit only if the agent must start before the workload. |
| Host-CPU-only reaper (`task.Metrics()`) | Viable — a reaper with no agent and no network change. **Rejected in favour of the heartbeat** (owner, 2026-08-05), which knows what is running rather than inferring it. Recorded because it was real, so the choice stays visible. |
| mtime / `last_used_at` reaper | Records exec/attach/start only, so a long build with nobody attached looks idle. D7 replaces the signal. |
| Netbird agent *inside* the guest | Enrolling needs outbound UDP, DNS and reachability to Netbird's coordination servers; the guest has none of the three. F2's host-routing-peer shape replaces it. |

## Dependencies

- **D1, D2, D3, D4 are independent** of each other and of the chain. Any order, or
  in parallel.
- **D5 → D6 → D7** is strict.
- Nothing here depends on another repository, a second node, or hardware.
- Builds on: PLAN-20 (mount model, reserved destinations, system-entry exemption
  shape), PLAN-21 (the proxy, the audit trail, the two walls), PLAN-22 (`stop`/
  `start`, `last_used_at`, `.apesandbox.yaml`), PLAN-23 (`ape` delivered into the
  guest and build-info-verified — which is what makes D6 free), PLAN-18 D2
  (`vmmstream`, which D2 extends) and D6 (the agent's design and threat model).

## Risks

- **D3's cheapness is unverified.** Transcripts are demonstrably written to a host
  directory; that they land in the *shape* the existing cost scan expects is not
  confirmed. If the layout differs, D3 grows a normalization step. Verify before
  sequencing it as small.
- **D7 inherits the agent's reliability.** The reaper is only as honest as the
  heartbeat. The "never seen" rule is what keeps a bad agent from becoming a bad
  reaper, and it must be tested, not assumed.
- **D5 widens what a guest can reach, by exactly one endpoint.** That is the point,
  and it is why the route is exact-match, system-owned, and audited with a
  distinguishing reason. The review question to ask is whether the audit line is
  actually distinguishable in practice.
- **D2 adds a new session kind to `vmmstream`.** The credit-flow protocol is live and
  carries `exec`/`attach`; a new payload type must not perturb them. Regression-test
  the existing kinds, not just the new one.
- **D6's exec-launch is re-run on `start`.** Restart paths are where lifecycle bugs
  live (PLAN-22 paid for reboot recovery). Cover `stop`→`start`, host reboot, and
  agent death.

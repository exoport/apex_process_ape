# 2026-07-11 — aped: Phase-2 limitations resolved + polish

Continuation of [2026-07-11-aped-autonomous-session.md](2026-07-11-aped-autonomous-session.md)
(which built + live-validated the full PLAN-18 Phase-2 backlog and the
exec/attach streaming feature). That session ended with two documented
limitations and three optional-polish ideas. This session resolves all of them.

Branch `feat/plan-18-phase2-aped`, starting from `5ff21a1`. All commits are
**local**; nothing pushed/tagged/merged. Exit gate run green before every
commit: `make lint`, `make test`, `make xcompile-windows`, `make docs-check`,
`make snapshot`. `govulncheck` is expected-RED on 3 pre-existing base vulns
(GO-2026-5932 openpgp no-fix; 5856 + 4970 cleared by a go1.26.5 bump) — none
from the containerd dep; not run as a gate (see the `ci-local-govulncheck-preexisting`
memory).

## Commits (5, oldest first)

| SHA | Summary |
| --- | ------- |
| `5021461` | `feat(vmmstream): reap abandoned interactive sessions` — limitation 2 |
| `d579f1e` | `feat(aped): make host-fs mounts work under ProtectHome` — limitation 1 |
| `4f0fe52` | `feat(sandbox): exec forwards the guest's exact exit code` — polish (a) |
| `bf09236` | `feat(aped): forward an interactive session-completion audit record` — polish (b) |
| `de195be` | `chore(deploy): stage the tier2 probe buildkit-free into the aped namespace` — polish (c) |

21 files, +573/−88.

## Limitation 2 — abandoned-session teardown (`5021461`)

A dropped attach client (network drop / `kill -9`) leaked a live guest exec: the
front relayed on `context.Background()` forever and the executor's containerd
exec was never killed, because core NATS gives a publisher **no** signal that its
subscriber vanished.

**Detection (transport, `internal/vmmstream`).** New `ControlPing` frame; the
client `Attach` pings the shared control subject every `KeepaliveInterval` (15s).
The server `ServerSession` runs an idle watchdog that reaps the session if **no**
inbound control traffic — a ping OR a credit grant, both of which prove a live
client — arrives within `IdleTimeout` (45s). A live-but-quiet shell stays warm on
keepalive pings, so only a truly vanished client trips it. `idleTimeout` is an
unexported field so tests can shrink it.

**Reaction (both ends).** `workspace.Process` gains `Kill(ctx)`.
- Executor (`internal/aped/stream.go`): the relay's inbound reader Kills the
  guest exec (`containerdProcess.Kill` → `SIGKILL`) when the priv conn drops; the
  signal makes the exec exit, so the pending `Wait` reaps it via the normal drain
  + `Delete`. In the normal flow the Kill lands on an already-exited exec (benign
  not-found).
- Front (`connProcess.Kill`): the watchdog cancels the session; Kill finishes the
  output pipes (so the pumps unblock rather than parking on a Read with no
  producer) and drops the priv conn (which triggers the executor-side kill).

**Tier-1:** `TestRelayKillsProcessOnConnDrop` (executor reaps on conn drop) +
`TestServerSessionIdleTimeout` (front watchdog reaps a client that never
attached). The fake priv-conn pair now models SEQPACKET peer-close so a hangup on
one side surfaces as EOF on the other. `-race -count=3` green.

## Limitation 1 — host-fs mounts under ProtectHome (`d579f1e`)

The default `ape sandbox up --mount host-fs` of the cwd failed from `/home` with a
raw `lstat …: permission denied`: both aped units set `ProtectHome=yes`, so
`/home` and `/root` are invisible to the daemon — it cannot even canonicalize the
path for the policy check. Resolved **without weakening ProtectHome**:

- **Error (`internal/aped/policy.go`).** `checkMount` now detects a source under a
  masked root (`/home`, `/root`) or a permission failure and returns actionable
  guidance (`… is not reachable by aped … ProtectHome=yes …`) instead of a bare
  lstat error. Tier-1: `TestPolicyMountProtectHomeHint`.
- **A default that works.** `deploy/tier2-setup.sh` `MOUNT_ROOT` defaults to
  `/srv/workspaces` (outside the mask → no unit changes), creates it, and wires it
  into policy `mount_roots`; it warns + points to the drop-in when a masked root is
  chosen. `deploy/policy.yaml` drops the dead `/home` entry for the working default
  with an explanatory note.
- **Escape hatch.** `deploy/systemd/aped.service.d/mount-root.conf.example` — a
  `BindReadOnlyPaths=` drop-in exposing one home subdir to the daemon's `lstat`
  (the Kata `virtiofsd`, a separate service, does the guest I/O) without unmasking
  `/home`. Install into **both** units' `.d/` dirs.
- **Docs.** `run-aped.md` gains a "Mounting your project under ProtectHome" how-to
  (three recipes); `sandbox-workspaces.md` cross-links it; `events.md` unchanged
  here.

## Polish

- **(a) exec exit code (`4f0fe52`).** `ape sandbox exec` exited 1 on any non-zero
  guest code. It now exits with the guest's exact status (ssh-style):
  `exitCodeError` carries the code in an `*exitError`; `main` routes `Execute`'s
  error through the new `apecmd.ExitCode(err) → (code, silent)`; the error is
  silent (no extra `Error:` line — the guest already streamed its output) and
  defers still run (returned, not `os.Exit`'d mid-RunE). Tier-1:
  `TestExitCodeError`.
- **(b) completion audit (`bf09236`).** Only the exec/attach OPEN reached
  `ape.audit.<node>.>`. The front now forwards a correlated `<op>Exit` completion
  record when the session's `Run` returns — the exit code, or the teardown error
  when the session was reaped. The executor-attested open (SO_PEERCRED peer +
  policy) stays authoritative; `completionAudit` derives the notice from it since
  the network-less executor can't publish post-handshake. Tier-1:
  `TestFullStackAttachStream` now asserts the `AttachVMExit` record reaches a
  HOST_OPS consumer.
- **(c) tier2 probe (`de195be`).** Dropped the fragile `nerdctl build` + buildkit
  path; the probe now stages buildkit-free (`nerdctl commit` + `images unpack`)
  straight into the `aped` namespace (the validated `--driver containerd` path).
  The printed acceptance command now runs `TestTier2ProvisionContainerd`.

## LIVE-VALIDATION QUEUE (Tier-2, needs the operator — no sudo in-session)

Redeploy per the `aped-live-validation-workflow` memory (rebuild BOTH `ape` +
`aped`, socket-first restart, re-copy the operator cred). Then:

1. **Limitation 2 — teardown.** With a containerd-driver node + a workspace up:
   - `ape sandbox attach dev &` then `kill -9 %1` (or drop the network). Within
     ~45s confirm the guest exec is gone (no lingering `ape-exec-*` in
     `ctr -n aped … tasks`/`processes` for the workspace) and the front didn't
     wedge.
   - Leave an `ape sandbox attach dev` **idle** > 60s and confirm it stays alive
     (keepalive pings feed the watchdog) — no spurious teardown.
2. **Limitation 1 — host-fs.** (i) `ape sandbox up dev --cwd /srv/workspaces/dev`
   succeeds through the hardened unit; (ii) from a `/home/...` cwd the error now
   reads `… is not reachable by aped … ProtectHome=yes …`; (iii) install the
   `mount-root.conf.example` drop-in on both units for a `/home` subdir, add it to
   `mount_roots`, and confirm a host-fs mount of that subdir works.
3. **Polish (a).** `ape sandbox exec dev -- sh -c 'exit 7'; echo "ape exit=$?"` →
   `ape exit=7` (was 1).
4. **Polish (b).** Subscribe `ape.audit.<node>.>` on a HOST_OPS cred, run an
   exec/attach, confirm both the open (`ExecVM`/`AttachVM`) and the completion
   (`ExecVMExit`/`AttachVMExit`, with the exit outcome) arrive.
5. **Polish (c).** Re-run `tier2-setup.sh` (or just its probe block); confirm the
   probe stages into the `aped` namespace with no buildkit, and
   `TestTier2ProvisionContainerd` passes.

## State / next

- Clean tree; 5 gates green; everything **local** on `feat/plan-18-phase2-aped`.
- **Push/release decision is the operator's.** Phase-2 is complete + (prior
  session) live-validated, so pushing to `main` + cutting a release is a real
  option — default is to KEEP LOCAL until the operator says otherwise.
- Phase 3 (device/GPU passthrough tier) stays BLOCKED (no discrete-GPU box).

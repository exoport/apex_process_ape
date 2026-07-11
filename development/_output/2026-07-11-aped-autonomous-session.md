# 2026-07-11 — aped autonomous session (PLAN-18 Phase-2 backlog)

Branch `feat/plan-18-phase2-aped`, starting from `fa97dac`. All commits are
**local**; nothing pushed/tagged. Exit gate for every commit: `make lint`,
`make test`, `make xcompile-windows`, `make snapshot`, `make docs-check` — all
green (govulncheck is expected-RED and not run as a gate).

**govulncheck (diligence on the containerd dep):** now **3** base vulns, up from
2 — the third, **GO-2026-4970** (stdlib `os` root-escape, **fixed in go1.26.5**),
is freshly disclosed and reached via **pre-existing** `internal/web` code. The
`aped` containerd-driver dep tree (containerd/v2 + grpc + otel + image-spec,
linux-only) added **ZERO** new findings — all 3 traces route through
stdlib/`internal/web`/`update.go`, none through containerd. A go1.26.5 toolchain
bump clears 2 of the 3 (5856 + 4970); openpgp (5932) still has no upstream fix.
See the `ci-local-govulncheck-preexisting` memory.

## Backlog status

| # | Item | State |
| - | ---- | ----- |
| 1 | Audit NATS forwarding on `ape.audit.<node>.>` | ✅ committed (Tier-1) |
| 2 | Clean up `docs/how-to/sandbox-workspaces.md` | ✅ committed (docs) |
| 3 | sd_notify + Type=notify units | ✅ committed (Tier-1; live-validate queued) |
| 4 | Operator-creds stability | ✅ committed (Tier-1) |
| 5 | Non-device `containerdDriver` (opt-in) | ✅ committed (Tier-1; live-validate queued) |
| 6 | Interactive exec/attach streaming | ✅ committed (Tier-1 scaffold) |

## Commit log

### 1) `feat(aped): forward audit records on ape.audit.<node>.>` — `796e967`

The network-less executor (holds no NATS listener) now returns the audit
record(s) it emits per command in the priv `Response.Audit`; the de-privileged
front forwards each on `ape.audit.<node>.<event>` as it round-trips. Local
append-only file sink unchanged. `serviceGrant` already permits the `ape.audit`
root within HOST_OPS; `VMGrant` already denies it to TELEMETRY — no authz change.

- Files: `command.go` (Response.Audit), `audit.go` (Record returns stamped rec +
  shared `auditSubject`), `exec.go` (dispatch/doCreate/mutate return records;
  handleConn attaches), `privclient.go` (`PrivClientConfig{Publish,Node}` +
  `forwardAudit`), `front.go` (wires `nc.Publish`).
- **Tier-1 verified:** `TestFullStackAuditForwarding` — drives create + mutate +
  policy-denied create through the real NATS→vmm→priv→executor stack (fake
  backend, no containerd) and asserts records arrive with resolved args, policy
  decision, and outcome. `go test -race ./internal/aped/` green.
- The SO_PEERCRED-reject record stays **file-only** by design (a rejected peer is
  never the front, so it is not handed the audit trail).

### 2) `docs(sandbox): rewrite sandbox-workspaces for the aped-client reality` — `1619df5`

Pure docs. `docs/how-to/sandbox-workspaces.md` rewritten for the PLAN-18
aped-client model (retired daemonless-runner framing, real client flags/verbs,
networkless Phase-2, executor-sandbox known limitation cross-linking
run-aped.md). docs-check green. No live validation needed.

### 3) `feat(aped): sd_notify READY=1 + watchdog; Type=notify units` — `5367e51`

Both aped processes signal `READY=1` once serving + `WATCHDOG=1` at
`WatchdogSec/2`; `STOPPING=1` on drain. Units switched to `Type=notify` +
`WatchdogSec=30s`. All no-ops without `$NOTIFY_SOCKET`.

- Files: `notify.go` (new — sdNotify/notifyTo/watchdogInterval/startWatchdog,
  no build tag, Windows-safe), `run.go`/`front.go` (signalReady/Stopping),
  `deploy/systemd/{aped,aped-front}.service` (Type=notify + WatchdogSec),
  plan-18 Appendix-A note refresh.
- **Tier-1 verified:** `notify_test.go` (datagram send against a real AF_UNIX
  DGRAM listener + WATCHDOG_USEC/PID decision). Both units pass
  `systemd-analyze verify` (exit 0) locally.

### 4) `feat(aped): reuse the operator credential across restart` — `fb165ce`

aped-front re-minted the operator `.creds` every restart (churning the human's
copy). It now reuses a persisted-valid cred (issuer + unexpired + node scope),
minting only when missing/foreign/wrong-node/corrupt. Sound because the account
seed persists (`StartServer` StoreDir).

- Files: `mint.go` (`Account.reusableOperatorCreds`), `front.go`
  (`ensureOperatorCreds`; logs `minted`/`reused`), `run-aped.md` (reuse note +
  socket-first restart section).
- **Tier-1 verified:** `front_test.go` — reuse byte-identical across a restart;
  foreign account / changed node / corrupt file each re-mint.

### 5) `feat(aped): opt-in containerd driver (barrier-3-free provisioning)` — `f2c68aa`

The barrier-3 fix, opt-in behind `aped run --driver containerd` (default stays
shellDriver). The containerd v2 Go client builds the OCI spec via
`applyImageConfig` — process user/env/args/cwd from the content-store image
config, **no rootfs mount** — so `ape sandbox up` can work through the hardened
executor. Deps (containerd/v2 v2.3.3 + image-spec) are linux-only in goreleaser;
xcompile-windows + snapshot stay green.

- Files: `internal/sandbox/imagespec.go` (+test), `containerd_driver.go`,
  `containerd_driver_linux.go`, `containerd_driver_other.go` (stub),
  `internal/aped/run.go` (`buildDriver` + `--driver`), `internal/apedcmd/run.go`
  (flags), run-aped.md + plan-18 updates.
- **Tier-1 verified:** `imagespec_test.go` (spec projected with ZERO mounts,
  numeric-user only, networkless) + `run_test.go` (driver selection; unknown
  fails closed). Full race suite (24 pkgs) green; xcompile-windows + snapshot green.
- **NOT live-validated:** the full lifecycle through the containerd client
  (create/exec/freeze/destroy on a real Kata VM) — queued below.

### 6) `feat(vmmstream): exec/attach framing + credit flow-control scaffold` — `c8c78ac`

The PLAN-18 D2 interactive-stream transport primitives: `internal/vmmstream`
(SessionSubject + channels, ≤32 KiB Chunks, ControlFrame codec, a
ctx-cancellable CreditWindow, and a Sender/Receiver pair).

- **Tier-1 verified:** pure codec/credit unit tests + a loopback nats-server
  integration test pushing a >5-frame payload through a 2-frame credit window
  (in-order reassembly, no deadlock) — end-to-end flow control. `-race` green.
- Scaffold only: nothing imports it yet (ape/aped unchanged); binding the server
  end to a containerd task PTY is the live-validated follow-on.

## VALIDATION QUEUE (steps needing root / live Tier-2 — hand to operator via `! sudo bash <script>`)

Redeploy recipe (socket-first restart — see `aped-live-validation-workflow`
memory): rebuild, `install -m0755 ./aped /usr/local/bin/aped`, then
`systemctl restart aped-priv.socket` → `systemctl start aped.service
aped-front.service`, then re-copy `/var/lib/aped/creds/operator.creds` to the
operator path.

- **Item 4 (operator-cred reuse), live:** after redeploy, confirm the front logs
  `operator creds: … (reused; …)` on the **second** restart (and `minted` on the
  first / after a state-dir reset), and that `~/.config/ape/aped-operator.creds`
  does **not** need re-copying between restarts:
  ```bash
  journalctl -u aped-front --since "1 min ago" | grep "operator creds"   # expect "reused" after 1st start
  # confirm the file is unchanged across a restart:
  sudo sha256sum /var/lib/aped/creds/operator.creds   # note it, restart socket-first, compare
  ```

- **Item 5 (containerd driver), live — EASIEST PATH (gated in-process test):**
  no systemd reconfig; validates the barrier-3-free Provision + full lifecycle
  through the containerd Go client. Needs `/dev/kvm` + running containerd + Kata
  + a pullable long-lived image (the `deploy/tier2-setup.sh` probe or the
  ape-sandbox image):
  ```bash
  # (operator, on the KVM+containerd+Kata box, from the repo root)
  sudo APE_APED_IT=1 APE_APED_IT_IMAGE=ape-tier2-probe:latest \
    /usr/local/go/bin/go test ./internal/aped/ -run TestTier2ProvisionContainerd -v
  # PASS = create/exec/freeze/unfreeze/destroy all worked via the containerd driver.
  ```
  Script: `scratchpad/validate-item5-containerd-test.sh`.

- **Item 5 (containerd driver), live — FULL end-to-end through the deployed daemon:**
  proves `ape sandbox up` works through the HARDENED unit with `--driver
  containerd` (the whole point — the shellDriver still dies at barrier 3 here).
  Uses a systemd drop-in so the shipped unit is untouched:
  ```bash
  sudo bash scratchpad/validate-item5-driver-e2e.sh   # installs drop-in, restarts socket-first
  # then, as the operator (non-root):
  export APE_NATS_URL=nats://127.0.0.1:4223 APE_NATS_CREDS=~/.config/ape/aped-operator.creds
  ape sandbox up dev --node "$(hostname)" --image ape-tier2-probe:latest --mount ephemeral
  ape sandbox exec dev --node "$(hostname)" -- true && echo "EXEC OK (barrier 3 cleared!)"
  ape sandbox inspect dev --node "$(hostname)"   # live state via containerd task
  ape sandbox down dev --node "$(hostname)"
  # revert the drop-in when done: sudo rm -rf /etc/systemd/system/aped.service.d/10-driver.conf && sudo systemctl daemon-reload
  ```

- **Item 3 (Type=notify units), live:** after installing the updated units +
  redeploying the `aped` binary, confirm systemd sees `READY=1` and the watchdog:
  ```bash
  # (operator) install updated binary + units, then:
  systemctl daemon-reload
  systemctl restart aped-priv.socket
  systemctl start aped.service aped-front.service
  systemctl show aped.service       -p Type -p WatchdogUSec -p NNotifyAccess -p ActiveState -p SubState
  systemctl show aped-front.service -p Type -p WatchdogUSec -p ActiveState -p SubState
  # Expect Type=notify, WatchdogUSec=30s, ActiveState=active, SubState=running.
  # Then confirm no watchdog restarts over ~1 min:
  journalctl -u aped.service -u aped-front.service --since "2 min ago" | grep -i watchdog || echo "no watchdog trips (good)"
  ```
  Then re-copy the operator cred (see below). Risk if it fails: a unit that never
  gets READY=1 will be killed at TimeoutStartSec — check `systemctl status` shows
  `Active: active (running)`, not `activating`/`failed`.

- **Item 1 (audit forwarding), live:** after redeploy, subscribe
  `ape.audit.<node>.>` on the operator cred's account and drive a `create`/
  `inspect`; confirm records arrive. NOTE the operator cred (`OperatorGrant`)
  currently subscribes only `_INBOX.>` + `$SRV.>`, so a *dedicated* audit
  consumer needs a HOST_OPS cred with `ape.audit.<node>.>` in its sub-allow (or
  the `serviceGrant` shape). Tier-1 proves the publish leg; live just confirms
  the deployed front actually forwards. Blocked in practice by the `ape sandbox
  up` executor-sandbox gap (create still fails through the hardened unit) — the
  read-only verbs don't emit audit records, so a full live audit check waits on
  item 5 (containerdDriver) or the gated in-process `TestTier2Provision`.

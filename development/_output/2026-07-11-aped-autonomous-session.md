# 2026-07-11 — aped autonomous session (PLAN-18 Phase-2 backlog)

Branch `feat/plan-18-phase2-aped`, starting from `fa97dac`. All commits are
**local**; nothing pushed/tagged. Exit gate for every commit: `make lint`,
`make test`, `make xcompile-windows`, `make snapshot`, `make docs-check` — all
green (govulncheck is expected-RED on 2 pre-existing base vulns; not run as a
gate — see `ci-local-govulncheck-preexisting` memory).

## Backlog status

| # | Item | State |
| - | ---- | ----- |
| 1 | Audit NATS forwarding on `ape.audit.<node>.>` | ✅ committed (Tier-1) |
| 2 | Clean up `docs/how-to/sandbox-workspaces.md` | ✅ committed (docs) |
| 3 | sd_notify + Type=notify units | ✅ committed (Tier-1; live-validate queued) |
| 4 | Operator-creds stability | ⏳ in progress |
| 5 | Non-device `containerdDriver` (opt-in) | ⬜ pending (stretch) |
| 6 | Interactive exec/attach streaming | ⬜ optional |

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

## VALIDATION QUEUE (steps needing root / live Tier-2 — hand to operator via `! sudo bash <script>`)

Redeploy recipe (socket-first restart — see `aped-live-validation-workflow`
memory): rebuild, `install -m0755 ./aped /usr/local/bin/aped`, then
`systemctl restart aped-priv.socket` → `systemctl start aped.service
aped-front.service`, then re-copy `/var/lib/aped/creds/operator.creds` to the
operator path.

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

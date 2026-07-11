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
| 2 | Clean up `docs/how-to/sandbox-workspaces.md` | ⏳ in progress |
| 3 | sd_notify + Type=notify units | ⬜ pending (live-validate) |
| 4 | Operator-creds stability | ⬜ pending (investigate) |
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

## VALIDATION QUEUE (steps needing root / live Tier-2 — hand to operator via `! sudo bash <script>`)

Redeploy recipe (socket-first restart — see `aped-live-validation-workflow`
memory): rebuild, `install -m0755 ./aped /usr/local/bin/aped`, then
`systemctl restart aped-priv.socket` → `systemctl start aped.service
aped-front.service`, then re-copy `/var/lib/aped/creds/operator.creds` to the
operator path.

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

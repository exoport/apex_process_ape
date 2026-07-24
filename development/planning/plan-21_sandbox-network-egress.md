---
plan_id: PLAN-21
created_at: 2026-07-23
status: proposed
tags:
  - sandbox
  - aped
  - network
  - egress
  - security
summary: >
  Give `ape sandbox` Kata workspaces deny-by-default, audited network egress to
  an allowlisted set of hosts (github.com, package registries) WITHOUT
  re-granting network privilege to aped's hardened root executor. Workspaces are
  `--network none` today because the executor is empty-caps + AF_UNIX-only and
  cannot run CNI ("barrier 2"). The CONNECT proxy + domain allowlist + egress
  audit + PlanEgress are already code-complete and Tier-1 tested but UNWIRED. The
  path: run the existing proxy in the de-privileged aped front (which already
  holds AF_INET on the host↔guest bridge) and add ONE narrow privileged
  netns/nft helper (separate from the executor) that wires a per-VM netns with an
  nft "only reach the proxy" wall. Effort is L, concentrated in that helper +
  live validation; a non-enforced "honest boundary" variant (bridge + proxy env,
  no nft) is ~M. Prerequisite for real dev work and for PLAN-22 toolchains
  (dependency downloads, git clones, research).
origin:
  - 2026-07-23 scoping (agent-assisted, cited file:line) — networkless workspaces
    block dependency downloads, git clones, and research; the user flagged this as
    the real usability blocker.
  - 2026-07-23 root-cause reading — the split's charter forbids widening the executor
    to run CNI (`docs/how-to/run-aped.md:282-284`, `plan-18:939-941`), so egress must
    come from a separate privileged actor, not the executor.
  - Assumptions marked inline were made at authoring time; flag at review.
---

# PLAN-21: Sandbox network egress (allowlisted, deny-by-default)

## Goal

A workspace can reach an **allowlisted** set of hosts (e.g. `github.com`,
package registries) over HTTPS — **deny-by-default and audited** — without
weakening aped's "root without power" executor. Not full L3, not a private
overlay (that is Netbird / Phase 4).

## Root cause — why it's networkless today

- The resolver hard-defaults every spec to `--network none`
  (`internal/aped/resolver.go:61-64,108`), deliberately: nerdctl/CNI runs
  client-side and needs `CAP_NET_ADMIN`/`CAP_NET_RAW`, `AF_NETLINK`,
  `CLONE_NEWNET`, and `@mount` (`internal/sandbox/kata.go:38-45`).
- aped's root **executor denies all four** — empty
  `CapabilityBoundingSet`/`AmbientCapabilities` (`deploy/systemd/aped.service:32-33`),
  `RestrictAddressFamilies=AF_UNIX` + `IPAddressDeny=any` (`aped.service:69-70`),
  `RestrictNamespaces=yes` (`:66`), `SystemCallFilter=~@mount` (`:79`). This is
  "barrier 2" (`docs/how-to/run-aped.md:270-273`, `plan-18:921-925`), and
  widening the unit is explicitly forbidden by the split's charter.
- So the blocker is not "write networking" — it is "who creates the per-VM netns
  + interfaces, with what privilege," since the executor cannot.

## What already exists (reuse — do not rebuild)

Code-complete, Tier-1 tested, **unwired** from the live path:

- **CONNECT proxy + audit** — `internal/sandbox/proxy.go` (deny-by-default, never
  decrypts TLS, per-tunnel `egress-audit.jsonl`).
- **Domain allowlist matcher** — `internal/sandbox/match.go` (exact + leading
  wildcard, deny-by-default).
- **Planner / supervisor** — `internal/sandbox/proxysup.go` (`PlanEgress`,
  `RunProxyDaemon`, `ProxySupervisor`, `ProxyState`).
- **Proxy-env injection** — `ProxyEnv` (`kata.go:184-193`), already consumed on
  both driver paths. Gap: the resolver never sets `WorkspaceSpec.HTTPSProxy`.
- **Profile schema** — `NetworkPolicy.AuthorizedDomains` / `.DirectAllow`
  (`internal/sandbox/profile.go:140-147`), parsed + validated.
- **Netns toggle** — `applyNetworkless` already supports *un*-setting the netns
  (`imagespec.go:102-104`); `BuildSpec` gates it (`spec.go:157-160`).

**Genuinely missing:** any netns/veth/tap/bridge/nft/netlink programming (none
exists), the privileged actor to run it, the resolver setting `HTTPSProxy`, aped
actually starting a proxy, and egress keys in `deploy/policy.yaml`.

## Approaches (given the AF_UNIX-only, empty-caps executor)

Kata's shim enters the pod netns and taps whatever interfaces it *finds*; it
does **not** create the netns/veth/bridge/routes/NAT — a privileged actor must
do that first. So the question is *who*, with what privilege.

| Approach | Fits hardening? | Effort | Tradeoff |
| --- | --- | --- | --- |
| **(a) user-mode net (passt/slirp) as a de-privileged sidecar** | Yes (needs `AF_INET`, so a separate sidecar like the front, not the executor) | M–L | Kata-integration risk on the passt↔virtio-net seam; allowlist still rests on the proxy unless further restricted |
| **(b) separate privileged netns/nft helper** (NOT the executor) that pre-wires per-VM netns + veth/route + nft wall; executor just references the netns path | **Yes — design-aligned** (mirrors the planned VFIO-bind helper, `plan-18:542-543`) | **L** (full) / **M** (shared-bridge+proxy-only) | a new narrow privileged unit to build/harden/lifecycle |
| (c) relax executor caps to run CNI | **No — rejected** by charter | S to change, but | sacrifices the whole "root without power" model |
| (d) containerd/CNI via CRI | No — CNI still client-side / needs a priv actor | L | heavy new machinery; collapses into (b)/(c) |
| (e) Netbird overlay | Orthogonal — **private** mesh, not public egress | L+ | doesn't satisfy this goal; separate workstream |

## Recommended path — (b), minimized

Reuse the CONNECT proxy; add the smallest privileged netns wired by a new narrow
helper. **Key simplification:** make `HTTPS_PROXY` an **IP:port** on the
host↔guest bridge (an address the front already permits,
`aped-front.service:59`). Then the guest needs **no DNS** and a route to exactly
one IP:port (the proxy resolves each CONNECT hostname), and the nft wall reduces
to "allow established + new → proxyIP:port, drop the rest."

## Deliverables

- [ ] **D1 — Resolver + policy wiring (S).** Thread the profile's
  `AuthorizedDomains` through; set `WorkspaceSpec.HTTPSProxy`; add egress keys to
  `deploy/policy.yaml` (allow/deny defaults, per-profile domain caps).
- [ ] **D2 — aped front runs the proxy (S).** Run `RunProxyDaemon`/`NewProxy`
  in-process in the de-privileged front, bound to the bridge IP, per-VM lifecycle
  (start at Create, stop at Destroy), audit to the per-VM NATS telemetry subject.
- [ ] **D3 — NEW privileged netns/nft helper (L — the effort driver).** A narrow
  root unit (only `CAP_NET_ADMIN` + `AF_NETLINK`, `RestrictNamespaces` relaxed to
  net, `@mount` only for the netns bind) that, on a typed command from the
  executor over the AF_UNIX boundary, creates the per-VM netns + veth-to-bridge +
  route + nft "only reach the proxy" wall and returns the netns path. Teardown on
  Destroy.
- [ ] **D4 — Resolver flip (S).** Stop defaulting egress-enabled profiles to
  `NetworkNone`; attach the netns path; flip `Networkless` off.
- [ ] **D5 — Tier-2 live validation (M).** On a KVM+containerd+Kata host: allow +
  deny + audit rows; confirm the guest is *forced* through the proxy (closes the
  "honest boundary" gap, `plan-16:138`).

## Effort

**L overall (multi-week)**, concentrated in **D3** (the netns/nft helper — new,
no reuse) and **D5** (live validation). D1/D2/D4 are ~S each (reuse). **Fallback
~M:** drop the nft wall (shared bridge + proxy env only) — egress works but the
allowlist becomes a non-enforced "honest boundary," a real security downgrade;
interim only.

## Non-goals

- Full L3 to arbitrary hosts (deny-by-default allowlist only).
- DNS inside the guest (the proxy does hostname resolution).
- Netbird / private overlay (PLAN-18 Phase 4 / platform repo).
- Re-granting network capabilities to the executor (charter-forbidden).

## Related

- **PLAN-22** (toolchains) — depends on this for the initial toolchain/dependency
  fetch (`asdf`/`bingo`/registries); offline-after-warm via cached mounts.
- **PLAN-20** (mounts) — orthogonal; the framework mount is deliberately network-free.
- **PLAN-18** (`ape`/`aped` split) — the executor hardening this plan must respect.

---
plan_id: PLAN-21
created_at: 2026-07-23
status: done
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

- [x] **D1 — Resolver + policy wiring (S).** DONE 2026-07-24. Thread the requested
  `authorized_domains` through — from the profile **and** the project's
  `.apesandbox.yaml` `egress:` section (PLAN-20's descriptor) — set
  `WorkspaceSpec.HTTPSProxy`; add egress keys to `deploy/policy.yaml` (allow/deny
  defaults, per-profile domain caps). The `.apesandbox.yaml` domains are a
  **request**: aped intersects them with the policy's allowed set (a project can
  narrow, never widen, what policy permits).
- [x] **D2 — aped front runs the proxy (S).** DONE 2026-07-24 (in-process
  `EgressSupervisor`, per-workspace port from the nft-permitted range, audit to
  both the JSONL trail and `ape.audit.<node>.egress`). Run `RunProxyDaemon`/`NewProxy`
  in-process in the de-privileged front, bound to the bridge IP, per-VM lifecycle
  (start at Create, stop at Destroy), audit to the per-VM NATS telemetry subject.
- [x] **D3 — NEW privileged netns/nft helper (L — the effort driver).** DONE
  2026-07-24 as `internal/netd` + `aped netd` + `aped-netd.service`, plus
  `aped-netbr.service` for the host bridge + host nft wall. Tier-1 tested
  end-to-end with recorder binaries (socket, peer gate, allocation, command order). A narrow
  root unit (only `CAP_NET_ADMIN` + `AF_NETLINK`, `RestrictNamespaces` relaxed to
  net, `@mount` only for the netns bind) that, on a typed command from the
  executor over the AF_UNIX boundary, creates the per-VM netns + veth-to-bridge +
  route + nft "only reach the proxy" wall and returns the netns path. Teardown on
  Destroy.
- [x] **D4 — Resolver flip (S).** DONE 2026-07-24 — with a fail-SAFE twist: the
  spec keeps `NetworkNone` and only the helper-created netns path grants a
  network, so a failed wire-up leaves a workspace networkless instead of on an
  open bridge. Stop defaulting egress-enabled profiles to
  `NetworkNone`; attach the netns path; flip `Networkless` off.
- [x] **D5 — Tier-2 live validation (M).** DONE 2026-07-24 on mmq4 (Ubuntu 26.04,
  kernel 7.0, Kata-CLH via containerd). Results in "Live validation" below —
  including the guest being *forced* through the proxy, which closes the honest-
  boundary gap inherited from PLAN-16:138. On a KVM+containerd+Kata host: allow +
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

## Live validation (2026-07-24, node mmq4)

Driven end-to-end against the deployed daemon with the `ape` CLI as the operator:

| Property | Evidence |
| --- | --- |
| Pre-wired netns reaches the guest | `169.254.42.0/24 dev apeg46b94a8d src 169.254.42.2` inside the VM; Kata replicated the netns config |
| No DNS in the guest (by design) | empty `/etc/resolv.conf`; the proxy resolves |
| Allowed domain tunnels | `CONNECT github.com:443` → `HTTP/1.1 200 Connection Established` |
| Denied domain refused | `CONNECT evil.example.com:443` → `403 Forbidden`, audit row `denied / domain not authorized` |
| Guest cannot bypass the proxy | direct `4.228.31.150:443`, host port 4223, and UDP `1.1.1.1:53` all blocked |
| Workspace↔workspace isolation | ws2 (`.3`) → ws1 (`.2`) blocked at ICMP **and** TCP (bridge port isolation) |
| Per-workspace allocation | second workspace got `169.254.42.3` + proxy port 3129, separate audit trail |
| Framework + cache system mounts | `/opt/apex-framework` carries the v0.3.1 tree; `/cache/go` mounted with `GOPATH=/cache/go` |
| Proxy survives a front restart | `systemctl restart aped-front` → "proxy restored on 169.254.42.1:3128"; the running workspace's tunnel still returns 200 |

Five defects were found by running it, each fixed with the reason recorded in the
unit or code it belongs to:

1. `dev-host.sh` wrote ExecStart drop-ins referencing flags the INSTALLED binary
   lacked, so a config step became an outage. Drop-ins are now capability-probed.
2. `RestrictNamespaces=net` blocked `ip -n` — iproute2 unshares a MOUNT namespace to
   bind the per-netns /sys. Now `mnt net`.
3. `/run/netns` was not a shared mount in the host, so the netns bind never reached
   containerd. `aped-netbr.service` establishes it.
4. Even then, a unit with ANY private-mount-namespace option is a SLAVE of the host
   peer group (measured: host `shared:13`, helper `shared:769 master:13`), so
   unit→host propagation is impossible. The helper now runs in the host mount
   namespace; the filesystem-view protections are gone by necessity and the unit says
   why.
5. A CONNECT client that half-closes (a piped `nc`) had its upstream dial cancelled
   by net/http's request-context cancellation — a legal client turned into a 502. The
   dial is now detached from request cancellation, bounded by the dial timeout.

Two more came out of using it rather than testing it: restarting the front silently
stripped egress from RUNNING workspaces (now restored from a per-workspace record, on
the same port, re-intersected with current policy), and the audit trail was
unreadable by the operator group (file *and* directory modes, both fighting
`UMask=0077`).

## Delivery notes (2026-07-24)

**Enforcement boundary, stated honestly.** The load-bearing walls are the HOST nft
input chain (`table inet ape_egress`: from the bridge, only the proxy port range),
bridge **port isolation** on each workspace veth (no workspace↔workspace traffic),
and the proxy's own deny-by-default domain allowlist. The per-netns ruleset the
helper installs is defence in depth only, because Kata's default
`internetworking_model=tcfilter` redirects packets between veth and tap at the tc
layer and bypasses netfilter inside that namespace. This closes the "honest
boundary" gap the plan inherited from PLAN-16:138 by naming which layer enforces
what, rather than implying the netns rules do the work.

**Two posture changes a reviewer should see.** (1) `aped-front` needs
`IPAddressAllow=any` to dial upstream — an allowlist is by hostname, which a cgroup
IP filter cannot express — so it ships as a `dev-host.sh`-installed drop-in, not in
the packaged unit. (2) `aped-netd.service` needs `MountFlags=shared` (so `ip netns
add` is visible to containerd) and `CAP_SYS_ADMIN` (netns creation), which is wider
than the executor but confined to one single-purpose unit with a root-only socket,
two verbs, no containerd access and no policy.

**Follow-ups DONE 2026-07-25** (validated live on mmq4):

- **Cold-start netns rebuild.** `start` rebuilds the workspace's namespace instead of
  reusing it, which covers two cases that look different and need the same answer: after
  a host reboot the namespace is GONE (verified by `ip netns del` + `start` → recreated
  at the same path/address, tunnel back to 200), and after `stop` it is DIRTY — Kata's
  `internetworking_model=tcfilter` adds a tc qdisc to the veth on boot and does not
  remove it when the task is killed, so reuse failed with "Failed to add qdisc for
  network index N : file exists". A persistent namespace is our design choice, so
  cleaning up after the previous boot is our job. Ordering in `Start` is load-bearing: a
  RUNNING workspace returns before anything touches its namespace (rewiring a live one
  would cut its network — a latent bug in the first cut of this code).
- **`ape sandbox egress set`** re-points a LIVE workspace's allowlist by restarting the
  host-side proxy on the same port; the guest keeps its `HTTPS_PROXY`. Verified live:
  swapped `github.com` → `proxy.golang.org` on a running workspace (200/403 flipped
  accordingly, no restart), the change persisted across a `stop`/`start`, and a domain
  outside node policy was refused. It reuses the resolver's `EgressPlanner`, so a live
  change and a create grant cannot drift apart on policy.
- **Proxy restoration across a front restart** (`RestoreAll`) observed in production
  during a redeploy: "proxy restored on 169.254.42.1:3128".

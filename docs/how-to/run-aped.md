# How to run aped (the VM-management daemon)

`aped` is the only rootful component of the ape platform (PLAN-18): a narrow,
audited daemon that provisions Kata-QEMU microVM workspaces. `ape` never runs as
root — it is a thin client that drives `aped` over embedded NATS using the
`ape.vmm.<node>.>` contract. This guide stands up `aped` on a Linux host and
points `ape sandbox` at it.

> **Requires** Linux with KVM + a rootful containerd + Kata (`ape doctor`
> reports the gaps). See [How to install the Tier-2 host stack](#tier-2-host-stack).

## The two processes

`aped` runs as two processes joined by a typed AF_UNIX command boundary (D1):

| Process | Unit | Runs as | Holds |
| ------- | ---- | ------- | ----- |
| `aped run` (root executor) | `aped.service` | root, **empty capability set** | the containerd client; **no network** beyond AF_UNIX |
| `aped front` (NATS surface) | `aped-front.service` | `aped` (de-privileged) | the embedded nats-server + the `vmm` micro service |

The guest-reachable surface is the de-privileged front-end. A guest that pops it
lands in a capability-less, TELEMETRY-account-scoped process that cannot name a
management subject or satisfy the executor's `SO_PEERCRED` gate.

## Install

1. Build and install both binaries:

   ```bash
   make install          # → /usr/local/bin/ape and /usr/local/bin/aped
   ```

2. Create the `ape` group and the `aped` service user:

   ```bash
   sudo groupadd --system ape
   sudo useradd --system --gid ape --no-create-home --shell /usr/sbin/nologin aped
   ```

3. Install the deploy assets from `deploy/`:

   ```bash
   sudo install -D -m 0644 deploy/policy.yaml            /etc/aped/policy.yaml
   sudo install -D -m 0644 deploy/tmpfiles.d/aped.conf   /etc/tmpfiles.d/aped.conf
   sudo install -D -m 0644 deploy/systemd/aped-priv.socket  /etc/systemd/system/aped-priv.socket
   sudo install -D -m 0644 deploy/systemd/aped.service      /etc/systemd/system/aped.service
   sudo install -D -m 0644 deploy/systemd/aped-front.service /etc/systemd/system/aped-front.service
   sudo systemd-tmpfiles --create /etc/tmpfiles.d/aped.conf
   ```

4. Edit `/etc/aped/policy.yaml` — this is the default-deny authorization
   boundary (D9). At minimum set the allowed `images:` and `mount_roots:` for
   your host. Unknown keys are rejected at load, so a typo fails closed.

5. (Optional) Install the kernel audit rules:

   ```bash
   sudo install -D -m 0640 deploy/audit/50-aped.rules /etc/audit/rules.d/50-aped.rules
   sudo augenrules --load
   ```

## Start

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now aped-priv.socket aped.service aped-front.service
```

`aped front` mints a scoped **host-operator credential** at startup and writes
it to `/var/lib/aped/creds/operator.creds`. Its log (`journalctl -u
aped-front`) prints the `APE_NATS_URL` to use.

That file is written `0600` owned by the `aped` service user, so your human
operator account cannot read it directly. Copy it to a path you own (what
`deploy/tier2-setup.sh` does):

```bash
sudo install -m 0600 -o "$USER" /var/lib/aped/creds/operator.creds \
  ~/.config/ape/aped-operator.creds
```

`aped` **reuses** this credential across restarts — the signing account seed is
persisted under `/var/lib/aped/keys`, so a credential minted before the restart
still validates. The startup log prints `minted` (first start / after the state
dir is reset) or `reused`; you only re-copy the file when it says `minted`.

Alternatively, add your account to the `ape` group and have `aped-front` write
the credential group-readable — but group `ape` is also the priv-socket gate, so
only add operators you intend to trust with the executor boundary.

Verify the security posture of both units:

```bash
systemd-analyze security aped.service        # predicted OK band ~3.0–3.8
systemd-analyze security aped-front.service   # ~2.5–3.5
```

## Restarting aped

Restart **socket-first**: bounce `aped-priv.socket` before the services.

```bash
sudo systemctl restart aped-priv.socket
sudo systemctl restart aped.service aped-front.service
```

`aped.service` sets `RuntimeDirectoryPreserve=yes` so stopping it does **not**
remove `/run/aped` (and the `aped-priv.socket`-owned `/run/aped/priv.sock` inside
it). Without that, the service's `RuntimeDirectory=aped` teardown deletes the
socket the socket unit just created, and the front fails to `connect()` it (`dial
unixpacket /run/aped/priv.sock: no such file or directory`) — even with a
socket-first restart. If you ever land in that state (e.g. an older unit), recover
with a clean dependency-ordered cycle:

```bash
sudo systemctl stop aped-front.service aped.service aped-priv.socket
sudo systemd-tmpfiles --create /etc/tmpfiles.d/aped.conf
sudo systemctl start aped-priv.socket aped.service aped-front.service
```

The operator credential is reused across the restart (above), so no re-copy is
needed unless `/var/lib/aped` was reset.

## Point `ape` at `aped`

```bash
export APE_NATS_URL=nats://127.0.0.1:4223
export APE_NATS_CREDS=~/.config/ape/aped-operator.creds     # the copy you own (see above)
ape sandbox ls --node "$(hostname)"
ape sandbox up dev --node "$(hostname)" --mount ephemeral
ape sandbox exec dev --node "$(hostname)" -- uname -r        # streams the GUEST kernel to your terminal
ape sandbox attach dev --node "$(hostname)"                  # interactive PTY shell (containerd driver)
ape sandbox freeze dev --node "$(hostname)"
ape sandbox down dev --node "$(hostname)"
```

### Mounting your project (`host-fs`) under `ProtectHome`

> **Two things are needed, not one.** The `BindReadOnlyPaths=` drop-in below makes
> the directory *visible* to the daemon; it does not make it *traversable*. The
> executor is root with an **empty capability set**, so it has no
> `CAP_DAC_READ_SEARCH` and a `0750` home stops it exactly as it would stop any
> other user (`lstat /home/you: permission denied`). Make the home execute-only for
> others — contents stay unlistable — as its owner, no sudo required:
>
> ```bash
> chmod o+x /home/you        # 0750 → 0751
> ```
>
> The alternative, granting the executor `CAP_DAC_READ_SEARCH`, widens the "root
> without power" model for a convenience; prefer the `chmod`, or keep projects under
> a root outside `/home`.

The default `up` mount is `host-fs` of your cwd. Both units set `ProtectHome=yes`,
so `/home` and `/root` are **invisible to the daemon** — a project under them
fails at the policy check with `host-fs mount path … is not reachable by aped`
(the daemon can't even `lstat` the path to canonicalize it). This is deliberate:
the root executor must not read operator homes. **Do not relax `ProtectHome`.**
Instead, pick one:

1. **A mount root outside `/home`** (simplest — no unit changes). Keep projects
   under e.g. `/srv/workspaces` (the shipped `mount_roots` default; `tier2-setup.sh`
   creates it). That tree isn't masked, so the daemon canonicalizes it fine and
   the Kata `virtiofsd` (a separate service) does the guest I/O:
   ```bash
   ape sandbox up dev --node "$(hostname)" --cwd /srv/workspaces/dev
   ```
2. **Expose one home subdir via a `BindPaths=` drop-in** (a drop-in is a `.conf`
   fragment under `<unit>.d/` that systemd merges onto the shipped unit — no edit
   to the packaged unit). Punch a single directory back through the mask on
   **both** units (read-only is enough — only the daemon's `lstat` needs it), then
   allow it in policy. See
   [`deploy/systemd/aped.service.d/mount-root.conf.example`](../../deploy/systemd/aped.service.d/mount-root.conf.example):
   ```bash
   sudo install -D -m0644 deploy/systemd/aped.service.d/mount-root.conf.example \
     /etc/systemd/system/aped.service.d/10-mount-root.conf
   sudo install -D -m0644 deploy/systemd/aped.service.d/mount-root.conf.example \
     /etc/systemd/system/aped-front.service.d/10-mount-root.conf
   # edit both to BindReadOnlyPaths=/home/you/projects, add that path to
   # /etc/aped/policy.yaml mount_roots, then daemon-reload + restart socket-first.
   ```
3. **No host files at all** — `--mount ephemeral` or `--mount volume`.

`--node` selects the `ape.vmm.<node>.>` group (default: the local hostname). The
node token is slugged the same way `<user>` tokens are.

> **Provisioning through the hardened executor works with `aped run --driver
> containerd`** (the barrier-3 fix, live-validated): `up` → `exec` → `attach` →
> `freeze` → `down` all run end-to-end through the deployed hardened units. The
> DEFAULT nerdctl shellDriver cannot (it does a client-side rootfs mount the unit
> forbids) — see [Known limitation](#known-limitation--executor-sandbox-vs-the-nerdctl-shelldriver-phase-2)
> below. The lifecycle is also proven in-process by `TestTier2ProvisionContainerd`.

## Per-VM credentials

At `create`, `aped` mints a per-VM NATS credential scoped **pub-only** to that
VM's `ape.{evt,log,metrics}.vm-<id>.>` telemetry and injects it into the guest
as a read-only `.creds` bind plus `APE_NATS_URL`/`APE_NATS_CREDS`. The in-VM
`ape` agent publishes telemetry on it but is **server-denied** every management
subject and every other VM's subjects — the VM→host-escape barrier. See
[NATS subjects & event payloads](../reference/events.md#per-vm-telemetry-plan-18-reuses-apeevtlogmetrics).

## Tier-2 host stack

Kata-QEMU needs a rootful containerd + Kata + nerdctl. The whole bring-up —
prereqs, the nerdctl-full bundle, Kata, the shim config fix, the containerd
memlock drop-in, a guest-kernel smoke test, the binaries, the user/group, the
deploy assets, and the operator credential — is scripted and idempotent:

```bash
sudo bash deploy/tier2-setup.sh     # tunables: NERDCTL_VERSION KATA_VERSION MOUNT_ROOT WITH_AUDIT
```

If you prefer to do it by hand (versions per PLAN-18's currency), the steps are:

```bash
# 1. prereqs — note zstd: the Kata static asset is now .tar.zst, not .tar.xz
sudo apt-get install -y curl tar xz-utils zstd

# 2. containerd + nerdctl + CNI + runc (the "full" bundle)
curl -fsSLO https://github.com/containerd/nerdctl/releases/download/v2.3.4/nerdctl-full-2.3.4-linux-amd64.tar.gz
sudo tar Cxzf /usr/local nerdctl-full-2.3.4-linux-amd64.tar.gz
sudo systemctl enable --now containerd

# 3. Kata Containers (static release; .tar.zst on current releases)
curl -fsSLO https://github.com/kata-containers/kata-containers/releases/download/3.32.0/kata-static-3.32.0-amd64.tar.zst
sudo tar --zstd -xf kata-static-3.32.0-amd64.tar.zst -C /
```

**Per-VMM shim config resolution (the #1 snag).** `ctr`/`nerdctl` do *not* honor
the containerd `ConfigPath` shim option — only the CRI/Kubernetes path does — so
a `ConfigPath` stanza in `config.toml` silently does nothing here, and a plain
symlink makes *both* the `-qemu` and `-clh` handlers read the default
`configuration.toml`. Install wrapper shims that export `KATA_CONF_FILE` instead
(what `deploy/tier2-setup.sh` does):

```bash
for vmm in qemu clh; do
  sudo tee /usr/local/bin/containerd-shim-kata-$vmm-v2 >/dev/null <<EOF
#!/bin/sh
exec env KATA_CONF_FILE=/opt/kata/share/defaults/kata-containers/configuration-$vmm.toml \\
  /opt/kata/bin/containerd-shim-kata-v2 "\$@"
EOF
  sudo chmod 0755 /usr/local/bin/containerd-shim-kata-$vmm-v2
done

# containerd memlock (VFIO pins guest RAM; QEMU locks memory)
sudo install -d /etc/systemd/system/containerd.service.d
printf '[Service]\nLimitMEMLOCK=infinity\nLimitNOFILE=1048576\n' | \
  sudo tee /etc/systemd/system/containerd.service.d/10-aped.conf
sudo systemctl daemon-reload && sudo systemctl restart containerd
```

Confirm with `ape doctor` (expects `kvm.available`, `containerd.running`, and
`kata.runtime` all OK) and a smoke test:

```bash
sudo nerdctl run --rm --runtime io.containerd.kata-qemu.v2 alpine uname -r  # prints the GUEST kernel
```

## Known limitation — executor sandbox vs the nerdctl shellDriver (Phase 2)

The hardened `aped.service` (Appendix A: `ProtectSystem=strict`, empty
`CapabilityBoundingSet`, `RestrictAddressFamilies=AF_UNIX`, `@mount` denied) is
written for an executor that is a **containerd _client_** — it talks to the
socket and does no host work itself. The current executor, however, shells out to
**`nerdctl`**, which does real host work in its own process. So **`ape sandbox up`
through the deployed units fails on the DEFAULT shellDriver** — the lifecycle logic
is correct (`TestTier2Provision` drives create → exec → freeze → unfreeze → destroy
against a real Kata-QEMU microVM and passes, because `go test` runs the executor
in-process, sandbox-free), but the deployed hardened executor cannot run `nerdctl`.
This is **resolved by `aped run --driver containerd`** ([below](#the-fix-aped-run---driver-containerd-opt-in)) — the live-validated provisioning path.

Three distinct barriers, peeled back in order (live-verified on Ubuntu 26.04 /
kernel 7.0):

1. **nerdctl metadata store (fixed).** `nerdctl run` writes to `/var/lib/nerdctl`,
   which `ProtectSystem=strict` makes read-only (`nerdctl run: mkdir
   /var/lib/nerdctl/…: read-only file system`). **Resolved without touching the
   unit:** the executor passes `--data-root <state-dir>/nerdctl` (default
   `/var/lib/aped/nerdctl`), relocating the store into the already-writable
   `ReadWritePaths=/var/lib/aped`. Override with `aped run --nerdctl-data-root`.
2. **Client-side CNI (avoided).** nerdctl's default bridge runs CNI (netns/veth/
   bridge) *in the executor's process*, needing `CAP_NET_ADMIN`/`CAP_NET_RAW`,
   `AF_NETLINK`, and `@mount`. **Resolved without touching the unit:** aped
   provisions workspaces **networkless** (`--network none`), so no CNI runs.
   Overlay connectivity is the Phase-3 job.
3. **Client-side rootfs mount (the wall).** Even networkless, `nerdctl run` does a
   `mount(2)` in its own process — `oci.WithImageConfig` → `WithAdditionalGIDs`
   RO-bind-mounts the image rootfs to a temp dir to read `/etc/group` (strace
   confirmed: it happens for `USER=root` images and is *not* avoidable with
   `--user`). The executor denies `@mount` and holds no `CAP_SYS_ADMIN`, so it is
   `operation not permitted`. **No nerdctl invocation can clear this.**

Barrier 3 is architectural: nerdctl (and containerd's `oci.WithImageConfig`
helper) resolves the image user/GIDs by mounting the rootfs client-side. Do
**not** widen the unit (`ProtectSystem=full`, net caps, `@mount`) to make nerdctl
work: that reintroduces the "root with power" the two-process split exists to
avoid.

### The fix: `aped run --driver containerd` (opt-in)

The clean fix is the non-device **`containerdDriver`** (PLAN-18 D3): the
containerd Go client builds the OCI spec as a typed object and sets the process
`user`/`env`/`args`/`cwd` directly from the image config read out of the content
store — **without** `oci.WithImageConfig` / `WithAdditionalGIDs` / any
`mount.WithTempMount`. All snapshot + rootfs mounting is left to the containerd
daemon + Kata shim (their own privileged units), so nothing mounts in the
executor's process and the hardened unit is untouched.

It is **opt-in** — the default stays the shellDriver:

```bash
# per-invocation
aped run --driver containerd  …

# or in aped.service, add `--driver containerd` to ExecStart
```

The barrier-3-free spec construction is unit-tested
(`internal/sandbox/imagespec_test.go`: user/env/args/cwd projected from the image
config, zero mounts added, numeric-uid only, networkless), and the **full
lifecycle is live-validated** (2026-07-11, Ubuntu 26.04 / kernel 7.0):
`TestTier2ProvisionContainerd` drives create → exec → freeze → unfreeze → destroy
on a real Kata-QEMU microVM, and the deployed daemon runs `ape sandbox up` →
`exec` → `attach` → `down` end-to-end through the hardened unit. The driver honors
numeric `USER` only (a named user would need the rootfs read this path avoids).
Tracked in PLAN-18 (Risks + Phase 3).

The containerd driver also enables **interactive `ape sandbox attach` and
streamed `ape sandbox exec`** (PLAN-18 D2): it opens a task exec with a PTY, and
the network-less executor relays the guest stdio to the de-privileged front over
the priv socket, which bridges it to the `ape.vmm.<node>.exec.<sid>.>` session
subjects (credit-based flow control). The shell driver has no interactive backend,
so `attach` reports `UNSUPPORTED` there and `exec` degrades to exit-status-only.
The bridge is Tier-1-proven end-to-end with a fake process; the containerd PTY is
live-validated on the Tier-2 host.

## Workspace egress (allowlisted, deny-by-default)

Workspaces are networkless until a node opts in. Egress is deliberately split
across three actors so the hardened executor never touches the network:

| Actor | Unit | What it does |
| --- | --- | --- |
| bridge + host wall | `aped-netbr.service` | creates `apebr0` (`169.254.42.1/24`) and loads `table inet ape_egress`: from the bridge, only the proxy port range is reachable, and nothing is forwarded |
| netns helper | `aped-netd.service` | per workspace, one netns + veth enslaved to the bridge with **bridge port isolation** (so workspaces cannot reach each other), `CAP_NET_ADMIN` + `CAP_SYS_ADMIN`, root-only socket, two verbs |
| CONNECT proxy | in `aped-front` | one deny-by-default proxy per workspace, never decrypting TLS, every decision audited to `egress-audit.jsonl` **and** `ape.audit.<node>.egress` |

The executor's only involvement is passing the netns **path** into the OCI spec.

Turn it on:

```bash
# 1. host config: bridge, nft wall, modules, mount roots, drop-ins (idempotent)
sudo bash deploy/dev-host.sh prereqs

# 2. policy: enable egress and set the outer allow-list
#    /etc/aped/policy.yaml
#    egress:
#      enabled: true
#      allowed_domains: ["github.com", "*.githubusercontent.com", "proxy.golang.org"]
#      max_domains: 32

# 3. install the helper + restart (needs ./ape and ./aped built first)
make build && sudo bash deploy/dev-host.sh redeploy
```

Then a project **requests** domains, and aped intersects the request with policy —
a project can narrow the node's list, never widen it:

```yaml
# .apesandbox.yaml
egress:
  authorized_domains: ["github.com", "proxy.golang.org"]
```

```bash
ape sandbox up dev --egress-domain github.com   # or ad hoc from the CLI
```

**Where enforcement actually lives** (stated plainly, because one layer is weaker
than it looks): the host nft input chain and bridge port isolation are the
load-bearing walls, plus the proxy's own domain allowlist — the only layer that can
reason about *domains*. The per-netns ruleset the helper also installs is defence in
depth only: Kata's default `internetworking_model=tcfilter` redirects packets
between the veth and the guest tap at the tc layer, which bypasses netfilter inside
that namespace.

**Two deliberate posture notes.** The front needs `IPAddressAllow=any` to dial
upstream (an allowlist is by hostname, which a cgroup IP filter cannot express), so
`dev-host.sh` installs that as a drop-in rather than baking it into the shipped
unit — delete the file to revert. And `aped-netd.service` sets `MountFlags=shared`
because `ip netns add` must be visible to containerd; that is functional, not
hardening slack.

## Giving workspaces your Claude session

Two separate hops, and it is worth keeping them apart because they fail differently:

1. **host → node.** `aped-front` runs as its own service user with `ProtectHome=yes`, so
   it cannot read `/home/<you>/.claude`. You publish the credential where the daemon can
   read it; nothing about your home changes.
2. **node → workspace.** Each workspace gets its **own writable copy** in its composed
   home. It is a copy, not a mount, for a concrete reason (below).

```bash
ape sandbox credentials publish     # hard link into /srv/ape-credentials/<user>
ape sandbox credentials status
ape sandbox credentials revoke
```

```
aped front … --host-home /srv/ape-credentials/<user> --credentials oauth
```

### Why each workspace gets a copy

`claude` does not edit its credential file — it writes a new one and **renames** over
it, which is what a login does and, on the same code path, a token refresh. A
single-file bind mount cannot be renamed over: the guest gets `Resource busy`
(measured). So a *bound* credential would mean a workspace could never refresh its own
access token — it would stop working within hours — and a host login would leave it
holding a dead token with no way to replace it.

A copy in the composed home is an ordinary file in an ordinary directory, so **login and
refresh inside a workspace work**.

### How rotation is handled

OAuth refresh tokens **rotate**: whichever party refreshes invalidates the token every
other party holds. That is why a per-workspace copy on its own would be a session that
breaks within hours, and why aped-front keeps every copy **converged** instead:

```
a workspace refreshes or logs in
  → its copy changes
  → written IN PLACE to the published file, a hard link to your real
    ~/.claude/.credentials.json — so the host has it too
  → and out to every other workspace          (~3s, one sync tick)
```

The in-place write is load-bearing: a temp-file-plus-rename there would create a new
inode and silently sever the link that makes host-side sharing work at all. Workspace
copies are replaced by rename, so a guest reading concurrently never sees a partial file.

Three bounds it keeps: it never **creates** a credential where none exists (a revoked one
stays revoked), never propagates content that is not valid JSON (a torn read must not
reach every workspace), and never rewrites identical content.

`--cred-sync-interval` tunes the tick (default 3s).

> **Known race.** If a workspace refreshes at the same instant as a host login, one side
> briefly holds the losing token and recovers on its next attempt after re-reading. The
> window is one tick and refreshes are hours apart, but it is real rather than eliminated.

`--credentials api-key` remains available and does not rotate at all — worth preferring
for unattended workloads where you would rather not share the interactive session.

### Logging in again

A `claude /login` **replaces** the credential file rather than editing it (verified: the
inode changes). That asymmetry is the one thing to know:

| Who logs in / refreshes | Reaches the others? |
| --- | --- |
| **a workspace** | **automatically, within one sync tick (~3s)** — its copy is written back to the published file, which is a hard link to your real credential |
| **the host** | **only once something re-publishes.** aped cannot notice on its own: it runs as another user with `ProtectHome=yes` and can never read your home, and the published link still points at the *old* inode |

Every `ape sandbox` command re-publishes as a side effect, so in normal use a host login
is handled the next time you touch a workspace. To need no command at all, run the
watcher — as **you**, because only your own session can read your home:

```bash
ape sandbox credentials watch --install-unit    # writes it; --print-unit just shows it
systemctl --user daemon-reload
systemctl --user enable --now ape-credentials-watch
sudo loginctl enable-linger $USER      # start at BOOT, not just at first login
```

`--print-unit` writes the unit with **this** binary's path, because a hand-written one is
easy to get wrong: it points at the path you INVOKED (e.g. `~/go/bin/ape`) rather than what
that resolves to (`ape-v0.0.48`), so the service keeps working after the next `ape` update.

**Not a shell rc file.** Putting it in `.zshrc` would start one watcher per terminal, each
dying with its shell, and none running when no terminal is open — a service supervised by
systemd is one process, restarted on failure, alive without a session.

The `enable-linger` line is what makes it a *boot* service: without it a user manager only
exists while you have a login session. It needs **no aped node, no running workspace, and
no publication** — with nothing published it simply idles, so enabling it before you ever
publish is safe.

A re-published credential is treated as **authoritative** for one sync pass, overriding
timestamps: a login starts a new session, so a token a still-running workspace refreshed
from the *old* one is dead however recently it was written.

**Logging in from INSIDE a workspace works** — there the credential is an ordinary file in
an ordinary directory, so the login's write+rename succeeds, and the sync carries the new
session out to the host and every other workspace.

### The access grant

`publish` adds a **POSIX ACL entry** granting exactly the `aped` user read+write:

```bash
getfacl -cE ~/.claude/.credentials.json
#   user::rw-
#   user:aped:rw-      ← the grant
#   group::---         ← the group gains NOTHING
```

Read-only would not be enough — a workspace's refreshed token has to be written *back*
through that file — and a group grant was rejected on purpose: group `ape` is also the
priv-socket gate, so it would hand your credential to every operator added there later.
`setfacl` also works from any shell, whereas `chgrp` needs the group in your *active*
session and fails with `EPERM` in a shell opened before you were added to it.

There is **no fallback**. A host without the `acl` package cannot share a session, and
`ape doctor` reports that as `sandbox.credential-acl` rather than letting it surface as a
workspace that mysteriously fails to start.

> `ls -l` will show the credential as `-rw-rw----+`. The group bits are the ACL **mask**,
> not group access — `group::---` above is the truth. Use `getfacl`, or
> `ape sandbox credentials status`, which prints the effective grant.

`revoke` removes the entry, returning the file to plain `0600`. Published directories are
`0751` (traverse-only) because a command running as you creates files with *your* group;
traversal exposes a path, not a secret.

## The framework mount + durable tool caches

The `ape-sandbox` image is public and framework-free, so the framework arrives as a
read-only mount at `/opt/apex-framework` (PLAN-20) and toolchain state lives in
durable host caches (PLAN-22):

```bash
# host-side, with YOUR git credentials — aped never fetches
ape sandbox framework materialize v0.3.1
ape sandbox framework ls

ape sandbox up dev --framework-ref v0.3.1
ape sandbox setup dev            # asdf install + bingo get, into /cache
```

Point the daemon at both roots (`dev-host.sh` writes this drop-in):

```
aped front … --framework-root /srv/apex-framework --framework-ref v0.3.1 \
             --cache-root /srv/ape-caches
```

A ref that is not materialized fails `up` with the exact command to run. The mount
is always read-only and always present when the node serves a framework — a project
cannot redirect, remove, or make it writable. See
[.apesandbox.yaml](../reference/apesandbox-yaml.md).

## See also

- [.apesandbox.yaml](../reference/apesandbox-yaml.md) — the per-project descriptor
  (repos, mounts, egress, toolchain) and the request-vs-grant boundary.
- [NATS subjects & event payloads](../reference/events.md) — the frozen `ape.vmm`
  contract.
- [How to run ape as a service](run-ape-as-a-service.md) — the PLAN-14 job
  daemon the in-VM `ape` can run.
- PLAN-18 (`development/planning/plan-18_ape-aped-split.md`) — the design +
  Appendix A units this guide installs.

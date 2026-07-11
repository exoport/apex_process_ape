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

Restart **socket-first**: bounce `aped-priv.socket` before the services. The
socket unit owns `/run/aped/priv.sock` (`RemoveOnStop=yes`); restarting only
`aped.service` can leave the path desynced so the front fails to `connect()` it
(`dial unixpacket /run/aped/priv.sock: no such file or directory`).

```bash
sudo systemctl restart aped-priv.socket
sudo systemctl restart aped.service aped-front.service
```

The operator credential is reused across the restart (above), so no re-copy is
needed unless `/var/lib/aped` was reset.

## Point `ape` at `aped`

```bash
export APE_NATS_URL=nats://127.0.0.1:4223
export APE_NATS_CREDS=~/.config/ape/aped-operator.creds     # the copy you own (see above)
ape sandbox ls --node "$(hostname)"
ape sandbox up dev --node "$(hostname)"
ape sandbox exec dev --node "$(hostname)" -- uname -r
ape sandbox freeze dev --node "$(hostname)"
ape sandbox down dev --node "$(hostname)"
```

`--node` selects the `ape.vmm.<node>.>` group (default: the local hostname). The
node token is slugged the same way `<user>` tokens are.

> **Today:** the read-only verbs (`ls`, `inspect`, `capabilities`) work end-to-end
> through the deployed units. The provisioning verbs (`up`, and the `exec`/
> `freeze`/`down` that follow it) do **not** yet work through the *hardened*
> executor — see [Known limitation](#known-limitation--executor-sandbox-vs-the-nerdctl-shelldriver-phase-2)
> below. The full lifecycle is proven in-process by `TestTier2Provision`.

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
through the deployed units still fails** — the lifecycle logic is correct
(`TestTier2Provision` drives create → exec → freeze → unfreeze → destroy against a
real Kata-QEMU microVM and passes, because `go test` runs the executor
in-process, sandbox-free), but the deployed hardened executor cannot run
`nerdctl`.

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
config, zero mounts added, numeric-uid only, networkless). The **full lifecycle
through the driver is not yet live-validated** — bring it up on a
KVM+containerd+Kata host and confirm `ape sandbox up` → `exec` → `freeze` →
`down` end-to-end. The driver honors numeric `USER` only (a named user would need
the rootfs read this path avoids). Tracked in PLAN-18 (Risks + Phase 3).

The containerd driver also enables **interactive `ape sandbox attach` and
streamed `ape sandbox exec`** (PLAN-18 D2): it opens a task exec with a PTY, and
the network-less executor relays the guest stdio to the de-privileged front over
the priv socket, which bridges it to the `ape.vmm.<node>.exec.<sid>.>` session
subjects (credit-based flow control). The shell driver has no interactive backend,
so `attach` reports `UNSUPPORTED` there and `exec` degrades to exit-status-only.
The bridge is Tier-1-proven end-to-end with a fake process; the containerd PTY is
live-validated on the Tier-2 host.

## See also

- [NATS subjects & event payloads](../reference/events.md) — the frozen `ape.vmm`
  contract.
- [How to run ape as a service](run-ape-as-a-service.md) — the PLAN-14 job
  daemon the in-VM `ape` can run.
- PLAN-18 (`development/planning/plan-18_ape-aped-split.md`) — the design +
  Appendix A units this guide installs.

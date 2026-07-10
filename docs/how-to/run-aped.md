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

Verify the security posture of both units:

```bash
systemd-analyze security aped.service        # predicted OK band ~3.0–3.8
systemd-analyze security aped-front.service   # ~2.5–3.5
```

## Point `ape` at `aped`

```bash
export APE_NATS_URL=nats://127.0.0.1:4223
export APE_NATS_CREDS=/var/lib/aped/creds/operator.creds   # readable by your operator user
ape sandbox ls --node "$(hostname)"
ape sandbox up dev --node "$(hostname)"
ape sandbox exec dev --node "$(hostname)" -- uname -r
ape sandbox freeze dev --node "$(hostname)"
ape sandbox down dev --node "$(hostname)"
```

`--node` selects the `ape.vmm.<node>.>` group (default: the local hostname). The
node token is slugged the same way `<user>` tokens are.

## Per-VM credentials

At `create`, `aped` mints a per-VM NATS credential scoped **pub-only** to that
VM's `ape.{evt,log,metrics}.vm-<id>.>` telemetry and injects it into the guest
as a read-only `.creds` bind plus `APE_NATS_URL`/`APE_NATS_CREDS`. The in-VM
`ape` agent publishes telemetry on it but is **server-denied** every management
subject and every other VM's subjects — the VM→host-escape barrier. See
[NATS subjects & event payloads](../reference/events.md#per-vm-telemetry-plan-18-reuses-apeevtlogmetrics).

## Tier-2 host stack

Kata-QEMU needs a rootful containerd + Kata + nerdctl. On a fresh Linux + KVM
box (versions per PLAN-18's currency):

```bash
# containerd + nerdctl + CNI + runc (the "full" bundle)
curl -fsSLO https://github.com/containerd/nerdctl/releases/download/v2.3.4/nerdctl-full-2.3.4-linux-amd64.tar.gz
sudo tar Cxzf /usr/local nerdctl-full-2.3.4-linux-amd64.tar.gz
sudo systemctl enable --now containerd

# Kata Containers (static release) + per-VMM shim symlinks
curl -fsSLO https://github.com/kata-containers/kata-containers/releases/download/3.32.0/kata-static-3.32.0-amd64.tar.xz
sudo tar -xf kata-static-3.32.0-amd64.tar.xz -C /
sudo ln -sf /opt/kata/bin/containerd-shim-kata-v2 /usr/local/bin/containerd-shim-kata-qemu-v2
sudo ln -sf /opt/kata/bin/containerd-shim-kata-v2 /usr/local/bin/containerd-shim-kata-clh-v2
```

Confirm with `ape doctor` (expects `kvm.available`, `containerd.running`, and
`kata.runtime` all OK) and a smoke test:

```bash
sudo nerdctl run --rm --runtime io.containerd.kata-qemu.v2 alpine uname -r  # prints the GUEST kernel
```

## See also

- [NATS subjects & event payloads](../reference/events.md) — the frozen `ape.vmm`
  contract.
- [How to run ape as a service](run-ape-as-a-service.md) — the PLAN-14 job
  daemon the in-VM `ape` can run.
- PLAN-18 (`development/planning/plan-18_ape-aped-split.md`) — the design +
  Appendix A units this guide installs.

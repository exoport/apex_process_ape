# How to run a sandboxed Kata VM workspace

`ape sandbox` provisions a long-lived, hardware-isolated **Kata microVM
workspace** (own guest kernel, KVM) for a project: your code is mounted inside,
`~/.claude` is composed per workspace, and you attach across many sessions to
run Claude Code, APEX pipelines, or Playwright. The workspace can't touch the
rest of the host even when every `ape`/`claude` session inside runs with
`--dangerously-skip-permissions`.

**`ape sandbox` is a thin client of `aped`, the rootful VM-management daemon
(PLAN-18).** Every verb speaks the `ape.vmm.<node>.>` contract over embedded
NATS; `aped` provisions the microVM, composes the workspace home, mints a per-VM
telemetry credential, owns the egress and the workspace registry — and is the
only component that runs as root. **`ape` never runs as root.** The daemonless
PLAN-16 runner path (where `ape` shelled out to `nerdctl` itself) is retired.

> **Linux + KVM only.** Kata needs `/dev/kvm`, and `aped` needs a rootful
> containerd + Kata. `ape sandbox` (the client) runs anywhere, but it can only
> drive an `aped` on a Linux+KVM host. macOS/Windows machines join as SSH /
> VS Code Remote *clients* of a Linux-hosted workspace, not as hosts.

## 1. Stand up (or reach) an `aped` node

The host stack (`aped` + its systemd units + a rootful containerd + Kata) is set
up once per node — see **[How to run aped](run-aped.md)**, which scripts the whole
Tier-2 bring-up (`deploy/tier2-setup.sh`) and prints the operator credential and
`APE_NATS_URL` to use.

Point `ape` at that node:

```bash
export APE_NATS_URL=nats://127.0.0.1:4223            # aped's management listener
export APE_NATS_CREDS=~/.config/ape/aped-operator.creds  # the operator .creds aped mints
export APE_APED_NODE="$(hostname)"                   # or pass --node per command
```

Without an endpoint configured, `ape sandbox` fails closed with instructions —
it has no local fallback. `--nats-url` / `--nats-creds` / `--node` override the
environment per invocation. `--node` selects the `ape.vmm.<node>.>` group and is
slugged the same way `<user>` tokens are (default: the local hostname).

## 2. Provision a workspace

```bash
ape sandbox up dev
```

This sends a `create` on `ape.vmm.<node>.create`; `aped` resolves the workspace,
composes a per-workspace `~/.claude`, mints a per-VM credential, and starts a
**detached** Kata microVM. The request carries only thin, typed fields — `aped`
resolves the composed home, egress, and creds server-side:

- `--profile <name>` — a **server-side** profile `aped` resolves by name (falls
  back to a default derived from the request when the node has no such profile).
- `--image <ref>` — image override (default: `aped`'s pinned `ape-sandbox` digest).
- `--runtime kata-qemu | kata-clh` — the runtime handler (default: the node's).
- `--mount host-fs | volume | ephemeral` — mount mode (default: `host-fs`).
- `--cwd <dir>` — project root to send as the `host-fs` mount source (default: the
  current directory). `aped` canonicalizes it and re-checks it against its policy
  `mount_roots` allow-list before binding — the caller's path is never trusted raw.
  Note `aped` runs with `ProtectHome=yes`, so a source under `/home` or `/root`
  is invisible to it (`… is not reachable by aped`); mount from a root outside
  `/home` or add a `BindPaths=` drop-in — see
  [Mounting your project under ProtectHome](run-aped.md#mounting-your-project-host-fs-under-protecthome).

The profile *fields* `aped` resolves are documented in the
[sandbox profile reference](../reference/sandbox-profile.md). For untrusted work
prefer `--mount ephemeral` and a scoped API key over a full OAuth token.

List and inspect what's provisioned (read-only verbs):

```bash
ape sandbox ls
ape sandbox ls --output-format json
ape sandbox inspect dev
```

## 3. Work inside

```bash
ape sandbox exec dev -- ape task apex-create-prd --args "--doc prd"
ape sandbox exec dev -- uname -r        # prints the GUEST kernel
ape sandbox attach dev                  # interactive login shell (raw PTY)
```

`exec` **streams** the command's stdout/stderr back to your terminal and returns
its exit code; `attach` opens an interactive login shell with a raw PTY (window
resizes forward on `SIGWINCH`). Both ride the vmm exec session subjects
(`ape.vmm.<node>.exec.<sid>.{stdin,stdout,stderr,resize,control,exit}`) with
credit-based flow control — bulk stdio never rides request/reply (PLAN-18 D2).

The interactive path needs an `aped` node running the **containerd driver**
(`aped run --driver containerd`): the network-less executor relays the guest PTY
to the de-privileged front over the priv socket, and the front bridges it to the
session subjects. On a shell-driver node `attach` reports `UNSUPPORTED` and `exec`
falls back to an exit-status-only run (output to the node's logs). The PTY itself
is live-validated on a KVM+containerd+Kata host (Tier-2).

## 4. Freeze and tear down

```bash
ape sandbox freeze dev    # cgroup-freeze the guest (frees CPU; guest RAM stays resident)
ape sandbox unfreeze dev  # thaw it — resumes instantly
ape sandbox down dev      # destroy the microVM + drop its aped registry entry
ape sandbox down dev --remove-volume   # also delete a persistent mount:volume
```

`freeze` is a **cgroup-freeze, not a VM suspend**: the guest stops using CPU but
its RAM stays resident, so `unfreeze` resumes instantly. A real suspend (save
guest RAM to disk) is not reachable through Kata-via-containerd today —
`ape sandbox suspend` returns `UNSUPPORTED` and points you back to `freeze`
(PLAN-18 D7).

`down` retains a persistent `mount: volume` volume unless `--remove-volume` is
set (data safety); `host-fs` and `ephemeral` workspaces leave nothing behind.

## Networking (Phase 2: networkless)

Phase-2 workspaces are provisioned **networkless** (`--network none`): the
client-side CNI that `nerdctl`'s default bridge would run is kept out of the
hardened executor (PLAN-18 D1). The deny-by-default CONNECT egress proxy and
overlay connectivity move **inside `aped`**, tied to the VM lifecycle, and land
with the Phase-3 overlay-networking work. Until then a workspace has no public
egress; `authorized_domains` in a profile is resolved by `aped`, not the client.

## Driver choice — provisioning through the hardened units

All verbs work end-to-end through the deployed hardened `aped` units **with the
containerd driver** (`aped run --driver containerd`) — `up`, streamed `exec`,
interactive `attach`, `freeze`/`unfreeze`, `down` are live-validated on a
KVM+containerd+Kata host.

The shipped **default `shellDriver`** does not: it shells out to `nerdctl`, which
does an irreducible client-side `mount(2)` (resolving the image user/GIDs) that
the executor sandbox (`@mount` denied, empty capability set) forbids — so `up`
fails through the hardened executor (its lifecycle is proven only in-process, by
`TestTier2Provision`). The `containerdDriver` is the fix: it builds the OCI spec
without a client-side mount. See the
[driver comparison in run-aped.md](run-aped.md#known-limitation--executor-sandbox-vs-the-nerdctl-shelldriver-phase-2)
for the full root-cause. Widening the units to make `nerdctl` work is explicitly
**not** the fix — it would reintroduce the "root with power" the split exists to
avoid.

## Security notes

The Kata microVM is the boundary, but a few things live *inside* it — choose the
workspace shape accordingly (details in the
[profile reference](../reference/sandbox-profile.md#honest-boundaries)):

- **`host-fs` mounts are writable by the guest** — an in-VM session can plant
  `.git/hooks` / `Makefile`s the *host* might later run. Use `--mount ephemeral`
  for untrusted code.
- **A full OAuth token in the VM is a full OAuth token in the VM.** Prefer a
  scoped API key for untrusted work.
- A compromised guest can poison **only its own** per-VM telemetry: its
  credential is scoped pub-only to `ape.{evt,log,metrics}.vm-<id>.>` and is
  server-denied every management subject and every other VM's subjects — the
  VM→host-escape barrier (see
  [per-VM telemetry](../reference/events.md#per-vm-telemetry-plan-18-reuses-apeevtlogmetrics)).
- The in-guest `--web` bridge still binds `127.0.0.1` — now inside the VM's own
  network namespace. See the [bridge security model](../reference/bridge-security.md).

## The image

Workspaces run the official `ape-sandbox` image (claude / node / ape / git /
sshd / chromium + Playwright), or any custom OCI ref via `--image` (or a
server-side profile's `image:`). `aped` pulls and pins it node-side. The image
is **public and framework-free** — its Dockerfile lives in this repo under
`images/ape-sandbox/` and publishes to the public `ghcr.io/exoport` package. The
**private** APEX framework is **not** baked; `aped` mounts a pinned host-side
framework checkout read-only at `/opt/apex-framework` at runtime (PLAN-20), and a
workspace installs it with `ape framework setup --no-fetch`.

## See also

- [How to run aped](run-aped.md) — stand up the daemon this client drives.
- [NATS subjects & event payloads](../reference/events.md) — the frozen
  `ape.vmm` + `ape.audit` contract.
- [How to run `ape doctor` in CI](run-doctor-in-ci.md) — the host prerequisite
  checks (`kvm.available`, `containerd.running`, `kata.runtime`).

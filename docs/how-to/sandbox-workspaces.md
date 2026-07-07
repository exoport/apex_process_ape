# How to run a sandboxed Kata VM workspace

`ape sandbox` provisions a long-lived, hardware-isolated **Kata microVM
workspace** (own guest kernel, KVM) for a project: your code is mounted
inside, `~/.claude` is composed per workspace, public egress is
deny-by-default, and you attach across many sessions to run Claude Code,
APEX pipelines, or Playwright. The workspace can't touch the rest of the host
even when every `ape`/`claude` session inside runs with
`--dangerously-skip-permissions`.

This is Phase 1 of the APEX Process Platform. It runs on one Linux box with
KVM; overlay networking, in-VM NATS workers, and preview environments are
later phases (in the `apex_process_platform` repo).

> **Linux + KVM only.** Kata needs `/dev/kvm`. macOS/Windows machines join as
> SSH / VS Code Remote *clients* to a Linux-hosted workspace, not as hosts.

## 1. Check the host prerequisites

```bash
ape doctor
```

The sandbox rows must be healthy before `ape sandbox up` can work:

| Check                | Fix                                                                              |
| -------------------- | -------------------------------------------------------------------------------- |
| `kvm.available`      | `/dev/kvm` present and openable. If present-but-inaccessible: `sudo usermod -aG kvm $USER`, then log out and back in. |
| `containerd.running` | Install containerd + `nerdctl` (ape shells out to it — no Go dependency).        |
| `kata.runtime`       | Install Kata Containers (`kata-deploy` or distro packages).                      |
| `sandbox.image`      | The official `ape-sandbox` image (or your profile's `image:`) is pulled.         |

These checks degrade to INFO on non-Linux hosts, so `ape doctor` stays green
for everyone else. See [How to run `ape doctor` in CI](run-doctor-in-ci.md).

## 2. Write a profile

Profiles live in `_apex/sandbox/<name>.yaml` and are meant to be checked into
git. A minimal local-dev profile:

```yaml
# _apex/sandbox/dev.yaml
name: dev
credentials: oauth        # bind your host OAuth (mode A)
mount: host-fs            # share the project rw over virtio-fs (default)
skills:
  - apex-create-prd       # only the skills you list reach the guest
network:
  authorized_domains:
    - api.anthropic.com
    - "*.githubusercontent.com"
```

Every field, default, and the validation rules are in the
[sandbox profile reference](../reference/sandbox-profile.md). For untrusted
work prefer `credentials: api-key` (a scoped key) and `mount: ephemeral`.

## 3. Provision the workspace

```bash
ape sandbox up dev
```

This loads `_apex/sandbox/dev.yaml` (override with `--profile <other>`),
composes a per-workspace `~/.claude`, resolves the image (the official
`ape-sandbox` unless the profile sets `image:`), and starts a **detached**
Kata container with the project mounted. Useful flags:

- `--cwd <dir>` — project root to mount (default: the current directory).
- `--ssh-port <n>` — forward a host-loopback port to the workspace's sshd (for
  `ape sandbox ssh` / VS Code Remote).
- `--proxy <host:port>` — wire `HTTPS_PROXY` to a running CONNECT egress proxy.

List what's provisioned:

```bash
ape sandbox ls
ape sandbox ls --output-format json
```

## 4. Work inside

```bash
ape sandbox attach dev              # interactive login shell inside the VM
ape sandbox exec dev -- ape task apex-create-prd --args "--doc prd"
ape sandbox ssh dev                 # over the forwarded --ssh-port
```

Inside the workspace you run Claude Code, APEX pipelines, or Playwright
exactly as on the host — the in-guest `ape`/`claude` allocate their own PTY.
VS Code Remote-SSH connects over the same forwarded port.

## 5. Suspend and tear down

```bash
ape sandbox pause dev     # suspend the microVM (frees CPU/RAM, keeps state)
ape sandbox resume dev    # wake it back up
ape sandbox down dev      # force-remove the container + drop its home & registry entry
```

`down` leaves a persistent `mount: volume` volume in place (data safety) —
remove it with `nerdctl volume rm` if you want to discard it. `host-fs` and
`ephemeral` workspaces leave nothing behind on the host.

## Public egress

Egress is **deny-by-default**. When a profile lists `network.authorized_domains`,
those hostnames are reachable over a host-side CONNECT proxy on 443; every
connection (allowed and denied) is audited to `egress-audit.jsonl` — hostnames
only, never payloads. Wire the proxy with `ape sandbox up --proxy host:port`.

> **Phase-1 limitation.** The persistent host-side proxy *supervisor* (a
> daemon started/stopped automatically by `up`/`down`) is not wired yet. Until
> it lands, pass `--proxy` pointing at a proxy you run; without it, a workspace
> whose profile declares authorized domains has no configured public egress
> (`ape sandbox up` prints a note).

## Security notes

The Kata microVM is the boundary, but a few things live *inside* it — choose
the profile accordingly (details in the [profile reference](../reference/sandbox-profile.md#honest-boundaries)):

- **`host-fs` mounts are writable by the guest** — an in-VM session can plant
  `.git/hooks` / `Makefile`s the *host* might later run. Use `mount: ephemeral`
  for untrusted code.
- **Mode A puts your full OAuth token in the VM.** Use `credentials: api-key`
  with a scoped key for untrusted work.
- The in-guest `--web` bridge still binds `127.0.0.1` — now inside the VM's own
  network namespace. See the [bridge security model](../reference/bridge-security.md).

## The image

Workspaces run the official `ape-sandbox` image (claude / node / ape / git /
framework / sshd / chromium + Playwright), or any custom OCI ref via the
profile's `image:`. The Dockerfile and its build/pin/publish instructions live
in the repo under `images/ape-sandbox/`.

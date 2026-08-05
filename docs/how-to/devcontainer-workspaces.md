# How to use a workspace as your dev container

You want a Kata VM workspace to be the place you actually develop: toolchain
installed, dependencies cached, rebuilds offline, your editor attached, and the
machine giving its memory back when you stop working.

This guide covers the moving parts that make that work — declaring a toolchain,
the durable caches, pre-warming and going offline, the environment a login shell
gets, which `ape` runs inside, and how a workspace's lifecycle ends. For
provisioning a workspace in the first place, see
[How to run a sandboxed Kata VM workspace](sandbox-workspaces.md).

## 1. Declare the toolchain in the project

Put a `toolchain:` section in the committed `.apesandbox.yaml`. Reference the
project's native files rather than duplicating versions into a second place:

```yaml
# .apesandbox.yaml
version: 1
repos:
  - { source: ., name: app, main: true }
toolchain:
  tool_versions: .tool-versions # asdf runtimes, repo-relative
  bingo: true                   # the repo's .bingo-pinned Go tools
  caches: [asdf, go]            # durable host-backed caches to mount
egress:
  authorized_domains: [github.com, proxy.golang.org, sum.golang.org]
```

Then materialize it inside the workspace:

```bash
ape sandbox up dev
ape sandbox setup dev        # asdf install, then bingo get
ape sandbox setup dev --dry-run   # print the script instead of running it
```

`setup` is idempotent. The **first** run needs egress — it downloads from the
registries in your allowlist — and every run after a warm cache is a no-op that
needs no network at all.

Naming a `toolchain:` without naming `caches:` gets you `asdf` and `go`, the two
that make a Go project offline-capable after one warm-up.

## 2. What the caches are, and where they live

A workspace's VM rootfs is destroyed on `down`. Anything that only lived there
makes every rebuild an online rebuild. So each cache is a **durable host
directory** mounted into the guest, and the toolchain is pointed at it by
environment variables `aped` derives from the mount it actually applied:

| `caches:` name | Guest path      | Host directory              | What it holds                                       |
| -------------- | --------------- | --------------------------- | --------------------------------------------------- |
| `asdf`         | `/cache/asdf`   | `/srv/ape-caches/asdf`      | asdf plugins + installed runtime versions           |
| `go`           | `/cache/go`     | `/srv/ape-caches/go`        | `GOPATH`, module cache, build cache, bingo's `GOBIN` |
| `cargo`        | `/cache/cargo`  | `/srv/ape-caches/cargo`     | `CARGO_HOME`                                        |
| `npm`          | `/cache/npm`    | `/srv/ape-caches/npm`       | the npm cache                                       |
| `pub`          | `/cache/pub`    | `/srv/ape-caches/pub-cache` | `PUB_CACHE`                                         |

Two things follow from the design that are worth knowing before you debug
something:

- **Caches are node-wide and shared between workspaces.** That is what makes the
  second workspace on a node warm immediately. It also means two projects pinning
  different versions of the same tool share one `GOBIN` — see
  [the bingo note](#5-which-ape-runs-inside-and-the-bingo-rule).
- **You cannot redirect a cache.** The descriptor picks a NAME from a closed
  table; the guest path and the environment come from `aped`. A committed file
  cannot point `GOPATH` somewhere of its choosing.

The host directories are created `root:ape` mode `2775`, so an operator in the
`ape` group can pre-warm them directly (next section).

## 3. Pre-warm, then work offline

The point of the caches is that a workspace stops needing the network. There are
two ways to get there.

**Warm from inside a workspace** — the normal path. Give it egress once, run
setup and a build, then narrow or drop the allowlist:

```bash
ape sandbox up dev                      # with egress in .apesandbox.yaml
ape sandbox setup dev
ape sandbox exec dev -- go mod download
ape sandbox exec dev -- go build ./...  # fills the build cache too

ape sandbox egress set dev              # no domains → networkless, live, no rebuild
```

`egress set` re-points a **running** workspace: the CONNECT proxy is host-side on
a fixed port, so it restarts on that same port and the guest keeps the
`HTTPS_PROXY` baked into its spec. No `down`/`up`.

**Warm from the host** — useful for seeding a fresh node before anyone works on
it. The cache directories are ordinary group-writable directories:

```bash
sudo -u "#$(id -u)" env GOPATH=/srv/ape-caches/go GOMODCACHE=/srv/ape-caches/go/pkg/mod \
  go mod download -C /srv/workspaces/app
```

Verify a workspace is genuinely offline-capable by taking its network away and
building:

```bash
ape sandbox egress set dev     # networkless
ape sandbox exec dev -- go build ./...
```

If that fails, something is still reaching out — check the workspace's egress
audit trail on the node (`/var/lib/aped/proxies/<ws>/egress-audit.jsonl`) to see
what it tried to reach.

## 4. The environment a login shell gets

Two kinds of session reach a workspace, and they get their environment
differently:

- `ape sandbox exec` and `ape sandbox attach` **inherit the container's
  environment** — caches, proxy, everything `aped` derived.
- **ssh and VS Code Remote do not.** `sshd` deliberately builds a fresh
  environment per session.

Without help, an ssh session would have no `GOPATH`, no `ASDF_DATA_DIR`, and no
`HTTPS_PROXY` — so `go` and `asdf` would write into the ephemeral rootfs instead
of the durable caches, silently defeating everything above, and the workspace
would appear to have no network even though it has egress.

So `aped` also writes the resolved environment to `~/.ape-env` in the composed
home, and the image's `/etc/profile.d` entry sources it. It carries an
**allowlist** — cache paths, proxy variables, the framework path — and
deliberately not credentials: an interactive shell needs paths, not secrets.

Two consequences:

- The workspace image must be **v1.1.1 or newer**. Earlier images gave login
  shells a stock `PATH` without `/opt/ape/bin`, so the delivered `ape` was
  missing from an ssh session.
- If you add something to a workspace's environment and an ssh session does not
  see it, that is this allowlist, not a bug in your shell.

Check what a login shell will get:

```bash
ape sandbox exec dev -- cat /sandbox/home/.ape-env
```

## 5. Which `ape` runs inside, and the bingo rule

`ape` is not baked into the image. `aped` mounts the `ape` installed beside it
read-only at `/opt/ape/bin`, first on `PATH`, so a workspace runs the version
matching the daemon that provisioned it. It is the **node's** `ape`, not your
client's — `ape sandbox ls` has an `APE` column so a difference is visible.

If your project pins a specific `ape` with `bingo`, that keeps working and does
not compete with the delivered one: bingo invokes version-stamped names by
absolute path (`$(APE)` from `.bingo/Variables.mk`), which never resolves through
`PATH`.

**Avoid `bingo get -l ape`.** The `-l` link creates an unstamped `$GOBIN/ape`,
and `$GOBIN` is inside the node-wide shared `go` cache — so two projects pinning
different versions would contend for that one filename. Use the stamped path.

## 6. The image pin and the node's policy travel together

The default image is digest-pinned:

```
ghcr.io/exoport/ape-sandbox:v1.1.1@sha256:b45a0674…
```

The digest is the actual pin (a tag is mutable, and re-pushing it would change
what every workspace runs); the tag is there to keep the version legible.

The node's `/etc/aped/policy.yaml` allows images by **exact string match** — not
a prefix, not a repository match. So the pin and the policy entry are a pair:

```yaml
# /etc/aped/policy.yaml
images:
  - ghcr.io/exoport/ape-sandbox:v1.1.1@sha256:b45a0674…
```

Move one without the other and `ape sandbox up` is refused as a **policy
denial**, not as a pull error — which is the right failure, but only reads that
way if you know the two are coupled. Change both together.

One related operational note: `aped`'s root executor has no network by design,
so it cannot pull. The image has to be present in the node's containerd
namespace before a create:

```bash
sudo nerdctl --namespace aped pull ghcr.io/exoport/ape-sandbox:v1.1.1@sha256:b45a0674…
```

Use the exact digest-pinned ref — containerd stores an image under the name it
was pulled with, and the lookup is an exact match. On a single box this is a
one-off; across several nodes it is a per-node step, and keeping a fleet's pins
and pre-pulls in sync is a fleet concern rather than something this guide solves.

## 7. Reaching what you are building

A workspace has **no inbound reachability** — the ruleset drops input, output and
forward, and the host bridge drops forwarding both ways. `exec` and `attach` work
because they ride `containerd`'s own channel, not the network.

To look at a dev server running inside, forward a port:

```bash
ape sandbox forward dev 8080          # localhost:8080 → workspace :8080
ape sandbox forward dev 3000:8080     # localhost:3000 → workspace :8080
```

Nothing is exposed by this: the guest end dials its own loopback, and the bytes
ride the same authenticated session transport as `exec`. It is private to you —
not a public URL, and not a route between workspaces.

For an editor, forward ssh. The image ships `sshd` and `aped` composes `~/.ssh`
with a pinned `known_hosts`, but **nothing starts `sshd` for you** — a workspace
has no listening socket at all until you start one. So it is two steps, the first
of which is once per workspace:

```bash
ape sandbox exec dev -- sh -c 'mkdir -p /run/sshd && /usr/sbin/sshd'
ape sandbox forward dev --ssh &
ssh -p 2222 root@127.0.0.1
```

That is a plain ssh target, and therefore a VS Code Remote one. If you skip the
first step the forward reports `nothing is listening on 127.0.0.1:22` and tells
you this — the guest end failing cleanly rather than hanging.

Forwards belong to the command, not to the workspace: Ctrl-C ends them and the
workspace is untouched. Run several at once in separate terminals.

## 8. freeze vs stop vs down vs idle-stop

Four ways a workspace stops costing you something. They are not
interchangeable:

| Verb                   | Frees               | Keeps                          | Survives a host reboot | Undo         |
| ---------------------- | ------------------- | ------------------------------ | ---------------------- | ------------ |
| `ape sandbox freeze`   | CPU only            | RAM stays **resident**         | no                     | `unfreeze`, instant |
| `ape sandbox stop`     | **RAM**             | rootfs, container, snapshot    | yes                    | `start`, ~30 s |
| `ape sandbox down`     | RAM **and disk**    | nothing in the VM              | n/a                    | `up`, a full boot |
| idle-stop (automatic)  | **RAM**             | same as `stop` — it *is* `stop` | yes                   | `start`      |

- **`freeze`** is a cgroup-freeze, not a VM suspend: the guest stops using CPU
  but its memory stays resident, so `unfreeze` resumes instantly. A real suspend
  (guest RAM to disk) is not reachable through Kata-via-containerd —
  `ape sandbox suspend` reports `UNSUPPORTED`.
- **`stop`** kills the task and keeps the container and its snapshot. This is the
  one to reach for: it gives the memory back, survives a reboot, and `start`
  brings the workspace back with its filesystem intact.
- **`down`** destroys the microVM. It is cheap *because* of everything above —
  the rootfs is ephemeral and the things of value live in host mounts (your repos,
  the caches, the read-only framework), so `down` + `up` costs a boot and loses
  nothing. A `mount: volume` volume is retained unless you pass `--remove-volume`.

### Automatic idle-stop

If the node's operator enabled it (`aped front --idle-stop 2h`), a workspace the
in-guest agent reports as idle is **stopped** — never destroyed. Disk is
reclaimed by a human.

The signal is guest CPU, sampled inside the workspace, not "when did someone last
exec". That distinction is the whole point: a three-hour build with nobody
attached is *busy*, and would be stopped mid-task by anything measuring
attention instead of work.

A workspace the node has **never heard a heartbeat from is never reaped**. Not
"probably idle" — unknown. The agent is best-effort, so a workspace whose agent
failed to start emits nothing, and reading that as idleness would stop healthy
workspaces.

Opt a workspace out, or give it its own threshold, in the committed descriptor:

```yaml
# .apesandbox.yaml
lifecycle:
  idle_stop: "off"   # never auto-stop this one
  # idle_stop: "8h"  # or: a threshold of its own
```

or per invocation:

```bash
ape sandbox up dev --idle-stop off
```

`ape sandbox ls` has an `IDLE-STOP` column showing what applies to each workspace
(`node` = the node's own setting), so "why did that stop?" and "why didn't it?"
are both answerable without reading the project file.

## 9. Seeing what the work cost

Sessions run inside workspaces write their transcripts to the composed home,
which is a host directory. The node can therefore total them up per workspace:

```bash
ape sandbox costs             # every workspace on the node
ape sandbox costs dev         # just one
ape sandbox costs --output-format json
```

There is nothing to enable and no agent involved. A model with no rate in the
node's price table contributes $0 and is called out, so a total that is a lower
bound is never mistaken for an exact one.

## See also

- [How to run a sandboxed Kata VM workspace](sandbox-workspaces.md) — provisioning,
  egress, mounts, and the security model.
- [How to run aped](run-aped.md) — standing up the daemon, including
  `--cache-root`, `--idle-stop`, and the host prerequisites.
- [Sandbox profile reference](../reference/sandbox-profile.md) — the node-side
  profile schema.

---
plan_id: PLAN-23
created_at: 2026-07-26
status: done
tags:
  - sandbox
  - image
  - aped
  - release
summary: >
  Stop baking `ape` into the sandbox image; have `aped` deliver it at runtime the
  way it already delivers the framework. Today the image pins an `ape` release
  (`ARG APE_VERSION`) and `ape` pins the image (`sandbox.DefaultImage` + the aped
  policy allow-list), which means a workspace runs whatever `ape` was current when
  the image was built — one release behind, structurally. Since project work
  happens INSIDE workspaces, an `ape` upgrade made to unblock project work does
  not reach the place the work happens until a new image ships. Runtime delivery
  removes the lag by construction: the workspace runs exactly the `ape` that the
  node's `aped` shipped with (they come from the same release archive), the image's
  version line becomes genuinely independent (base / asdf / bingo / Playwright
  only), and the in-guest version floor stops existing because no stale `ape` can
  be baked. The image build also stops depending on an `apex_process_ape` release
  at all, so the cross-repo release ordering problem disappears rather than being
  automated around.
origin:
  - 2026-07-26 design conversation, after publishing ape-sandbox v1.0.0 and
    digest-pinning `DefaultImage`. Observation that the two pins form a lag, not a
    deadlock — but a lag in the wrong direction: "the idea of the image is to use the
    latest ape, and usually if we need to change ape is because we need something to
    continue working in our project, and to work in our projects we use the image."
  - Independent version lines are still wanted, for the converse reason: the image
    changes for its own causes (base, asdf/bingo, Playwright) with no `ape` release
    involved. What is NOT wanted is `ape` releases forcing image releases.
  - Precedent: PLAN-20 D5 already moved the APEX framework out of the image for the
    same class of reason (own cadence, must match what the operator chose) and mounts
    it read-only at runtime. `ape` has that property more acutely — it must match the
    daemon driving the workspace.
  - Decision: runtime delivery, single phase. Existing workspaces may break; the only
    workspaces in existence are this repo's test ones (owner's call, 2026-07-26).
---

# PLAN-23: Runtime `ape` delivery into sandbox workspaces

## Goal

A workspace runs **the `ape` that the node's `aped` shipped with**, mounted
read-only at provision time. The image carries **no `ape` at all**.

## Background — where we are

- The image bakes `ape` at `/usr/local/bin/ape` from a pinned release
  (`ARG APE_VERSION`, currently `v0.0.49`), downloaded anonymously from the public
  GitHub release during the build.
- `sandbox.DefaultImage` + `deploy/policy.yaml`'s `images:` allow-list pin the image
  from the other side (exact-string match, so both must move in one commit).
- Consequence: the pair ratchets one release apart. `ape v0.0.50`'s default image is
  the one baking `v0.0.49`. For work done inside workspaces, that is the version that
  matters, so the upgrade does not arrive where it is needed.
- A floor exists and is documented in three places: the baked `ape` must be ≥ v0.0.49,
  the release that scoped git's `safe.directory` exemption, or `ape framework setup`
  cannot read the read-only host-owned framework mount. The floor is prose, enforced by
  nothing.
- The framework is already delivered this way — `aped` mounts a pinned checkout
  read-only at `/opt/apex-framework` (PLAN-20 D5) — so the mechanism, the reserved
  destination handling, and the operator story all exist.

Facts confirmed while designing (each one decides part of the shape):

- **The release archive carries both binaries.** `ape_linux_amd64.tar.gz` contains
  `LICENSE`, `README.md`, `deploy/`, `ape`, `aped`. A node running `aped` therefore
  already has the exactly-matching `ape`, with no fetch and no pin.
- **The Kata path takes PATH from the image.** `imagespec.go:75-78` builds the process
  env as image config first, then appends aped's additions — so PATH precedence belongs
  in the image, not in an appended `PATH=` (which would depend on duplicate-key
  last-wins behaviour).
- **`GOBIN=/cache/go/bin` is not on the guest PATH**, and the cache source is
  `filepath.Join(cacheRoot, "go")` (`resolver.go:291`) with **no per-workspace
  component** — one GOBIN shared by every workspace on the node. bingo's
  version-stamped names are what make that sharing safe.
- **`ape` builds with `CGO_ENABLED=0`** and stamps its version via
  `-X …/internal/apecmd.Version={{.Version}}` (`.goreleaser.yaml`). Static, and its
  identity is readable from the binary without running it.

## Design

### Guest path

```
/opt/ape/bin/ape        read-only, mounted by aped, first on PATH
/opt/apex-framework     read-only, mounted by aped          (PLAN-20, unchanged)
```

- **A directory mount (`/opt/ape/bin`), not a single-file bind.** Directory mounts are
  the well-trodden mechanism in this stack (home, framework, caches); a single-file bind
  brings the caveats the credential work already paid for. It also leaves room for
  `/opt/ape/share` (completions) without minting another reserved destination.
- **Under `/opt`, beside the framework.** One mental model: `/opt/<x>` means *aped put
  this here at runtime, read-only, on its own authority*.
- **PATH precedence is set in the image**: `ENV PATH="/opt/ape/bin:${PATH}"`, and the
  image creates the directory empty so the entry is never dangling.
- **Container `ENV` is not enough on its own.** It covers `attach`/`exec`, whose env comes
  from the container spec, but the entrypoint also starts `sshd` (the ssh / VS Code Remote
  path) and sshd builds a FRESH session environment rather than passing its own on. Today
  `ape` survives that only by accident: `/usr/local/bin` happens to be in sshd's default
  PATH. Moving it to `/opt/ape/bin` breaks ssh sessions unless the image also exports it
  for login shells — see D9.
- **The mount must stay exec-allowed.** System binds are built `{"rbind", "ro"}`
  (`containerd_driver_linux.go:608`); `noexec` appears only on `/dev/pts`, `/dev/shm` and
  tmpfs. If system mounts are ever hardened with `noexec`, this breaks and presents as an
  unexplained "Permission denied" on a file that is plainly executable.
- **The guest runs as uid 0** (`USER 0`; `parseNumericUser("")` → `0:0`), so there is no
  unprivileged guest user to grant access to. What matters host-side is that the file is
  readable and executable by the identity `virtiofsd` runs as — an `install -m 755`
  root-owned binary satisfies that and the D2 ownership check together.
- **`/opt/ape` joins `reservedDests`** as a whole subtree (like `/workspace`), so a
  committed `.apesandbox.yaml` can neither shadow it nor make it writable.

### Which binary, and how it is verified

Source: the `ape` in the same directory as the running `aped` executable
(`os.Executable()` → dir → `ape`), overridable by flag/policy for unusual installs.
Same archive ⇒ right OS, right arch, matching version, no new pipeline.

Verification reads the binary with `debug/buildinfo.ReadFile` and **never executes it**.
Executing a candidate inside the rootful daemon to ask its version would be the wrong
shape; the fields needed are all in the build info:

| Check | Source | Catches |
| --- | --- | --- |
| Parses as a Go binary | `buildinfo.ReadFile` | a shell script, a wrapper, a truncated file |
| `info.Path == github.com/exoport/apex_process_ape/cmd/ape` | build info | **a different program named `ape`** — the strongest identity signal, stronger than any version string |
| `GOOS=linux`, `GOARCH` = guest arch | `info.Settings` | a darwin/arm64 binary that would fail as `exec format error` inside the VM |
| Version == `aped`'s own compiled-in version | `-X …apecmd.Version` in `info.Settings` | a stale `ape` beside a freshly deployed `aped` |
| `vcs.revision` == `aped`'s, when both expose it | `info.Settings` | two `dev` builds from different commits, which compare EQUAL on version alone |
| root-owned, not group/world-writable | `os.Stat` | anyone who can write that directory choosing what runs as the workspace's `ape` |

The last two matter more than they look:

- Locally built binaries carry `Version == "dev"`, so version equality passes between two
  `dev` builds from different commits — exactly the staleness case that has already bitten
  this project three times. `vcs.revision` closes it when present; `dev-host.sh`'s
  staleness warning (extended to cover `ape`, not just `aped`) closes it in the dev loop.
- The guest executes the mounted file as root inside the VM, with the credential and the
  project mounts in reach. The VM is the security boundary, but the binary is still worth
  an ownership check before it is handed a workspace.

**Fail closed, twice.** At daemon start, so a bad install is visible in
`systemctl status` rather than at someone's first `up`; and again at create, cheaply,
because the file can be replaced under a running daemon — which is precisely what the
redeploy loop does.

### The bingo interaction

Both needs are already served, and they never contend:

| What | Invoked as | Which binary |
| --- | --- | --- |
| Ambient `ape` (typed by a human, or run by `claude` in the guest) | bare name on PATH | **delivered** — `/opt/ape/bin/ape`, always matching `aped` |
| A project's pinned `ape` | `$(APE)` from `.bingo/Variables.mk` → `/cache/go/bin/ape-v0.0.47` | **pinned** — version-stamped, by explicit path |

bingo installs version-stamped names and its generated `Variables.mk` calls them by full
path, so a project that pins `ape` keeps getting its pinned one in every `make` target,
while bare `ape` stays the workspace's own. Nothing about pinning changes.

Two rules, both consequences of that shared GOBIN:

1. **Do not `bingo get -l ape` in a workspace.** `-l` creates an unstamped `$GOBIN/ape`,
   and since `/cache/go/bin` is shared by every workspace on the node, two projects
   pinning different versions would contend for one name. The stamped names are why one
   shared cache is safe.
2. **If `$GOBIN` is added to PATH for convenience, it goes AFTER `/opt/ape/bin`**, so bare
   `ape` still means the delivered one. A project may choose the opposite and owns the
   consequence — an older pinned `ape` is where the `safe.directory` class of failure came
   from.

### What this retires

- The **cross-repo release cycle**: the image build stops referencing an
  `apex_process_ape` release, so `ape-sandbox` CI has no dependency on it. No dispatch,
  no PAT, no auto-PR choreography needed for the `ape` direction.
- The **in-guest version floor** and its prose in three files. The error message added in
  `19d8c09` becomes a backstop for a case that can no longer arise through the ambient
  path, rather than a live diagnostic.
- **One of the two pins.** `DefaultImage` (+ the policy allow-list) stays; `ARG
  APE_VERSION` goes. The remaining pin moves only when the image changes for its own
  reasons, which is what "independent version lines" was supposed to mean.

## Deliverables

- [x] **D1 — Guest mount + reserved destination.** DONE 2026-07-26 — `/opt/ape/bin` mounted read-only from a STAGED copy under the state dir, not from the host bin directory: mounting `filepath.Dir(apePath)` would have exposed all of `/usr/local/bin` (48 entries on the dev node — containerd, the Kata shims, aped itself) into every workspace FIRST on PATH, shadowing the image's own `bingo`/`asdf` with the host's. Found by inspecting the real node before live validation. `/opt/ape` reserved as a subtree. Original scope: `aped` mounts the resolved `ape`'s
  directory read-only at `/opt/ape/bin`; `/opt/ape` added to `reservedDests` as a
  subtree. Resolved server-side like every other system mount — never accepted from the
  wire.
- [x] **D2 — Resolution + verification.** DONE 2026-07-26 — `internal/aped/apebin.go`: resolved beside `os.Executable()`, verified via `debug/buildinfo` (main path → GOOS/GOARCH → version → vcs.revision → writability), fatal at start and re-checked per create. World-writable fatal, group-writable warns (a blanket check rejected ordinary `go build` output). Original scope: Resolve beside `os.Executable()`, with a
  flag/policy override. Verify via `debug/buildinfo.ReadFile` per the table above
  (identity → arch → version → vcs → ownership), never by executing the candidate.
  Refuse at daemon start AND at create, with a message naming the resolved path and both
  versions. A node that cannot produce a deliverable `ape` must not create a workspace.
- [x] **D3 — Image: drop `ape`.** DONE 2026-07-26 in exoport/ape-sandbox (`0cf57f9`) — download layer, `ARG APE_VERSION`, workflow `DEFAULT_APE_VERSION`/`ape_version` input and the floor note all removed; mountpoint created and first on PATH; `make smoke` checks the mountpoint + PATH and a new `make smoke-delivery` mounts an ape in and resolves it. Original scope: Remove the download layer, `ARG APE_VERSION`, the
  `ape version` build smoke, and the floor note. Create `/opt/ape/bin` empty and put it
  first on `PATH`. `make smoke` changes from "run `ape version`" to "mount an `ape` in
  and check it runs" — a better test, since it exercises delivery. New image version
  (own line; the change is the image's, not `ape`'s).
- [x] **D4 — In-guest `ape update`.** DONE 2026-07-26 — detected by location (the mount is aped's), refusing with the operation that actually helps: update the node's ape. Original scope: A read-only mount makes self-update fail on a
  write error. Detect the delivered case and say so instead: this `ape` is delivered by
  `aped`; update the node's `ape`.
- [x] **D5 — Observability.** DONE 2026-07-26 — recorded on the registry row and the wire record, `APE` column in `ls`, printed by `up` beside the client's version, plus the `sandbox.ape-delivery` doctor check. Original scope: Record the delivered version in the workspace registry;
  surface it in `ape sandbox ls` and in the `up` output. This is what tells a laptop
  operator that their workspace runs the NODE's `ape`, not theirs. Add a `doctor` check
  for "node can resolve a deliverable `ape`".
- [x] **D6 — The other spec builder.** DONE 2026-07-26 — `spec.go` now leads its hardcoded PATH with `ApeBinDest`. Original scope: `spec.go:98` hardcodes
  `PATH=/usr/local/sbin:/usr/local/bin:…`. Either add `/opt/ape/bin` there too or record
  that this driver is out of scope for delivery — otherwise `/opt/ape/bin` is silently
  absent under it.
- [x] **D7 — Guardrails that outlive this plan.** DONE 2026-07-26 — a test asserts the shipped `deploy/policy.yaml` allows `sandbox.DefaultImage`; `dev-host.sh` checks BOTH binaries for staleness (and its version line no longer prints nothing — `aped version` is not a subcommand). Original scope: (a) A test asserting
  `deploy/policy.yaml`'s `images:` contains `sandbox.DefaultImage` — the exact-match trap
  turns drift into a policy denial rather than a pull error. (b) `dev-host.sh` installs
  BOTH binaries and extends its staleness warning to `ape`, since D2's version check is
  only as good as what the deploy script puts in place.
- [x] **D9 — Login-shell environment (ssh / VS Code Remote).** DONE 2026-07-26 — `internal/sandbox/profileenv.go` writes an ALLOWLISTED, shell-quoted env file into the composed home (quoting asserted by sourcing it in `sh`); the image ships `/etc/profile.d/ape-sandbox.sh` to re-establish PATH and source it. Original scope: `attach`/`exec` inherit the
  container spec's env; `sshd` does not — it builds a fresh session env, so nothing set as
  container `ENV` reaches an ssh session. Two parts:
  (a) the image ships `/etc/profile.d/ape.sh` putting `/opt/ape/bin` first on PATH, so the
  delivered `ape` is reachable over ssh (today `/usr/local/bin` is in sshd's default PATH
  by luck, which is the only reason the baked `ape` works there);
  (b) `aped` writes the per-workspace env it derives — `GOPATH`, `GOBIN`, `GOMODCACHE`,
  `GOCACHE`, `ASDF_DATA_DIR` — into a file under the composed home that the profile
  sources. This is a **pre-existing** bug, not one this plan introduces: those variables
  are container env today, so an ssh or VS Code Remote session currently points `go` and
  `asdf` at ephemeral rootfs paths instead of the durable caches, silently defeating
  PLAN-22 D4 for anyone working over that path. Keep the file server-side-derived (a
  caller must never be able to inject `GOPATH`).
- [x] **D8 — Docs + CHANGELOG.** DONE 2026-07-26 — README, sandbox-workspaces (incl. the bingo rules), run-aped (both binaries required), apesandbox-yaml (reserved subtree), the image pointer, both repos' READMEs, CHANGELOG; `cli.md` regenerated (it was already stale). Original scope: `README.md` (sandbox section), `docs/how-to/
  sandbox-workspaces.md` (the image section, `--image`, the bingo rules), `docs/how-to/
  run-aped.md` (node prerequisite: `ape` beside `aped`), `docs/reference/
  apesandbox-yaml.md` (new reserved destination), `images/ape-sandbox/README.md`, and the
  image repo's README (drop the floor + `APE_VERSION` pin, document that the workspace's
  `ape` comes from the node).

## Non-goals

- Multi-arch images. Decided separately and documented in the image repo: the published
  image is `linux/amd64`, and delivery inherits that constraint (D2 asserts it rather
  than working around it).
- Delivering `claude` the same way. Different cadence, different problem, no version
  coupling to `aped`.
- In-guest self-update. The node owns the version; D4 makes that explicit.
- A per-project override of the delivered `ape`. A project that needs a specific version
  pins it with bingo and calls it by path, which is the mechanism that already exists.
- Phased migration with a baked fallback. Explicitly waived: existing workspaces may
  break, since the only ones are this repo's test workspaces.

## Dependencies

- **PLAN-20 (mounts + framework delivery)** — supplies the reserved-destination model and
  the read-only runtime mount pattern this copies.
- **PLAN-22 (toolchain caches)** — supplies `GOBIN=/cache/go/bin`, the shared cache whose
  sharing semantics drive the two bingo rules.
- A new `ape` release and a new `ape-sandbox` image version, in that order — but for the
  last time in this direction: after D3 the image no longer references an `ape` release.

## Risks

- **Arch mismatch** — a darwin or arm64 `ape` mounted into an amd64 guest fails as
  `exec format error`, deep inside the VM. Mitigated by D2's `GOOS`/`GOARCH` assertion at
  create time.
- **Two `dev` builds comparing equal** — version equality is blind between local builds
  from different commits. Mitigated by `vcs.revision` and by D7(b).
- **Trusting a writable directory** — "mount whatever is beside me" is only as safe as
  that directory's permissions. Mitigated by D2's ownership check.
- **Env that does not reach ssh sessions** — moving `ape` off `/usr/local/bin` removes the
  accident that made it work over ssh. D9(a) is therefore not optional polish; without it
  the VS Code Remote path loses `ape` entirely. D9(b) fixes the same class of gap that
  already affects the toolchain env.
- **Old `aped` + new image** — nothing mounts, so the guest has no `ape` at all
  (`command not found`). Accepted per the single-phase decision; D2 makes the node-side
  half fail loudly, and the pair ships together.

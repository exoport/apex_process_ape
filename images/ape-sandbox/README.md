# ape-sandbox image

The official OCI image for `ape sandbox` Kata VM workspaces (PLAN-16 D6). It
is provisioned by `ape sandbox up` and run as a long-lived, hardware-isolated
Kata microVM (own guest kernel, KVM). A profile's `image:` field overrides it
with a custom image.

## What's inside

Built `FROM` [`agent-infra/sandbox`](https://github.com/agent-infra/sandbox)
(Apache-2.0 — headless browser via VNC + CDP, VS Code Server, terminal, MCP),
plus:

- **claude** — the Claude Code CLI (`@anthropic-ai/claude-code`)
- **node** — Node.js LTS (for claude, Playwright, JS tooling)
- **ape** — the pinned `ape` release binary
- **git** + **build-essential** — clone/build tooling
- **the APEX framework** — a pinned checkout at `/opt/apex-framework`; run
  `ape framework setup` inside a mounted project to install its skills +
  pipelines from there
- **sshd** — key-auth-only, for the SSH / VS Code Remote access path. The
  `ape` user's home is `/sandbox/home` (where the composed `~/.claude` mounts),
  so sshd reads the workspace's `~/.ssh/authorized_keys` — written by the
  composer from the profile's `access.authorized_keys` — and `HOME` is the same
  for `ssh` and `exec`. `StrictModes` is off (the bind-mount's in-guest
  ownership needn't match `ape`; the VM is the boundary)
- **chromium + Playwright** — browser workloads (e.g. Excalidraw rendering)

The composed `~/.claude` (credentials, curated skills/agents, git config) is
**not** baked in — `ape sandbox up` bind-mounts it at `/sandbox/home` per
workspace, and `HOME` points there at runtime.

## Build

```bash
# From the repo root. Multi-arch needs buildx; single-arch works with build.
docker build \
  --build-arg APE_VERSION=v0.0.40 \
  --build-arg FRAMEWORK_REF=v0.0.71 \
  -t ghcr.io/exoport/ape-sandbox:v0.0.40 \
  images/ape-sandbox
```

`nerdctl build` works the same way (`nerdctl build -t … images/ape-sandbox`).

### Build args

| Arg                    | Default                             | Purpose                                             |
| ---------------------- | ----------------------------------- | --------------------------------------------------- |
| `BASE_IMAGE`           | `ghcr.io/agent-infra/sandbox:1.11.0@sha256:6328d7fd…f906e7` | Base to build `FROM` — pinned to the 1.11.0 multi-arch digest (below). |
| `APE_VERSION`          | `v0.0.40`                           | ape release tag; installed from the release tarball.|
| `FRAMEWORK_REF`        | `main`                              | `apex_process_framework` git ref cloned to `/opt`.  |
| `CLAUDE_CODE_VERSION`  | `latest`                            | `@anthropic-ai/claude-code` npm version.            |
| `PLAYWRIGHT_BROWSER`   | `chromium`                          | Playwright browser to install with OS deps.         |
| `NODE_MAJOR`           | `20`                                | Node LTS major (skipped if the base ships node).    |
| `TARGETARCH`           | `amd64`                             | Set by buildx; selects the ape release asset arch.  |

## Pinning policy

This image is **versioned with ape + the framework** — a workspace should be
reproducible.

1. **Base pinned to a digest.** `BASE_IMAGE` is
   `ghcr.io/agent-infra/sandbox:1.11.0@sha256:6328d7fd2f0ff0b4c147c3d05b3df1ce331f4a482eb6e550ecd64ed1fcf906e7`
   — the multi-arch (linux/amd64 + linux/arm64) manifest-list digest for
   upstream release 1.11.0. Never a floating `:latest` in a published image.
   To bump the base, re-resolve the digest for a newer release, e.g. with a
   plain public-registry query (no docker needed):

   ```bash
   tok=$(curl -sSL "https://ghcr.io/token?scope=repository:agent-infra/sandbox:pull&service=ghcr.io" \
         | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
   curl -sSI -H "Authorization: Bearer $tok" \
     -H "Accept: application/vnd.docker.distribution.manifest.list.v2+json" \
     https://ghcr.io/v2/agent-infra/sandbox/manifests/<TAG> | grep -i docker-content-digest
   ```

   Then update this `ARG` + this section. (Or `crane digest ghcr.io/agent-infra/sandbox:<TAG>`.)
2. **Tag to match the ape release** (e.g. `ghcr.io/exoport/ape-sandbox:v0.0.40`).
3. **Update `sandbox.DefaultImage`** in `internal/sandbox/kata.go` to the new
   tag so `ape sandbox up` (with an empty profile `image:`) resolves to it.

> **seccomp:** the base expects `seccomp=unconfined`. That is acceptable here —
> the Kata microVM is the security boundary, not the container's seccomp
> profile.

## Offline framework install

The image sets `ENV APEX_FRAMEWORK_REPO=/opt/apex-framework`, so inside a
workspace `ape framework setup` resolves the framework from the baked-in
checkout. Add `--no-fetch` to skip the `git fetch` and install fully offline:

```bash
ape framework setup --no-fetch     # installs skills + pipelines from /opt/apex-framework
```

## Known follow-ups

- **VMM/device tier.** GPU/USB workspaces (`vmm: qemu`) need host IOMMU +
  `vfio-pci` binding — a later phase; this image is VMM-agnostic.

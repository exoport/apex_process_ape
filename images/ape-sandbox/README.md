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
- **sshd** — key-auth-only, for the SSH / VS Code Remote access path
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
  -t ghcr.io/diegosz/ape-sandbox:v0.0.40 \
  images/ape-sandbox
```

`nerdctl build` works the same way (`nerdctl build -t … images/ape-sandbox`).

### Build args

| Arg                    | Default                             | Purpose                                             |
| ---------------------- | ----------------------------------- | --------------------------------------------------- |
| `BASE_IMAGE`           | `ghcr.io/agent-infra/sandbox:latest`| Base to build `FROM` — **pin to a digest** (below). |
| `APE_VERSION`          | `v0.0.40`                           | ape release tag; installed from the release tarball.|
| `FRAMEWORK_REF`        | `main`                              | `apex_process_framework` git ref cloned to `/opt`.  |
| `CLAUDE_CODE_VERSION`  | `latest`                            | `@anthropic-ai/claude-code` npm version.            |
| `PLAYWRIGHT_BROWSER`   | `chromium`                          | Playwright browser to install with OS deps.         |
| `NODE_MAJOR`           | `20`                                | Node LTS major (skipped if the base ships node).    |
| `TARGETARCH`           | `amd64`                             | Set by buildx; selects the ape release asset arch.  |

## Pinning policy (before publishing)

This image is **versioned with ape + the framework** — a workspace should be
reproducible. Before the first real publish:

1. **Pin the base to a digest.** Resolve the `agent-infra/sandbox` reference
   to a `…@sha256:…` digest and set `BASE_IMAGE` to it. Never ship a
   published image built on a floating `:latest`. Confirm the exact upstream
   registry coordinates against the [agent-infra/sandbox](https://github.com/agent-infra/sandbox)
   repo first.
2. **Tag to match the ape release** (e.g. `ghcr.io/diegosz/ape-sandbox:v0.0.40`).
3. **Update `sandbox.DefaultImage`** in `internal/sandbox/kata.go` to the new
   tag so `ape sandbox up` (with an empty profile `image:`) resolves to it.

> **seccomp:** the base expects `seccomp=unconfined`. That is acceptable here —
> the Kata microVM is the security boundary, not the container's seccomp
> profile.

## Known follow-ups

- **authorized_keys wiring.** sshd is key-auth-only; the per-workspace
  `~/.claude` composer does not yet drop an `authorized_keys` into the staged
  home. Until it does, use `ape sandbox attach`/`exec` (via nerdctl) rather
  than `ape sandbox ssh`, or add the key manually.
- **Offline framework install.** `/opt/apex-framework` is a plain checkout;
  wiring `ape framework setup` to prefer it (no network at provision) is a
  follow-up.
- **VMM/device tier.** GPU/USB workspaces (`vmm: qemu`) need host IOMMU +
  `vfio-pci` binding — a later phase; this image is VMM-agnostic.

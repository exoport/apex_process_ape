# ape-sandbox image

The official OCI image for `ape sandbox` Kata VM workspaces (PLAN-16 D6). Built
`FROM` [`agent-infra/sandbox`](https://github.com/agent-infra/sandbox) (browser
+ VS Code + MCP), plus claude / node / ape / git / sshd / chromium + Playwright.

**Public + framework-free (PLAN-20).** The private APEX framework is **not**
baked, so this image is public and pulls with **no credential**. At runtime
`aped` mounts a pinned host-side framework checkout **read-only** at
`/opt/apex-framework` (`ENV APEX_FRAMEWORK_REPO` points there), and a workspace
installs it offline with `ape framework setup --no-fetch`. The image runs as
numeric **`USER 0`** (required by aped's containerd driver).

## Build (anonymous — no secret)

```bash
# From the repo root. Multi-arch needs buildx; single-arch amd64 builds directly.
nerdctl build --build-arg APE_VERSION=v0.0.45 \
  -t ghcr.io/exoport/ape-sandbox:v0.0.45 \
  images/ape-sandbox
```

`docker build` works identically. On an `aped` host, add `--namespace aped` so
the image lands where `aped` reads it. `ape` is pulled from the **public**
releases; nothing here needs a token.

### Build args

| Arg | Default | Purpose |
| --- | --- | --- |
| `BASE_IMAGE` | `agent-infra/sandbox:1.11.0@sha256:6328d7fd…` | Digest-pinned base. |
| `APE_VERSION` | `v0.0.45` | ape release tag (public download). |
| `CLAUDE_CODE_VERSION` | `latest` | `@anthropic-ai/claude-code` npm version. |
| `PLAYWRIGHT_BROWSER` | `chromium` | Playwright browser to install. |
| `NODE_MAJOR` | `20` | Node LTS major (skipped if the base ships node). |
| `TARGETARCH` | `amd64` | Set by buildx; selects the ape asset arch. |

## Publish (public ghcr)

```bash
nerdctl login ghcr.io                          # PAT: write:packages
nerdctl push ghcr.io/exoport/ape-sandbox:v0.0.45
```

Keep the package **public** (it contains nothing private). Then update
`sandbox.DefaultImage` (`internal/sandbox/kata.go`) + `deploy/policy.yaml` to the
new tag per the pinning policy.

## Framework at runtime (not baked)

The framework is private, so it is delivered at workspace runtime, not baked
into this image — see
[`development/planning/plan-20_sandbox-mounts-and-framework-delivery.md`](../../development/planning/plan-20_sandbox-mounts-and-framework-delivery.md):
a pinned host-side checkout is mounted read-only at `/opt/apex-framework`, and
`ape framework setup --no-fetch` installs it into the project. That keeps this
image public and lets the framework version change without rebuilding it.

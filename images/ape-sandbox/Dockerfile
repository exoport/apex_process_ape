# syntax=docker/dockerfile:1

# ─────────────────────────────────────────────────────────────────────────────
# ape-sandbox — the official OCI image for `ape sandbox` Kata VM workspaces
# (PLAN-16 D6). Provisioned by `ape sandbox up` and run as a long-lived,
# hardware-isolated Kata microVM. Contains: claude, node, ape, git, the APEX
# framework, sshd, chromium + Playwright, and build tools.
#
# PINNING POLICY — read before publishing (see README.md):
#   * This image is versioned WITH ape + the framework. Tag it to match the
#     ape release and update `sandbox.DefaultImage` in internal/sandbox/kata.go.
#   * NEVER track a floating `:latest` base in a published image. Resolve
#     BASE_IMAGE to a digest (`…@sha256:…`) before the first real publish.
#   * The base (agent-infra/sandbox) expects `seccomp=unconfined`. That is
#     acceptable here: the Kata microVM *is* the security boundary, not the
#     container's seccomp profile.
# ─────────────────────────────────────────────────────────────────────────────

# Base / reference: agent-infra/sandbox (Apache-2.0) — headless browser
# (VNC + CDP), VS Code Server, terminal, and MCP servers. Build FROM it so
# workspaces get the browser + editor + MCP layer for free.
#
# TODO(publish): pin to a digest, e.g.
#   ARG BASE_IMAGE=ghcr.io/agent-infra/sandbox@sha256:<resolved-digest>
# Confirm the exact upstream registry/coordinates against
# https://github.com/agent-infra/sandbox before the first build.
ARG BASE_IMAGE=ghcr.io/agent-infra/sandbox:latest
FROM ${BASE_IMAGE}

# Pinned versions — bump deliberately, rebuild, then re-tag the image.
ARG APE_VERSION=v0.0.40
ARG FRAMEWORK_REF=main
ARG CLAUDE_CODE_VERSION=latest
ARG PLAYWRIGHT_BROWSER=chromium
ARG NODE_MAJOR=20
# buildx populates TARGETARCH (amd64 / arm64) for multi-arch builds.
ARG TARGETARCH=amd64

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
USER root
ENV DEBIAN_FRONTEND=noninteractive

# System packages: git, ssh server, build tools, curl/ca-certs, sudo.
RUN apt-get update && apt-get install -y --no-install-recommends \
      git openssh-server ca-certificates curl sudo build-essential \
 && rm -rf /var/lib/apt/lists/*

# Node.js LTS — only if the base doesn't already ship a usable node.
RUN if ! command -v node >/dev/null 2>&1; then \
      curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - && \
      apt-get install -y --no-install-recommends nodejs && \
      rm -rf /var/lib/apt/lists/*; \
    fi

# Claude Code CLI.
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"

# ape — pinned release tarball (mirrors docs/how-to/install.md, option 1).
RUN set -euo pipefail; \
    url="https://github.com/diegosz/apex_process_ape/releases/download/${APE_VERSION}/ape_linux_${TARGETARCH}.tar.gz"; \
    curl -fsSL "$url" | tar -xz -C /usr/local/bin ape; \
    /usr/local/bin/ape version

# APEX framework source, pinned. `ape framework setup` inside a mounted
# project installs the skills + pipelines from this offline checkout.
RUN git clone --depth 1 --branch "${FRAMEWORK_REF}" \
      https://github.com/diegosz/apex_process_framework.git /opt/apex-framework

# Chromium + Playwright (Excalidraw rendering / browser workloads run inside
# the VM, where a real guest kernel avoids gVisor's syscall-compat gaps).
RUN npx -y playwright@latest install --with-deps "${PLAYWRIGHT_BROWSER}"

# Workspace user. The Kata VM is the boundary, so in-guest privilege is not
# the security control; day-to-day work runs as an unprivileged `ape` user
# with passwordless sudo for in-guest setup convenience. Its home is
# /sandbox/home — the same path the composed ~/.claude is bind-mounted at —
# so HOME is consistent across `ssh` and `exec`, and sshd's default
# AuthorizedKeysFile (~/.ssh/authorized_keys) resolves to the composed
# /sandbox/home/.ssh/authorized_keys. The dir is created + chowned below.
RUN useradd --home-dir /sandbox/home --shell /bin/bash ape \
 && echo 'ape ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/ape \
 && chmod 0440 /etc/sudoers.d/ape

# sshd: key auth only, no root login, no passwords. Access is over the
# host-loopback port ape forwards (`ape sandbox up --ssh-port`); Phase 3
# moves it onto the Netbird overlay. The composer writes the workspace's
# authorized_keys into the bind-mounted /sandbox/home/.ssh (see
# internal/sandbox composeSSHAccess). StrictModes is off: the home is a
# virtio-fs bind whose in-guest uid/ownership need not match `ape`, which
# StrictModes would otherwise reject — acceptable because the Kata VM, not
# file ownership, is the boundary.
RUN mkdir -p /run/sshd \
 && sed -ri 's/^#?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && sed -ri 's/^#?PermitRootLogin.*/PermitRootLogin no/'               /etc/ssh/sshd_config \
 && sed -ri 's/^#?PubkeyAuthentication.*/PubkeyAuthentication yes/'     /etc/ssh/sshd_config \
 && sed -ri 's/^#?StrictModes.*/StrictModes no/'                        /etc/ssh/sshd_config

# The composed ~/.claude is bind-mounted at /sandbox/home and HOME points
# there at runtime (see internal/sandbox). Create the mountpoint + the
# project mountpoint.
RUN mkdir -p /sandbox/home /workspace && chown ape:ape /sandbox/home

COPY entrypoint.sh /usr/local/bin/ape-sandbox-entrypoint
RUN chmod 0755 /usr/local/bin/ape-sandbox-entrypoint

EXPOSE 22
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/ape-sandbox-entrypoint"]
# Default command keeps the workspace long-lived so `nerdctl exec`/`attach`
# work across many sessions. `ape sandbox up` provisions detached (-d).
CMD ["sleep", "infinity"]

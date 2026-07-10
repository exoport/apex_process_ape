# `_apex/sandbox/<name>.yaml` reference

A sandbox profile describes one `ape sandbox` workspace: which image and VMM
to run, how the project is mounted, how `~/.claude` is composed (credentials,
skills, git), and what public egress is allowed. Profiles live under
`_apex/sandbox/` in the project and are meant to be reviewed and checked into
git — nothing sensitive belongs in them (secrets are referenced by
`env:`/`file:` source, never inlined).

`ape sandbox up <name>` loads `_apex/sandbox/<name>.yaml` (override the file
with `--profile <other>`). See the [sandbox workspaces how-to](../how-to/sandbox-workspaces.md)
for the end-to-end flow.

> Kata VM workspaces are Phase 1 of the broader APEX Process Platform. Phases
> 2–4 (in-VM NATS worker, Netbird overlay networking, preview/staging
> environments) live in the `apex_process_platform` repo; the design is
> recorded in `development/planning/plan-16_kata-vm-workspaces.md`.

## Example

```yaml
name: dev
backend: kata                 # kata (only)
vmm: clh                      # clh (default) | qemu (device tier, later)
image: ""                     # "" → official ape-sandbox; or a custom OCI ref
mount: host-fs                # host-fs (default) | volume | ephemeral
credentials: oauth            # oauth (mode A) | api-key (mode B)
skills:                       # guest ~/.claude/skills (bare name or /abs/path)
  - apex-create-prd
agents: []                    # guest ~/.claude/agents (bare name or /abs/path)
ignore_project_settings: true
preferences:                  # → guest ~/.claude/settings.json (may carry hooks)
  model: opus
network:
  authorized_domains:         # public egress allowlist (CONNECT 443)
    - api.anthropic.com
    - "*.githubusercontent.com"
git:
  mode: none                  # none | token | deploy-key | agent
```

An `api-key` (mode B) profile instead reads the key from a source and injects
no credential files:

```yaml
name: ci
credentials: api-key
api_key_source: env:APE_JOB_ANTHROPIC_KEY
git:
  mode: token
  token_source: env:APE_JOB_GITHUB_TOKEN
```

## Fields

### Workspace shape

| Field     | Type   | Default     | Description                                                                                          |
| --------- | ------ | ----------- | ---------------------------------------------------------------------------------------------------- |
| `name`    | string | *required*  | Profile name. Free-form; the workspace name is the `ape sandbox up <name>` argument, not this field. |
| `backend` | enum   | `kata`      | Isolation backend. **`kata` only** — gVisor was dropped after the spike. Any other value is rejected.|
| `vmm`     | enum   | `clh`       | VMM Kata launches: `clh` (Cloud-Hypervisor, default) or `qemu` (device tier — GPU/USB, a later phase).|
| `image`   | string | `""`        | OCI image ref. Empty → the pinned official `ape-sandbox` image; or any custom ref.                   |
| `mount`   | enum   | `host-fs`   | How the project is mounted — see [Mount modes](#mount-modes).                                        |

### Credentials

| Field            | Type | Default    | Description                                                                             |
| ---------------- | ---- | ---------- | --------------------------------------------------------------------------------------- |
| `credentials`    | enum | *required* | `oauth` (mode A) binds the host's real `~/.claude/.credentials.json` (rw, for refresh); `api-key` (mode B) injects `ANTHROPIC_API_KEY` from `api_key_source` and writes no credential files. |
| `api_key_source` | string | —        | Required for `api-key`; forbidden for `oauth`. A secret source: `env:NAME` or `file:PATH`. |

### `~/.claude` composition

| Field                     | Type          | Default | Description                                                                                          |
| ------------------------- | ------------- | ------- | ---------------------------------------------------------------------------------------------------- |
| `skills`                  | list\<string> | `[]`    | Skills copied into the guest `~/.claude/skills`. A bare name resolves under the host `~/.claude/skills/<name>`; an absolute path copies that directory. Empty → nothing (nothing leaks by omission). |
| `agents`                  | list\<string> | `[]`    | Agents copied into `~/.claude/agents`. Bare name → host `~/.claude/agents/<name>.md`; or an absolute `.md` path. |
| `hooks`                   | list\<string> | `[]`    | **Reserved — must be empty in v1.** Express user-layer hooks under `preferences.hooks` instead; a non-empty list is rejected. |
| `project_skills_overlay`  | string        | `""`    | Reserved for project-skill overlay resolution.                                                       |
| `ignore_project_settings` | bool          | `false` | Passed through to the guest session's settings resolution.                                           |
| `preferences`             | map           | `{}`    | Written verbatim to the guest `~/.claude/settings.json`. May carry a `hooks` block.                  |

### `network` — public egress (D4)

Deny-by-default. Egress rides a host-side CONNECT proxy (`HTTPS_PROXY` into
the guest); every connection, allowed or denied, is recorded to
`egress-audit.jsonl` (hostnames only, never payloads).

| Field                | Type          | Default | Description                                                                                          |
| -------------------- | ------------- | ------- | ---------------------------------------------------------------------------------------------------- |
| `authorized_domains` | list\<string> | `[]`    | Allowlisted hostnames on port 443. Exact (`api.anthropic.com`) or a single leading wildcard (`*.githubusercontent.com`, matches any subdomain depth but not the apex). |
| `direct_allow`       | list\<string> | `[]`    | Fixed `host:port` pairs for non-HTTP endpoints (e.g. `nats.example.com:4222`). No wildcards; numeric port required. |

### `git` — credential composition (D5)

| Field          | Type   | Default | Description                                                                                          |
| -------------- | ------ | ------- | ---------------------------------------------------------------------------------------------------- |
| `git.mode`     | enum   | `none`  | `none` · `token` (generated credential helper serving an env token over HTTPS) · `deploy-key` (read-only key bind + pinned `known_hosts`) · `agent` (bind the host ssh-agent socket — live signing for the workspace's life). |
| `git.token_source` | string | —   | Required for `token`. A secret source (`env:NAME` / `file:PATH`). The token rides env, never a file. |
| `git.deploy_key`   | string | —   | Required for `deploy-key`. Host path to the private key (bound read-only).                            |

### `mounts` — extra binds

| Field             | Type          | Default | Description                                                        |
| ----------------- | ------------- | ------- | ------------------------------------------------------------------ |
| `mounts.extra_rw` | list\<string> | `[]`    | Extra host paths bind-mounted read-write at the same path in-guest.|

### `access` — inbound SSH (D7)

`ape sandbox ssh` is key-auth-only. List the public key(s) the workspace's
sshd should accept; the composer writes them to the guest
`~/.ssh/authorized_keys`. Empty → key auth is unconfigured (use
`ape sandbox attach`/`exec`, which go through `nerdctl`).

| Field                    | Type          | Default | Description                                                                                          |
| ------------------------ | ------------- | ------- | ---------------------------------------------------------------------------------------------------- |
| `access.authorized_keys` | list\<string> | `[]`    | Each entry is a public-key literal (`ssh-ed25519 AAAA… me@host`) or a path to a `.pub` / `authorized_keys` file (`~/.ssh/id_ed25519.pub`; a leading `~` expands to the host home). |

```yaml
access:
  authorized_keys:
    - ~/.ssh/id_ed25519.pub
```

## Mount modes

| Mode        | Project source                              | Use                                                                 |
| ----------- | ------------------------------------------- | ------------------------------------------------------------------- |
| `host-fs`   | The host project, shared over virtio-fs (rw)| **Local-dev default** — edits live-reflect both ways.               |
| `volume`    | A persistent VM-owned block volume          | Long-lived server workspaces — survives freeze/unfreeze, no host coupling. |
| `ephemeral` | Nothing from the host                       | Untrusted / preview work — the workspace clones the repo in-guest and discards it on teardown. |

## Secret sources

`api_key_source` and `git.token_source` never contain a literal secret — they
name where to read it:

- `env:NAME` — read environment variable `NAME` (trimmed; empty is an error).
- `file:PATH` — read file `PATH` (trimmed; empty is an error).

## Validation

`ape sandbox up` fails loudly at load rather than producing a broken or
insecure workspace. A profile is rejected when:

- `name` is empty; an unknown YAML key is present (typos are hard errors).
- `backend` is not `kata`; `vmm` is not `clh`/`qemu`; `mount` is not
  `host-fs`/`volume`/`ephemeral`.
- `credentials` is missing, or `api-key` without `api_key_source`, or `oauth`
  *with* `api_key_source`.
- a secret source uses a scheme other than `env:`/`file:`.
- `git.mode: token` without `git.token_source`, or `deploy-key` without
  `git.deploy_key`.
- `hooks:` is non-empty (reserved in v1).
- an `authorized_domains` entry has a non-leading or double wildcard; a
  `direct_allow` entry lacks `host:port`, has a wildcard, or a non-numeric port.
- an `access.authorized_keys` entry is empty (a missing key *file* is caught at
  provision time, not load time — matching `git.deploy_key`).

## Honest boundaries

- **The project mount is inside the boundary.** In `host-fs` mode an in-VM
  session can write `.git/hooks`, `Makefile`s, or direnv files the *host* may
  later execute. Use `mount: ephemeral` for untrusted work.
- **Mode A places the full OAuth token inside the boundary.** Egress
  allowlisting doesn't stop exfiltration *to Anthropic* via prompt content.
  Prefer `credentials: api-key` with a scoped, low-limit key for untrusted work.
- **`git.mode: agent`** binds a live signing capability (any key in the agent)
  for the workspace's lifetime — prefer `token` for untrusted work.

See [Bridge security model](bridge-security.md) for how the in-guest `--web`
bridge composes with the VM boundary.

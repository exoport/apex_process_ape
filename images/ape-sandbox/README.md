# ape-sandbox image → public `exoport/ape-sandbox` repo

The official `ape-sandbox` OCI image (PLAN-16 D6) is **not built from this
repo** — this repo is intentionally a CLI only (see `CLAUDE.md`). It is
**public and framework-free**, built + published from the separate public
**`exoport/ape-sandbox`** repo to the public **`ghcr.io/exoport/ape-sandbox`**
package (builds anonymously — no secret).

The private APEX framework is **not** baked; `aped` mounts a pinned host-side
framework checkout **read-only** at `/opt/apex-framework` at runtime, and a
workspace installs it with `ape framework setup --no-fetch` (PLAN-20).

Neither is **`ape`** (PLAN-23): `aped` mounts the binary installed beside it
read-only at `/opt/ape/bin`, first on `PATH`, so a workspace runs the version
matching the daemon that provisioned it instead of whatever release the image was
built with. That is also why this image no longer references an `ape` release at
all — the build has no dependency on `apex_process_ape`.

`ape sandbox` resolves the image via `aped`'s pinned default
(`sandbox.DefaultImage` in `internal/sandbox/kata.go` — currently
`ghcr.io/exoport/ape-sandbox:v1.1.0`, **digest-pinned** to the OCI index so
re-pushing the tag cannot change what workspaces run) or a per-request `--image`
/ profile `image:` override. The `aped` policy `images:` allow-list in
`deploy/policy.yaml` is matched by **exact string** against the resolved ref, so
bumping one without the other produces a policy denial rather than a pull error.

The image carries its **own version line** — it changes for reasons `ape` does
not (a new base, a newer asdf/bingo, a Playwright bump). Since PLAN-23 there is
only **one** pin, and it points this way (`DefaultImage` here → the image);
nothing in the image repo references an `ape` release. The old in-guest version
floor went with the baked binary: a *delivered* `ape` is the node's own, so it
cannot predate the daemon that mounts it. See
[`development/planning/plan-20_sandbox-mounts-and-framework-delivery.md`](../../development/planning/plan-20_sandbox-mounts-and-framework-delivery.md).

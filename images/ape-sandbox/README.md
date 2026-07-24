# ape-sandbox image → public `exoport/ape-sandbox` repo

The official `ape-sandbox` OCI image (PLAN-16 D6) is **not built from this
repo** — this repo is intentionally a CLI only (see `CLAUDE.md`). It is
**public and framework-free**, built + published from the separate public
**`exoport/ape-sandbox`** repo to the public **`ghcr.io/exoport/ape-sandbox`**
package (builds anonymously — no secret).

The private APEX framework is **not** baked; `aped` mounts a pinned host-side
framework checkout **read-only** at `/opt/apex-framework` at runtime, and a
workspace installs it with `ape framework setup --no-fetch` (PLAN-20).

`ape sandbox` resolves the image via `aped`'s pinned default
(`sandbox.DefaultImage` in `internal/sandbox/kata.go`) or a per-request
`--image` / profile `image:` override. See
[`development/planning/plan-20_sandbox-mounts-and-framework-delivery.md`](../../development/planning/plan-20_sandbox-mounts-and-framework-delivery.md).

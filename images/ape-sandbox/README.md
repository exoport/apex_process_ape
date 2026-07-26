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
(`sandbox.DefaultImage` in `internal/sandbox/kata.go` — currently
`ghcr.io/exoport/ape-sandbox:v1.0.0`) or a per-request `--image` / profile
`image:` override. The `aped` policy `images:` allow-list in `deploy/policy.yaml`
is checked **independently** of that default, so bumping one without the other
produces a policy denial rather than a pull error.

The image carries its **own version line** — it changes for reasons `ape` does
not (a new base, a newer asdf/bingo, a Playwright bump), so the two are ordinary
dependency pins, one in each direction (`ARG APE_VERSION` there,
`DefaultImage` here). The baked `ape` has a floor of **v0.0.49**: earlier builds
lack the scoped `safe.directory` fix and cannot read the read-only, host-owned
framework mount. See
[`development/planning/plan-20_sandbox-mounts-and-framework-delivery.md`](../../development/planning/plan-20_sandbox-mounts-and-framework-delivery.md).

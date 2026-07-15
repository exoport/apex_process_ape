# ape-sandbox image — moved

The official `ape-sandbox` OCI image (PLAN-16 D6) is **no longer built from this
repo.** Because it bakes the **private** APEX framework, its Dockerfile + build
pipeline live in the private **`exoar/ape-sandbox`** repo and publish to a
private `ghcr.io/exoar/ape-sandbox` package — keeping a framework-bearing image
out of this public repo (and avoiding two Dockerfiles drifting).

`ape sandbox` resolves the image via `aped`'s pinned default
(`sandbox.DefaultImage` in `internal/sandbox/kata.go`) or a per-request
`--image` / profile `image:` override. See
[`development/planning/plan-16_kata-vm-workspaces.md`](../../development/planning/plan-16_kata-vm-workspaces.md)
(D6 note, 2026-07-15) for the rationale, the live-validation results, and the
build findings (authenticated framework fetch, numeric `USER`, `aped` pull creds).

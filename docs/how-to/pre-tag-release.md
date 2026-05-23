# How-to — verify a release before tagging

A safe release sequence that catches Windows-runtime and release-config bugs *before* a public release fires. Two complementary gates: a local one (`make ci-local`) that covers most ground in 30–60 seconds, and a remote one (a pre-release tag) that exercises the actual GitHub Windows runner.

## Why this exists

The default release flow (`git tag v0.0.X && git push origin v0.0.X`) triggers the public Release workflow immediately. If the tagged SHA has a Windows-only test bug, you ship a broken release — exactly what happened between v0.0.18 and v0.0.19. This guide describes a two-step gate so that doesn't happen again.

## Step 1 — local verification

Run every gate CI and the release workflow would run, against the current working tree:

```bash
make ci-local
```

This expands to:

1. `make test` — Linux race-detector test suite.
2. `make lint` — `golangci-lint`.
3. `make govulncheck` — vulnerability scan.
4. `make xcompile-windows` — cross-compile + cross-vet for `GOOS=windows GOARCH=amd64`, plus a per-package test-binary cross-compile. Catches portability *compile* errors (broken `//go:build` tags, missing functions, unused imports per platform).
5. `make snapshot` — `goreleaser` snapshot build. Catches release-config regressions before the real release machinery sees them.

What `ci-local` catches:

- ✅ Linux test failures, including race conditions.
- ✅ Lint and vulnerability issues.
- ✅ Per-package Windows compile errors and `go vet` regressions.
- ✅ Goreleaser config bugs (archive name collisions, missing build targets, signing config drift).

What it **does not** catch:

- ❌ Windows runtime behaviour — e.g. `exec.LookPath` needing `.exe`, `os.UserHomeDir` reading `%USERPROFILE%` not `$HOME`, path-separator handling. These only show up when the test binary actually runs on Windows.

For those, use step 2.

## Step 2 — remote pre-release tag

After local gates pass and `main` is pushed, tag a pre-release and push it:

```bash
git push origin main
git tag -a v0.0.20-rc1 -m "v0.0.20 release candidate 1"
git push origin v0.0.20-rc1
```

The CI workflow re-runs the full matrix (Linux + Windows + lint + vuln) against the *exact tagged SHA*. The Release workflow is deliberately **not** triggered by this tag — its filter only matches final-semver tags (`v[0-9]+.[0-9]+.[0-9]+`), so no GitHub Release is created.

Wait for that CI run to finish green. Then promote:

```bash
git tag -a v0.0.20 -m "v0.0.20 — what changed"
git push origin v0.0.20
```

The final tag fires the Release workflow against the same SHA you already verified.

If the rc tag's CI fails, **don't** push the final tag. Fix the issue, push the fix to main, then `v0.0.20-rc2` for a second attempt:

```bash
git push origin main
git tag -a v0.0.20-rc2 -m "v0.0.20 release candidate 2"
git push origin v0.0.20-rc2
```

There's no limit on rc numbers. Delete obsolete rc tags afterwards if you want to keep the tag list clean:

```bash
git push origin :refs/tags/v0.0.20-rc1
git tag -d v0.0.20-rc1
```

## Tag-filter mechanics

| Workflow      | Trigger condition                                                          |
| ------------- | -------------------------------------------------------------------------- |
| `ci.yml`      | push to `main`, pull request, **or** push of any `vX.Y.Z-*` pre-release tag |
| `release.yml` | push of a `vX.Y.Z` final-semver tag (no suffix)                             |

Both filters use the `vX.Y.Z` regex shape rather than `v*` so a stray non-semver tag (e.g. `vendor-update`, `v2024`) doesn't trigger either workflow.

## When to skip step 2

If your change is **Go-only** with no OS-conditional code paths (no `runtime.GOOS` switches, no `exec.LookPath`, no `os.UserHomeDir`, no path-separator-sensitive code, no shell-out, no new file I/O patterns), `make ci-local` is sufficient — the rc-tag round-trip is overhead for no benefit.

The bugs that escaped to v0.0.18 were *both* OS-conditional (PATHEXT extension on `exec.LookPath`, `USERPROFILE` vs `$HOME`), so anything touching those primitives is a candidate for step 2.

## Related

- [How to run `ape doctor` in CI](run-doctor-in-ci.md) — strict JSON invocation, exit codes, GitHub Actions snippet.
- `.github/workflows/ci.yml` and `.github/workflows/release.yml` — the actual filter definitions live here.
- `Makefile` — `ci-local`, `xcompile-windows` targets.

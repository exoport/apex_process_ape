# How-to — verify a release before tagging

A safe release sequence that catches Windows-runtime and release-config bugs *before* the public Release workflow fires. Two complementary gates: a local one (`make ci-local`) covering most ground in 30–60 seconds, and a remote one (the regular push-to-`main` CI run) that exercises the actual GitHub Linux + Windows runners against the exact SHA you're about to tag.

## Why this exists

The naive release flow (`git tag v0.0.X && git push origin v0.0.X`) triggers the public Release workflow immediately. If the tagged SHA has a Windows-only test bug, you ship a broken release — exactly what happened between v0.0.18 and v0.0.19. This guide describes the two-step gate that prevents it.

> **History — why this guide no longer uses an rc tag.** Earlier
> versions of this flow used a `vX.Y.Z-rcN` pre-release tag to drive
> the remote CI gate. That was dropped after the v0.0.21 incident:
> when the rc and final annotated tags landed on the same commit,
> goreleaser's `git describe`-based tag resolution misrouted the
> build artifacts to the rc prerelease and no `v0.0.21` GitHub
> Release was ever created. The same misrouting silently affected
> v0.0.20. The fix is to skip the rc cycle entirely — push to `main`,
> wait for the regular CI run to pass on the SHA you'll tag, then
> push the final tag against the same SHA. The final tag is the only
> annotated tag on that commit, so goreleaser cannot pick the wrong
> one.

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

- Linux test failures, including race conditions.
- Lint and vulnerability issues.
- Per-package Windows compile errors and `go vet` regressions.
- Goreleaser config bugs (archive name collisions, missing build targets, signing config drift).

What it **does not** catch:

- Windows runtime behaviour — e.g. `exec.LookPath` needing `.exe`, `os.UserHomeDir` reading `%USERPROFILE%` not `$HOME`, path-separator handling. These only show up when the test binary actually runs on Windows.

For those, use step 2.

## Step 2 — remote CI on the SHA you'll tag

Push your commits to `main`:

```bash
git push origin main
```

The CI workflow re-runs the full matrix (Linux + Windows + lint + vuln) against the pushed SHA. **Wait for it to finish green** — open `https://github.com/diegosz/apex_process_ape/actions`, find the CI run for that SHA, and confirm `conclusion: success`.

Only then push the final tag:

```bash
git tag -a v0.0.X -m "v0.0.X — what changed"
git push origin v0.0.X
```

The tag fires `release.yml` against the same SHA the CI just verified.

If the push-to-`main` CI fails, **don't** tag. Push more commits to `main` until CI is green, then tag the resulting SHA.

## Tag-filter mechanics

| Workflow      | Trigger condition                                                  |
| ------------- | ------------------------------------------------------------------ |
| `ci.yml`      | push to `main`, pull request                                       |
| `release.yml` | push of a `vX.Y.Z` final-semver tag (no suffix)                    |

`release.yml`'s `push.tags` glob `v[0-9]+.[0-9]+.[0-9]+` is not end-anchored, so it would still match a stray tag like `v1.2.3-rc1`. A job-level `if: !contains(github.ref_name, '-')` guard keeps the workflow safe even if a pre-release tag slips past the glob.

## When to skip step 2

If your change is **Go-only** with no OS-conditional code paths (no `runtime.GOOS` switches, no `exec.LookPath`, no `os.UserHomeDir`, no path-separator-sensitive code, no shell-out, no new file I/O patterns), `make ci-local` is sufficient — the round-trip through GitHub Actions is overhead for no benefit.

The bugs that escaped to v0.0.18 were *both* OS-conditional (PATHEXT extension on `exec.LookPath`, `USERPROFILE` vs `$HOME`), so anything touching those primitives is a candidate for step 2.

## Related

- [How to run `ape doctor` in CI](run-doctor-in-ci.md) — strict JSON invocation, exit codes, GitHub Actions snippet.
- `.github/workflows/ci.yml` and `.github/workflows/release.yml` — the actual filter definitions live here.
- `Makefile` — `ci-local`, `xcompile-windows` targets.
- `.claude/skills/release/SKILL.md` — automated `/release` walkthrough that drives this flow.

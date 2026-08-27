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
4. `make docs-check` — every doc reachable from `docs/README.md`, every link resolving.
5. `make check-prices` — the built-in price table against the model ids the local Claude Code is emitting.
6. `make xcompile-windows` — cross-compile + cross-vet for `GOOS=windows GOARCH=amd64`, plus a per-package test-binary cross-compile. Catches portability *compile* errors (broken `//go:build` tags, missing functions, unused imports per platform).
7. `make snapshot` — `goreleaser` snapshot build. Catches release-config regressions before the real release machinery sees them.

What `ci-local` catches:

- Linux test failures, including race conditions.
- Lint and vulnerability issues.
- Per-package Windows compile errors and `go vet` regressions.
- Goreleaser config bugs (archive name collisions, missing build targets, signing config drift).

What it **does not** catch:

- Windows runtime behaviour — e.g. `exec.LookPath` needing `.exe`, `os.UserHomeDir` reading `%USERPROFILE%` not `$HOME`, path-separator handling. These only show up when the test binary actually runs on Windows. For those, use step 2.
- The installed Claude Code having broken the contract ape drives it through. For that, use step 1b.

## Step 1b — the Claude Code harness contract

```bash
make check-harness HOOK_PROJECT=~/work/some-apex-project
```

`ci-local` proves *ape* is internally consistent. It cannot prove ape still works, because ape's real dependency is not a library it pins — it is the `claude` binary on the machine, which auto-updates on a schedule ape does not control and makes no compatibility promise about its TUI, its flags, its hook payloads, or its transcript format.

`check-harness` is the whole sweep — three gates that each read what the locally-installed Claude Code is *actually doing*:

| Gate | Reads | Catches |
| --- | --- | --- |
| `check-prices` | `~/.claude/projects` transcripts | a model id or family alias the price table does not cover — tokens keep counting, cost silently goes to zero |
| `check-hooks` | `$HOOK_PROJECT/_output/ape/tasks` runlogs | a hook field ape's step-completion gates read being renamed or dropped — the gate stops firing and ape resumes reporting success on runs that did nothing |
| `check-claude` | a live PTY session | everything below |

> **`check-hooks` needs a real project.** Hook drift is observed from the `hook-events.jsonl` files ape itself wrote, so it can only be judged against a project you have actually run `ape` pipelines in. `HOOK_PROJECT` defaults to `.` — the ape repo, which has no runlogs and will always report a skip. Point it somewhere real or the gate is decorative.

`make check-claude` on its own spawns the locally-installed Claude Code through ape's own PTY path and verifies the couplings that would otherwise fail *silently*:

| Coupling | What breaks if it moves |
| --- | --- |
| `bypass permissions on` footer | `WaitForReady`'s primary ready signal. Falls back to the `❯` glyph, so nothing errors — until that goes too. |
| `❯` prompt glyph | The fallback ready signal, and `emptyPromptRe`'s anchor. |
| Pre-REPL modals | A *new* blocking modal `blockingModals` cannot dismiss makes every run idle until timeout. |
| `--dangerously-skip-permissions`, `--model` | A rejected flag means no session starts at all. |
| `CLAUDE_CODE_EFFORT_LEVEL` | Every run silently uses the harness default effort instead of the one the pipeline asked for. |
| Family-alias model ids | An id that no longer exists does **not** error — Claude Code starts anyway on a fallback model. |
| Transcript persistence | The v0.0.28–32 root cause: every cost, token, and model figure silently becomes zero. |
| `claude --version` shape | The manifest's `claude_version` stamp and hookdrift's version attribution stop resolving. |

None of this is part of `ci-local`, and none of it runs in GitHub CI: it needs `claude` on PATH, working auth, network, and local runlogs — none of which a CI runner has. Run it on a developer machine before tagging, and after any Claude Code upgrade.

Takes ~40 s. Every check but one costs zero tokens — they read local artifacts, or launch the REPL and read the rendered pane. The exception submits a single short Haiku turn to prove a transcript is really written and that ape can still parse it; `APE_CLAUDE_LIVE_TOKENS=0` skips that one.

> **A SKIP is not a pass.** Each gate reports "not verified" rather than green when it finds nothing to judge — no transcripts, no runlogs, no `claude` on PATH. Read the output rather than the exit code: absence of evidence is not coverage, and every one of these is designed so an empty run cannot masquerade as a clean one.

## Step 1c — the APEX framework contract

Step 1b asks whether the local **Claude Code** still honours what ape drives it
through. This asks the other question: does ape still satisfy what the local
**APEX framework** requires? Two dependencies, two release schedules.

```bash
make check-framework APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
```

Framework v0.11.0 moved deterministic project-data work into ape subcommands
and deleted every fallback branch — 74 of 90 skills shell out. An ape missing
one does not degrade: it fails a skill deep inside a multi-hour stage. Four
gates close that:

| Gate | Asks |
| --- | --- |
| `TestCommandSurface_AgainstRealManifest` | does this binary provide every command the framework's shipped `_apex/ape-commands.yaml` requires? |
| `TestContract_LiveConfigTemplate` | do the config variables ape resolves match the framework's **live** template, not a copied fixture? |
| `TestParity_*` | do the commands that replaced the retired Python scripts still behave identically? |
| `ape doctor --only framework.command_surface,framework.terminal_contracts` | the same surface as a project actually **received** it — what a skill meets at run time |

Both framework layouts resolve: released (`_apex/` at the repo root) and build
(nested under `framework/`).

Reading the result:

- **Variable unset** → "framework contract NOT verified", exit 0. A skip, not a pass.
- **Path that is not a framework checkout** → hard error. Setting the variable
  says you want the gate to run, so a path resolving to nothing is a typo —
  every gate would otherwise have passed green.
- **`TestParity_*` all skip** → expected against a modern framework. The
  scripts they compare against are retired, which is what that gate was built
  to guard.

> **Why this can't be a CI job.** Every check here is a statement about the harness *installed on this machine right now*. A CI runner has none of the inputs, so the honest result there is a skip — and a gate that always skips is worse than no gate, because it reads as a pass. The same reasoning is why `ape costs coverage` exits 0 with "coverage NOT verified" rather than green when it finds nothing.

## Step 2 — remote CI on the SHA you'll tag

Push your commits to `main`:

```bash
git push origin main
```

The CI workflow re-runs the full matrix (Linux + Windows + lint + vuln) against the pushed SHA. **Wait for it to finish green** — open `https://github.com/exoport/apex_process_ape/actions`, find the CI run for that SHA, and confirm `conclusion: success`.

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

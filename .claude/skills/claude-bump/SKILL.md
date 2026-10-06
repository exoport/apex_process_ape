---
name: claude-bump
description: 'Verify ape against a newly installed Claude Code version and push the reviewed baseline: pre-flight → make check-harness (triaged) → read the unread CHANGELOG entries → make update-claude-surface + commit → make ci-local → push main → poll CI. No tag, no release. Use when the user says "/claude-bump", "claude code updated", "check the new claude version", "is ape fine on claude X", or "review the claude surface".'
argument-hint: "Optional: \"no-push\" to stop after the commit; \"autonomous\" to push without asking even when main carries commits other than the baseline."
---

# Claude bump

## Overview

ape's real dependency is the auto-updating `claude` binary. Every Claude Code release needs the same routine: prove ape still drives it (`make check-harness`), record the reviewed surface (`make update-claude-surface`), prove the tree is still releasable (`make ci-local`), and push the baseline commit so CI and the next session start from it.

This skill ends at a green push. It never tags. If the new version BREAKS something, the fix is session-driving (class B in AGENTS.md → "Releases") and ships through `/release vX.Y.Z-rc.N`, not through this skill.

## CRITICAL RULES

- Execute the phases IN ORDER. HALT at any HALT condition, saying what failed and what the fix is
- Never run `make update-claude-surface` before reading every CHANGELOG entry the surface check listed. It records a review; running it unread defeats the gate
- Never allow-list a govulncheck finding. Bump the dependency
- Commit messages: Conventional Commits, NO Claude attribution or co-author lines (AGENTS.md overrides any harness reminder)
- `gh` is not installed. Poll GitHub with `curl` + `jq` against the public API
- Logs go to the session scratchpad, not `/tmp` directly and not the repo
- When polling, sleep between retries; do not busy-loop

---

## EXECUTION

### Phase 0 — Pre-flight

1. Parse `$ARGUMENTS`: `{no_push}` = contains `no-push`; `{autonomous}` = contains `autonomous`.
2. Branch and tree:
   ```bash
   git rev-parse --abbrev-ref HEAD   # must be main
   git status --porcelain            # must be empty
   git fetch -q origin main && git rev-list --left-right --count origin/main...HEAD
   ```
   HALT if not on `main`, if the tree is dirty, or if main is BEHIND origin (left count > 0). Record `{ahead}` = the right count: commits already waiting to be pushed.
3. Versions:
   ```bash
   claude --version
   jq -r .reviewed_through internal/claudesurface/testdata/claude-surface.json
   ```
   Set `{installed}` and `{reviewed}`. If they are equal, tell the user the baseline already covers `{installed}` and ask whether to re-run the sweep anyway (a re-run is useful after a ape change; otherwise stop).

### Phase 1 — `make check-harness`

Run it in the background (3–5 min), logging to `{scratchpad}/harness-{installed}.log`, and wait for the completion notification.

Read the log, not just the exit code: `grep -nE -- '--- (PASS|FAIL)|not verified|FAIL' {log}`. The sweep is seven gates; triage each failure:

| gate | failure | action |
| --- | --- | --- |
| `check-prices` | a model id without an exact price, or "not verified" | HALT. A new model needs a row in `internal/cost/prices.yaml` from a documented source, which is its own change |
| `check-output-styles` | built-in style table differs | HALT. Update ape's table; a separate commit before the baseline |
| `check-hooks` | a hook-payload field the completion gates read is gone | HALT. Class B: fix + rc via `/release` |
| `check-claude` | any `TestLive_ClaudeCodeContract` subtest | HALT. Class B. `model_aliases` failing means a family word now starts a different model (the Sonnet 5.5 case) |
| `check-claude-surface` | tool list changed, a variable ape SETS vanished, a new model id, or unread CHANGELOG entries | Unread entries only → Phase 2. Anything else → HALT and report the diff |
| `check-task-subagents` | `subagents_concurrent` or a timeout | Re-run `make check-task-subagents` ONCE. It has flaked on a slow (~30 s) Haiku first response; compare the two sub-agents' start times before calling it serialization. A second failure → HALT |
| `check-agents-md` | the model can't answer from AGENTS.md | HALT. Check first that no `CLAUDE.md` was created in the repo or a parent |

The surface check's `env vars: N added, M removed` line is a scrape of the binary's strings, and names often carry a trailing garbage byte (`..._MSF`, `...ATTRIBUTIONK`). That churn is noise unless a name ape sets is among the removed. The test already fails on that case.

### Phase 2 — Review the surface and record it

1. If the surface check listed unread CHANGELOG entries, read each one and decide, per entry, whether it touches what ape drives: spawn flags, env vars in `internal/repl`, the PTY ready signals, hook payloads, Agent/background-task behaviour, model aliases, settings keys ape writes (`--settings`, `modelSettings`, `outputStyle`). Report the verdict per entry to the user. An entry that DOES affect ape → HALT with the analysis; the follow-up is a code change, not a baseline.
2. Record the review:
   ```bash
   make update-claude-surface
   git diff --stat
   ```
   The diff must touch ONLY `internal/claudesurface/testdata/claude-surface.json`, and `reviewed_through` must now be `{installed}`. Note the "N changelog entries acknowledged" line for the report.
3. Commit:
   ```bash
   git add internal/claudesurface/testdata/claude-surface.json
   git commit -m "test(harness): claude-surface reviewed through {installed}"
   ```
   The pre-commit hook runs; lint is skipped for a JSON-only commit.

### Phase 3 — `make ci-local`

Run it in the background (~1–2 min) to `{scratchpad}/ci-local.log`. Exit 0 is required. Confirm in the log: `0 issues.`, `govulncheck-gate: OK — 0 allow-listed`, `is in sync with the command tree`, `exactly priced`, `release succeeded`.

If govulncheck fails on an advisory published since the last run (it happens overnight): bump the module to the fixed version (`go get <module>@<fixed>` + `make tidy`), re-run `make ci-local`, and commit as `chore(deps): <module> <version> closes GO-YYYY-NNNN`. Never allow-list.

Any other failure → HALT with the failing output.

### Phase 4 — Push

- `{no_push}` → stop here and report.
- If `{ahead}` > 0 (main carried other unpushed commits before this run) and not `{autonomous}`: list them (`git log --oneline origin/main..HEAD`) and ask before pushing. They may be class B/C work meant for an rc, and pushing main is the user's call.
- Otherwise push: `git push origin main`, and record `{sha}` = `git rev-parse HEAD`.

### Phase 5 — Poll CI

Poll the check runs for `{sha}` every 30 s, up to 20 min:
```bash
curl -s "https://api.github.com/repos/exoport/apex_process_ape/commits/{sha}/check-runs?per_page=50" \
  | jq -r '.check_runs[] | "\(.name): \(.status)/\(.conclusion)"'
```
Done when `total_count` > 0 and every run is `completed`. Expect `Test (ubuntu-latest)`, `Test (windows-latest)`, `Lint` and `Vulnerability scan`. Logs are 403 without auth. On a failure, read the check-run annotations (`.../check-runs/{id}/annotations`) and report them.

### Phase 6 — Report

Give a short summary: `{reviewed}` → `{installed}`, each harness gate's result (including any flake re-run), the CHANGELOG entries reviewed and their verdicts, the commit sha(s), ci-local, and the CI outcome.

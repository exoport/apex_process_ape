---
plan_id: PLAN-9
created_at: 2026-07-02
implemented_at: 2026-07-04
status: implemented (v0.0.36)
implementation_notes: Shipped in v0.0.36. F1 (all three bugs — ape --version, ape update human output, costs help) done. F2 (PTY-only removal) done — deleted runStages/buildArgv/runClaude + the stream-json stdout parse, removed -P/--programmatic, -I/--interactive, --eval (now hidden, error with a pointer to why-pty-only.md), collapsed resolvePipelineMode/describeMode/PipelineMode to the UI axis, deleted runPlain/runWithTUI and the --web -P path, dropped RunOptions.Interactive; programmatic-path tests ported to the interactive PTY shim + a resolveCommitOutcome unit test. F3 substantially done — --output-format on pattern/adr/trait/sessions, hidden stubs, bootstrap --no-picker (deprecated --no-tui alias), exitcodes.go, update-check skip on hidden commands, root Long rewrite, Example blocks; NOT done — cobra Example on *every* command (only touched ones) and costs gaining a yaml output (still human|json). F4 partial (scoped to docs directly affected by the code change) — why-pty-only.md + claude-spawn-modes / invocation-matrix / exec-modes / bridge-architecture / step-contract / tui-keybindings / pipeline-run-manifest refreshed for PTY-only + per-model, indexes updated; DEFERRED F4 items — the first tutorial (tutorials/first-pipeline.md), generated reference/cli.md + its make target, standalone reference/exit-codes.md and cost-model.md, the docs link-check step in make ci-local, and the CHANGELOG v0.0.21 backfill. See v0.0.36 CHANGELOG.
tags:
  - cli-ux
  - docs
  - bugfix
  - pty-only
  - dependency-removal
summary: Fix the three shipping CLI bugs (costs help advertises unimplemented subcommands, ape update human output broken by a never-matching type assertion, no root --version), standardize --output-format / exit codes / help text across the command surface, and remove the last non-PTY claude execution path (`buildArgv → runClaude → runStages`, reached via `-P`/`--eval` and the programmatic UI branches). Removal is safe — the apex_process_framework_eval harness was audited 2026-07-02 and re-audited 2026-07-03 (post `ape task` migration) and never uses `--eval`/`-P`; it drives ape via `ape task --output-format json` (reads the stdout JSON envelope) and `ape pipeline --no-tui` (interactive PTY; reads manifest.yaml), consuming only the envelope + exit code + manifest.yaml + on-disk artifacts. Companion docs overhaul: fix the ~50%-stale docs index, refresh README, merge duplicate schema references, add CLI/exit-codes/env-vars/cost-model references and the first tutorial.
origin:
  - 2026-07-02 full project review (see `_output/review-20260702/project-review.md` and companions) — bugs, dead surface, and docs gaps enumerated there.
  - 2026-07-02 user decision — everything must run through interactive PTY; never `claude -p` nor the SDK. Condition: verify the eval repo first. Verified same day, and **re-audited 2026-07-03** after the eval migrated to `ape task` (PLAN-11 / v0.0.27): `/home/diegos/_dev/exoar/apex_process_framework_eval` now drives ape via two surfaces — `ape task <skill> --cwd <tmp> --output-format json` (`apex_eval/runner.py:789`, the primary skill path; consumes the stdout JSON envelope + `returncode == 0`) and `ape pipeline <name> --no-tui --cwd <tmp>` (`runner.py:1072`; consumes exit code + `manifest.yaml`), plus `ape version --output-format json` and `ape framework setup|update`. Repo-wide, **zero** uses of `--eval`/`-P`/`--programmatic`/`--interactive` and no runtime `ape bootstrap`; the eval branches only on 0-vs-non-zero exit codes (never on a specific code), and never parses ape's stream-json stdout (its own stream-json is a direct raw-`claude` path that bypasses ape). The migration made F2 *safer* — `ape task` never carried the removed flags — and F2 preserves `--no-tui` semantics, so the pipeline path is unchanged.
---

# PLAN-9: CLI/docs hygiene + PTY-only consolidation

## Goal

One release in which: the known CLI bugs are gone, the command surface behaves
consistently (output formats, exit codes, help), the docs index actually
indexes the docs, and `ape` no longer contains any code path that spawns
`claude -p`. After PLAN-9, `internal/repl` (PTY) is the only way ape executes
claude, which is the load-bearing precondition for PLAN-12/14/15 (command,
service, script) all being PTY-only by construction. (PLAN-11's `ape task`
already shipped interactive-only in v0.0.27 and is untouched by this plan.)

## Why now

- The `-P` removal gets *harder* every release the new commands build on the
  dual-axis mode resolver. Removing it before PLAN-12 lands shrinks that
  plan by an axis (PLAN-11 already shipped without one).
- The eval audit removed the only reason to hesitate.
- The bugs (B1–B3 below) are user-visible today.

## Non-goals

- No new commands (PLAN-11/12/14/15).
- No telemetry changes (PLAN-10) — except deleting the stream-json *stdout*
  parsing that dies with programmatic mode.
- No behavior change to interactive exec, the bridge, manifests, or commits.

## Scope — IN

### F1: Bug fixes

1. `internal/apecmd/costs.go:20-28` — drop `ape costs run` / `ape costs chat`
   from `Long` (PLAN-10 reintroduces `costs run` for real).
2. `internal/apecmd/update.go` — move `updateResult` to package scope; make
   `printUpdateResult` switch on that named type. Add a regression test that
   asserts the human output contains `current:`.
3. `internal/apecmd/root.go` — set `rootCmd.Version` (reuse `versionString()`);
   `ape --version` works.

**Acceptance.** `ape update` prints formatted human output; `ape --version`
prints the version; `ape costs --help` matches the registered subcommands.

### F2: Remove the programmatic exec axis (PTY-only)

Delete, in `internal/pipeline`: `buildArgv`, `runClaude`, `runStages`,
`parseResultEvent`'s stdout-scan path (keep the `resultEvent` struct — the
interactive adapter `stepTelemetryToResultEvent` feeds `recordStep` through
it). Delete in `internal/apecmd`: `-P/--programmatic`, `-I/--interactive`
(interactive is the only exec, the flag is vestigial), `--eval`, `runPlain`'s
exec branch (keep pipeline *listing*), `runWithTUI`'s programmatic variant,
`runWithWeb(..., false)`. `resolvePipelineMode` collapses to the UI axis only
(none | tui | web). `describeMode` simplifies accordingly.

Flag compatibility: `-P`, `-I`, `--eval` are **removed** (error with a
pointing message: "programmatic mode was removed in vX.Y.Z; interactive PTY
is the only exec mode — see docs/explanation/why-pty-only.md" — substitute
the actual release that ships F2; the repo is already past v0.0.27). `--no-tui`,
`--tui`, `--web`, `--open` keep their exact semantics.

**Tests.** Existing interactive tests keep passing; add a test that the
removed flags produce the pointer error; delete programmatic-path tests.

**Acceptance.** `grep -rn '"-p"' internal/ | grep -v repl` finds no claude
spawn with `-p`; the three claude spawn sites reduce to two
(`internal/repl/repl.go` PTY + `chat.go` inherited-stdio REPL). Eval harness
still green against the new binary — re-run **both** eval surfaces on a
fixture: `ape task <skill> --output-format json` (JSON envelope + exit 0
unchanged) and `ape pipeline <name> --no-tui` (manifest fields unchanged).

**Risk.** Anything *outside* the audited eval repo calling `-P`/`--eval`
breaks loudly with a clear message. Accepted; CHANGELOG entry documents it.

### F3: CLI consistency

1. `--output-format human|json|yaml` via `internal/output` added to:
   `pattern list`, `adr list`, `trait validate`, `sessions`,
   `sessions prune`; `costs` re-routed through `internal/output` (gains yaml);
   `costs roll`/`costs update` emit a result object.
2. Hide the stubs: `pattern sync`, `sync patterns`, `sync adrs` get
   `Hidden: true` (delete outright if nobody objects in review).
3. `bootstrap --no-tui` → `--no-picker` (hidden deprecated alias kept one
   release).
4. `internal/apecmd/exitcodes.go`: one table of named exit codes; commands
   reference it; per-command meanings documented in each `Long`. The table
   starts from the **shipped PLAN-11 convention** (`0` ok · `1` run
   failed/idle-timeout · `2` usage/preflight · `3` REPL never ready,
   `internal/apecmd/task.go:21`); PLAN-12 adds `4` (claude died before
   Stop) and PLAN-17 registers its reporting codes here — this table is the
   single reconciliation point.
5. Populate cobra `Example:` on every runnable command.
6. Help text: rewrite root `Long` (pipelines/task/chat first); purge plan
   vocabulary from `pipeline` flag help; print the resolved-mode line
   (`describeMode`) on every pipeline start; skip the background update check
   when the invoked command is hidden (`mcp-bridge`, `notify`).

**Acceptance.** Every data-emitting command answers `--output-format json`;
`ape --help` shows no "not yet implemented" commands; help contains no
"PLAN-" references.

### F4: Docs overhaul

1. `docs/README.md`: index every doc on disk (7 reference + 2 explanation + 3
   how-to currently orphaned).
2. `README.md`: add `chat`/`costs`/`sessions`/`planning` to the commands
   table; state the web+interactive default; link the invocation matrix
   (which itself shrinks to the UI axis after F2).
3. Merge `reference/pipeline-spec.md` into `reference/pipeline-yaml-schema.md`
   (field-for-field parity with `internal/pipeline/spec.go`); fix the dead
   `how-to/authoring-pipelines.md` link by writing that how-to.
4. Rewrite `reference/claude-spawn-modes.md` for the PTY-only world; add
   `explanation/why-pty-only.md` (hooks, REPL parity, cache behavior,
   contract verification — the standing rationale).
5. New references: `cli.md` (generated — wire `cobra/doc.GenMarkdownTree`
   behind a make target so it can't drift), `exit-codes.md` (from F3.4),
   `environment-variables.md` (ape's own env vars — the Claude Code side,
   `CLAUDECODE`/`CLAUDE_CODE_*` and ape's scrub, already landed as
   `reference/claude-code-env-vars.md`; cross-link, don't duplicate),
   `cost-model.md` (formula, multipliers, overrides, honest accuracy note
   pointing at the open discrepancy).
6. `tutorials/first-pipeline.md`: install → doctor → framework setup →
   `ape pipeline design` → read `_output/pipelines/<name>/<run-id>/` →
   `ape costs`.
7. `make ci-local` gains a docs link/reachability check (script under
   `scripts/`; every file in `docs/` reachable from `docs/README.md`, no dead
   relative links).
8. CHANGELOG: backfill the missing v0.0.21 note (rc-tag incident, already
   narrated in CLAUDE.md); update `explanation/bridge-architecture.md:142`
   with the confirmed 2026-07 pricing.

**Acceptance.** Link-check script green in `make ci-local`; a new reader can
reach every doc from `docs/README.md`.

## Sequencing

F1 → F2 → F3 → F4 as four PRs (F4 can start any time; its claude-spawn-modes
rewrite waits for F2). Everything lands before PLAN-12/14/15 start
(PLAN-11 shipped first, in v0.0.27 — the original "PLAN-9 first" sequencing
was reversed to unblock the eval; see PLAN-11's notes).

## Open questions

- Delete the stub commands instead of hiding? (Recommend delete; `sync`'s
  intent is preserved in the framework repo's roadmap.)
- Should `ape chat` also move from inherited-stdio to `internal/repl` for
  uniformity? Not required here; PLAN-12 (`ape command`) will share its
  scaffold either way.

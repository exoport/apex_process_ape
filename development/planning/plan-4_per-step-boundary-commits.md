---
plan_id: PLAN-4
created_at: 2026-05-11
implemented_at: 2026-05-11
status: done
tags:
  - pipeline
  - git
  - workflow
  - pipeline-spec-schema
  - breaking-default
summary: Per-step boundary commits during `ape pipeline <name>`, on by default. After each successful step ape runs `git commit` with a deterministic `ape:<pipeline>/<stage>/<skill>` message (or a per-step override the pipeline YAML can supply via a new `commit:` field). A `--no-commit` CLI flag opts the whole run out — pipeline-level kill switch with absolute precedence over any per-step YAML setting. The pipeline-spec gains an optional `commit:` field per step accepting bool-or-string (omit = default-derived message; `false` = skip this step's commit; `"string"` = use this message). The manifest schema bumps to v2 to record `commit_sha` + `commit_status` per step.
origin:
  - 2026-05-11 ape session — surfaced while reviewing PLAN-3's interaction with the framework's mode-aware Commit Policy change. PLAN-3 closes the observability gap (per-step metrics); PLAN-4 closes the workflow gap (per-step git history).
  - The framework Commit Policy change (drafted at `apex_process_framework_eval/_output/framework-prompt-anti-self-commit-clause.md`) makes leaf skills strictly non-committing in `--autonomous` mode. Pipelines invoke skills autonomously, so the dirty-tree-at-completion shape becomes the default once that change ships — PLAN-4 makes ape itself the rightful committer of that output, by default, with clean atomic per-step history.
  - Eval harness already does this manually for direct-skill stages via `commit_skill_state()` (apex_process_framework_eval/apex_eval/harness.py); PLAN-4 brings the same shape into ape and lets the eval simplify in turn.
---

# PLAN-4: Per-step boundary commits (on by default)

## Goal

Make every successful pipeline step its own git commit by default. Each commit's message is deterministic (`ape:<pipeline>/<stage>/<skill>`) unless the pipeline YAML overrides it per step. The user opts out with `--no-commit`. The manifest's per-step record carries the resulting commit SHA. Without `--no-commit`, `ape pipeline design` against a fresh sandbox produces 13 commits — one per skill step — readable as a clean linear history via `git log --oneline`.

End state: ape owns commits during pipeline runs, framework skills don't commit themselves (Commit Policy land), and the eval harness can drop its `commit_skill_state()` boundary commits for ape-mode stages.

## Why now

Three converging signals:

1. **The framework's Commit Policy** (drafted, awaiting review in the framework repo) makes leaf skills strictly non-committing in `--autonomous` mode. After it ships, every `ape pipeline` invocation lands in a "no commits during the run" world by default. The dirty-tree-at-completion shape becomes the expected outcome — fine for short pipelines, awkward for the long ones. **Ape needs to be the rightful committer** to fill the vacuum the framework change opens.
2. **The eval harness already solves this** with `commit_skill_state()` after each stage. It works; the pattern is proven. Bringing the same shape into ape lets real-project users get the same experience without the eval as a bridge, and lets the eval drop its boundary-commit logic in favor of ape's manifest-recorded SHAs.
3. **PLAN-3 added per-step manifest records.** The infrastructure for "knowing what step we're in, when it ended, what it produced" already exists. Threading a commit operation through the same hook is cheap.

Default-on rather than default-off is intentional: the most common shape (a user running `ape pipeline design` against a clean tree) gets the cleanest history without ceremony. Users with non-standard workflows opt out with one flag.

## Scope — IN

### C1: Pipeline-spec schema extension — `commit:` field per step

The pipeline YAML schema gains an optional `commit` field on every step. It accepts three shapes:

```yaml
stages:
  prd:
    chain:
      # Shape 1: omit `commit:` entirely → default behavior.
      # Step commits with message `ape:design/prd/apex-create-prd`.
      - skill: apex-create-prd
        agent: apex-agent-pm

      # Shape 2: string → commit with this message.
      - skill: apex-shard-doc
        args: "--doc prd"
        commit: "docs: shard PRD"

      # Shape 3: boolean false → skip the commit for this step.
      - skill: apex-validate-prd
        commit: false

      # Shape 4: boolean true → synonym for omitting the field
      # (commit with default-derived message). Allowed for explicitness.
      - skill: apex-implementation-readiness
        commit: true
```

YAML parsing follows the existing `Step` struct in `internal/pipeline/spec.go`:

- Decode into a `commit yaml.Node` field so we can inspect the node kind.
- `null` / absent → `defaultCommit{}` sentinel (commit with derived message).
- Scalar bool `true` → same as default; bool `false` → `skipCommit{}`.
- Scalar string → `explicitCommit{message: <string>}`.
- Any other shape → spec-load error with line number, matching the existing pattern in `decodeStages`.

The decoded value is exposed on `Step` as `CommitDirective interface{}` with three concrete types (sealed via an unexported method). Runner branches on the type.

### C2: CLI surface — `--no-commit` kill switch

- New `ape pipeline <name> --no-commit` boolean flag. Default: false (commits on).
- When set: **ape never invokes `git commit`** during the run. Every per-step `commit:` value in the YAML is ignored. The manifest records `commit_status: "skipped-by-flag"` on every step.
- Flag composes cleanly with `--commit-allow-dirty` (see C4) — `--no-commit` makes the dirty-tree gate moot, so `--commit-allow-dirty` becomes a no-op when paired with `--no-commit`.
- Help text: "Do not commit anything during the run; leave the working tree dirty. Overrides any `commit:` field in the pipeline YAML."

### C3: Commit-message derivation

- Default message format: `ape:<pipeline>/<stage>/<skill>`.
  - Example: `ape:design/prd/apex-create-prd`.
  - Stage and skill names sanitized identically to the manifest's directory layout (the existing `sanitizeFsName` in `internal/pipeline/manifest_writer.go`).
- Explicit message (from YAML `commit: "string"`): used verbatim. No `ape:` prefix added — the YAML author has full control. We do not validate the message format; if they want Conventional Commits (`docs:`, `feat:`), they write that.
- Single-line message only. Multi-line YAML scalars are rejected at spec-load with a clear error.
- Author/committer: whatever git's local config provides; ape sets nothing.

### C4: Lifecycle integration

After each step's `runClaude` returns successfully (and after the manifest's `RecordStep` updates):

1. If `--no-commit` set → record `commit_status: "skipped-by-flag"`, skip.
2. If step's `CommitDirective` is `skipCommit{}` → record `commit_status: "skipped-by-spec"`, skip.
3. Otherwise: run `git -C <projectRoot> status --porcelain` to check for changes.
   - Empty output → record `commit_status: "no-op"`, no commit.
   - Otherwise: `git -C <projectRoot> add -A` then `git commit -m "<message>"` where `<message>` is the explicit string or the derived default. Record `commit_status: "committed"` + the resulting SHA.
4. On any git error: log to observer, set `commit_status: "failed"`, **fail the pipeline** with a wrapping error. We do not silently skip git failures — they likely mean the working tree is in an unexpected state.
5. On step failure: do not commit. The step's manifest record carries `status: failed`; the working tree retains whatever the failed step produced for the user to inspect. Record `commit_status: "skipped-step-failed"`.
6. On context cancellation mid-step: do not commit. `commit_status: "skipped-cancelled"`.

### C5: Pre-run dirty-tree gate

- With commits enabled (i.e., `--no-commit` not set AND at least one step would commit): before the first step runs, fail-fast if `git status --porcelain` returns any non-ignored output in the project root.
- Rationale: per-step commits assume each step's diff is exactly what that step produced. Prior dirty state would conflate the user's WIP with skill output in the first step's commit.
- Error message guides the user:
  > Working tree has uncommitted changes. Commit or stash them before running `ape pipeline` (which commits per step by default).
  > Bypass options:
  > • `--no-commit` to leave the entire run uncommitted (today's pre-PLAN-4 behavior)
  > • `--commit-allow-dirty` to commit anyway (your prior WIP merges into the first step's commit — usually not what you want)
- Bypass: `--commit-allow-dirty` flag, off by default. When set, the gate is suppressed and the first committing step's diff includes whatever was dirty at start.

### C6: Manifest schema additions (v1 → v2)

- Bump `schema_version: 1` → `schema_version: 2`.
- Add to `StepRecord`:
  - `commit_sha string` — the resulting commit SHA (empty unless `commit_status == "committed"`).
  - `commit_message string` — the message that was used (empty for skipped / failed).
  - `commit_status string` enum:
    - `"committed"` — commit succeeded
    - `"no-op"` — `--no-commit` not set, spec didn't skip, but no diff
    - `"skipped-by-flag"` — `--no-commit` was set
    - `"skipped-by-spec"` — pipeline YAML said `commit: false`
    - `"skipped-step-failed"` — the step itself failed before commit
    - `"skipped-cancelled"` — context cancellation
    - `"failed"` — git commit returned non-zero
- Add to `Manifest.totals`:
  - `commits_made int` — count of `committed` steps in the run.
- The consumer-facing schema doc (`docs/reference/pipeline-run-manifest.md`) gets a "Commits in the manifest" subsection explaining the new fields and their mapping to `--no-commit` / per-step config.

### C7: Report renderer

- The rendered `pipeline-report.md` per-step table gains a `Commit` column when any step recorded a non-`skipped-by-flag` status. Column shows:
  - committed step → short SHA (`abc1234`)
  - `no-op` → `—`
  - `skipped-by-spec` → `(spec: skip)`
  - `skipped-by-flag` → blank (the whole run was no-commit; column not rendered)
  - `failed` → `✗`
- Header section gains a `commits made: N` line.

### C8: CLI surface — end-of-run summary

The end-of-run summary line that PLAN-3 introduced (`📊 report: ...`) gets a companion when commits were made:

```
📊 report: _output/pipelines/design/<run_id>/pipeline-report.md
📌 commits: 13 (run `git log --oneline --grep '^ape:design/'` to inspect)
```

When `--no-commit` is set, the `📌 commits:` line is omitted.

### C9: Tests

- Unit: spec-load — round-trip a YAML with all four `commit:` shapes (omitted, true, false, string); assert each decodes to the right `CommitDirective` variant. Reject multi-line string + reject unexpected mapping shape with line-number errors.
- Unit: message derivation — `sanitizeFsName` already covers the formatting edge cases; add one explicit test that builds the full `ape:<p>/<s>/<sk>` message for a step with hyphens, dots, and underscores.
- Integration: extend the shim-driven `TestRun_EmitsManifest` to a variant with a 2-step spec covering all three lifecycle outcomes: one step commits (writes a file, default message), one step is `commit: false` (writes a file, recorded `skipped-by-spec`, no SHA), one step is no-op (writes nothing, `no-op`).
- Integration: dirty-tree gate — start with an uncommitted file in the project root, expect the runner to refuse with the documented error. Then rerun with `--no-commit` and confirm it passes.
- Integration: failed-step path — shim exits non-zero; assert no commit was made and `commit_status: "skipped-step-failed"`.
- Integration: `--no-commit` kill switch — same spec as the first integration test plus `--no-commit`; assert every step has `commit_status: "skipped-by-flag"` and no SHAs.
- Integration: pre-commit hook failure — install a failing hook in the test repo; assert the pipeline fails on the first commit and `commit_status: "failed"`.
- Cancel-path: extend existing cancellation tests; assert no commit at the cancel boundary.

### C10: Docs

- Update `docs/reference/pipeline-run-manifest.md` "Commits during a run" section to describe the new default-on commit behavior, the `--no-commit` opt-out, and the manifest's commit fields.
- Update `docs/reference/pipeline-spec.md`:
  - Add `commit` to the Step object fields table.
  - Add a "Commits" subsection with the four-shape syntax + worked example.
- New `docs/how-to/inspecting-pipeline-commits.md` — short guide: `git log --grep`, `git show <sha>`, how to revert a single step, how to squash a stage's worth of commits if desired.
- CHANGELOG entry for whatever release ships PLAN-4 — frame as a default-behavior change ("ape now commits per step by default; pass `--no-commit` for the prior shape").

### C11: Framework pipeline updates (separate PR, framework repo)

After PLAN-4 ships, the canonical `design` / `governance` / `epics` pipelines distributed by `ape framework update` should adopt human-readable commit messages via the new `commit:` field. Concrete proposals:

```yaml
# design.yaml
stages:
  prd:
    chain:
      - skill: apex-create-prd
        agent: apex-agent-pm
        commit: "docs: add product requirements document"
      - skill: apex-shard-doc
        args: "--doc prd"
        commit: "docs: shard PRD into focused sections"
  ux:
    chain:
      - skill: apex-create-ux-design
        agent: apex-agent-ux-designer
        commit: "docs: add UX design"
      ...
```

This is a framework-side change that PLAN-4 makes possible but does not require. Old pipelines without `commit:` fields keep working (default-derived messages).

## Scope — OUT

- **Smarter `git add` scoping** (e.g., add only the files this step's stream-json shows it touched). A real feature, but reads the NDJSON event stream for tool-call file paths and infers what to stage. Defer — start simple with `git add -A`.
- **Stage-level squash option** (one commit per stage instead of per step). Plausible follow-up; not in v1. The YAML's per-step `commit: false` lets a pipeline author batch a stage's work by skipping intermediate commits and committing only the last step with a stage-level message.
- **Custom message-template variables** (`commit: "feat({stage}): {skill}"`). YAGNI; pipeline authors who want structured messages can write them out.
- **Auto-tagging at end of run** (e.g., `ape-design-<run_id>` annotated tag). Out of scope.
- **Reverting a previous run** (`ape pipeline revert <run_id>`). Possible follow-up if PLAN-4 sees real use.
- **Skipping pre-commit hooks** (`--no-verify`). Hooks run as configured; if they fail, the pipeline fails. Users who want to bypass hooks for ape commits can configure that themselves.

## Open questions

1. **Should commit_status report multi-line failure detail?** Today the proposal is a single enum value plus the per-step `EventsPath` for forensics. If a commit fails (C4.4), the user might want the stderr in the manifest for triage. Lean: capture `commit_error string` (empty on success) as a v2 schema field. Worth adding.
2. **What about `.gitignore`?** `git status --porcelain` honors `.gitignore`, so `_output/pipelines/<run_id>/` (where ape writes the manifest) won't trip the dirty-tree gate **as long as** the user has `_output/` in their `.gitignore`. The pre-run gate's error message should mention this so first-time users don't get confused.
3. **Should `git commit` go through `git -c commit.gpgsign=false`?** If the user's global config signs commits, ape's per-step commits will too — slow and noisy across 13 steps. Default proposal: don't override. Users who want fast unsigned ape commits can set `commit.gpgsign=false` for the repo locally. Mention in the how-to.
4. **What about empty pipelines (zero steps)?** Spec-load already rejects empty chains; nothing to do.

## Verification plan

1. `make lint` zero issues. `go test ./...` clean, including the new spec-load and integration tests.
2. Smoke `ape pipeline design` against a fresh sandbox after both the framework Commit Policy and PLAN-4 have shipped. Confirm:
   - 13 commits land
   - Messages match the framework pipeline's `commit:` field values (or the derived default if the framework hasn't adopted yet)
   - Every diff matches the corresponding skill's output
   - `pipeline-report.md` renders the Commit column correctly
   - `git log --grep '^ape:design/'` retrieves the run's commits
3. Smoke the dirty-tree gate: start a sandbox with an uncommitted file, expect a clear refusal; rerun with `--no-commit`, expect it to pass; rerun with `--commit-allow-dirty`, expect the first step's commit to swallow the WIP.
4. Smoke `ape pipeline design --no-commit`: confirm zero commits land, working tree mirrors the pre-PLAN-4 shape, manifest's per-step `commit_status` is `skipped-by-flag` everywhere.
5. Smoke spec-level skip: hand-edit a pipeline to put `commit: false` on one step, run, confirm that step is `skipped-by-spec` and adjacent steps still commit.
6. Confirm the manifest's `schema_version: 2` is recognized by the eval consumer (apex_process_framework_eval PLAN-9). PLAN-9 was drafted against `schema_version: 1`; coordinate with the eval to bump its reader before or alongside the PLAN-4 release.

## Release / coordination

- **Order matters**: framework Commit Policy ships → ape PLAN-4 ships → eval PLAN-9 reader bumped to schema_version: 2 → eval drops its `commit_skill_state()` for ape-mode stages.
- The framework's Commit Policy change should ship **before** this. Until it does, leaf skills can still auto-commit, and ape's per-step commits would race with the model's improvised ones. (Concretely: a step's model emits its own commit, then ape tries to commit and finds an empty diff, records `no-op`. Functional but messy.)
- Released as **ape v0.0.10** (2026-05-11). Default-behavior change is documented in the CHANGELOG with the `--no-commit` opt-out for users who prefer the pre-PLAN-4 shape. Patch-version bump rather than minor; the manifest's `schema_version: 1 → 2` change is forward-compatible (new fields are `omitempty`).
- Eval-side coordination: PLAN-9's reader should accept both schema_version 1 and 2 (1 is what ape v0.0.9 writes; 2 is what v0.0.10+ writes). Lean on the existing forward-compat clause in the manifest schema doc.

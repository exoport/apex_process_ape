# Framework asks from PLAN-63 and PLAN-64 (2026-09-03)

Filed by the framework build repo (`/home/diegos/_dev/github/diegosz/apex_process_framework`, branch `intent-releaser`) for one `ape` release carrying the whole bundle. Two floors block framework half-phases; one convenience ask rides with them; five further requests are not dependencies. The source of truth for every item is the plan section it cites; this document is the queue entry, not a restatement.

- PLAN-63 `development/planning/plan-63_release-record-and-proportionality-ladder.md` § "`ape` requests and version floors" (lines 3607-3667).
- PLAN-64 `development/planning/plan-64_intent-gates-and-the-record.md` § "`ape` requests and version floors" (lines 2915-2972), 5b (669-731), 6b (862-966), 9.12b (1825-1836), 13.3b (2592-2603).

Every claim about `ape` in those sections was verified against `v0.0.66` (this repo's `94d853d` HEAD carries no later tag).

## Floor 1 - the two dispatch assertions and the timestamp writer (PLAN-64 C3, 5b)

Source: PLAN-64 § `ape` requests item 1 and step 5.6. Dispatch path: `internal/apecmd/task.go` (flags 153-172) and `sessiondriver`.

- For a skill **absent** from the project's `_apex/commit-owners.csv`: assert HEAD unchanged **and** the index empty **and** the stash reflog unchanged across the dispatch. (`git add` and `git stash` both leave HEAD alone; the framework's operating rules forbid them because a stash silently destroys the caller's working tree.)
- For a skill **present** in the CSV: assert HEAD advanced by exactly the declared message format (a regex per row). This is the assertion that catches a suppressed commit.
- The timestamp writer: `ape` writes `timestamp` (local wall-clock `YYYYMMDDHHMMSS`) for the dispatched skill, with monotonicity ("a field answering when was this last written never moves backwards") as a unit test in the binary and an `ape sprint verify` readback.
- The CSV's shape is the framework's (PLAN-64 5a authors it: skill, message-format regex; the batch skills, `apex-sprint-planning`, `apex-epic-retrospective`, and the conducting session as rows). Read it from `_apex/commit-owners.csv`; treat an absent file as "no skill commits".

## Floor 2 - `ape story verify --file` takes the story shape (PLAN-64 C4, 6b; 9.12b and 13.3b ride it)

Source: PLAN-64 § `ape` requests item 2 and step 6.4. Anchor: `internal/story/verify.go:19-24`, six check classes today, no unknown-key class.

- A **derived section-set** check over the story **body**: the expected set is a function of (resolved config × story type), using the four `ext_*` booleans `ape config resolve` already resolves (`## Feature Scope` is a member on `ext_features` alone). No `sections:` frontmatter declaration exists or will exist (PLAN-64 O-5): there is nothing to compare a declaration against.
- **The story-type input, decided for this ask:** no writer stamps a story type into frontmatter today (the template's block carries `story_id`, `epic`, `status`, `review_count`, `output_document` and nothing else), so **the derived set is a function of resolved config alone until a writer stamps a key**. Design the check so a later `story_type:` key can narrow the set without changing the exit contract.
- Plus: File List marker vocabulary (`(created)` / `(modified)` / `(deleted)`, one marker in parens after the path, the em-dash-prose form forbidden), placeholder residue at `status: review` (`_(populated during dev)_` surviving in `### Agent Model Used` / `### File List` / `### Completion Notes List`; `### Debug Log References` with `_No issues encountered._` is a terminal convention, not residue), compliance-table header shape, and GCC line form.
- Exit-code contract preserved: corpus mode reports, `--file` mode gates. Never `--strict` from inside a skill (framework rule; unchanged).
- **9.12b, same floor:** recompute `adrs_considered` from the tag match and bind it against a story's declared `adrs_applicable`; a story declaring `adrs_applicable: 0` against a non-zero tag match exits non-zero in `--file` mode, and the message names `adrs_applicable` and the recomputed `adrs_considered` - never an "applicability mismatch".
- **13.3b, same floor:** an ADR id cited in `governance.adrs` that does not resolve at HEAD exits non-zero in `--file` mode; an id that resolves to an ADR whose status is not `accepted` (e.g. `proposed`) exits 0 and is flagged in the report.
- None of this goes to `ape registry verify`, which takes no story input.

## Convenience ask - two overlay keys (PLAN-64 7.1, PLAN-63 § `ape` requests item 1; O-2)

`internal/apexcfg/apexcfg.go` `OverlayKeys()` (281-301, list 283-299) iterates its own 17 keys and silently skips the rest, so `ape config resolve` cannot emit a key the framework adds. Add two, once: `model_profile` (default `strong`; a ceiling - it can force `full`, never force `lean`) and `evidence_folder` (default per the framework's `config.yaml`). Until they land each key is framework-read with a documented default; with them, `config.local.yaml` can override either. (Also corrects the merged proposal's §7 finding 3: the `unknown overlay key` error is reachable only from a direct `decodeInto` call.)

## Not requested

`--scaffold` on `ape task` (PLAN-64 O-3): `--args` forwards skill flags verbatim (`task.go:156`) and both paths append `--autonomous` (`internal/pipeline/interactive.go:26-27`), so `ape task <skill> --args "--scaffold lean"` is the operator's opt-in already.

## Five requests that are not dependencies (PLAN-63 § `ape` requests items 2-5)

1. `ape context check`, mirroring `ape memory check` against `project-context.md` (`internal/memory/memory.go:45-53` holds the two budget constants and the `state` field skills branch on).
2. `ape release status`, projecting the release record - for after PLAN-63 ships.
3. M14's migration-list runner in `ape framework update` (`--plan`, `ape doctor`'s `migrations.pending`, applied ids in `_apex/framework.yaml`); the file shape is fixed in PLAN-63 § "Maintainer review, 2026-09-02" (lines 3901-3949): `_apex/migrations/v<version>_seq-<seq>_<slug>.md`, `_seq-` as the parsing anchor, semantic ordering on frontmatter `version`/`seq`/`after:`, `kind: derivable|judged`, `check:`, `command:|skill:`. A framework follow-up plan owns the other side.
4. A `story.requirement_ids_missing` class in `ape story verify`, report-only, no `--fix`.
5. Two observations carried from PLAN-62: `ape deferred discard` refuses a closed record with "reopen it first", naming an operation `ape` does not provide; and it stamps `resolved_at` on a record that was never resolved, where `discarded_at` would say what happened.

## What the framework greps once the tag is cut

The framework's blocked acceptance blocks are runnable the day the release is cut, with `<X.Y.Z>` filled in from the release notes. Please name in the notes: the version, the new `story verify` check class names, and the message shapes below.

```text
ape version --output-format json | grep -Eq '"version": *"v?<X\.Y\.Z>'
ape story verify --file <eval>/fixtures/gf-hello-world/overlays/story-missing-derived-section/development/implementation/1.1.story.md
  → non-zero; the message names `### Debug Log References` as the missing derived section
  (the overlay is story 1.1 with that one heading removed, everything else byte-identical)
ape story verify --file <story declaring adrs_applicable 0 against a non-zero tag match>
  → non-zero; the message names `adrs_applicable` and the recomputed `adrs_considered`
ape story verify --file <story citing a governance.adrs id absent at HEAD>      → non-zero
ape story verify --file <story citing a proposed ADR>                            → exit 0, flagged
uv run apex-eval run --skill apex-dev-story --fixture gf-hello-world
  → the non-committer assertion holds: HEAD unchanged, index empty, stash reflog unchanged
```

The framework records the floor at `framework/_apex/apex-operating-rules.md:17`, never as a `version:` key in `_apex/ape-commands.yaml` (its header forbids one: a locally built `ape` reports an unstamped pseudo-version). The framework-side halves that wait on this release: PLAN-64 5b (the 85 `## Commit Policy` deletions), 6b, 9.12b, 13.3b.

## Handshake

Cross-session peer on the framework side: `apex-process-framework-b6` (session names change on restart; `ListAgents` before sending). Send the version and the check-class names when the tag is cut.

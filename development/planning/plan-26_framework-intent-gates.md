---
plan_id: PLAN-26
created_at: 2026-09-03
status: in-progress
tags:
  - cli
  - config
  - stories
  - governance
  - dispatch
  - timestamps
  - framework
summary: >
  The `ape` half of the APEX framework's PLAN-63 and PLAN-64. Three deliverables ship as one
  release, because two framework half-phases are gated on the release existing and a split
  would put prose in front of the assertion that enforces it. (1) The dispatch path gains two
  per-dispatch git assertions read from the framework's `_apex/commit-owners.csv`, and `ape`
  takes ownership of timestamp monotonicity so ~53 skills can delete the clamp paragraph they
  each carry verbatim. (2) `ape story verify --file` grows from a frontmatter gate into the
  story-shape gate the framework's structural prose currently substitutes for — a derived
  section set, File List markers, placeholder residue, compliance-table headers, GCC line form,
  plus the `adrs_considered` recomputation and the ADR-resolves-at-HEAD gate. (3) Two keys join
  `apexcfg`'s overlay allow-list. Nothing here invents a default the framework already owns.
origin:
  - `development/pending/framework-plan-63-64-asks-20260903.md` — the intake filed by the
    framework build repo on 2026-09-03, pointing at PLAN-63 § "`ape` requests and version
    floors" (3607-3667) and PLAN-64 § same (2915-2972), 5b (669-731), 6b (862-966),
    9.12b (1825-1836), 13.3b (2592-2603).
  - Four design questions answered by the framework session on 2026-09-03, recorded inline
    below as D1-D4. Each answer is quoted from the plan text, not from memory.
---

# PLAN-26 — the framework's intent gates, on the `ape` side

## Why one release

PLAN-64 5b, 6b, 9.12b and 13.3b are each **blocked** until this release exists, and the
framework's sequencing rule is "never split so prose goes before the assertion". 5.7 deletes
85 `## Commit Policy` sections whose replacement is the runner assertion D2 describes; 5.8
deletes the timestamp-clamp paragraph from ~53 skills and rewrites the meta-rule that would
re-author it; 6.5, 6.5b, 6.6 and 6.7 reduce structural prose to "verified by
`ape story verify --file`". Each of those deletions is only safe once the binary asserts what
the prose asserted. So: one tag, one floor, one message back to the framework session.

The floor is recorded on the framework side at `framework/_apex/apex-operating-rules.md:17`,
never as a `version:` key in `_apex/ape-commands.yaml` — its header forbids one, because a
locally built `ape` reports an unstamped pseudo-version.

## D1 — the timestamp writer

**`ape` owns the monotonic clock.** Today `apexcfg.ResolveAt` sets `Timestamp` from a bare
`time.Now()`, so two agents with skewed clocks can each emit a stamp earlier than the last
write. That is exactly what `VAR-09-TSBASIS`'s clamp paragraph exists to catch on the skill
side, and it is the paragraph 5.8 deletes.

The issuer, in one place:

- Basis is the system's **local wall-clock** — its own configured timezone, never UTC unless
  the system itself is UTC. Format `YYYYMMDDHHMMSS` (`apexcfg.TimestampLayout`, unchanged).
- Every issue returns `max(now, last_issued)`. A field that answers "when was this last
  written" never moves backwards.
- `last_issued` persists under `{output_folder}/ape/`, resolved through `runlog`'s layout and
  never by joining `_output` by hand.
- **When that file is absent** — a fresh clone, a cleaned `_output` — `last_issued` seeds from
  the newest stamp already present in `sprint-status.yaml`'s `updated_at`, so the first resolve
  after a clone cannot regress. Without the seed, a clean checkout would silently reset the
  monotonic floor and `sprint verify`'s exit-5 readback would be the only thing left holding
  the guarantee.

Three consumers: the `timestamp` `ape config resolve` emits (which after PLAN-64 Phase 3 is the
preamble every skill reads, and which skills copy into `updated_at`, `generated_at`,
`frozen_at` and the review reports' own `timestamp`), `ape sprint reconcile`'s `updated_at`
write, and the dispatch record in `ape task`'s manifest.

Monotonicity is a unit test over the issuer. `ape sprint verify`'s existing exit-5
backwards-write check is the readback, and stays the detector of last resort.

## D2 — the two dispatch assertions

Read `_apex/commit-owners.csv`. Header, verbatim from PLAN-64 5.2:

```csv
skill,commit_kind,message_regex
```

- Every regex carries its own `^…$` **in the file**. Compile each verbatim; no `MULTILINE`,
  no anchors added by the reader. Match against the **commit subject line only**.
- **Two rows per skill is the schema's own shape**, not a duplicate:
  `apex-story-batch-dev,dev,^dev: story \d+\.\d+ [a-z0-9 ]+$` beside
  `apex-story-batch-dev,review,^review: story \d+\.\d+ [a-z0-9 ]+$`; `apex-epic-retrospective`
  carries `retro` and `retro-waive`.
- **An absent file means "no skill commits"** — every dispatch takes the non-committer
  assertion. Not an error.

The assertions, per dispatch:

| Skill in the CSV | Assertion |
| ---------------- | --------- |
| **absent** | HEAD unchanged **and** the index empty **and** the stash reflog unchanged across the dispatch. `git add` and `git stash` both leave HEAD alone, and the operating rules forbid them precisely because a stash silently destroys the caller's working tree — so HEAD alone is not the assertion. |
| **present** | Every commit in `pre_head..HEAD` matches one of *that skill's* rows, **and there is at least one**. A batch dispatch makes N commits (one `dev` and one `review` per story), so this is a per-commit predicate over the range, never "HEAD advanced by one". |

Two tolerances the assertion must carry:

- **Rows for skills `ape` never dispatches are tolerated, never failed on.** The conducting
  session's rows (`release:`, `evidence:`, the project's declared repair type,
  `chore(<area>):`) carry `apex-orchestrator` in the skill column. It is a persona that is
  adopted, never an `ape task` target, so the per-dispatch assertion simply never fires for it.
- **`ape task --task-commit` is `ape`'s own commit** and keeps its own derived format
  (`ape:task/<skill>`). It is not a skill commit and is not judged against the CSV.

## D3 — the overlay key

`evidence_folder` joins `Config`, `OverlayKeys()` and `decodeInto`, so `config.local.yaml` can
override it.

**Emit the raw key only. Fabricate no default, derive no path.** PLAN-64 7.1's wording is "the
key ships with a documented default that every consumer applies when the resolver omits it" —
the framework owns the `evidence_folder` fallback chain (the `config.yaml` key when present,
else an existing `evidence/` under `{governance_folder}`, else `evidence/`). Reimplementing it
here would put a second source of truth behind a key whose whole point is that the framework
resolves it. When it is set it passes through as written; no `Paths.Evidence` is derived.

**`model_profile` was asked for, implemented, and then withdrawn** (maintainer decision,
2026-09-05) before release. The framework removed the lean scaffold profile the key was a
ceiling over — the recapture measured lean against full and it did not earn its complexity, with
`compactions_observed: 0` on every run killing the context-headroom argument it rested on. Peak
occupancy across 57 measured steps was 4.4%–37.1% of a 1M window, and the 37.1% worst case was a
**full** step, so the pressure was absent even at maximum. The key had nothing left to bound. Removed from the resolver, the overlay list and its tests.

The exhaustiveness test over `OverlayKeys()` ↔ `decodeInto` already exists and must keep
passing — it is what stops a key being added to one list and forgotten in the other.

## D4 — fixtures

The eval-repo overlay (`fixtures/gf-hello-world/overlays/story-missing-derived-section/`) and
the three story fixtures for `adrs_applicable`, the absent ADR id and the proposed ADR are
**authored on the framework/eval side** as part of 6b, 9.12b and 13.3b, once this tag exists.
This repo writes its own `testdata/` fixtures and does not touch the eval repo.

## The story-shape classes

`ape story verify --file` gains the classes below. **`--file` mode only**: the corpus scan
reads at most 8 KiB per file and never opens a body (`story.FrontmatterCap`), which is the
difference between a 67 KB scan and a 23.5 MB one. Every class here is a body check, so
wiring them into the corpus walk would trade that guarantee away for a report nobody gates on.
Corpus mode still reports the frontmatter classes exactly as it does today.

The exit contract is preserved: corpus mode reports, `--file` mode gates, `0` / `2` / `3`.
Never `--strict` from inside a skill — the framework rule is unchanged.

1. **Derived section set.** The expected set is a function of **resolved config alone**, per
   O-5: no writer stamps a story type into frontmatter today (the template's block carries
   `story_id`, `epic`, `status`, `review_count`, `output_document` and nothing else), so there
   is no type input to read. The check is designed so a later `story_type:` key can *narrow*
   the set without changing the exit contract. `## Feature Scope` is a member on `ext_features`
   **alone** and is always expected when that flag is set — the `_No features apply to this
   story._` sentinel is "no matches", never a missing section. That resolves a standing
   disagreement between the template (always emit) and
   `governance-integration.md:259` (omit when zero matched) in the template's favour, which is
   what PLAN-64 6.3b decides.
2. **File List markers.** One marker in parens immediately after a backticked path.
   **The vocabulary is all five the template's table declares** — `(created)`, `(modified)`,
   `(deleted)`, `(planned)`, `(deferred)` — not the three the intake lists. `(planned)` and
   `(deferred)` are `apex-lift-project`'s, and rejecting them would fail every lifted story.
   The em-dash-prose form (`— created: …`) is forbidden **as a marker substitute**; em-dash
   prose after a valid marker is a legal annotation.
3. **Placeholder residue at `status: review`.** `_(populated during dev)_` surviving in
   `### Agent Model Used`, `### File List` or `### Completion Notes List`.
   `### Debug Log References` carrying `_No issues encountered._` is a **terminal convention,
   not residue** (`apex-dev-story/SKILL.md:109-114`) and is never a finding. Scoped to
   `status: review` — at `ready-for-dev` those placeholders are what the template is required
   to write, so asserting non-placeholder at mint would invert the template's own contract.
4. **Compliance-table header shape.** Verbatim: `| ADR | Why it applies | Key constraints |`
   and `| Pattern | Why it applies | Key constraints |`.
5. **GCC line form.** `- [ ] **[ID]** {instruction} -- {scope}`: `--` as the separator, never
   an em-dash; `ADR-NNNN` / `PAT-NNNN` / `PATLOC-NNNN` ids and never `PATCAN`. Review's
   `- [x]` and `- [x] N/A: <reason>` markings are legal forms of the same line.

## The two governance gates

Both ride the same verifier and the same floor. Neither goes to `ape registry verify`, which is
declared as one skill's work list over registry-index-versus-record drift and **takes no story
input**.

- **9.12b — `adrs_considered` recomputation.** Recompute the tag-match candidate count from the
  story body and bind it against the story's declared `governance_pass.adrs_applicable`. Two
  findings, both **bounds, never equalities**: `adrs_applicable > adrs_considered`, and
  `adrs_applicable == 0` against a non-zero candidate set — the self-certifying skip the check
  exists to catch. A recomputed-*applicability* mismatch is **not** a finding: step 2 of the
  digest algorithm is declared to be the agent's own inline judgement, so asking a
  deterministic CLI to reproduce it would fire on every legitimate keep-or-drop. The message
  names `adrs_applicable` and the recomputed `adrs_considered`, never an "applicability
  mismatch".
- **13.3b — ADR ids resolve at HEAD.** An id cited in `governance.adrs` that does not resolve
  to an ADR at HEAD exits non-zero. An id that resolves to an ADR whose status is not
  `accepted` (e.g. `proposed`) exits **0 and is flagged in the report** — a different outcome
  from a different fact, and the distinction is the point.

**Decided here, and reported in the release notes so the framework can correct it:** the
tag-match candidate set is the tag match over ADRs that are *eligible to produce a GCC line* —
`status: accepted` and `type != pattern`, the same filter
`governance-integration.md` applies at emission. An ADR that can never be applicable should not
be "considered", and counting one would make `adrs_applicable == 0` fire against a candidate
the agent was right to drop. Matching is case-insensitive on word boundaries against the story
body, which is deliberately inclusive per the algorithm's step 1 ("extra entries cost little,
missed entries are expensive").

**Consequence for `--file` mode:** these two classes need the ADR corpus, which the mode does
not read today ("a single file cannot see the corpus, exactly as the Python could not"). The
mode gains an *optional* project resolution walked up from the story's own path. When no
project resolves — a story outside any project, which is a property the mode is required to
keep — the two governance classes **skip**, and every other class still gates. A skipped class
is reported as skipped, never as a pass.

## The five non-dependency requests — moved back in

The intake's five non-dependency requests were originally deferred here: none blocks a framework
phase, and bundling them would put a much larger surface behind a tag two framework half-phases
are waiting on. **The maintainer moved all five back into this release on 2026-09-05**, before
the tag existed, so the deferral no longer describes what ships. Recorded rather than silently
rewritten, because the reasoning for deferring them was right at the time and the reason it was
overridden is that the tag had not been cut yet.

| Request | Status |
| ------- | ------ |
| `story.requirement_ids_missing` | in — advisory, opt-in behind `--include-advisory`, no `--fix` |
| the two `ape deferred discard` observations | in — the refusal stops naming a command `ape` does not provide, and a discard stamps `discarded_at` |
| `ape context check` | in — see below |
| `ape release status` | in |
| the M14 migration-list runner | in |

### `ape context check`

**One caller of the two constants, not two restatements of the numbers.** That is the request as
filed, and it is the whole design: `internal/memory`'s `DefaultSoftBudget` (40960) and
`DefaultHardCeiling` (204800) already band `team-memory.md`, and `project-context.md` is bounded
by the same 256 KiB Read cap for the same reason — its own writer reads it whole. So the command
calls `memory.CheckSize`, and the `--fail-at` policy, the leading size line and the flag set are
extracted into one place both commands use rather than copied.

Four decisions:

- **Exit 0 by default, whatever the band**, exactly as `ape memory check` does. The framework's
  prose convention is "on non-zero exit: HALT", so a failing exit would abort
  `apex-generate-project-context` at the moment compaction is due — the gate would break the
  ceremony it exists to trigger. `--fail-at soft|hard` is the CI opt-in.
- **The canonical path only.** Reader skills carry a `**/project-context.md` fallback; the
  generator writes `{development_folder}/project-context.md`, and a size gate has to be able to
  say which file it measured. `absent` therefore prints the path it stat'd, so a project that
  put the file elsewhere can see that from the output alone rather than reading `absent` as
  "there is no such file anywhere".
- **No `development_folder` is not `absent`.** With no configured folder there is no path to
  stat, so the check has no basis to report the file missing; it exits 2, the preflight code
  `ape story` already uses for an unconfigured folder. A check with no basis to judge must not
  invent a verdict.
- **No `ape doctor` row**, and this is a deliberate omission rather than an oversight.
  `memory.size` is one of only two Required project-data checks, and the framework's
  `apex-orchestrator/resources/preflight.md` documents that pair by name and forbids proceeding
  past a doctor red. Adding a third Required failure would change a documented framework
  statement from this side of the repo boundary, on a project whose `project-context.md` is
  already over the hard ceiling — i.e. on exactly the projects that need to keep working while
  they compact. Offered to the framework session as a follow-up they can request.

`ape config resolve` gains a `project_context` path alongside `team_memory`, since the command
needs the location and every project-data command routes through `apexcfg`.

### `ape release status`

PLAN-63 filed this as "deferred by M4 until the record exists to be projected". It exists now:
`apex-release-record`, its four steps and its **record template** are committed on the framework's
`intent-releaser` branch, and the framework session confirmed on 2026-09-05 that the template's
frontmatter key set is **final for v0.16.0** — none of PLAN-64's four still-blocked half-phases
touches record frontmatter. So the reader is pinned to it rather than written tolerantly.

- **A slice is released when its record says so, and by no other route.** The status is asserted
  in exactly one place, the record's `status:` field; a `release_slices:` entry carries no
  `status:` of its own precisely so a shipped release cannot be counted as unshipped by a writer
  that only ever wrote `declared`. An absent `releases/` folder, an absent record and an
  **unreadable** record all mean *not released* — the framework's own rule at
  `apex-story-batch-create/steps/step-01-discover.md:84` — so the slice's epics stay in scope.
  The unreadable one is reported as unreadable, never given a status.
- **Frontmatter only, confirmed by the framework as the right cut.** The nine body tables belong
  to `apex-release-record`, and a Markdown-table parser here would be a second source of truth
  behind the release's own verdict. It would also add no verdict information: `status:`,
  `blocking:` and `acceptance:` are already the three fields that carry it. Gates are reported as
  a **declaration** — count, required count, tier — never a result.
- **No tracker means null, not empty.** Epics are enumerated from tracker rows and there is no
  other source here; the framework's resolution discovers them from the epic shards and falls
  back to "every discovered epic", which is byte-for-byte the old `--epics all` behaviour and
  exists so a pipeline can never mint nothing by accident. Reporting `[]` would say "no epics",
  the one answer that is actively wrong.
- **`tagger_from_object` is provenance, never authorization.** It appears only on a
  `--backfill-legacy` record, naming whoever the git tag object credits. It is carried in its own
  field, labelled at every point it prints, and a test asserts it never populates
  `tag_authorization` — otherwise a reconstructed record could assert something no human said.
- **Exit 0 always.** A projection that halted on one bad record could not report the others.

The framework does **not** adopt the call in v0.16.0, by its own decision: a skill reading
`ape release status` would acquire a version floor on v0.0.67, and four half-phases are already
blocked on exactly that floor. The prose resolution also works with no `ape` at all and never
HALTs. So this ships as the field the caller will read, and the skill adopts it in v0.17.0.

### The migration-list runner

Division of labour with the framework's PLAN-66: `ape` owns parsing `_apex/migrations/*.md`,
semantic ordering, applying `derivable` entries, reporting `judged` ones, the applied-id ledger,
`--plan` and the doctor row. The framework owns the migration files, `apex-upgrade-project`, its
terminal contract, its help row and the orchestrator's routing event.

**The four rulings, applied.** All four were proposed here and confirmed by the framework
maintainer; they are not re-opened.

1. **Applied ids live in `_apex/framework.yaml` as an ORDERED list** of `{id, version,
   applied_at}`. The ledger — not the check — decides applied-ness, which is what makes a second
   run a no-op and a failed run resumable independently of what any command does. `applied_at` is
   issued through `internal/stamp`, so it is monotonic like every other field `ape` writes.
   **`framework.yaml` is regenerated wholesale by every update**, so the ledger is carried forward
   explicitly at the same site that already carries the config source — on *every* path including
   bootstrap, since a setup re-run over an existing project would otherwise drop it.
2. **`--plan` prints and does nothing**, is readable against a project in any state, and
   distinguishes pending / applied / cannot-tell. It reports a fourth state, `half-applied` — the
   ledger says it ran and the check says the post-condition does not hold — because PLAN-66 asks
   the doctor row for it and the ordered ledger exists to make exactly that visible.
3. **`kind:` gates whether the runner may ACT.** `judged` is listed and never executed under any
   flag, including when the entry also carries a `command:`; an unrecognised or absent `kind:` is
   treated as judged, because "ape does not understand this" is a reason not to run it.
4. **`check:` reports and never gates.** A check that cannot run leaves the entry
   unapplied-and-unverifiable; the runner does not act on it, which is the ruling's own reason —
   collapsing cannot-tell into pending builds a runner that re-applies things. An entry with **no**
   check declared is pending rather than cannot-tell: the absence of a check must not stop the
   runner either, and the ledger plus idempotency cover the re-run risk there.

**The exit-code mapping, and how it was found.** Checks map `0` → applied, `1` → not applied,
`≥2` → the check itself failed and the entry is cannot-tell. That was **not** the first design,
which read every non-zero code as "not applied". A test running a real `sh -c` — rather than the
fake every other test uses — showed that a shell reports a missing binary as exit **127**, so the
framework's own `… | jq -e '…'` check on a machine without `jq` would have come back non-zero,
made the entry pending, and been applied. The re-application arrives through the one door nobody
watches. The corrected mapping is the convention `grep -q`, `jq -e` and `test` already use, and it
means a framework check must exit 1 for "no" — `test -f x && grep -q y x`, never a bare
`grep -q y x`, which exits 2 when the file is absent.

**`ape` inside a check is the running binary.** Checks and commands run with a one-entry shadow
directory at the front of `PATH`, in which `ape` is a link to `os.Executable()`. Found by running
the framework's *authored* entry — `framework/_apex/migrations/v0.16.0_seq-01_retro-rows-per-epic.md`
— against a real machine rather than a fixture of it. Its check is
`ape sprint check --output-format json | jq -e …`; an `ape` v0.0.56 was on `PATH`; that binary has
`sprint check --output-format json`, emits valid JSON, and reports `findings: []` because
`sprint.epic_without_retro` did not exist yet. `jq` returned `true`, the check reported satisfied,
and the entry would have been recorded **applied on a project that was never migrated** — permanent,
since the ledger is never revisited. A check naming `ape` can only coherently mean the `ape`
deciding whether to apply the migration. When the shadow cannot be built the runner says so rather
than proceeding silently, because proceeding silently is the failure.

**Binding properties, and where each one actually lives.** Idempotent — the ledger, not the
command. Resumable — ledger rows are persisted in success order, so a failure leaves the completed
prefix recorded. Runs between dispatches — the caller's placement in `ape framework update`,
outside the build loop. Row-preserving — the framework's, since `apex-sprint-sync` already halts
rather than regenerate a tracker whose release keys it cannot re-emit; `ape` adds a test that a
second run is a no-op instead of relying on the command.

**A failure stops the sequence**, because `after:` means a later entry may depend on an earlier
one. The remaining entries are reported as not attempted, and the command still exits **0**: the
framework install succeeded, and collapsing a migration failure into a non-zero exit would make
the install look like it had not happened.

**Doctor row `migrations.pending`**, warn and never fail — a project that owes a migration is
behind, not broken, and a red here would sit on the orchestrator's never-worked-around list. It
runs no checks, answering from the ledger alone, so it stays cheap and predictable in `--strict`
CI. The name is the framework's request and is one character from the existing `migration.pending`;
both are kept, with a comment at the registration site and a note in the how-to saying which is
which, because the two have different subjects, remedies and owners.

`--scaffold` on `ape task` is explicitly **not requested** (O-3): `--args` already forwards
skill flags verbatim and both paths append `--autonomous`, so
`ape task <skill> --args "--scaffold lean"` is the operator's opt-in already.

## Acceptance

`make ci-local`, then `make check-harness`, then
`make check-framework APEX_FRAMEWORK_REPO=/home/diegos/_dev/github/diegosz/apex_process_framework`,
then the release flow in `docs/how-to/pre-tag-release.md`.

The release notes must name, because the framework's blocked acceptance blocks grep for them:
the version, the new `story verify` check-class names, and the two governance message shapes.

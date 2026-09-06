# CHANGELOG

## v0.0.67 (unreleased — draft)

> Draft entry for the PLAN-63/PLAN-64 bundle. The version is provisional
> until the tag is cut; the framework's blocked acceptance blocks fill
> `<X.Y.Z>` from the released tag, not from this heading.

### What the framework greps

The exact surfaces the framework's blocked acceptance blocks assert
against, quoted so they can be matched without reading the source. **Every
string below was run against the built binary**, not transcribed from the
source — including the dispatch row, verified with two live `ape task`
runs against a project carrying the roster: one that asserted and held,
one that asserted nothing and said so.

> **Pass `--active-extensions ext-adrs` on the three governance rows.**
> The classes are gated on the extension. `--file` takes it from that flag
> when given, and **otherwise from the project's own `extensions`** — so a
> block run inside a project that enables ADRs now exercises them either
> way. Pass it anyway: an acceptance block should assert a class in
> isolation rather than inherit whatever its fixture happens to declare.
>
> This used to be a trap rather than a preference. The flag was the only
> source, so on an otherwise-clean story citing a `proposed` ADR the exit
> code was **0 either way** and a block asserting "exit 0, flagged" passed
> while measuring nothing. Two changes closed it: the classes now derive
> from the project when no flag is given, and a run that skips them says
> so **on the summary line** (`… is valid — 2 check(s) SKIPPED`) rather
> than only on the lines beneath it.

| Command | Outcome |
| ------- | ------- |
| `ape story verify --file <story with a derived section removed>` | non-zero (**4**); the message names the section: `the derived section set requires ### Debug Log References and the body does not carry it (an empty section is accepted; a missing header is not)` |
| `ape story verify --file <story declaring adrs_applicable 0 against a non-zero tag match> --active-extensions ext-adrs` | non-zero (**4**); `adrs_applicable is 0 but the recomputed adrs_considered is 1 — the digest pass certified that none of 1 candidate ADRs applies, which is the one judgement it cannot make silently`. Never the words "applicability mismatch" |
| `ape story verify --file <story citing a governance.adrs id absent at HEAD> --active-extensions ext-adrs` | non-zero (**4**); `ADR-9999 does not resolve to an ADR at HEAD` |
| `ape story verify --file <otherwise-clean story citing a proposed ADR> --active-extensions ext-adrs` | **exit 0**, reported under `flagged`: `ADR-0004 resolves to an ADR whose status is "proposed", not accepted — reported, not gated`. The story must be otherwise clean — any other body finding makes the exit 4 for its own reasons |
| a non-committer dispatch — `ape task <skill absent from the roster> --output-format json` | exit **0**; `"commit_contract": {"skill":"<skill>","declared":false}` — the assertion RAN and held: HEAD unchanged, no path staged that was not staged before, stash unchanged. **Assert that shape, not the exit code**: a dispatch that asserted nothing is also exit 0, and carries `"skipped":true` with a `skip_reason` instead. The project must have `_apex/commit-owners.csv` installed or every dispatch skips |

The new `story verify` check-class names, in full:
`story.section_missing`, `story.file_list_marker`,
`story.placeholder_residue`, `story.compliance_table_header`,
`story.gcc_line_form`, `story.adrs_considered`, `story.adr_unresolved`,
and the report-only `story.adr_not_accepted`.

The dispatch-assertion check names: `dispatch.head_moved`,
`dispatch.index_staged`, `dispatch.stash_changed`, `dispatch.no_commit`,
`dispatch.message_format`, and `dispatch.committed_under_no_commit`.

One new `ape sprint check` class, for the framework's own v0.16.0
migration entry: **`sprint.epic_without_retro`**. Its `check:` line, which
`ape sprint check`'s always-exit-0 contract makes safe to gate a migration
on:

```bash
ape sprint check --output-format json \
  | jq -e '[.findings[] | select(.check=="sprint.epic_without_retro")] | length == 0'
```

New exit codes: **`ape story verify --file` 4** (body problem, beside the
unchanged 0/2/3) and **`ape task` 6** (declared commit ownership
violated).

---

The APEX framework is about to delete ~89 KB of git-prohibition prose from
85 skills, a timestamp-clamp paragraph from ~53 more, and the structural
rules four skills each restate in their own words. It can do that only
once the binary asserts what the prose asserted. This release is that
assertion — the `ape` half of the framework's PLAN-63 and PLAN-64, shipped
as one tag because two framework half-phases are gated on it and splitting
it would put prose in front of the check that enforces it.

- **feat(task): assert the project's declared commit ownership on every
  dispatch.** `_apex/commit-owners.csv` (`skill,commit_kind,message_regex`)
  says which skills commit and in what format. A skill **absent** from it
  must leave HEAD, the index and the stash reflog unchanged across the
  dispatch — and all three are checked, because HEAD alone is not the
  assertion: `git add` and `git stash` both leave HEAD exactly where it
  was, and a stash silently destroys the caller's working tree. A skill
  **present** must produce at least one commit, with every commit in
  `pre..HEAD` matching one of its declared formats — a per-commit
  predicate, not "HEAD advanced by one", because a batch dispatch makes a
  dev and a review commit per story.

  New exit code **6**, distinct from 1 because the skill's own work may
  have succeeded: the run finished and the repository is not in the state
  the project declared it would be, which is a different thing to fix.
  Rows naming a skill `ape` never dispatches — the conducting session's
  `apex-orchestrator` rows — are read and never reached. `--task-commit`
  is `ape`'s own commit and is not judged against a skill's declaration.
  A malformed CSV fails preflight rather than degrading to an empty one,
  since that inversion is precisely what would let a suppressed commit
  through.

  **A project with no `commit-owners.csv` is not asserted at all.** The
  ask specified "absent file = no skill commits", and that reading —
  which shipped first — puts every dispatch on the non-committer
  assertion, converting "this project has not adopted the declaration"
  into "this project asserts that nothing may commit". The framework's
  own eval found it: a successful `apex-story-batch-dev` dispatch made
  six correctly-formatted commits, `ape task` exited 6, and an 89-minute
  capture was discarded. Every framework skill that legitimately commits
  would have failed on every project that has not adopted the CSV, which
  today is all of them. With no declaration there is no basis for either
  assertion, so the verdict is now `skipped` with a reason. Where a CSV
  *does* exist and omits a skill, that is a real statement about the
  skill and the non-committer assertion still binds.

  Two further hardenings from the same investigation. HEAD advancing
  while the commit subjects come back empty is now a **skip**, not a
  suppressed-commit verdict — the two reads disagree and the one that
  failed must not decide the most serious verdict this check issues. And
  the verdict is now written to the run manifest as `commit_contract`,
  because the JSON envelope is ephemeral: a consumer that parses it, sees
  failure and discards stdout leaves nothing on disk saying why, which is
  exactly the position the eval was in. `totals.commits_made` also gained
  a doc note that it counts ape's own boundary commits and never the
  skill's, after reading as a smoking gun during that diagnosis.

- **feat(stamp): `ape` owns timestamp monotonicity.** Every skill resolves
  a `timestamp` and copies it into a field recording when something was
  last written. Two of those were seen moving backwards in a real project
  — `sprint-status.yaml`'s `updated_at` by ~2h44m, an ADR index's
  `generated_at` by ~57min, written by different agents. The framework's
  answer was a paragraph carried verbatim in ~53 skills telling each
  writer to clamp, which works exactly as well as an instruction is
  followed.

  The contract is unchanged and its implementation moves into the binary:
  local wall-clock, `YYYYMMDDHHMMSS`, and every issue returns
  `max(now, last_issued)`. The floor persists under
  `{output_folder}/ape/timestamp.state`, and when that file is absent — a
  fresh clone, a cleaned `_output` — it seeds from the newest `updated_at`
  already in `sprint-status.yaml`, so a clean checkout cannot silently
  reset it. `ape config resolve`, `ape sprint reconcile` and the run
  manifest's new `timestamp` field all issue through it. `ape sprint
  verify`'s exit-5 backwards-write check stays the detector of last
  resort rather than the only detector.

- **feat(story): `ape story verify --file` takes the whole story shape.**
  Seven new gating classes over the body, plus one that reports without
  gating:

  | Class | What it catches |
  | ----- | --------------- |
  | `story.section_missing` | a section the derived set requires is absent |
  | `story.file_list_marker` | a File List entry with no marker, an unknown one, or the em-dash-prose form standing in for one |
  | `story.placeholder_residue` | `_(populated during dev)_` surviving at `status: review` |
  | `story.compliance_table_header` | a compliance table header that is not the declared shape |
  | `story.gcc_line_form` | a GCC line that is not `- [ ] **[ID]** {instruction} -- {scope}` |
  | `story.adrs_considered` | `adrs_applicable` not binding against the recomputed tag-match count |
  | `story.adr_unresolved` | a `governance.adrs` id resolving to no ADR at HEAD |
  | `story.adr_not_accepted` | a cited ADR that is not `accepted` — **exit 0**, flagged |

  The derived section set is a function of **resolved config alone**: no
  writer stamps a story type today, so there is nothing else to read, and
  the check is built so a later `story_type:` key narrows the set without
  changing the exit contract. `## Feature Scope` is a member on
  `ext_features` alone and always expected — the `_No features apply to
  this story._` line is "no matches", never a missing section.

  New exit code **4** for a body problem, beside the unchanged 0 / 2 / 3;
  3 still wins when a file has both, because the keys are the more
  fundamental failure. Corpus mode is untouched and still never opens a
  body — that cap is what keeps a 465-story sweep at 67 KB rather than
  23.5 MB.

  **Every body class skips fenced code blocks.** A fence holds an example
  of a shape, not an instance of it — and the story template puts its own
  canonical File List entry inside a ```` ```markdown ```` fence that is
  copied verbatim into every minted story. Read as an entry, it reports
  the literal word `marker` as an unknown marker. The framework's pre-tag
  sweep caught this: 51 of 60 failing fixture stories, and 140 of one real
  project's 483 stories plus 2 of another's 296, failed on nothing but the
  template quoting itself. Tag matching is the one thing that still reads
  inside fences, because a tag named in an example is still the story
  talking about that subject.

  Two details worth stating because they differ from the ask as filed.
  The File List vocabulary is **all five** markers the template declares,
  not three: `(planned)` and `(deferred)` are `apex-lift-project`'s, and
  rejecting them would report a finding on every lifted story. And
  `### Debug Log References` carrying `_No issues encountered._` is a
  terminal convention rather than residue, so it is never a finding.

  The two governance classes need the ADR corpus, which `--file` mode
  finds by walking up from the story's own path. Against a story outside
  any project they **skip and say so** — the mode is required to keep
  working there, and a governance class that says nothing when it could
  not run reads as one that passed.

- **fix(repl): stop leaking ape's tmux pane address into PTY children.**
  `TMUX` and `TMUX_PANE` say which terminal a process is attached to, and
  for a child on ape's in-process PTY the inherited pair describes **ape's**
  terminal rather than the child's. Claude Code reads them and records the
  pane in its session registry as `"tmux": "session:@window.%pane"` without
  checking the pane is its own controlling terminal. Driving that address
  then fails both ways at once: the control message lands as literal text
  in whatever the operator has in that pane, and the claude on the PTY sees
  nothing.

  ape itself was never affected — the runner writes to its own PTY master
  and never reads the field — which is exactly why this went unnoticed. The
  damage lands on external consumers of the registry, and it would have
  landed dozens of times during an unattended multi-step run inside tmux.

  Reproduced and fixed under a real tmux server rather than a simulated
  one, which turned out to matter: a fake `TMUX` value does not reproduce
  it at all, presumably because Claude Code queries a live server. Inside a
  genuine session the pre-fix binary recorded `ape-tmuxleak-test:@1.%1`,
  a pane running `sleep`; the fixed binary records **no `tmux` key at all**.
  That absence is the correct answer rather than a missing one — the only
  way into that REPL is ape's PTY master, which no external process can
  reach, so a tool reading the registry should conclude there is no
  keyboard to drive.

  **`ape chat` deliberately keeps them.** It direct-execs claude onto the
  user's real terminal, where the inherited pane address is correct and
  useful. That asymmetry is why the filter is a separate unexported
  function rather than two more lines in the shared `ScrubClaudeCodeEnv`:
  a caller outside `internal/repl` cannot reach it, so it cannot be applied
  to the chat path by accident, and a test asserts the shared scrubber
  still passes tmux through so folding the two together fails loudly.

  `TMUX` is stripped alongside `TMUX_PANE` on purpose. A child that still
  sees `TMUX` believes it is inside tmux, and with `teammateMode` set to
  `auto` or `tmux` it would open agent-team panes in the operator's visible
  window while its own output went to ape's PTY. Inert today, closed for
  free.

- **feat(story): a report-only `story.requirement_ids_missing` class, and
  `ape deferred discard` stops asserting two untrue things.**

  `story.requirement_ids_missing` fires on a story that declares no
  `requirement_ids` — the field the release record's coverage table reads.
  **Advisory: off by default behind `--include-advisory`, with no `--fix`,
  and it never decides an exit code.** The
  value lives in the story's prose, where two sections can disagree, and
  choosing between them is the judgement `apex-frontmatter-repair` exists
  to make; a `--fix` that guessed would be exactly the re-derivation that
  skill's contract forbids. The class exists so that skill can branch on a
  check name like its other classes instead of globbing the corpus. It
  fires on every story until the field is backfilled — hundreds on a real
  corpus — which is why it is opt-in rather than merely non-gating.
  `ape doctor` reds sit on the orchestrator's never-worked-around list, so
  a class that cannot be cleared for the duration of a migration would
  either get that rule suspended in practice, teaching operators that
  doctor reds are sometimes ignorable, or force an escalation nobody can
  act on. Invisible to the gate whose job is to be trustworthy; fully
  visible to every consumer whose job is to work it —
  `apex-frontmatter-repair` asks, a migration entry asks. A present-but-
  empty list counts as missing: it asserts nothing the coverage table can
  use.

  `ape deferred discard` had two fields asserting things that were not
  true. Its refusal on an already-closed record ended "reopen it first",
  naming an `ape deferred reopen` that does not exist — the fix is to stop
  naming it, not to build a command because an error string mentioned one.
  And a discard stamped `resolved_at`, claiming work was resolved that
  nobody resolved; it now stamps a new `discarded_at`. **Records written
  before this are not rewritten**: guessing which historical `resolved_at`
  values were really discards is the kind of invention this store refuses
  everywhere else, so old records keep the wrong field and only new
  discards are honest.

- **feat(spawn): pin the output style on every spawned session.** Every
  session `ape` starts inherits whatever output style the machine has
  configured, and an output style claims precedence over other
  communication and formatting guidance — which is exactly what the
  framework depends on at the end of a run: the fenced return contracts a
  batch orchestrator parses, the guided menus and HALT prompts, the
  completion summaries the eval asserts on. A developer who set `Concise`
  for their own conversations would silently change what every skill run
  emits, on their machine only, in a way no test elsewhere would
  reproduce.

  `ape` now writes `"outputStyle": "Default"` into the `--settings` blob
  it already builds, on every spawn path. The key must be written
  explicitly: precedence is enterprise-managed → `--settings` →
  project-local → shared-project → user, and an omitted key falls through
  to the highest file that defines one, so leaving it out neutralises
  nothing. `--ignore-project-settings` was never the answer — it passes
  `--setting-sources user`, which drops the project files and keeps the
  user one. `--output-style inherit` opts out; `--output-style
  <name>` pins a specific one. Available on `task`, `pipeline`, `prompt`
  and `chat`.

  Two paths that used to return an empty blob now carry the pin, which is
  the point: a pin written inside the hooks block would have been missing
  from exactly the plain interactive spawn most runs use. **`--eval` stays
  byte-empty** on purpose — PLAN-6 invariant #1 locks spawn-shape
  equivalence with an external consumer, and nothing needing the pin
  reaches that path.

  Verified against Claude Code 2.1.259 rather than taken from the docs,
  which only ever describe *omitting* the key. An unknown style name is
  silently ignored rather than rejected, so "it did not error" was not
  accepted as evidence: the check was a custom style with a unique marker,
  confirming the key is honoured from `--settings`, that an explicit
  `Default` overrides the same style set in a settings file, and — through
  a real `ape prompt` run against a project configured with that style —
  that the marker is absent by default and present under
  `--output-style inherit`.

- **feat(context): `ape context check`, the same two budgets over
  `project-context.md`.** `project-context.md` is the standards document
  every skill loads whole, it grows by append, and it is bounded by the
  same 256 KiB Read cap `team-memory.md` is — past which its own writer
  can no longer read it. The request was explicitly for *one caller of the
  budget constants rather than two restatements of the numbers*, so the
  command calls the same `memory.CheckSize` with the same soft 40960 B /
  hard 204800 B, and the `--fail-at` policy and the leading size line are
  extracted into one place both commands share. Same four states, same
  flags, same JSON shape.

  **Exit 0 by default, whatever the band**, for the reason `ape memory
  check` keeps that contract: "non-zero means HALT" would abort
  `apex-generate-project-context` at exactly the moment compaction is due.
  `--fail-at soft|hard` is the CI opt-in.

  Two things it deliberately does not do. It measures **only**
  `{development_folder}/project-context.md`, the path the generator
  writes — reader skills glob for a fallback copy, but a size gate has to
  name the file it measured, so `absent` prints the path it stat'd rather
  than searching. And with no `development_folder` configured it exits 2
  instead of reporting `absent`: there is no path to stat, so there is no
  basis to call the file missing. `ape config resolve` gains a
  `project_context` path beside `team_memory`.

- **feat(release): `ape release status`, projecting the release record.**
  A slice is an operator-declared set of epics living in
  `sprint-status.yaml`'s `release_slices:` / `active_slice:` keys; a
  record is one file per release under `{planning_folder}/releases/`,
  whose frontmatter asserts that release's status. `ape` reads both and
  writes neither.

  **A slice is released when its record says so, and by no other route.**
  A `release_slices:` entry deliberately carries no status of its own —
  that is what stops a shipped release being counted as unshipped by a
  writer that only ever wrote `declared`. An absent `releases/` folder, an
  absent record and an **unreadable** record all mean *not released*, so
  the slice's epics stay in scope; the unreadable one is reported as
  `unreadable` with its parse error rather than handed a status it never
  asserted.

  **Frontmatter only.** The record's body carries nine assembled tables,
  including the gate table with its per-row `PASS` / `RED` / `NOT-RUN` /
  `PENDING` results. Those belong to `apex-release-record`; re-deriving
  them from a Markdown table would put a second source of truth behind
  the release's own verdict, and there is nothing to gain — the verdict is
  already in the frontmatter, where `status:` asserts it, `blocking:` says
  why it is not `prepared` and `acceptance:` says why a run that reached
  no verdict reached one. What is reported about gates is the
  declaration: how many, how many required.

  `active_slice` resolves `declared`, `undeclared`, or `dangling` — the
  last being an id `release_slices:` does not carry. All three yield a
  scope, and `dangling` is kept separate because it is a repair rather
  than a default. With **no tracker** the epic sets come back `null`
  rather than `[]`: epics are enumerated from tracker rows, and "nothing
  to enumerate from" is not "no epics". `tagger_from_object` is carried
  apart from `tag_authorization` and labelled *provenance, not
  authorization*, everywhere it prints — it is reconstructed from a git
  tag object on a `--backfill-legacy` record, and reading it as
  authorization would let a record assert something no human said.

  **Exit 0 always**: a projection that halted on one bad record could not
  report the others. The key set is pinned against the framework's own
  record template, confirmed final for framework v0.16.0.

- **fix(repl,chat): `ape` inside a spawned session is the binary that
  spawned it.** Every session ape starts now gets a one-entry directory at
  the front of `PATH` in which `ape` is this binary, removed when the
  session is reaped.

  About 69 framework skill files run `ape …` lines, and those resolved
  through the operator's `PATH` — whatever the machine has installed,
  which need not be the binary running the dispatch. Observed live on a
  machine carrying both `~/go/bin/ape` and `/usr/local/bin/ape`: **ape
  0.0.67 spawned a session and `ape version` inside it reported 0.0.56.**

  This release exists to raise the framework's `ape` **version floor**, so
  a skill running a pre-floor binary inside a dispatch by the post-floor
  one makes that floor unenforceable from the inside. And it fails
  silently: a merely-old binary still has the commands, still emits valid
  output, and answers about a world where the newer checks do not exist. A
  *missing* command would have errored and been caught — the third time in
  this release that a coherent wrong answer beat a loud failure.

  Found by extending the migration-runner fix below one layer up, after
  the framework session confirmed the stale binary was the machine's
  default rather than an artifact of one probe. The framework's own eval
  harness had reached the same remedy independently eight weeks earlier
  (`_pin_ape_on_path`, observing v0.0.52), which neither side knew.

  The pin covers the PTY path and `ape chat` alike — unlike the tmux
  scrub, there is no asymmetry here, because this is about which binary
  `ape` names rather than which terminal the child is attached to. If it
  cannot be made, the run says so and proceeds unpinned.

- **fix(story,task): a summary line now carries its own skips.** Three
  gates this release had a skip read as a pass, and the sharpening that
  came out of it is the rule this implements: **none of the three failed
  to report the skip.** Each printed it honestly, on its own line. What
  defeated us every time was the *summary above it*, because the summary
  is what gets quoted into a message, pasted into a report and carried
  forward as evidence.

  So `ape story verify --file` now prints `OK: <path> is valid — 2
  check(s) SKIPPED, listed below` when governance classes did not run,
  and `ape task` prints `✅ task X done … — commit contract NOT asserted`
  when the dispatch's assertion was never made. Both were previously a
  clean summary with the skip on the line beneath, which is exactly the
  shape that let a v0.15.0 manifest be reported as a green v0.16.0 gate
  for a whole release. Neither suffix appears when nothing was skipped,
  so an ordinary run reads as it always did.

- **feat(framework,story,task): three gaps this release exposed, closed
  before the tag.** All three were queued for "the next release" until the
  framework maintainer pointed out there is no released v0.0.67 to defer
  *from* — nothing is pushed or tagged, so the version number was a
  fiction. They land here.

  **An installer-coverage gate, derived from the tree.** It walks a
  framework's `_apex/` and asserts every file arrives in the project,
  with a small default-DENY list naming what ape deliberately does not
  install and why. That direction is the point: a list of what to install
  cannot catch a missing entry, because absence looks like completeness —
  which is how `commit-owners.csv` and `_apex/migrations/` both shipped
  unreachable. Re-breaking each install in turn makes the gate fail
  naming exactly that file, so it is verified against the two defects it
  exists for rather than only against a green run.

  **`dispatch.committed_under_no_commit`.** The counterpart to the skip
  above: `--no-commit` excuses the *absence* of a commit from a declared
  committer and forbids its *presence*. Reported instead of
  `dispatch.message_format`, including when the subject matches
  perfectly — when the commit should not exist, its wording is not the
  defect, and naming the format would send the operator to fix a commit
  whose fix is deletion. **Proven by live dispatch rather than shipped
  with a caveat**, both branches: a declared committer that commits under
  the flag exits 6 with the new class; the same skill making the same
  commit without the flag exits 0 clean.

  **`ape story verify --file` derives extensions from the project it
  already resolved.** The mode walks up to find the ADR corpus and then
  discarded that project's own `extensions`, requiring the caller to
  retype them via `--active-extensions`. An explicit flag still wins;
  omitting it now means "ask the project" rather than "no extensions".

  **This is stricter than it sounds, and it can newly fail a story that
  passed.** The extensions do not only gate the two ADR classes — they
  decide the **derived section set**. A flagless run on a project enabling
  `ext-patterns` now requires `### Pattern Compliance Table`; one enabling
  `ext-features` requires `## Feature Scope`. A story that reported
  `is valid` flagless yesterday can report `story.section_missing` today,
  and the three skills above enforce it. That is the correct set — those
  skills were under-checking the shape as well as the governance — but it
  is a behaviour change and not only an unlocking.

  Note the inversion it creates: `--active-extensions ext-adrs` now checks
  **less** than no flag at all on a four-extension project, because it
  pins the set to one. That is what you want for testing a class in
  isolation and not what you want for reproducing what a skill sees.

  This one was worse than a convenience. Of fourteen framework skills
  referencing `ape story verify`, three pass the flag and eight use
  corpus mode (which always read the project). **Three call `--file` with
  no flag** — `apex-orchestrator`, `apex-review-story` and
  `apex-story-governance` — so the two ADR classes silently did not run
  on every invocation and the caller read `is valid`;
  `apex-review-story`'s is the structural pre-review gate four other
  steps cite as their verdict source. At corpus scale the framework's own
  sweep passed no flag, so **982 of 982 stories skipped both classes
  across seven sweeps** and were reported clean, 94% of them on projects
  that enable ADRs.

- **fix(task): `--no-commit` convicted a declared committer for obeying
  it.** A skill in `commit-owners.csv` dispatched with `--no-commit` made
  no commit — as several framework skills mandate in their own text
  (`apex-sprint-planning`: "When `{no_commit}` is `true`: make NO commits
  and NO `git add`/`git stash`") — and the contract reported
  `dispatch.no_commit`, exit **6**.

  The defect is sharper than a missing exemption. The roster is
  `skill,commit_kind,message_regex`: it has **no conditionality column**,
  and it declares the *shape* of a commit rather than its inevitability —
  "when this skill commits, the subject looks like this". Reading it as
  "this skill always commits" is a claim the framework never made and has
  no column in which to make. As shipped, `--no-commit` was unusable for
  all six declared committers.

  Under the flag, a declared committer producing no commit is now a
  **skip with a reason**: a suppressed commit means *the skill was
  permitted to commit and produced none*, never *the operator said not to
  and it complied*. The flag excuses the absence of a commit, not a
  malformed one — a skill that commits anyway is still held to its
  declared shape.

  **Why it surfaced only now, and it is this release's pattern once
  more:** the roster was never installed into a project until this
  release, so every dispatch before it took the *non-committer* branch.
  The committer branch had never executed against any fixture, ever — and
  its first live run misfired on the one path that deliberately suppresses
  commits. Correct code, unreachable, wrong the first time it was reached.

  Both directions are tested, live and in unit tests, because a fix that
  only made the first case pass would have disarmed the check entirely: a
  committer under `--no-commit` producing none **skips**; the same
  dispatch without the flag still **exits 6**.

- **fix(task): `--task-commit` reported a commit-contract pass it never
  ran.** On that path ape makes the dispatch's own commit, so the skill's
  declaration cannot be asserted against the range — deliberately. But the
  verdict was left as the **zero** `Result`, which marshals as
  `{"skill":"","declared":false}`: no violations, `OK()` true, `skipped`
  absent. A consumer reads that as "the non-committer assertion ran and was
  clean". Nothing was asserted at all.

  That directly contradicts the envelope field's own documented contract —
  "a consumer must be able to tell 'asserted and clean' from 'could not
  assert', and a field that appears only on failure cannot". It now emits
  an explicit skip naming the skill and the reason, through a new
  `commitowners.Skipped` so the shape lives in one place.

  Found while writing the first tests for `commit_contract` at all: the
  key the framework's acceptance block greps for existed in one struct tag
  and one CHANGELOG line, with nothing tying them together. It now has
  three — the key by name, a clean verdict still being emitted, and a skip
  not marshalling like a pass.

- **fix(framework): install `_apex/migrations/` too — the same gap, one
  item over.** The upgrade runner reads the list from
  `{apex_folder}/migrations/` in the **project**, and nothing put it
  there. So on every project `ape framework update --plan` reported
  *"this framework ships no migration list"* — which on a framework that
  ships one is not unhelpful, it is **false**, and it is the sentence an
  operator would act on. The whole runner was unreachable.

  Found while updating the docs: `framework-update.md`'s "What gets
  touched" table is a contract about what the command writes, and writing
  the roster row into it meant checking what else the table was missing.
  The lesson from the roster fix — *when a feature reads a file from the
  project, check that something puts it there* — had been written down
  and not applied to the very next feature in the same release.

  Refreshed rather than synced, like the aboard recipe library: an entry
  the framework prunes stays put, because the applied-id ledger records
  entries **by id** and a removed file would leave a ledger row naming a
  migration nobody can read. No empty directory is left behind when the
  framework ships none — `--plan` distinguishes "no folder" from "a folder
  declaring nothing", and an empty one would report the second when the
  truth is the first.

- **fix(framework): install `_apex/commit-owners.csv`, without which this
  release's headline assertion never fires.** The framework ships the
  commit-ownership roster and its own `_apex/README.md` lists it in the
  installed folder structure beside `terminal-contracts.csv`. ape's
  installer had an explicit list of framework-owned `_apex/` files and the
  roster was not on it.

  `ape task` reads the roster **from the project**, and an absent file
  correctly means "this project has not adopted the declaration" — a
  `skipped` verdict with a reason, never a conviction. Those two facts
  compose into the worst possible outcome: on every project installed the
  normal way the file was absent, every dispatch's assertion skipped, and
  the skip looked exactly like the deliberate behaviour it is. The
  assertion that lets the framework delete 85 `## Commit Policy` sections
  would have been inert everywhere, reporting normally.

  Installed on the same version-skew terms as every other optional
  framework file: a framework that predates the roster installs none. The
  update line now reports **both** directions — an absent roster is not a
  quiet default when it disarms the check the release exists for.

  Found by verifying the last unverified row of the greps table above,
  which needed a real dispatch, which needed a project with a roster —
  and there was no way to get one.

- **fix(story): a governance class that did not run says so.**
  `ape story verify --file` gates the two ADR classes on `ext-adrs`, which
  in `--file` mode comes from `--active-extensions` alone. When the
  extension is off the classes were skipped **silently** — unlike the
  adjacent unresolvable-corpus branch, which has always reported its skip
  on the stated grounds that "a governance class that says nothing when it
  could not run reads as one that passed".

  That branch is reached two ways which look identical from outside: a
  project that genuinely does not use ADRs, and a caller that forgot the
  flag. Measured on an otherwise-clean story citing a `proposed` ADR,
  `--file` exits **0 with the flag and 0 without it**, and only the first
  run produces the flagged finding — so an acceptance block asserting
  "exit 0, flagged" passes while measuring nothing at all. Found while
  verifying every string in the greps table above against the built
  binary rather than transcribing it from the source.

  The run now reports `skipped story.adrs_considered` and
  `skipped story.adr_unresolved` with the reason. Report-only and
  additive: no exit code and no existing message changes.

- **feat(sprint): a `sprint.epic_without_retro` check class.** One finding
  per epic carrying no `epic-N-retrospective` row, in a command that
  already always exits 0. It exists because the obvious substitute cannot
  answer the question: a framework upgrade migration needs a `check:` for
  "is there a retrospective row per epic", and the only datum before this
  was `retrospective_rows >= epic_rows` — which two retros on one epic and
  none on another satisfies. A migration whose check can report *applied*
  while the thing it checks is false is worse than one with no check at
  all, because the runner writes the id to its ledger and never looks
  again: a false "applied" is permanent.

  The epic set is every epic with an `epic-N` row **or** a story row
  belonging to N, so an epic mid-mint is still asked. A retrospective row
  whose key names no epic — `project-retrospective` — discharges none;
  crediting it to whichever epic is missing one would invent an
  attribution the key does not carry.

  The summary line changed with it. `N divergence(s) — neither side is
  assumed correct` is a claim about a two-sided finding, and this class
  names one missing row with nothing to weigh it against, so the claim is
  now made only when the findings actually carry two sides.

- **feat(framework): run the framework's upgrade-migration list.** A
  framework release changes what project data must look like. Until now
  the only upgrade path was prose in a CHANGELOG bullet and whatever the
  operator remembered to run, and a project that skipped a version had no
  way to find out. The framework now ships `_apex/migrations/*.md`, one
  entry per migration, and `ape framework update` runs it.

  **`kind:` is an authority model, not a hint.** `derivable` runs
  unattended. `judged` is listed with the skill to dispatch and is **never
  executed, under any flag** — an entry that also carries a `command:`
  still does not run it, and an unrecognised `kind:` is treated as judged,
  because ape not understanding an entry is a reason not to run it.

  **The ledger decides applied-ness.** Applied ids live in
  `_apex/framework.yaml` as an ordered list of `{id, version, applied_at}`
  — a list rather than a set, because "which migrations ran, in what
  order" is what an operator asks when one half-applies. It is what makes
  a second run a no-op and a failed run resumable, independent of what any
  command does. `framework.yaml` is regenerated wholesale by every update,
  so the ledger is carried forward explicitly on every path: losing it
  would make every applied migration look pending, which is the exact
  re-application it exists to prevent.

  **`--plan` prints and does nothing** — no install, no fetch, no
  migration — and is readable against a project in any state. It
  distinguishes `pending` / `applied` / `half-applied` / `cannot-tell`
  rather than collapsing them: an entry whose check *could not run* is
  unapplied **and unverifiable**, and reading that as pending is how a
  runner re-applies things.

  **A check's exit code is read the way `grep -q` and `jq -e` already
  work: 0 applied, 1 not applied, 2-and-above the check itself failed.**
  This was not the first design, and a real shell is what corrected it. A
  shell reports a missing binary as exit **127**, so the framework's own
  `… | jq -e '…'` check on a machine without `jq` comes back non-zero —
  and the original mapping read every non-zero code as "not applied",
  made the entry pending, and would have applied it. The failure arrives
  through the one door nobody watches, and only a test that ran a real
  `sh -c` found it.

  **`ape` inside a check means the binary running the migration**, not
  whatever `ape` the machine has installed. Checks and commands run with a
  one-entry shadow directory at the front of PATH. This was found by
  running the framework's own first entry on a real machine rather than a
  fixture of it: its check is `ape sprint check --output-format json | jq
  -e …`, an `ape` v0.0.56 was sitting in `~/go/bin`, and that binary *has*
  `sprint check --output-format json`, emits perfectly valid JSON, and
  reports `findings: []` because the check class did not exist yet. `jq`
  said `true`, the check reported satisfied, and the entry would have been
  recorded applied on a project that was never migrated — permanently,
  because the ledger is never revisited. The same defect shape as the 127
  one above and as everything else this release turned up: the check ran,
  it reported success, and it was measuring a stand-in.

  Ordering is **semantic** — semver on `version`, integer on `seq`,
  honouring `after:` — and never the filename's, under which `v0.9.0`
  sorts after `v0.10.0`. An `after:` cycle leaves the order undefined and
  none of the entries in it runs; an invented order is how a migration
  runs before what it depends on. A failed entry stops the sequence, since
  a later one may depend on it, and the rest are reported as not
  attempted. The command still exits 0 — the install succeeded, and a
  migration failure is not an install failure.

  `ape doctor` reports `migrations.pending`, warning rather than failing:
  a project that owes a migration is behind, not broken. That row runs no
  checks and answers from the ledger alone, so it stays cheap in
  `--strict` CI. It is a **separate row** from the existing
  `migration.pending`, whose near-identical name is deliberate but
  confusable: that one is ape's own project-data conversion detected from
  disk state, this one the framework's authored list.

- **feat(config): `evidence_folder` joins the overlay allow-list.**
  `OverlayKeys()` iterated seventeen keys and silently skipped the rest,
  so `ape config resolve` could not emit a variable the framework added
  and `config.local.yaml` could not override one. The key is emitted
  **raw, with no default and no derived path**: the framework owns its
  fallback chain, and re-deriving it here would put a second source of
  truth behind a key whose whole point is that the framework resolves it.
  It is marked optional, so the config-template drift guard no longer
  requires the framework's own template to declare it.

  A second key, `model_profile`, was asked for, implemented, and then
  **withdrawn before release** at the framework maintainer's decision. The
  framework removed the lean scaffold profile the key was a ceiling over,
  so it had nothing left to bound. Recorded here rather than silently
  dropped, because it shipped in this branch's history: the recapture
  measured lean against full across three pairs and it did not earn its
  complexity — one stage cheaper, one more expensive in both lean runs,
  run-to-run variance on the third about three times the claimed effect —
  and the context-headroom argument died on `compactions_observed: 0`.
  Peak occupancy across 57 measured steps ran 4.4%–37.1% of a 1M window,
  and the worst case anywhere — 37.1%, with zero compactions — was a
  **full** step, so the pressure lean exists to relieve was absent even at
  maximum pressure.

## v0.0.66 (2026-09-01)

Two checks had been reporting for weeks with nothing that could act on
them, and the one documented remedy turned out to be destructive on the
case that needed it most. Found by repairing two real projects by hand.

- **fix(hookdrift): the seeded hook gate was a coin flip.** `make check-hooks`
  writes its own corpus by running one unattended session that delegates to a
  sub-agent — and whether it delegates is the model's call. A Haiku session at
  effort `low`, told to spawn a sub-agent for a one-file read, will sometimes
  just read the file: a perfectly good answer to the question asked, and a
  useless corpus for this gate. Caught it failing a release gate at 2 turns
  instead of 6, after passing three times, on an unchanged contract.

  The gate was right to refuse — it reported "broken seed, NOT a passing
  contract", which is the distinction it exists to draw. But a release gate
  that fails at random gets re-run until it is green, and then it is not a
  gate. Producing the corpus is the means, not the thing under test, so an
  attempt that provokes no sub-agent is now a wasted setup and is reseeded,
  up to three times. **Drift is never retried**: a field seen but absent
  fails on the first attempt, because re-rolling that is exactly what this
  package exists to stop. The seed prompt now also forbids doing the work
  directly, which is what makes delegation the only way to comply.

- **fix(cost): price Claude Fable 5.1, and move the `fable` alias onto it.**
  `claude-fable-5-1` turned up in local transcripts and the table had no row
  for it, so its turns priced through the family tier — an estimate, flagged
  everywhere but still not a rate. Worse, `fable` still resolved to
  `claude-fable-5`: a bare `fable` in a pipeline spec or `--model` selected
  the superseded model, silently. Both confirmed against the model docs
  rather than accepted from the tool's own estimate: `claude-fable-5-1` is
  $10.00/$50.00 per MTok with a 1M window, and Fable 5 is now listed under
  "Legacy models (still available)" — so it keeps its exact row, because a
  transcript an older Claude Code wrote must still price exactly. The window
  is locked in `TestContextWindow_GenerationBoundary` alongside the rest of
  the 1M generation.

  `make check-prices` caught this as a release-gate failure, which is what
  it is for — the same shape as the `opus[1m]` gap that priced real usage at
  zero for thirteen days.

- **fix(registry): `sync` offered to delete the metadata that would have
  repaired the record.** A record with no frontmatter block claims no id,
  so it is absent from the on-disk id set and its index entry reads exactly
  like a phantom. `sync` proposed `remove` — and that entry was the last
  copy of the record's id, title, type, status, version and dates, on a
  file sitting on disk the whole time. The two findings always arrive
  together (`registry.phantom_entry` beside `registry.record_unparseable`)
  and the framework's own preflight names this command as the fix for the
  first of them.

  Reproduced from a field ADR whose header had been lost. Removals are now
  withheld where a phantom cannot be proven, narrowest rule first: an entry
  whose `file:` names an unreadable record is that record's entry; and
  while any record in the family is unreadable, no entry in it is provable,
  because the unreadable one may claim that id. Adds and file-repointing
  still run — forward progress is not what loses data. Withheld removals
  are reported under their own heading, not as a note beneath a list of
  successes.

- **feat(registry): `ape registry restore-headers` — `sync` in the other
  direction.** Gives a headerless record the frontmatter its index entry
  already states, which dissolves both findings and loses nothing. Values
  are copied verbatim, sequences like `tags` included (`Entry.Fields` keeps
  only scalars, so this reads the YAML node), minus the two
  index-bookkeeping keys and plus the `output_document` self-reference.
  Nothing is invented.

  That self-reference is the one derived value, and it is taken relative to
  the resolved project root. An earlier cut found it by searching the
  absolute path for a segment literally named `development` — but
  `development_folder` is a config variable, so a project that renamed it
  got its own absolute, machine-specific directory written into a committed
  record, and a project living under a coincidental `/…/development/…`
  ancestor got a path rooted somewhere else entirely. Every fixture used
  the default name, so nothing caught it.

  It refuses a record that HAS a frontmatter block, even one that fails to
  parse. Overwriting a header someone authored to satisfy a checker is a
  different and far worse operation than giving a headerless file the
  header its index says it always had.

- **feat(story): `ape story verify --fix`, scoped to one class on
  purpose.** A `depends_on` item that decoded as a number is re-quoted:
  same characters, one right answer, derivable from the finding with no
  second source. Both the flow and block shapes, and an already-quoted item
  is left as authored.

  It refuses the two classes that look equally mechanical. A `features`
  item needs a contribution, whose only source is the prose `## Stories`
  table in the feature record — and where that table has no row for the
  story, the disagreement between the documents IS the defect. A missing
  `features:`/`capabilities:` key needs a value, and `[]` claims the story
  contributes to nothing, which is a registry question. Both are reported
  as remaining rather than dropped, and both route to
  `apex-frontmatter-repair`: the same split as `ape deferred verify` and
  `apex-deferred-repair`.

  The repair is lexical so the rest of the file survives byte-for-byte —
  this corpus has hand-wrapped flow sequences a node round-trip would
  reflow. That is also how a fixer corrupts a file while reporting success,
  so every touched file is re-verified and rolled back if the count of the
  findings `--fix` OWNS did not fall.

  Two bugs caught before release, both in that guard's neighbourhood. A
  single regex anchored on the delimiters has to consume an item's trailing
  comma, which eats the next item's leading one — `[112.1, 112.2]` became
  `["112.1", 112.2]`, worse than no fix because it looks repaired; the
  fixture caught that one. And the rollback originally counted every
  `story.type_mismatch`, a check that covers `features[i]` as well as
  `depends_on[i]` — so on a story carrying both, one surviving features
  finding cancelled one repaired depends_on finding, tripped the `>=`, and
  reverted a correct repair, leaving it reported as "remaining" with
  nothing to distinguish it from a class `--fix` refuses on purpose. Every
  other test here puts the two classes in separate files, which is why it
  took a review to find. The count is now scoped to the one class this
  command owns.

- **internal(atomicfile): both writers rewrite documents a person authored,
  so neither uses `os.WriteFile`.** It truncates before it writes, which
  means an interrupted call destroys the original and leaves nothing in its
  place — on exactly the files whose loss is least recoverable. The new
  package writes a temp file beside the target and renames over it, so a
  reader sees the whole old file or the whole new one.

  The rename alone does not make that survive a crash: it can reach the
  disk ahead of the data it points at, leaving a file that is present,
  correctly named and empty. The temp file is therefore synced before the
  rename and the directory after it, and the package documents which of the
  two guarantees each step buys. Directory sync is best-effort — some
  platforms refuse to open a directory for it, and failing a write that is
  already correct on disk would trade a durability nicety for an outage.

## v0.0.65 (2026-09-01)

Verified against Claude Code 2.1.252: `check-prices` (5/5 observed models
exactly priced) and `check-claude` (all six subtests) both pass. The third
gate could not answer, and that turned out to be the interesting part.

- **feat: `check-hooks` seeds its own corpus instead of hoping to find one.**
  The gate read `hook-events.jsonl` files left behind by past interactive
  runs, so it could only speak about a project someone had recently run
  `ape` in. `HOOK_PROJECT` defaulted to this repo, which has no runlogs and
  always skipped — and on the machine where it was finally checked, **no
  project on the entire filesystem had a single `hook-events.jsonl`**. In its
  whole existence the gate had never once fired, while contributing a
  permanent SKIP line to `check-harness`. That is the defect that retired the
  eight `TestParity_*` gates in v0.0.55: a skip is not a pass, and a gate
  that can only skip reads as one.

  It now writes the evidence it judges — `TestLive_HookContract` copies
  `testdata/apexproject` to a temp dir, drives one unattended `ape prompt`
  session, and reads the runlog that session wrote. Reproducible on any
  machine with `claude` + auth, no pre-existing project, ~20 s and a few
  cents. The corpus is deliberately a *real* session: synthesising a
  `hook-events.jsonl` would test ape against ape's own idea of the payload,
  which is the tautology `hookdrift` exists to escape.

  The seed spawns a sub-agent on purpose. `tool_response` is only counted on
  Agent-tool `PostToolUse` and `agent_id` only on `SubagentStop`, and
  `Observation.OK()` passes a field seen zero times — so a seed that merely
  answered a question would verify one field of three and still report green,
  reintroducing the exact failure mode inside the fix. A field observed zero
  times therefore fails as a **broken seed**, reported separately from a
  drift verdict.

  The observational read did not go away: it is `ape doctor --only
  hooks.contract_drift --cwd <project>`, unchanged. Only the Makefile alias
  for it is gone.

- **refactor: `HOOK_PROJECT` → `APEX_PROJECT`.** Once `check-hooks` stopped
  reading a project, the variable's only remaining consumer was
  `check-framework`'s installed-command-surface half, and the name described
  nothing it did. It now sits beside `APEX_FRAMEWORK_REPO` under a comment
  stating the distinction the two names imply but never said: a framework
  **checkout** versus a project with the framework **installed**. A stale
  `HOOK_PROJECT=` on the command line is a hard `$(error)` naming the
  replacement, because silently ignoring it would gate the default `.` while
  looking like it had targeted a project.

- **fix(framework): `ape framework update` pulled release commits and left
  their tags behind.** git auto-follows tags on a plain `git fetch`, but not
  when the command line names a refspec — and both fetch sites named one
  (`git fetch origin main`). So an update took the release commit without its
  tag, `git describe --tags --exact-match HEAD` then correctly found no tag,
  `ExactTag` mapped that to the honest `("", nil)`, and the install recorded
  `version_tag: ""` about a clone that genuinely lacked the tag. Nothing
  errored at any step, which is how it survived: there was no test that
  fetched from a real remote, so no test could observe a tag failing to
  arrive.

  Reproduced on git 2.53.0, and both consequences are cosmetic — `git_hash`
  stays authoritative. `ape doctor` printed `framework <hash> installed`
  instead of the version (the `doctor_checks.go` hash fallback), and
  `TagDrift` compared a recorded `""` against a tag the user had since pulled
  by hand, so `ape framework check` reported drift permanently. An indicator
  stuck on is as uninformative as one that never fires.

  Tags are now mirrored by `MirrorTags`, deliberately **not** by adding
  `--tags` to the branch fetch. That one-flag version passes the obvious test
  and introduces a worse bug: when upstream moves a tag, `git fetch --tags`
  exits 1 with "would clobber existing tag", and `FetchAndFastForward` would
  return that before reaching the ff-merge — one retagged release upstream
  would break `ape framework update` outright. The mirror is a separate
  best-effort call, so tag trouble can never block an update, and uses
  `--force` so a moved tag is actually mirrored rather than left stale. Both
  properties are locked by tests that fail against the previous code.

  Existing projects self-heal on their next `ape framework update`.

- **feat: `make docs-cli-check` — the generated CLI reference cannot go stale
  unnoticed.** `docs/reference/cli.md` is generated from the cobra tree and
  says "do not edit by hand", but nothing could tell that the tree had moved
  underneath it: `docs-check` is a link checker, and no CI job regenerated
  it. Editing a command's `Long` text desynced the committed reference
  silently — which happened in this very changeset, to `ape doctor`. The new
  target regenerates to a temp file and diffs, printing the offending hunk
  and the one-line fix. Hermetic — no `claude`, no auth, no network — so
  unlike the `check-*` gates it runs in **GitHub CI** as well as `ci-local`.

## v0.0.64 (2026-08-31)

Three corrections to v0.0.63's closure-marker reading, found by the
framework's own review of the skill that consumes it: two wrong readings of
real data, and the shape that made getting them wrong easy. Plus the flaky
CI job, which turned out to be an earlier fix that was correct and
incomplete.

- **fix: the LAST closure marker dates the discharge, not the first.**
  Markers are appended and never edited, so a run of them is a chronology —
  `applyStatusAnnotation` already read the last one and this did not. On the
  reference ledger a record carried a disposition note saying it was **not
  yet closed**, overruled six days later by a `RESOLVED` clause that calls
  that note "now-stale"; the record was stamped with the earlier date, the
  one that denies the discharge.

- **fix: `Disposition recorded` is no longer read as a discharge.** The
  phrase asserts that a decision was written down, not that the work was
  done. Across the three field ledgers — 730 records — it occurs exactly
  once, and there it says the entry is **not yet closed**; matching it closed
  a record that denies being closed. No anchor could fix that, because the
  negation is in the prose after the token and this design does not read
  prose. `RESOLVED` is now the only in-body token. Nothing is lost: the one
  record carrying the phrase is still discharged, by the later `RESOLVED`
  clause that overrules the note.

- **feat: a supersession is its own check, because it is the opposite
  verdict.** `deferred.closure_marker_in_open_record` covered both "the work
  was done" and `[Superseded: <artifact>]`, which means the work was
  overtaken and never done. One code, two opposite remediations — `close` and
  `discard` — so the only consumer had to re-open the record body and
  re-derive the difference from prose, and got it backwards: it routed
  superseded records at `close`, which asserts a delivery that never happened
  and appends a discharge line to a body the migration wrote byte-for-byte.
  That is the defect `discarded` exists to prevent, so the distinction is now
  machine-readable as `deferred.superseded_marker_in_open_record`. `verify`
  reads structure everywhere else; this was the one place it handed prose
  back to be re-parsed.

- **fix(test): the intermittently-red `Test (ubuntu-latest)` job.** A mutex
  guards `missingCommands` because `cobra.Find` is not read-only — but
  `aboard_test.go` walked the same shared root without taking it, on the
  strength of a comment asserting that "Find only reads". A lock only works
  if every accessor takes it, so the two raced whenever they overlapped, and
  the failure named a different aboard test each run. The earlier mutex fix
  was correct and incomplete; it did not cover the accessor that believed it
  did not need covering.

  Tests now build a private tree with `newRootCmd()` instead of resolving
  against the process-wide one, which is independent by construction rather
  than by scheduling luck. `rootShell()` + `rootSubcommands()` are the one
  definition both trees come from, so a test still asserts on ape's real
  surface — verified identical command-and-flag tree before and after. The
  package failed roughly two runs in three under `-race`; it now passes 8 of
  8. The false comment is gone: leaving it is how the next reader re-derives
  the same wrong mitigation.

## v0.0.63 (2026-08-31)

Three corrections from a third field migration, verified against three real
ledgers at once — 85, 8 and 387 records. The third one is not the defect it
was reported as: the reported symptom was a missing `verify` check, and
underneath it was a migration that severed the link between an entry and
the marker discharging it.

- **fix: a record that says it is closed no longer lands in the open
  working set.** Registers close entries by appending a marker rather than
  deleting them — which is the practice this store's own rationale
  recommends — and the migration read none of them, so the better a
  register's hygiene, the more of its history arrived as live work. Two
  shapes are now recognised, and `ape deferred verify` reports either one if
  it appears later. Nothing is closed on a word: the marker must OPEN a list
  item, because on one ledger `RESOLVED` appears in 32 of 85 records while
  only 20 are closures — the other 12 are entries describing how they will
  eventually be closed.
  - **A positional marker cost the most, and looked like nothing.** Where
    the marker is appended as a sibling bullet at column 0, every one of
    them became a record of its own: one discharged entry turned into two
    open records — the entry with nothing in its body saying it was closed,
    and the closure evidence filed separately. On the largest ledger, 238
    entries became 637 records, 252 of them pure annotations, and every one
    of the 637 was open. It now reads 387 records, 56 of them discharged.
    The rule covers BOTH vocabularies. Reading only the bracketed tags —
    on the theory that indentation told the two families apart — left a
    column-0 `RESOLVED` clause splitting exactly as before, and worse: the
    marker discharged itself and moved to `closed/`, so the entry stayed
    open with the evidence no longer even beside it, and invisible to
    `verify`, whose body no longer carried a marker.
  - **The markers on one entry are a chronology, and the last one wins.**
    They are appended, never edited in place, and a run of them is absorbed
    whole — including lines that narrate a neighbouring entry. Reading the
    first `[Closed]` instead of the last tag closed four records whose own
    final word is `[Open]`.
  - **Nothing was lost, which is why nothing caught it.** The pre-write
    check compares a multiset of lines, so document order — the only thing
    linking a marker to its entry — was invisible to it. The guarantee was
    true of the text and false of the relation the text depended on. The
    marker is absorbed into the body it belonged to rather than
    reinterpreted, so it still appears exactly once.
  - `[Superseded: <artifact>]` discards rather than closes. The entry was
    overtaken, not delivered, and recording it as closed asserts work that
    never happened.
  - **History recovery keeps a recovered record's own discharge.** A
    historical copy is read by the same parser, so it can arrive already
    closed or superseded; the recovery now appends its provenance to that
    reason instead of overwriting it. Overwriting traded why a record left
    the working set for merely where it was found, and on a superseded
    record it asserted a delivery that never happened.
  - **`ingest` reads the markers too, so the invariant holds at every write
    door.** It is the one `verify` now reports as `certain`, and while
    ingest skipped the readings ape could write a record and then flag it
    itself. A bullet that arrives discharged goes to `closed/` rather than
    the working set — the open set is a directory, not a status filter, so a
    `status: closed` file written beside the open records would sit in
    `list` forever — and says so as a warning rather than re-routing in
    silence.
  - `resolved_at` on a `[Closed]` annotation comes off the winning tag's own
    line, the same rule the appended-clause form already followed. A record
    cites several dates and only one of them is when it was discharged.
    `[Superseded]` writes none: there is no discarded-at field, and a
    `resolved_at` on a discarded record would assert a delivery.

- **fix: the migration reads `story dev` headings.** Half the records on two
  of the three ledgers were filed as `source: unknown` because only
  `review` and `correct-course` were recognised. `story-dev` joins the
  vocabulary — 102 of 206 provenance headings in the field say it. It is a
  register convention operators maintain by hand, not one any skill emits,
  and filing half a ledger under `unknown` described the parser rather than
  the corpus.
  - **Ingest and migration now agree.** They shared a comment claiming they
    were comparable and two fallbacks that were not: a migrated
    `story dev of 30-4` became `unknown` while a freshly-ingested
    `apex-dev-story` became the literal string `apex-dev-story`. Both go
    through one classifier, and an unrecognised filer is `unknown` on both —
    `skill` already carries it verbatim.

- **fix: the stub no longer promises a preamble that was never written.**
  A ledger whose first line is its first heading has no preamble, so no
  `PREAMBLE.md` is written — and the signpost left behind said it was
  "preserved verbatim" there anyway. Two of the three field ledgers are that
  shape. The sentence now describes what the migration actually wrote, and
  is omitted when it wrote nothing.
  - The completion line says the same three things. `PREAMBLE.md` has two
    independent reasons to exist — prose above the first boundary, and
    headings that titled no record — so `preamble_written` alone cannot say
    what is in it, and calling an orphan-headings-only file "the ledger
    preamble" names something the ledger never had. `preamble_bytes` and
    `orphan_headings` join the payload as the two facts both surfaces read.
  - The already-discharged count is reported as "discharged", not
    "resolved": it includes the records a `[Superseded: …]` annotation
    discarded, and those were never delivered.

## v0.0.62 (2026-08-30)

Two discoverability defects from the `axon_tenax_engine` second migration.
Neither is a parser bug — the `v0.0.61` fixes held in the field, with all
118 record files reproducing byte-identical on a fresh run. Both are a
correct capability a caller could not find, and each cost real data.

- **feat: history recovery is ON by default, and reachable afterwards via
  `ape deferred recover`.** The legacy ledger's only eviction mechanism was
  deletion, so its git history holds records that exist nowhere else. The
  migration could mine them — behind an opt-in flag that the automatic path
  never passed.
  - **`ape framework update` was the lossy path.** It called `Migrate` with
    no `RecoverDeleted`, and that is how most projects migrate, because
    `framework update` runs the migration for them. The reporting project's
    ledger history holds 226 removed bullets across 178 commits, of which
    the flag recovers **180 records**.
  - **The flag was not merely opt-in, it was single-use.** `Migrate` returns
    `AlreadyDone` before reaching the recovery branch, so afterwards
    `--recover-deleted` exits 0 having done nothing, with no warning —
    indistinguishable from a run that found nothing. The one state means the
    history is still recoverable and the other does not.
  - **The default moved because the choice is not symmetric.** Recovery only
    ever writes tombstones into `closed/`, so it cannot touch the open
    working set; the cost is one `git show` per revision; the cost of
    skipping it is permanent. Opting in cost seconds. Missing it cost 180
    records, silently. `--no-recover-deleted` opts out and says the choice
    is one-way; `--recover-deleted` is still accepted, because the
    framework preflight already tells operators to pass it.
  - **`ape deferred recover`** runs the same mining against an existing
    store. It reads history THROUGH the stub now sitting at the ledger path
    — the stub is just another revision of that path — and de-duplicates
    against what is on disk rather than a fresh parse, so it is safe to
    re-run. It is the only route that does not cost every `close`,
    `discard` and repair edit made since the migration, which is what the
    documented "restore the ledger, delete the store, re-migrate" route
    throws away.
  - Recovery now reports its outcome **even at zero**. A silent step is how
    an operator concludes there was never any history to recover.

- **feat: `ape deferred discard`, and `discarded` named wherever the status
  vocabulary appears.** The store has modelled three statuses since the
  beginning — `StatusDiscarded`, `discard_reason`, `discard_evidence`,
  `verify` accepting either `resolved_by` or `discard_reason`,
  `--status closed` matching both, and `--status discarded` already working.
  The only thing a running model could read said `open|closed|all`.
  - **A capability with no command reads as a capability that is not real.**
    The discard was the one write in the whole store with nothing behind it,
    so the judgment phase took the flag help at face value, chose
    `ape deferred close` for its 20 discards, and stated the deviation
    rather than hiding it.
  - **It cost two things, and the second is the worse one.** Twenty records
    now assert `resolved_by`/`resolved_at` — the work was done — for
    findings whose whole point was that the work was never needed. And
    `close` appends a discharge marker to the BODY, so those twenty bodies
    stopped being byte-identical to what the migration wrote: the one
    property the migration's entire verification design exists to protect,
    broken by the workaround that looked most sanctioned.
  - **Nothing catches it afterwards.** `verify`'s schema check accepts
    `resolved_by` OR `discard_reason`, so a discard written as a close is
    indistinguishable from a real close by any check ape has.
  - `discard` records the two fields, moves the record to `closed/`, leaves
    the body untouched, requires `--reason` (an unexplained discard cannot
    be reviewed later), and refuses a record already closed as done rather
    than overwriting that claim.

- **test: `source_heading` is asserted against the true enclosing heading.**
  The field harness's own check, and the cheap one that catches the whole
  inherited-section-context class: locate each record's body back to its
  ledger line, walk backwards to the nearest `##`, and require
  `source_heading` to equal it. It recomputes from the SOURCE rather than
  asking the parser what it thinks, so a parser that reintroduces stale
  context agrees with its own bookkeeping and disagrees with this. Holds
  118/118 on the real corpus.

## v0.0.61 (2026-08-30)

**`ape deferred migrate` was wrong in six ways at once, and reported clean
success on all of them.** The first real-project migration
(`axon_tenax_engine`: 2,535 lines, 456,144 bytes, 124 records) produced a
store that asserted stories records never sat under, promoted headings to
records, filed the ledger's preamble as the largest open item, and stored
truncated half-sentences as authoritative fields. The project reverted the
framework update. Every finding was reproduced against the real parser
before anything was changed, and the field project's own regression suite
ships here as `internal/deferred/migrate_regression_test.go` — it failed
seven of eight at v0.0.60.

- **fix: `ParseLegacy` is heading-aware and fence-aware, not bullet-aware.**
  One structural change closes three of the six findings, because they were
  one bug. The parser consulted a single heading pattern and let every other
  `##` line fall through into text accumulation, so it had no concept of a
  heading it did not recognise.
  - **An unrecognised heading now CLEARS section context instead of leaving
    the previous one live.** Eleven records asserted a story they never sat
    under, and the inherited date is hashed into the record id — so the
    defect survived any frontmatter edit and could only be undone by
    re-running. This is the one that forced a re-run rather than a repair.
  - **A heading is never body text.** It titles the first record beneath it.
    Previously it became a record on its own when the next line was a bullet
    and merged with the content when the next line was prose, so the store
    held titles with no body *and* bodies with no title. Deterministic, but
    determined by the wrong thing.
  - **Content above the first boundary is preamble, not a record.** In the
    field corpus that was one 434-line, 37,822-byte "open item" that was
    pure document history. A ledger that opens with bullets and no heading
    still parses: the first top-level bullet is a boundary too.
  - **A fenced code block suspends all of it.** A `## build everything`
    comment inside a pasted Makefile was being consumed as a section
    heading: the line deleted, the fence split across two records with the
    opener in one and the closer in the other. Found by the framework in
    review of the first fix, and it is the worst shape of regression —
    heading-shaped lines were the losslessness check's exempt class, so
    deleting one was invisible to it. Both halves are fixed: fences suspend
    boundary detection (bullets included), and the exemption is now counted
    rather than ignored.
  - **A heading that titles no record keeps its text.** `## Still open` is
    noise and `## Deferred-at-decision: <a real title>` is not, and nothing
    here can tell them apart — so neither is deleted. They go to
    `PREAMBLE.md` with a note saying why.

- **fix: the section heading's slug and date are extracted independently.**
  `sectionRe` made them alternatives inside one lazy expression, so a
  heading carrying a prose parenthetical failed the date group, backtracked,
  and collapsed the entire remainder into the label — losing the slug too,
  with both values sitting in plain sight on the line.
  - The slug anchor is the last `of <token>` **whose token starts with a
    digit**, falling back to the first `of`. Taking the last
    unconditionally reads a slug out of ordinary prose ("of 91-2, deemed out
    of scope" -> `scope`), which is the same failure mode arriving through a
    different door: a confidently wrong value that reads as authoritative.
  - The date is the last ISO date anywhere in the heading.

- **fix: a `— defer:` value is the whole value or it is absent.** The corpus
  soft-wraps the tail, and `[^;\n]+` stopped at the wrap, so
  `outside-story=none of the 4 files are in` was stored as though it were
  the whole answer. Continuation lines are now joined onto the tail before
  matching.
  - **Only when the continuation carries field syntax.** A wrapped value and
    a continued sentence are told apart only by what the continuation says,
    and joining unconditionally corrupts the other direction —
    `owner=alice` followed by prose became
    `owner="alice continuation line for the first"`. The vocabulary is the
    six field keys and nothing else; a bare `;` was tried and rejected,
    because a semicolon is also ordinary punctuation and reintroduced the
    corruption (`"it broke; then we reverted it"` -> `owner="alice it
    broke"`). It was never load-bearing: a continuation that opens the next
    field names it.
  - This can shorten a value that wrapped with no field marker at all.
    Absence is honest, truncation is not, and `ape deferred repair` exists
    for the difference.

- **fix: `free_form: true` now means nothing on the record was
  interpreted.** `applyTail` ran over the entire body for free-form records,
  so any text containing the substring `outside-story=` — including prose
  describing something else — acquired a field, and records came out
  simultaneously flagged unparseable and carrying parsed values. The repair
  skill can now treat the body as the only evidence instead of distrusting
  half-filled fields.

- **fix: a record whose body announces its own resolution is not written
  `status: open`.** The migration stamped `StatusOpen` unconditionally with
  no inspection of the body, so a `✅ RESOLVED` banner had no path to any
  other status and landed in the live working set. It now goes to `closed/`
  with `resolved_by` naming the evidence — "resolution banner in the legacy
  ledger (migrated, not verified)", because nothing checked the claim. The
  test is narrow on purpose: blockquote-only *and* saying RESOLVED. A
  blockquote-only block is a section annotation rather than a deferred item,
  which is what makes reading its claim about itself safe.

- **feat: the verify-before-write assertion is checked against the ledger.**
  This is the finding underneath all six. `Migrate` advertised "every body
  must survive byte-for-byte, or NOTHING is written", and the only check it
  made was rendering each record and parsing it back — a serialiser round
  trip that never compared the output to the input. A completely wrong parse
  is perfectly self-consistent, which is why 124 corrupt records reported
  success. The field project found the corruption by building a line-multiset
  harness by hand, afterwards; that harness now runs in front of the write.
  - **No exempt line class.** Every significant source line must come back
    out in something the migration writes — a record body, a record's
    `source_heading`, or `PREAMBLE.md`. An exemption that is merely ignored
    is a hole the check cannot see through, which is exactly how the fenced
    `##` comment above went undetected.
  - **Every counted bucket is an output.** `Headings` was briefly counted at
    parse time and discarded at write time, which made the check prove
    something about the *parse* while reading as a claim about the
    *migration*. Caught by the framework in review.
  - Line endings are the single normalisation: the line scanner drops a
    trailing carriage return, so a CRLF ledger yields LF bodies.
    "Byte-for-byte" means byte-for-byte modulo line endings, and on this
    repo's history that distinction is worth stating rather than assuming.

- **feat: `source_heading` carries the ledger heading verbatim.**
  `source`, `source_story` and `created` are extractions, and a heading
  holds more than they take — "retroactively backfilled by Story 89.2" is
  attribution, on exactly the two-field headings the independent slug/date
  extraction exists to rescue. The asymmetry was the tell: an *unrecognised*
  heading survived as a record Title, so the better-understood heading was
  the one losing text.

- **feat: the migration refuses a populated store, and says how to
  proceed.** A wrong section date is hashed into the id, so that class of
  defect can only be fixed by re-migrating — and re-running into a store
  that still held the old records wrote the new ones alongside them and only
  then failed the post-write count assertion, leaving two migrations
  interleaved on disk. `already migrated` now names the two steps
  (restore the ledger from git, remove the store directory) rather than
  being a dead end for the operator trying to do exactly that.

- **feat: the completion output reports the relocation.** The migration
  moves cited text out of `implementation_folder`. The reporting project's
  anchor ratchet went 1039 -> 995 purely because 456 KB of cited text left
  the scanned directory, and that read as a regression the migration had
  caused. It now says how many bytes moved and that a gate scoped to the old
  folder should be re-baselined rather than chased. A reporting gap, not a
  bug — and the one that cost the most to diagnose.

- **docs: the hard-coded corpus statistics are gone.** `26 of 109 real
  records are free-form` appeared in four places including the shipped CLI
  reference, and `227 record files` in two more. The true rate was unknown
  until these fixes landed, because several causes of it were the bugs
  themselves. The property is stated instead: any real ledger has records
  that land there, and what fraction is a property of your corpus.

- **The preamble is preserved, not dropped.** "Not a record" had to mean
  "kept somewhere that is not the record set", because losing history on a
  migration is the exact failure this store exists to end. It goes to
  `{store}/PREAMBLE.md`, which the loader skips by name alongside
  `README.md`.

## v0.0.60 (2026-08-29)

Four independent items from two framework-side change requests
(`ape-contract-telemetry-and-run-hygiene` and
`ape-context-window-in-the-price-table`, both 2026-08-29). They touch
nothing of each other and nothing of the completion gates, exit codes, or
the PTY contract.

- **feat: the price table carries a context window, so an occupancy ratio
  has a single correctable denominator.** `prices.yaml` rows gain an
  optional `context_window:`, and it reaches the manifest on every step.
  The eval was maintaining a second copy of this table, and the first copy
  had already gone stale once — a 200k sonnet entry outlived a 1M window and
  inflated every affected step's occupancy fivefold.
  - **A maintained value, never a measurement.** Claude Code reports the
    real per-model window only on the stream-json result event; ape is
    PTY-only by design and does not use that surface, so the reported window
    cannot reach ape on any path ape drives. That is a consequence of the
    PTY invariant, not a deferred feature, and nothing here is labelled as
    reported.
  - **Values from the Models API (`GET /v1/models` → `max_input_tokens`),
    curated by hand and locked by a test.** Everything from Opus 4.6 and
    Sonnet 4.6 onward is **1M** at standard pricing; 200000 is the pre-4.6
    window. The first draft of these values put 200000 on the whole Opus
    family — wrong by 5x, on the most-used models, in the direction that
    inflates every ratio computed from them. What guards against a repeat is
    `TestContextWindow_GenerationBoundary`, which asserts the split model by
    model: hermetic, in `make test`, and it makes changing a window a
    deliberate edit to a test rather than a quiet edit to data.
    - A live gate against the Models API was built and then **removed**. It
      needed an API key; this project's machines authenticate through Claude
      Code, so it skipped everywhere it was meant to run — and a gate that
      always skips reads as a pass, which is the failure mode `check-hooks`
      and `check-prices` are written to avoid. Better no gate than a
      decorative one.
  - **Both manifest window fields honour the `[1m]` suffix.** The per-model
    `model_usage[].context_window` is resolved from the RAW model spelling
    the transcript recorded, before normalization folds a model and its
    context variant into one pricing bucket. The fold is right for cost and
    would be wrong for context; ape now keeps both. A bucket holding turns
    of two different sizes — a step's sub-agents running a different variant
    than the step was spawned with — records no window rather than the side
    that is wrong by 5x. Found by the eval, which measured that Claude Code
    keeps `[1m]` in the `modelUsage` key with the base name in
    `canonicalModel`; ape's own transcripts carry it in the assistant
    `model` field too, so it was ape discarding the distinction, not the
    data lacking it.
  - **The `[1m]` suffix wins, and had to.** A suffixed and unsuffixed model
    are one model at one rate — `NormalizeModel` strips the suffix on
    purpose so both attribute to a single pricing bucket — but they can be
    different amounts of context. Window resolution reads the suffix
    *before* normalizing. The gap has narrowed: `opus[1m]` was the motivating
    case when the Opus base was 200K, and now that 1M is the base the two
    agree. It still bites on the models that kept a 200K default with a 1M
    opt-in — `claude-sonnet-4-5[1m]` against a 200K base — which is what the
    test asserts on, because a test that cannot fail is worse than none.
    Consequence worth knowing: `model_usage[].context_window` is keyed by
    the attribution id and so always reports the base model, while the new
    `steps[].context_window` is resolved from the effective `--model` and
    honours the suffix. On an `opus[1m]` step the two differ by 5×, and the
    step-level one is what an occupancy ratio needs.
  - **Unknown stays unknown, with no family fallback.** A model with no
    window gets no field — not a zero, not a default. The schema supports a
    family window and the shipped table deliberately has none: a family
    price that is off lands in the right order of magnitude and is flagged
    as an estimate, whereas a family window that is off yields a
    precise-looking ratio wrong by the ratio of the windows, and the guess
    fails toward the *smaller* one — pre-loading the exact bug this retires.
  - A window travels through `ape costs update --from` like a rate, so a
    correction ships without a binary. A price-only override does not erase
    a built-in window.
  - `ape costs coverage` gains a CONTEXT column and names any observed model
    with no known window. Deliberately **not** folded into the report's
    `OK()`: `ape costs coverage --strict` is `make check-prices` and
    `ape doctor --strict` is scripted in CI, so gating on a missing window
    would have turned a reporting addition into a gate change. Verified:
    `make check-prices` still exits 0.

The three from the first request:

- **feat: the terminal-contract verdict is recorded instead of discarded.**
  Gate C already loaded the framework's per-skill pattern table and matched
  every step's closing message against it — and then printed the verdict to
  stderr and dropped it. The comment on the check said warn-only telemetry
  would establish a base rate before the exit-code path landed; that was not
  true of a number nothing recorded. Each step's manifest record now carries
  `contract: present | missing | no-transcript`, and the run's
  `checkpoints.jsonl` gets a matching `contract` row with the diagnostic.
  - **Omitted for a skill the table does not enrol**, rather than written as
    `not-enrolled`. The check short-circuits an absent table to the same
    status, so recording it would mean a project whose framework ships no
    table writes nothing while a project that enrolled one unrelated skill
    writes `not-enrolled` for everything else it runs — the same step
    carrying two different values for the same reason. Omitting gives the
    field one invariant: present exactly when the skill was enrolled.
  - **`missing` means the closing text did not match, NOT that the skill
    failed to emit.** The check reads the MAIN session's last assistant
    message, and that is a relay whenever anything below it did the work —
    which for the batch skills this table enrols is the normal case, under
    `--agent` and without it. Treating the two as one would blame a
    framework-side quality problem for a relay artifact, which is the exact
    decision warn-only exists to defer. The caveat is on the field, not just
    in the release note.
  - Additive under `schema_version: 2` — verified rather than assumed, so
    nobody bumps the version defensively: the eval builds its step record
    from explicit key lookups and drops what it does not name, and nothing
    in ape decodes a manifest with `KnownFields(true)`. A version bump is
    what would break the eval's `[1,2]` reader range; a new field is not.

- **feat: `ape doctor` reports whether ape's run subtree is gitignored.**
  New `output.ape_ignored` row. Everything ape writes lands under the
  resolved `{output_folder}/ape/` and is rewritten on every run; untracked
  it waits in `git status` for a `git add -A`, and tracked it dirties the
  tree on every run and fails the next `ape pipeline`'s dirty-tree
  pre-flight. The framework already requires the output folder to be
  gitignored and nothing installed the rule, so a project could be years
  into violating it with no line of output saying so.
  - **Reports, never writes**, matching `sprint.lock_ignored` — `.gitignore`
    is the operator's file. An earlier draft had `ape framework update`
    append the line; it was dropped for two reasons that only showed up on
    inspection. An ignore line does not untrack anything, so on the projects
    most in need of the fix it would have written a line, changed nothing,
    and reported success. And `.gitignore` is captured into the eval's
    fixture overlays, whose `_output` stripper matches the literal line
    only — so the write would have propagated into 20+ committed fixtures.
  - Separates **COMMITTED** from merely unignored and offers
    `git rm -r --cached` for the first, because that is the case where the
    obvious advice cannot work on its own.
  - Resolves `output_folder` rather than assuming `_output`, and reports
    nothing when it points outside the repository.
  - `dirtyTreeGate`'s error text hardcoded `_output/` and now names the
    resolved path — on a renamed project it was pointing at a directory that
    does not exist, at the one moment the operator needed the right one.

- **feat: `SessionStart` and `PreCompact` are delivered into
  `hook-events.jsonl`.** Two entries in the inline `--settings` hook map,
  both async. Recorded and read by nothing: neither carries a field a
  completion gate depends on, which is what lets both be async and what
  leaves `internal/hookdrift` untouched — it watches gated FIELDS keyed to
  the event carrying them, so an event with no gated field has no drift to
  detect and adds no observation. That matters beyond tidiness:
  `make check-hooks` runs `ape doctor --only hooks.contract_drift --strict`,
  where a WARN is exit 1, so a fourth observation added by reflex would
  break a Make gate rather than print a stray line.
  - **`SessionStart` fires roughly once per STEP, not once per spawn.** Its
    `source` is one of `startup` / `resume` / `clear` / `compact`, and the
    runner sends `/clear` between steps within a stage. Read `source` before
    counting these as sessions. Those rows also carry an empty `step`,
    because `/clear` is sent before the next step's contract is registered.
    `PreCompact` fires mid-step and attributes normally.
  - The settings blob went from 876 to 1164 bytes, so the `<1 KB` canary
    moves to 2 KB. It was never an argv limit — that is 128 KB on Linux —
    but each registered event costs a hook subprocess per occurrence, and
    the size is the cheap proxy for the cost that matters.

## v0.0.59 (2026-08-29)

- **fix: `ape` shuts down on a signal instead of dying where it stands.** Ctrl-C
  and SIGTERM now cancel the command's context; before, no signal handler was
  installed anywhere in `ape` and the process was simply terminated.
  - **What it broke, and had been breaking since v0.0.55: `ape aboard serve`
    never cleaned up.** aboard installs its own signal handler inside its
    `cli.Execute`, and the whole point of the mount is that ape does not call
    that — it adds aboard's command tree to its own. So the board ran under a
    context nothing ever cancelled, its shutdown path never ran, and **every
    stopped board left `.aboard/run/instance.json` behind.**
  - A stale record is what makes tooling believe a dead board is alive. It cost
    an afternoon of the VS Code extension showing no *Start the Board* button,
    diagnosed at the time as a crashed server — because a graceful stop does
    clean up, which is true of the standalone `aboard` binary and was never true
    of an ape-hosted one. Found by stopping a board and noticing the record
    survive; confirmed by an A/B against the standalone binary, which cleans up
    on the same signal.
  - **At the root, not on the board subtree.** Nothing about it is
    aboard-specific: every long-running command here — `chat`, `pipeline`,
    `sandbox exec` — was being killed outright rather than asked to stop.
  - **A second signal still kills.** `signal.NotifyContext` keeps swallowing
    signals until its stop function runs, so the handler releases itself the
    moment the context is cancelled. Without that, a command that ignores its
    context would have traded a process that dies too eagerly for one that
    cannot be stopped at all.
  - Verified live, which is the only place this is observable: `ape aboard
    serve` stopped with SIGTERM and with SIGINT now leaves an empty run
    directory, matching the standalone binary.

## v0.0.58 (2026-08-29)

- **feat: `ape sprint reconcile` refreshes a `Sprint` tab on the project's
  board.** During a long or autonomous APEX run there was no way to see where
  the work was without interrupting it. Reconcile already runs at every
  boundary that moves a story, which makes it the thing that OBSERVES tracker
  changes rather than the thing that causes them — so the refresh lives there
  and nothing in the framework has to know a tab exists.
  - The tab is `ui`, found by **key** (`apex-sprint`) on every refresh — never
    by name, which the human may change, and never by index. Story counts by
    status, epic counts by **derived** status, what is in flight, and what is
    blocked. `blocked` and `cancelled` get their own cells rather than folding
    into a total: a sprint of nothing but blocked work must not read as healthy.
  - **It cannot affect the command.** A board that is absent, stopped, hung,
    mid-restart or serving a malformed document changes neither the exit code
    nor a byte of stdout, and even a panic below it is contained. This is the
    whole risk of the feature rather than a nicety: reconcile sits on mutation
    paths inside `apex-review-story`, `apex-code-review` and
    `apex-epic-batch-review` where a non-zero exit is read as a CONTENT
    VERDICT — it converts a defer into a patch, raises `unfixed_patches` and
    demotes the story. A broken board would otherwise corrupt review outcomes
    on projects that happen to have one and not on projects that do not. The
    test asserts stdout EQUALITY against a run with no board at all. An
    unreadable document is reported on stderr and left exactly as it was.
  - **Two write paths, chosen by whether a board answers.** A listening board is
    POSTed to: that is the only way a page already showing the board is pushed
    the change — the server emits SSE frames from its own write path and never
    watches the file — and it gets the write a real compare-and-set. Verified
    live against a client on `/events`, which received
    `{"checked":["ab1"],"origin":"apply"}`, the frame that triggers a reload.
    When nothing is listening the file is written directly, so the tab is
    already current when somebody starts a board rather than filling in at the
    next story boundary; that document is stamped the way the server stamps its
    own (`rev` advanced, `updatedAt`, `version`, `lastEditedBy`) and written
    through a temp file and a rename, so a reader sees the whole old document
    or the whole new one and never a torn read.
  - **The fallback is entered only when nothing is listening.** A server that is
    there and refuses — a 409, a timeout, an HTTP error — stops the refresh
    instead of writing around it, because a 409 is another writer's work and
    going behind a live server is the one move here that can destroy it. Bounded by a 2s timeout. Written through
    the server's compare-and-set, never straight to the state file, so a
    concurrent change from the browser is refused rather than clobbered.
    Skipped on `--check`, and it never creates a board.
  - **The derivation is a library function** (`sprint.Summarize`), not part of
    the board-writing path, so a second consumer — a TUI, a status line — reuses
    the numbers rather than reimplementing arithmetic that could then disagree.
  - **Two departures from the request, both because the board says so.** It
    asks for `state.readOnly` and `state.heartbeat` on a `ui` tab; `ui` declares
    neither (they belong to `kanban`, and `readOnly` to `table`), and
    `aboard apply` reports both as "stored and ignored" on every write. A
    freshness strip that renders nothing fails the requirement it exists for, so
    the write time is rendered **in the tab** instead. `readOnly` is moot: the
    tree carries no `field` and no `button`. Also, `reopened_by` lives in story
    frontmatter and parked patches in the deferred store — neither is in
    `sprint-status.yaml`, and reading the corpus on a command called many times
    per run is exactly the cost this design avoids — so Attention reports
    blocked work and says on the tab what it is not counting.

- **feat: `ape framework setup|update` create the project's board.** A project
  with the framework installed now has `.aboard/` already, so there is nothing
  to initialise before `ape aboard serve` — `ape aboard init` is left for
  projects without the framework and for second boards (`--name`).
  - **`.aboard/.gitignore` is seeded beside it**, ignoring everything except
    itself, so the DIRECTORY is committed and none of its CONTENTS ever are.
    This is deliberately not the `.aboard/` line in the repo-root `.gitignore`
    that aboard's own docs suggest: ignoring the directory would ignore that
    file too, so the board's home would be missing on a fresh clone and the
    next person would have to know to run `init`. Byte-identical to this repo's
    own `_apex/.gitignore`, which solves the same problem the same way.
  - **An existing board is never overwritten.** `aboard.Init` refuses to
    replace a document — the one mistake here with no undo — and the installer
    treats that refusal as "already done" rather than raising it.
  - **A board root ABOVE the project is reported, not nested under.** aboard
    refuses a root inside a root, because the inner board would be invisible to
    every command run from the outer one. Asking `FindRoot` first means that
    case never surfaces as an error that would fail the whole install.
  - The ignore file is **seeded, not refreshed**, like `_apex/config.yaml`: a
    project that edited it meant to.
  - The invocation string the board uses in its own messages now exists in two
    packages, because `apecmd` already imports `framework` and the dependency
    cannot run the other way. `TestAboardInvocationMatchesTheMount` holds them
    to one value — a drifted copy would tell somebody to run a command that
    does not exist.

## v0.0.57 (2026-08-29)

Recipes reach projects, and a `ui` write that was legal-but-dead now warns at the
write instead of on someone's screen.

- **feat: `ape framework setup|update` installs the framework's `ape aboard`
  recipe library** into `<project>/_apex/aboard/recipes/`. A recipe is a short
  markdown method for one board move, written for an agent to follow — prose
  plus, usually, a tab skeleton. Built-in recipes already travel inside the ape
  binary; this is how a framework ships the curated ones on top.
  - **Refreshed, not synced.** Framework-named files are overwritten; every other
    file in that directory is left alone. That differs from skills, which are
    wiped by prefix, and the difference is whose directory it is: aboard
    documents `_apex/aboard/recipes/` as the workspace-wide location, so a sync
    would delete recipes ape never installed.
  - **An empty library installs nothing and leaves no directory.** aboard reports
    a recipe's SCOPE by the directory it came from, so an `_apex/aboard/recipes/`
    that exists and holds nothing reads as a library somebody emptied rather than
    one never installed.
  - **A framework that ships no library is version skew, not a failure** — the
    same terms as the operating-rules fragment, the terminal-contracts table and
    the ape-commands manifest. Every built-in still reaches the project inside
    the binary, so nothing is lost.
  - Only top-level `.md`: a recipe is one flat file with frontmatter, and the
    directory is a library rather than a tree.
  - **Not a place to copy a BUILT-IN into.** `_apex/aboard/recipes/` is the
    highest-precedence of aboard's four recipe directories, so a built-in copied
    there shadows the binary's own copy and freezes it at install time — the
    recipe then silently stops tracking the renderer it describes, which is the
    drift the capsHash beacon exists to catch, reintroduced by hand. Documented
    where somebody about to do it will read it.

- **chore(deps): aboard v0.1.3** — `apply --check` now reads the VALUE of a
  layout prop, not only its name. `gap` reaches the stylesheet as-is, so a size
  token (`"lg"`) is not CSS: the substitution is invalid, the declaration becomes
  guaranteed-invalid, and a flex row closes to zero — four stats rendered as one
  run-together string while `apply` printed `applied` and exited 0. A bare number
  is the same defect (`12` has no unit). `grid.columns` silently falls back to 2
  and clamps at 6; `spacer.size` fails like `gap`. All three warn now. Found by
  writing a tab in this repo, applying it clean, and looking at a screenshot.

- **docs: recipes, as a concept rather than a catalogue.** A new section in
  [use the board](docs/how-to/use-the-board.md#recipes): what a recipe is, the
  four directories and their precedence, the frontmatter schema and the
  `aboard-template` fence, and the three commands. It deliberately enumerates no
  recipes — the set depends on what the binary carries and what a project's own
  directories add, so `ape aboard recipes list` is the only answer that cannot be
  wrong for somebody. The install rows are in the setup and update how-tos.

## v0.0.56 (2026-08-29)

The board, used in anger for the first time, and everything that fell out of
doing it. v0.0.55 mounted `ape aboard`; this release is what a session driving
that mount actually found — a shipped crash, a missing doctor probe, and a skill
that told an agent to wait without saying what to wait for.

- **feat: `ape doctor` gains `aboard.skill_reference`** — a WARN when a
  `.claude/skills/aboard/` reference copied into the project was generated for a
  different `capsHash` than this binary serves. The renderers are compiled into
  ape and the skill is a COPY, so the two drift independently; an agent reading
  a stale one can write state no renderer reads, and `apply` still prints
  `applied` and exits 0, which is why nothing else catches it. A project that
  never copied the skill reports SKIP — absence is not drift, and most projects
  are that project.
  - **Deliberately NOT a "does ape provide `ape aboard`" check.** The tree is
    compiled in, so such a check could only assert that this binary contains a
    package it demonstrably contains. Command presence is the other list's job:
    put `aboard` in the framework's `required_commands` and the existing
    `framework.command_surface` covers it, at the cost of hard-failing every ape
    older than v0.0.55. Both were weighed; `ape aboard` stays out of
    `_apex/ape-commands.yaml` for now, because the `sanctioned` list governs a
    framework skill step shelling out and no framework skill does.
  - The verdict is delegated to `aboard.Status` rather than re-parsed here. The
    stamped-hash read and the manifest hash belong to the library, and a second
    implementation in ape would be free to disagree with the board it serves.

- **fix: `ape aboard status` no longer panics on a malformed skill reference**
  (via aboard v0.1.2, pinned here). `stampedHash` sliced
  `strings.Split(body, "\n")[:6]` on a file that might have fewer than six
  lines, and indexed `strings.Fields(after)[0]` on a line reading `capsHash:`
  with nothing after it. v0.0.55 shipped both: a two-line
  `reference.generated.md` killed the command with
  `panic: slice bounds out of range [:6] with capacity 3`. The file is somebody's
  copy — truncated, hand-edited or half-written by an interrupted redirect are
  ordinary states — and the commands it took down are `status` and `doctor`, the
  two run precisely when something is already wrong. Found by adding the check
  above, which widened the reach of a crash that was already released. `capsHash`
  is unchanged at `207b5d93`, so no copied reference goes stale over it.

- **feat(skill): the aboard skill, rewritten for `ape aboard`** — copied from the
  aboard repository with all 179 CLI invocations across six files renamed to the
  command an agent in this repo actually has. Three classes deliberately left
  alone: `/aboard --<recipe>` (the slash command, not a CLI call), `.aboard/`
  paths (identical under both hosts), and the `aboard-template` fence tag.

- **docs(skill): wait for the nudge, not for the first click.** The session
  driving all of the above used `--for change` and paid for it twice: every edit
  is a write, so a human working through a four-field form saves once per field
  and `change` fires on the FIRST one. The agent woke holding one answer and
  three defaults, read the defaults as a decision, and wrote back under someone
  still reading. The skill showed `wait` without saying which predicate to use,
  so the more attentive-looking one won. It now says `poke` is the default
  because it is the only signal meaning *I have finished*, that waking to half a
  form means the wrong predicate rather than a reason to react, and that
  `change` belongs to a board another AGENT is driving. Also: tell the human the
  button is what releases you.

## v0.0.55 (2026-08-28)

- **feat: `ape aboard` — the board, mounted** — `github.com/exoport/aboard`
  v0.1.0 serves a browser UI for a project whose state a human and one or more
  agent sessions read and write. It is a library by construction (no
  package-level cobra state, no `init()` registration, no `os.Args` reads, no
  `os.Exit` outside its own `Execute`), so ape *mounts* the whole tree with one
  `AddCommand` rather than porting it. Both hosts resolve the same `.aboard/`
  per project, derive the same port from it, and write the same state file: a
  board started by `ape aboard serve` is the board a bare `aboard status`
  reports, and either binary can drive it.
  - **Exactly one string differs.** `app`/`host` in `/health` and
    `.aboard/run/instance.json` become `ape-aboard`, so a message can name the
    command the reader has. The capability manifest must NOT differ —
    `aboard.AppName` describes the board, not the process serving it — and
    `TestAboardCapabilitiesAreHostIndependent` asserts full-output equality
    rather than just `capsHash`, since a change that altered a declared command
    but left the hash stale would pass a hash check and still be the drift.
    Verified live: `207b5d93` under both binaries, manifests byte-identical.
  - **aboard's exit table survives ape's mapping.** It declares 0/1/2/3 (2 =
    usage, 3 = `wait` timed out, both documented and scripted against), and
    ape's `ExitCode` recognised none of them — it matches ape's own
    `*exitError` and otherwise returns 1, so `ape aboard export` and a timed-out
    `ape aboard wait` would both have exited 1. `ExitCode` now offers errors it
    does not own to aboard's table first. Verified end to end: exit 2 on a usage
    error, exit 3 on a `wait` that timed out against a live board.
  - **The board does not inherit ape's update check.** Cobra runs the closest
    `PersistentPreRun` walking up from the executed command and aboard's tree
    defines none, so every board command would have run ape's — which can print
    `update available: … run 'ape update'` onto stderr mid-board-output, and
    fires as a goroutine, so whether it appeared at all depended on a race with
    process exit. The mount shadows it with a no-op. The board is reachable from
    the standalone binary too and its output must not differ by host.
  - **Version reporting needs nothing from ape.** aboard resolves its own
    version from `info.Deps` when it is not the main module, so `ape aboard
    version` reports `0.1.1` rather than ape's tag. There is no field to pass
    and none is wanted: a bug report carrying the host's version would name the
    wrong project.
  - **The VS Code extension works against a board ape started**, and knows to
    prefer this host: aboard-vscode **v0.1.2**'s *Start a board* button runs
    `ape aboard serve` in a project that has an `_apex/` directory, and probes
    `ape aboard --version` first so an ape older than this release — which has
    no `aboard` subcommand at all — is never offered. Confirmed by driving the
    extension's compiled discovery against a live `ape aboard serve` board:
    `acceptHealth` accepts the real `app: "ape-aboard"` payload, and every
    route it uses (`/health`, `/aboard.json`, `/capabilities`, `/events`,
    `/waiters`, `POST /poke`, `POST /aboard.json`) answers.
  - New how-to: [use the board](docs/how-to/use-the-board.md), plus a README
    section. The generated CLI reference grows by the mounted tree.
  - **The invocation strings are fixed too, in aboard v0.1.1** (released the
    same day, and this dependency is pinned to it). Under `ape aboard` the board
    used to print "run `aboard init`" — a command this user does not have —
    beneath a cobra `Usage:` line that was always correct. `Options.Argv0` now
    reaches message text through an `aboard.Invocation`. The recorded count was
    low twice over: 55 + 12 became 71 sites, because the measuring grep only
    matched double-quoted literals and never saw the backtick `Long:` help. 16
    sites deliberately stay literal — the generated artifacts, the README written
    into `.aboard/recipes/`, the declared table that feeds `capsHash`, and
    `boards`' help, which names both spellings on purpose. Verified here:
    `ape aboard status` in a directory with no board now says `ape aboard init`,
    and `capsHash` is still `207b5d93` against the standalone binary.

- **test: retire the eight `TestParity_*` gates, porting the one case they
  alone asserted** — they ran the ten retired framework Python scripts side by
  side with the commands replacing them, which made them a *differential* gate:
  their value was catching divergences nobody had transcribed wrongly on
  purpose, and it was spent at migration. Framework v0.11.0 deleted the
  scripts, so against any supported framework they could only skip — eight
  SKIP lines in the output of `make check-framework`, a gate whose whole
  discipline is "a skip is not a pass". A test that can never fail again is
  not a gate, and skip noise taxes exactly the careful reading that gate asks
  for. The behaviours themselves stay covered natively:
  `TestStoryVerifyFile_ExitCodesTravelAsErrors`,
  `TestSprintVerify_ExitCodesTravelAsErrors` (all five codes),
  `internal/sprint`'s reconcile suite, `internal/apexdoc`, and
  `TestProject_CRLFAndBOM` / `TestSplit_BOM` for the BOM asymmetry.
  - **One case was NOT covered elsewhere, and is now.** The parity gate
    recorded that ape is deliberately *stricter* than the Python on a
    backwards write: an unquoted 14-digit `updated_at` is valid YAML for an
    int, so `verify-sprint-status-row.py`'s `str` guard skipped the comparison
    and reported OK on a real regression, while ape compares the rendered
    value and returns exit 5. The native test only used quoted timestamps, so
    that asymmetry was asserted nowhere that ever ran — nothing stopped the
    render-then-compare being "simplified" back into the guard that had the
    bug. Now `internal/sprint`'s
    `TestVerifyRow_BackwardsWriteAgainstUnquotedCommittedValue`, verified to
    fail (exit 0 instead of 5) when the type guard is reinstated.

## v0.0.54 (2026-08-28)

- **fix(hookdrift): every run kind now stamps the Claude Code that produced
  it** — the harness version was read only from the pipeline manifest, which
  `ape prompt` and `ape chat` do not write, so their runs carried no version
  at all. `hookdrift` scopes its verdict to one Claude Code version and picks
  that version from the *newest* run in the window, so a single recent prompt
  run set the judged version to the unstamped bucket and pushed every
  properly-stamped pipeline run in the window into `Ignored` — the verdict was
  then computed from the unstamped runs alone, where pre- and post-upgrade runs
  fuse together. That fusion is exactly the masking version scoping exists to
  prevent, and it was reachable on any project driven mainly by `ape prompt`.
  The stamp now lives in `runlog.Writer`, which every producer opens (it is what
  creates `hook-events.jsonl`), written to a new per-run `harness.yaml` — so a
  run kind that does not exist yet cannot forget it, the same reasoning that
  makes `RunRoots` the single list of run trees. `hookdrift` reads that file
  first and keeps `manifest.yaml` as a fallback, because every runlog already on
  disk predates the stamp and is the whole corpus for a project with history.
  Verified end-to-end against Claude Code 2.1.251: `make check-hooks` on a
  prompt-only project now reports `background_tasks 1/1, tool_response 1/1,
  agent_id 1/1 (Claude Code 2.1.251)` where it previously named no version.
- **fix(hookdrift): the unjudged-runs count no longer reports "0 other
  version(s)"** — the summary derived the count from `len(Versions)-1`, but
  `Versions` holds only *stamped* versions, so ignored unstamped runs were
  counted as zero versions. Distinct ignored versions are now counted directly,
  with the unstamped bucket counting as one. Reachable whenever a window
  straddles an upgrade, which is when the line is read.
- **chore(runlog): drop the dead `WriteSessionYAML`** — `session.yaml` was
  never written; the function had no callers anywhere, tests included. The one
  field that mattered is now covered for every run kind by `harness.yaml`.

## v0.0.53 (2026-08-28)

- **feat: the project-data commands the framework skills call (PLAN-25)** —
  33 new subcommands under nine new groups, five new packages, and ten of the
  framework's seventeen Python scripts replaced. The headline is that `ape`
  could not previously see a project's artifacts at all: no `.go` file read
  `_apex/config.yaml`'s folder variables, so `ape adr list` reported "no ADR
  directory found" against a project with 64 ADRs on disk.
  - **`ape config resolve`** resolves the seventeen framework variables, the
    `config.local.yaml` overlay (key-wise), the four `ext_*` flags and the
    absolute paths they denote. Everything else routes through it, including
    `findADRDir`, `findPatternsDir`, `adr new` and `ape bootstrap` — which had
    a live bug of its own: a record's catalog-relative `../adrs/<file>` joined
    onto `--out` wrote *outside* `--out`. A malformed `config.local.yaml` is
    now a loud exit 2, not a silent fall-back to base values.
  - **`ape adr|pattern|feature|capability verify|sync|update`** plus
    `ape registry verify|sync` as the fan-out. Exactly four checks: set
    equality both ways, `file:` resolution, duplicate ids, and whether a
    record parses at all. It replaces `runMarkdownDirValidate`, which printed
    "OK:" for every `.md` without ever calling `os.Open`. Each family answers
    to its plural. `ape sync adrs` becomes `ape adr sync`; the verb-first
    spelling and `validate` survive one release as hidden pointers.
  - **`ape story fields|verify`** — frontmatter projection that reads at most
    8 KiB per file and never opens a body, and a verifier with three check
    classes and no enum checks. `ape sprint check|verify|reconcile` —
    divergence reporting that never picks a winner, a row gate with five
    preserved exit codes, and the epic projection with a targeted two-line
    write and a real advisory lock on POSIX *and* Windows.
  - **`ape memory index|show|check`** — `team-memory.md` is 431,950 bytes on
    the reference project, past the 256 KiB Read cap, so the retrospective
    that writes it can no longer read it. `check` stats rather than reads, and
    exits 0 even over the hard ceiling: a failing exit would abort the
    retrospective at exactly the moment compaction is due. `ape doctor` fails
    on that state instead.
  - **`ape deferred`** — a one-file-per-record store replacing a
    456,144-byte ledger whose only eviction mechanism was deletion. `ingest`
    cannot fail for a content reason (a non-zero exit there demotes a story
    two skills away), one malformed record loses exactly that record, and
    `close` moves to `closed/` rather than deleting. `migrate` verifies
    before it writes, is idempotent from disk state, and never deletes the
    source; `repair` dispatches the judgment phase on opus through the
    existing task runner, refuses without a TTY, and asserts no record was
    lost.
  - **`ape doc verify|shard|assemble|analyze`** — markdown sharding with the
    `index.md` contract its caller verifies, and a byte-identical round trip.
  - **`ape framework update`** gains `--dry-run`, `--no-migrate` and
    `--repair`, and runs pending project-data migrations. It still commits
    nothing: the result sits in the working tree and the run prints the
    `git add` line. Its clean gate is scoped to the migration's own paths,
    which are disjoint from the install's — so the two are order-independent.
  - **`ape doctor`** gains six project-data rows. `config.resolved` and
    `memory.size` are the only required ones, because a non-required FAIL is
    downgraded to WARN and those two are the states that must not be
    ignorable.
  - Gate commands now return their exit code as an error through the existing
    `ExitCode` path instead of calling `os.Exit` inside `RunE`, which is what
    makes them testable at all. `gen-docs` now emits cobra aliases, which it
    never did.

- **fix: the project-data commands were wrong on real projects, and the tests
  agreed with them** — a post-implementation review of PLAN-25 against real
  project shapes and against the Python scripts being retired. Every fixture
  in the original tests was written alongside the code, so it agreed with the
  code rather than with the framework.
  - **`ape sprint check` joined on the wrong key.** A tracker rows on the
    STORY KEY — the story file's stem — while a story's frontmatter carries a
    separate, dotted `story_id` (`1-1_greet-a-name` vs `"1.1"`). Matching
    rows against `story_id` matched nothing: on a real project it reported
    every row as having no story file and every story as having no row — 24
    findings, all false, and it could never surface the status divergence it
    exists to find. `ape doctor`'s `sprint.divergence` row WARNed on every
    healthy project as a result.
  - **`ape registry verify` demanded a `file:` from every capability.**
    `capability-index-schema.json` defines no `file` property — a capability
    record is located by id + slug — so the check emitted one
    `registry.file_unresolved` per entry on every project with the extension
    on, and no `sync` could ever clear it. `sync` no longer writes a `file:`
    into a capability entry either, which would have produced an index the
    framework's own schema rejects.
  - **`ape sprint reconcile --file <path>` silently stopped stamping.** The
    timestamp was resolved only on the branch that had no `--file`, so the
    form the framework's call sites use left `updated_at` untouched on
    mutation. `--file` also keeps working outside a project, as the script it
    replaces does.
  - **`ape story verify` accepted an empty required key.** `story_id: ""`
    passed, where `verify-story-frontmatter.py` exits 3 — exactly the
    half-completed write a post-write gate exists to catch. An empty
    frontmatter block is now a parse failure (2), matching the Python.
  - **`ape sprint verify` returned four wrong exit codes.** An empty or
    comment-only tracker gave 3 where the Python gives 2; a
    `development_status` that is present but not a mapping gave 2 where it
    gives 3; and the exit-5 backwards-write comparison ran on values that
    are not 14-digit stamps, so a malformed `updated_at` on either side
    produced a spurious 5 — whose documented repair is "re-write updated_at
    as the value this reports", i.e. a instruction to write the malformed
    value into the tracker. It still catches one real backwards write the
    Python misses, where the committed value is unquoted.
  - **`ape sprint reconcile` now verifies its write and restores on
    failure.** The line-level edit that preserves comments is also the one
    that can cross a line boundary; the script it replaces re-reads, asserts
    every untouched row is unchanged, and puts the original bytes back if
    not. That guard was lost in the port.
  - **`ape sandbox capacity` reported an unreadable `/proc/meminfo` as a box
    with no RAM.** PLAN-25 generalised `humanBytes` — which `ape sandbox
    capacity` owns — so a 0-byte `team-memory.md` would render as `0 B`, and
    that turned the capacity table's "not reported" dash into `0 B total,
    0 B available`, three lines above a verdict that carefully distinguishes
    the two. Capacity readings render through their own helper again.
  - **New: `testdata/apexproject`**, a committed project fixture in the
    shapes a real one has, and `TestContract_*` in `internal/apecmd` — the
    framework usage contract as tests: config-variable parity with the
    framework's own template, the install/migration disjointness derived from
    the framework package's constants rather than a copied list, a lock test
    proving `ape framework update` writes no commit, and the sandbox
    framework-delivery handoff (`/opt/apex-framework`, its reserved status,
    and the `--no-fetch --repo` pair the docs tell operators to type).
  - **New: `TestParity_*`**, the retirement gate PLAN-25 D15 promised. It runs
    the real Python scripts out of a framework checkout beside the commands
    that replace them and compares exit codes, bytes and the JSON fields the
    calling skills read. Opt-in via `APEX_FRAMEWORK_REPO`, since CI has no
    sibling checkout.

- **fix: act on the framework's PLAN-25 defect report** — five findings handed
  over from the framework repo's own call-site audit. Its Finding 1 was the
  `sprint check` correlation bug above, found independently on both sides.
  - **Empty JSON arrays marshal as `[]`, never `null`.** Six payloads emitted
    `null` on a clean run — `findings` on `story verify`, `sprint check`,
    `deferred verify` and `registry verify`, and `changes` on
    `sprint reconcile` and `registry sync`. The deferred-repair skill is explicitly
    told to take its work list from `ape deferred verify --output-format json`
    rather than globbing, so a clean store handed it a `null` where it expects
    a list.
  - **Each record family's `update` help shows its own example.** All four
    printed `ape adr update` with an `ADR-0001` id, because the families are
    one descriptor-driven code path — so `ape feature update --help` told the
    reader to run the wrong command with the wrong id shape against the one
    index whose layout actually differs. Now built from the descriptor
    (`PAT-0001`, `FEAT-1-1`, `CAP-1`).
  - **The five commands without `--cwd` now say why.** `sprint verify` and the
    four `doc` verbs resolve nothing from the project — every input is an
    explicit path — so there is no project for `--cwd` to select. That is a
    rule, not an oversight; it was just never stated. Decision 1 amended to
    match.
  - **The memory index size field stays `bytes`,** and ape's own prose stops
    calling it `size`. It is what `os.Stat` returns, what the 256 KiB Read cap
    is measured in, and what `ape memory check` already emits — a `size` in
    one and a `bytes` in the other would be worse than either.

- **fix(deferred): follow the framework's `apex-defer-repair` →
  `apex-deferred-repair` rename, without a lockstep release** — the skill
  name was a hardcoded constant, so the rename would have broken
  `ape deferred repair` until ape shipped too. It now resolves against what
  is actually installed, preferring the current name and accepting the
  pre-rename one, which removes the ordering constraint rather than
  documenting it: a hard switch would break a new ape against an older
  framework *and* an older ape against the renamed framework, so the two
  repos would have had to ship in the same hour.
  - **And it refuses when neither name resolves, `--dry-run` included.** A
    real dispatch was already safe — `runTask` builds a single-step spec and
    `pipeline.Run` calls `PreflightSkills`, so an unresolvable skill exits 2
    without reaching claude. A dry run never reaches the runner, though, so
    it would have printed a plan naming a skill that does not exist, said
    "no session spawned", and read as clean, with the real run failing
    later. The check also picks between the two spellings, names both in its
    message, and fires before the TTY refusal so an operator is not told to
    pass `--force` first and find out afterwards.

- **feat(doctor): report a `sprint-status.yaml.lock` that git is not
  ignoring** — new `sprint.lock_ignored` row. `ape sprint reconcile` takes an
  advisory lock on that sidecar and never unlinks it: releasing a lock and
  deleting the file are different acts, and deleting one another process may
  be waiting on is how the mutual exclusion is lost. So the file stays, the
  framework reconciles at six boundaries, and an untracked runtime artifact
  sits beside the tracker waiting to be swept up by a `git add -A`. That is
  not hypothetical — it is how one reached a commit in *this* repository
  while the check was being written. `reconcile-epic-status.py` leaves the
  same file, so it is not a regression ape introduced; it is a hygiene
  problem neither side had noticed. WARN, never a write: `.gitignore` is the
  operator's file. Already-committed is reported as the worse state it is,
  with `git rm --cached` as the fix, because ignoring a tracked file changes
  nothing. The check asks `git check-ignore` rather than matching patterns
  against `.gitignore` by hand — nested ignore files, `.git/info/exclude`,
  `core.excludesFile` and later negations all decide this, and only git
  agrees with what git will do. `sprint.LockPath` is now one definition
  shared by the locker, the check and the remediation text.
  - **`ape framework setup` and `ape framework update` fix it, not just
    report it.** A doctor row that tells every operator to add the same line
    by hand scales badly, so setup writes the entry — a project is born
    ignoring the artifact — and update is the verify-and-fix pass for
    projects that predate this. That is a deliberate widening of what those
    verbs write into a user's tree, defensible where the doctor row is not:
    they are explicitly-invoked write commands that already install skills,
    pipelines and a `CLAUDE.md` managed block, while `ape doctor` stays
    diagnostic.
  - **The entry is `sprint-status.yaml.lock`, never `*.lock`.** A blanket
    `*.lock` is fine in ape's own repository and destructive in a user's:
    `Cargo.lock`, `flake.lock`, `Gemfile.lock`, `poetry.lock` and
    `composer.lock` all match it and all belong in history. A test asserts
    each of those five stays un-ignored. Ape's own `.gitignore` was narrowed
    to the same pattern.
  - It **appends and never manages a region** — an ignore file's job is to be
    hand-curated, and owning part of it to hold one line is the wrong trade —
    and it **asks git before writing**, so a project that already ignores the
    sidecar by its own rule, a nested `.gitignore`, `.git/info/exclude` or
    `core.excludesFile` collects no redundant entry.

- **feat(sprint): report a tracker row key the two epic-projection
  implementations count differently** — new `sprint.nonstandard_row_key`
  finding. A story row keyed `2-1`, with no separator and slug, is wrong
  twice: it names no story file (`apex-create-story` writes
  `{story_key}.md`, and a story key carries a slug), and
  `reconcile-epic-status.py` does not count it toward its epic while
  `ape sprint reconcile` does. Both readings are defensible; they are not the
  same, so until the Python is retired the same tracker reconciles
  differently depending on which one ran, and neither one's output would tell
  you. Now: `ape sprint check` reports it as its own finding with the fix in
  the message (not as a missing story file, which would send a reader off to
  create the wrong thing), `ape doctor` surfaces it through
  `sprint.divergence`, and `ape sprint reconcile` names the rows on every run
  that reads one — including a no-op, because the divergence is in what was
  counted, not in what was written — and carries them as `bare_row_keys` plus
  a `bare_row_key_remediation` string in its JSON.
  - **The finding names the actual rename.** When exactly one story file
    matches the row's ordinal prefix (`7-3` and `7-3_payment-retry.md`),
    `ape sprint check` says `rename the row \`7-3\` to \`7-3_payment-retry\``
    rather than quoting a generic example, and suppresses the
    `story_without_row` that file would otherwise get — one misnamed row is
    one finding, and telling the reader to add a tracker row for a story that
    already has one is the same misdirection as telling them to create the
    missing file. Two candidate files is a different problem: the message
    names both, picks neither, and suppresses nothing.
    `ape sprint reconcile` keeps the generic form, because it is tracker-only
    by design and never reads a story file — it says so and points at
    `sprint check`.
  - **When nothing matches, the finding says so.** That branch used to fall
    back to the generic advice, whose tail points at `ape sprint check` — the
    command printing it, having already searched and found nothing. It now
    reports the search ("no story file matches this row") and names the two
    real options: the story is filed under an unrelated name, or the row is
    stale.
  - **`candidates[]` is a field, not just prose.** Declining to pick between
    two possible story files is a deliberate refusal; making the decider parse
    a sentence to learn what the options were is not. Populated in the
    one-candidate case too, so a consumer reads one field either way.

- **test: `make check-claude`, a local gate against the installed Claude
  Code** — ape's real dependency is not a library it pins, it is the `claude`
  binary on the host: ape types into a TUI over a PTY and reads the rendered
  grid back. Every coupling to it is an undocumented detail of a program that
  auto-updates on a schedule ape does not control, and when one moves nothing
  errors — ape keeps running and silently stops doing the thing the coupling
  bought. Two such detectors already existed (`ape costs coverage` for model
  ids, `hookdrift` for hook payload fields), but both read artifacts a past
  run left behind. Neither can see the terminal contract at all.
  `TestLive_ClaudeCodeContract` spawns the local Claude Code through ape's own
  `internal/repl` path and checks the ready-signal footer and `❯` glyph
  *separately* (WaitForReady accepts either, so a rotted footer would hide
  behind the fallback), that no unknown pre-REPL modal blocks the prompt, that
  `CLAUDE_CODE_EFFORT_LEVEL` still moves the rendered effort, that every model
  id in ape's family-alias table still names a model Claude Code knows, that a
  spawned session still persists a transcript ape can parse (the v0.0.28–32
  root cause), and that `claude --version` still parses. Gated behind
  `APE_CLAUDE_LIVE=1`, so `make test` and GitHub CI never run it — a CI runner
  has no `claude`, no auth and no network, and a gate that can only skip there
  reads as a pass. `make check-harness` runs it with `make check-prices`.
  - **The model check keys on the raw id being ABSENT from the pane.** An id
    Claude Code no longer knows is not an error: it starts the REPL anyway and
    echoes the id verbatim where a live one renders as its display name
    (`claude-opus-5` → `Opus 5`). So a dead alias silently downgrades a run to
    a fallback model, and the assertion that catches it also survives
    Anthropic renaming the human-facing labels.

- **feat(doctor): `ape doctor --only`, and `make check-hooks` on top of it** —
  the hook-drift detector has existed since the gates it guards, but it was
  reachable only by running all 33 doctor checks, so no release ever consulted
  it. `--only` narrows the run to named checks, which makes a single gate
  scriptable: `make check-hooks` is `ape doctor --only hooks.contract_drift
  --strict --cwd $(HOOK_PROJECT)`. An unknown name in `--only` is a hard error
  listing the valid ones, not a silent no-op — a typo that ran zero checks
  would exit 0 and read as a pass, which is the exact failure mode a
  single-gate flag exists to avoid. `--skip` keeps its lenient behaviour,
  where a typo costs nothing.
  - **`HOOK_PROJECT` must name a project ape has actually run.** Hook drift is
    observed from the `hook-events.jsonl` files ape wrote under
    `<project>/_output/tasks`, so it cannot be judged from the ape repo, which
    has no runlogs. The default (`.`) therefore reports a skip; the docs and
    the release skill both say plainly that this is "not verified" rather than
    a pass.

- **fix(repl): claude 2.1.248 made "No, exit" the default on the folder-trust
  dialog, and ape was pressing it** — the dialog's wording did not change, so
  ape's matcher fired correctly; what moved was the SELECTION. Until 2.1.247
  the accept option was preselected and a bare Enter took it. In 2.1.248
  "No, exit" comes first and is highlighted, so that same Enter quit Claude:
  the REPL never became ready, every stage burned its idle window, and runs
  reported zero turns.
  - **ape now reads the selection back before confirming.** It walks the menu
    to the option that grants trust, wherever it sits, and presses Enter only
    there. Position is Claude's business; encoding it here is what broke.
  - **Matched on words, not sentences**, on both axes. The dialog has been
    "Do you trust the files in this folder?", then "Quick safety check: Is
    this a project you created or one you trust?", and the heading moved to
    "Accessing workspace:" — so the match asks whether the screen talks about
    *trusting* a *place*. The option test asks whether a row grants trust and
    is not a refusal, because a naive `contains("trust")` selects "Don't
    trust this folder".
  - **A painted dialog is not a dialog ready for input.** Claude draws the
    menu before its key handler is live; a keystroke sent into that gap is
    echoed as literal text — visible as `^[[B` above the dialog — and left in
    the input buffer to be submitted with the first real prompt. ape waits
    for the pane to stop changing before touching it, then polls for the
    selection to move rather than sleeping a guessed interval.
  - Arrow keys, never a typed digit: a digit surfaces as a UserPromptSubmit
    the step-contract verifier can consume as the skill prompt. That
    reasoning predates this fix and still holds.
  - **`make check-claude` caught this.** Against 2.1.248 the pre-fix binary
    fails `ready_signals` in 120 s with the diagnostic written for exactly
    this case — "a NEW pre-REPL modal is blocking that blockingModals does
    not know how to dismiss". Verified by reverting the fix and re-running.

- **chore: Go 1.27, refreshed dependencies, and the pinned toolchain** —
  `go 1.26.6` → `go 1.27.0` (CI reads `go-version-file: go.mod`, so the bump
  carries there and to the release workflow with no second edit). All direct
  and indirect dependencies updated; `govulncheck` reports **0 vulnerabilities
  and 0 allow-list exceptions**.
  - Pinned tools: gofumpt `v0.10.0` → `v0.11.0`, golangci-lint `v2.6.0` →
    `v2.13.1`, govulncheck `v1.3.0` → `v1.7.0`, goreleaser `v2.15.4` →
    `v2.18.0` — the last held to the newest **stable** tag, since
    `goreleaser@latest` resolves to a nightly and release machinery should not
    be pinned to one.
  - **Two linters had been silently re-enabled by renames.** golangci-lint
    v2.13 versioned `exhaustruct` to `exhaustruct_v5`, so the existing
    `disable` entry stopped matching and 50 findings appeared; `gomodguard` is
    deprecated in favour of `gomodguard_v2`. Both spellings are now handled,
    the same way `wsl`/`wsl_v5` already were. A disable entry that names only
    the old spelling is a check turning itself back on at the next upgrade.
  - **goconst's `min-len: 2` no longer earns its keep.** It flagged every
    cobra verb, output format, wire field and YAML tag in the tree — 50+
    findings, none an improvement, since `Use: "verify"` beside `Use: "update"`
    reads better than two consts. Raised to 16, at which exactly one real
    finding survives (a message repeated five times across three packages,
    now `apexcfg.MsgImplementationFolderUnset`) — tuned to keep the linter
    useful rather than to reach green.
  - **gosec grew two checks that this program's threat model answers
    differently.** G703 (path traversal by taint analysis) marks any path
    reaching a file operation from config, flags or the environment; for a CLI
    whose job is operating on directories the invoking user names, that is
    every file operation and there is no privilege boundary to traverse — one
    of the three sites it flagged already rejects `""` and the filesystem root
    before stat'ing. Excluded as a category, with the reasoning in the config.
    G122 (WalkDir symlink TOCTOU) and G115 (uint32→byte) are single sites and
    carry their own justification at the call site.
  - Go 1.27 idioms adopted where the linter proposed them: `errors.AsType`,
    `strings.Cut`, `reflect.TypeAssert`, `slices.Backward`. One of those
    auto-fixes regressed a hot TUI loop into copying 144 bytes an iteration —
    ranging on the index alone satisfies both `modernize` and `gocritic`.

- **build: `make check-framework`, the release gate for the other dependency
  axis** — `check-harness` asks whether the local *Claude Code* still honours
  what ape drives it through. Nothing asked whether ape still satisfies what
  the local *APEX framework* requires, even though three families of tests
  existed to answer it — `TestCommandSurface_AgainstRealManifest`,
  `TestContract_LiveConfigTemplate` and the eight `TestParity_*` — all gated
  on `APEX_FRAMEWORK_REPO` and therefore skipping silently in every run,
  including the release. They now have a target, and the release skill a phase.
  - It also runs `ape doctor --only framework.command_surface,framework.terminal_contracts`
    against `HOOK_PROJECT`, which is the same surface as a project actually
    *received* it rather than as a checkout declares it.
  - **A path that is not a framework checkout is a hard error**, not a skip:
    setting the variable says you want the gate to run, so a typo that resolves
    to nothing would otherwise pass green on four gates at once. An unset
    variable still skips, loudly.
  - **Both framework layouts resolve.** Released puts `_apex/` at the repo
    root; the build repo nests it under `framework/`. Resolution is now in one
    helper — the first real run of this target failed against a build checkout
    while the gate beside it passed, purely because one had learned about both
    layouts and the other had not.
  - `TestParity_*` skipping against a modern framework is the intended end
    state, not a gap: the scripts they compare against are retired, which is
    what the gate existed to guard. Documented so a skipped parity run is not
    read as a hole.

- **feat(doctor): `framework.command_surface` — does this binary provide what
  the installed framework requires?** The second half of the manifest work.
  From framework v0.11.0, 74 of 90 skills shell out to ape subcommands and
  PLAN-57's DD3 deleted every fallback branch, so an ape predating a command
  makes a skill fail deep inside a multi-hour stage. Nothing noticed:
  `framework.metadata` compares versions of the *framework*, not of ape. An
  eval capture came within a hand-check of measuring eight hours of broken
  runs against exactly that mismatch.
  - **A name diff, not a version floor.** A locally-built ape reports a Go
    pseudo-version (`0.0.53-0.20260823120345-def342771795`) that a floor check
    skips as unstamped — it would have passed on precisely the binary in
    question. Resolving each entry against cobra's own tree tests the
    capability rather than the label, and holds for local builds and forks.
  - **Required, and FAIL on a miss**: a framework whose commands are absent is
    broken rather than degraded. The message names the whole difference, never
    the first miss — an operator on an old binary wants one line, not a bisect.
  - **Reads `required_commands` only.** The manifest carries a second list,
    `sanctioned`, which is strictly smaller because it exists to lint skills
    and so omits the operator-facing and dispatch commands the binary
    nonetheless owes. Reading it would under-declare the surface.
  - A partial match counts as missing: `cobra.Find` returns the deepest
    command it reached plus leftover args, so `ape story fields` on a binary
    with `ape story` but no `fields` comes back as the parent. Treating that
    as present is the failure the check exists to prevent.
  - Compares command **names**, not flag sets — a binary can provide
    `ape story fields` with an older flag set and pass. Stated in the check's
    own `--help` so it is not mistaken for version compatibility. An absent
    manifest reports a skip: the framework predates the contract.
  - Resolution is mutex-guarded, because `cobra.Find` is **not** read-only:
    it lazily merges persistent flags and writes to every command it walks.
    `runDoctor` runs checks sequentially so production never raced, but a
    helper safe only by its caller's scheduling is a trap for the next one —
    the race detector found it the moment two tests resolved in parallel.

- **feat(sessiondriver): a dead session fails with its reason, not with
  silence** — when the session's own turn fails against the API and nothing
  follows it, ape waited out `--idle-timeout` (60 min by default) and then
  reported "nothing happened". It now fails as soon as the failure is
  decidable and carries the upstream message verbatim, under a new exit code
  5: distinct from 1 because it is upstream and retryable — the skill did not
  misbehave — which is a distinction a caller could not previously make.
  - **The grace window does the work, not the error text.** Across every
    `API Error` in a local transcript corpus, a session that hit one and
    recovered wrote its next entry at the *same timestamp* — recovery latency
    0.0 s in every case — and none of them was the transcript's last line.
    Failing on sight of the message would have killed three healthy sessions
    on one machine. So the check fires only after every progress signal
    (hook, transcript, PTY) has been quiet for `DefaultAPIErrorGrace`
    (3 min), keyed off overall progress rather than the transcript alone so a
    session still emitting hooks is never touched.
  - **Matched on the message PREFIX.** "API Error" anywhere in the text also
    matches an assistant *discussing* an outage — the transcripts of this very
    change contain several — and enumerating status codes would need a release
    each time upstream grows one. Of 52 local `API Error` lines, 30 were
    `tool_result` rows from tools failing inside healthy sessions; a substring
    match would have treated those as fatal too.
  - Reading is memoized on the transcript signature, so an unchanged
    multi-megabyte file is scanned once per stall rather than once per poll.
    Detection latency is the grace window plus one poll interval (30 s at the
    default idle window), so ~3.5 min against the 60 min it replaces.

- **feat(framework): install `_apex/ape-commands.yaml`** — the framework-owned
  manifest of the ape command surface an installed framework requires. From
  framework v0.11.0 the skills shell out to ape subcommands with no fallback
  branch, so a binary missing one makes a skill fail deep inside a stage.
  ape installs the file but does not read it yet; the check that diffs
  `required_commands` against this binary's command tree is separate work.
  Copying it first is the point — the framework can ship the manifest and have
  it reach projects on their next `ape framework update`, with no coordinated
  ape release. Absent in the framework repo = version skew, not an error, on
  the same terms as the operating-rules fragment and the terminal-contracts
  table.

- **BREAKING (on-disk): everything ape writes now lives under
  `{output_folder}/ape/`** — the output folder is the *framework's*. Its path
  comes from the `output_folder` config variable and the skills write
  handoffs, briefs and verify-orchestrator reports there. ape had been
  scattering run artifacts across it as siblings of that content —
  `_output/pipelines/`, `_output/tasks/`, and inconsistently
  `_output/ape/prompts/` and `_output/ape/chats/` — at two different nesting
  depths, while ignoring `output_folder` entirely and hardcoding `_output`.
  ape now resolves the variable and owns exactly one subtree of it,
  `{output_folder}/ape/`, writing nothing outside it.
  - **`output_folder` is now honoured.** A project with `output_folder:
    build/artifacts` gets its ape artifacts at `build/artifacts/ape/`.
    Resolution degrades rather than failing: run paths are needed where a
    project config is not guaranteed (`ape chat` in a bare directory, a
    `config.yaml` with a syntax error), so an absent or unparseable config
    falls back to the framework's own default, `_output`.
  - **The migration is asymmetric for a renamed output folder.** Because ape
    hardcoded `_output` *before* this change, legacy artifacts sit under a
    literal `_output` whatever the config says. A default project therefore
    has two relocations (pipelines, tasks); a project that renamed
    output_folder has four, because its prompts and chats were also written
    to the literal `_output/ape/`. Emptied `_output/` husks are pruned — but
    never the live output folder, which is the framework's.
  - **`ape framework setup|update` relocates an existing project.** Whole run
    directories at a time, so a run is never half at each path; a destination
    that already exists is reported as a conflict and left alone rather than
    overwritten or merged; `latest` symlinks are recreated pointing at the
    same run; emptied legacy trees are pruned; and it is idempotent. Cross-
    filesystem moves fall back to copy-then-remove. `--no-migrate` defers it,
    `--dry-run` reports it without writing, and `ape doctor` flags a project
    that still needs it (`runs.legacy_layout`).
  - **The layout is now defined once**, in `internal/runlog/layout.go`, and
    `runlog.RunRoots` is the only way to ask where the runs are. Eight
    consumers used to re-derive it, and their coverage varied: `hookdrift`
    read one root of four, `ape metrics --run-id` matched two layouts only by
    coincidence of nesting depth, and only `cost.ScanProject` had the full
    list. A fifth run kind is now one line in one file.
  - Two things that look like the same bug are **not**, and stay as they were:
    `ape costs reprice` skips chats because `session.yaml` carries no
    per-model token breakdown to recompute from, and `cost.FindRunManifest`
    covers only the manifest-bearing kinds because `ape costs chat|prompt`
    serve the other two. Both are now expressed as explicit decisions against
    the shared root list rather than as hand-written path globs that happened
    to omit them.

- **fix(hookdrift): the detector was reading one of four runlog roots, and a
  Claude Code upgrade masked drift for a month** — two defects that both made
  it quieter than it looked, found by pointing the new `make check-hooks` at a
  real project.
  - **It swept only `_output/tasks`.** ape writes runlogs to four roots:
    `_output/pipelines` (`ape pipeline`), `_output/tasks` (`ape task`,
    `ape script`), `_output/ape/prompts` and `_output/ape/chats`. The
    flagship command was invisible to it, so a project that runs pipelines
    and nothing else reported "no interactive runs in the last 30 days" for
    ever and the check had never once fired — while its own doc comment
    claimed the corpus was "every interactive run". It now walks `_output`
    whole, which is also the shape that cannot regress when a fifth producer
    is added.
  - **The verdict is now scoped to one harness version.** A field was only
    reported absent when it was missing from *every* payload in the window,
    so a 30-day window straddling a Claude Code upgrade kept the verdict
    green on the strength of pre-upgrade runs while every run on the harness
    actually installed had lost the field. The verdict now comes from the
    version that wrote the most recent run; older versions are counted and
    named, never judged. Chosen over a presence-ratio threshold or a
    newest-N window because it needs no invented constant, reuses the
    `claude_version` the manifest already carries, and asks the question the
    gate actually means — does the Claude Code I have installed *now* still
    send this? Partial presence within a single version stays healthy.

- **`make check-harness` is now the whole sweep** — `check-prices` (model ids
  in local transcripts) + `check-hooks` (hook payload fields in local runlogs)
  + `check-claude` (a live PTY session). Three gates, each reading what the
  installed Claude Code is *actually* doing rather than what it did at release
  time, and each reporting "not verified" rather than green when it finds no
  evidence. The release skill runs it as one named phase so it cannot drift
  out of sync with what "the harness contract holds" means.

## v0.0.52 (2026-08-15)

- **feat(sandbox): finish the single-node workspace story (PLAN-24)** — seven
  deliverables that make a Kata VM workspace somewhere you can actually live:
  look at what you are building, know what it cost, know whether another one
  fits, and have idle VMs stop themselves honestly. Everything stays in this
  repo — no image bump, no digest re-pin.
  - **`ape sandbox forward <ws> <port>`** — reach a port inside a workspace. A
    workspace has *zero* inbound reachability by design, and this does not change
    that: the guest end dials its own loopback and the bytes ride the same
    authenticated, audited session transport as `exec`, so no listener is opened
    and neither firewall moves. `--ssh` makes it a plain ssh (and VS Code Remote)
    target, since `sshd` and `~/.ssh` already existed inside every workspace with
    no way to reach them. The transport is unchanged — a forward is the exec
    session carrying raw bytes rather than a PTY — so `exec`/`attach` are
    untouched. `ape sandbox ssh` now says how to do this instead of reporting
    "Tier-2".
  - **`ape sandbox costs [name]`** — what Claude sessions inside workspaces cost,
    per workspace. The transcripts were already on the host the whole time (a
    workspace's `$HOME` is a bind from its composed home), so this needed no
    agent, no telemetry wire, and no network into the guest — only something to
    read them. The node does the scan because the composed homes are private to
    the daemon; an unpriced model is called out rather than counted as free.
  - **`ape sandbox capacity`** — cores, memory, how many workspaces the node
    carries, how many are *running* (only those hold RAM), and roughly how many
    more fit. `Capabilities()` had declared `KVM`, `Mem` and `Factory` since
    PLAN-18 and left all three empty.
  - **Automatic idle-stop, off by default** (`aped front --idle-stop 2h`).
    PLAN-22 deliberately shipped no reaper because the only signal available was
    `last_used_at` — "when did someone last reach in" — which would have stopped a
    three-hour build with nobody attached. The signal is now guest CPU sampled
    *inside* the workspace by a new `ape sandbox-agent`, so idle means idle. It
    **stops, never destroys**, and a workspace the node has never heard a
    heartbeat from is **never** reaped: silence is unknown, not idle. Opt out or
    retune per project with `lifecycle.idle_stop` in `.apesandbox.yaml`, visible
    as the `IDLE-STOP` column in `ape sandbox ls`.
  - **The in-guest agent reaches the host through the proxy it already has.**
    PLAN-16/18 assumed a bridge-IP NATS listener behind a new firewall hole; none
    was needed. The CONNECT proxy and the NATS server live in the same `aped`
    process, so a *system route* (exact `aped.internal:4222`, checked before both
    allowlists, audited with a distinguishing reason, and unremovable by
    `egress set`) reaches it via a loopback dial the daemon makes to itself. The
    per-VM credential minting built in PLAN-18 D6 and dormant since now has an
    endpoint to turn on. The agent is a subcommand of the `ape` PLAN-23 already
    delivers into every workspace, carries no path that could build a management
    request (asserted by a test, not a comment), and is launched and supervised by
    `aped` rather than the image entrypoint.
  - **Live-validated on a real Kata host**, which found two defects in the capacity
    probes: they read `/proc/meminfo` and `/dev/kvm`, and aped's units run
    `ProcSubset=pid` + `PrivateDevices=yes`, so neither exists in either aped
    process *by design*. A node running a VM reported `kvm: no` and no memory at
    all. Both now fall back to `/sys`, which the hardening leaves readable. Also
    corrected a claim this work was planned on: the image ships `sshd` but does
    **not** start it, so `forward --ssh` needs one `exec` first and now says so.
    `deploy/validate-plan24.sh` reproduces the whole run.
  - **Docs**: a new [devcontainer how-to](docs/how-to/devcontainer-workspaces.md)
    closing PLAN-22 D7 — toolchain, the durable caches, pre-warm and offline
    rebuilds, the login-shell environment, the bingo-vs-delivered-`ape` rule, the
    image-pin ↔ policy pairing, and freeze/stop/down/idle-stop in one table.

- **feat: fail a step whose agent detached instead of finishing** — ape derived
  step completion from the `Stop` hook alone, so an orchestrating skill that
  spawned a sub-agent, said it was waiting for the result, and ended its turn was
  indistinguishable from one that had finished. The run was torn down, a manifest
  written, and the whole thing reported a **success with zero work done**. Two
  captured instances on different Claude Code versions: a detached teammate under
  2.1.232, and a still-running `subagent` under 2.1.220 that exited 0 having
  stopped after its second story.
  - **The harness was already saying so.** Every `Stop` payload carries
    `background_tasks` — what was still running when the turn ended, foreground
    work already filtered out. Across **16,965 captured `Stop` events spanning
    Claude Code 2.1.199–2.1.233, exactly one was non-empty**, and it is the
    2026-07-31 incident: a measured false-positive rate of zero. That beat
    tracking `SubagentStart`/`SubagentStop` pairs, which needs cross-event
    bookkeeping vulnerable to reordering and dropped hooks — and which would not
    have fired at all for a teammate.
  - **A `Stop` now completes a step only when nothing blocking is outstanding.**
    A teammate fails it immediately; a backgrounded sub-agent or an unrecognised
    task kind defers the signal and re-decides on the next `Stop`; shells,
    monitors, workflows and scans are ignored. An Agent-tool result of
    `teammate_spawned` fails at the spawn instead, naming the agent — redundant on
    purpose, so losing one signal upstream costs redundancy, not correctness.
  - **Teammates fail in milliseconds because non-resolvability is provable** — the
    harness delivers their result by mailbox, never as a tool result. A
    backgrounded sub-agent may legitimately run for hours, so that branch is
    bounded by the existing idle backstop instead: killing a healthy sub-agent
    abandons real work, while waiting too long only costs time. No path reaches a
    timeout for a condition decidable at the `Stop`.
  - **Terminal-contract check, warning-only this release.** Skills that end a run
    with a machine-readable block are declared by the framework in
    `_apex/terminal-contracts.csv`, matched against the closing assistant message
    of the step's own transcript — not a hook field, so it survives the harness
    renaming one. `ape` never learns the word `run_status`; the vocabulary stays
    framework-owned. Warning-only because it depends on a dispatching agent
    relaying its sub-skill's block verbatim, and a paraphrase would turn a
    framework-side quality problem into a hard failure here.
  - **Two new `ape doctor` checks, because a silent gate is worse than no gate.**
    `hooks.contract_drift` sweeps the project's own `hook-events.jsonl` and reports
    whether the fields the gates read are still present — a renamed field would not
    error, it would just stop the gates firing. `framework.terminal_contracts`
    reports whether the table is installed and which skills it enrols. Same
    reasoning as `costs coverage`: the change lands on the harness's schedule,
    under an already-released binary, where no release-time check can see it.
  - Every input degrades safely — an absent `background_tasks`, an absent contract
    table, an unenrolled skill and an unreadable transcript all behave exactly as
    before, and the regression locks assert it. New fixtures under
    `testdata/hookpayloads/` are real captures; the detached shape is no longer
    reproducible on demand now that the framework forbids the spawn that caused it.
    Full rationale in
    [step-completion-gates.md](docs/explanation/step-completion-gates.md).

- **chore(deps): require Go 1.26.6** — six standard-library advisories were failing
  the govulncheck gate against 1.26.5: GO-2026-5026 (`x/net/idna` via `net/http`),
  GO-2026-5972 (`encoding/asn1`), GO-2026-6089 (`net/http`), GO-2026-6090
  (`crypto/tls`), GO-2026-6091 (`html/template`) and GO-2026-6218 (`net/url`). All
  six are fixed in 1.26.6 with no workaround short of the bump. Both workflows
  resolve Go via `go-version-file: go.mod`, so one directive covers CI and the
  release build; the gate keeps its zero-exception allow-list.

## v0.0.51 (2026-07-28)

- **chore(deps): bump google.golang.org/grpc to v1.82.1** — clears GO-2026-6061
  (xDS RBAC authorization engine + HTTP/2 transport server), reachable through
  containerd's client in `internal/sandbox`. An indirect-only patch bump; the
  govulncheck gate keeps its zero-exception allow-list.

- **fix: an unpriced model reported $0.00 instead of saying it had no price** —
  `opus[1m]` began resolving to `claude-opus-5` on 2026-07-14; the price table had no
  such row, so every cost from that day on was `$0.00` while token counts stayed
  perfectly correct. The row is added, but the row was never the real defect:
  `LookupAt` already returned an `ok` flag and both call sites in the scanner
  discarded it, which made "no price for this model" and "this model is free"
  the same value everywhere downstream. That is what let it run 13 days unnoticed.
  - **Nothing prices silently any more.** Pricing now resolves through
    `LookupSourceAt`, which returns *how* a price was reached — `exact`, `override`,
    `dated`, `family`, or `none` — and that source rides with the number. A step that
    used an unpriced model says so on stderr and in its manifest `telemetry_note` as
    it finishes; `ape costs`, `ape costs run`, and `ape costs prompt` warn from the
    per-model keys the rollup already carries. `Lookup`/`LookupAt` keep their exact-match
    contract unchanged, so no existing caller shifted behaviour.
  - **A model newer than your `ape` no longer prices at zero.** An id with no exact
    row falls back to its family tier (`claude-opus-*` → Opus rate, and so on),
    flagged as an estimate everywhere it surfaces — the right order of magnitude
    instead of a wrong zero, never presented as authoritative. Dated snapshot ids
    (`claude-haiku-4-5-20251001`) now attribute to their base rate, and `<synthetic>`
    — Claude Code's sentinel for locally-generated turns, whose usage is all zeros —
    is priced as the real zero it is rather than reported as a gap.
  - **`ape costs coverage`** reads the transcripts the locally-installed Claude Code
    is writing right now and reports every model id the table does not cover exactly.
    This is the detector the incident needed: Claude Code ships independently, so the
    only correct time to ask is at scan time, not at release time. Also wired as the
    `cost.price_table_coverage` doctor check and, as `make check-prices`, into
    `make ci-local` and the `/release` pre-flight. With no transcripts (CI) it reports
    a **skip, never a pass** — absence of evidence is not coverage.
  - **`ape costs reprice`** recomputes stored costs from the per-model tokens already
    on disk, so runs recorded during a gap are recoverable rather than written off.
    Dry run by default; `--write` rewrites only `cost_usd` scalars, preserving
    comments and key order. A still-unpriced model is left alone and named.
  - **Model names are resilient across every entry point.** `--model` on `ape task` /
    `ape prompt` / `ape chat`, the `apescript` task/prompt runners, and `model:` at step,
    stage, or pipeline level in a spec now accept
    `sonnet`, `Sonnet`, `claude-sonnet`, `sonnet-5`, `claude-sonnet-5`,
    `claude-sonnet-4.6`, and `claude_sonnet_4_6`. **A bare family word resolves to that
    family's current generation** — `model: sonnet` in a spec spawns
    `claude-sonnet-5` — so a run records a concrete id and its per-model attribution
    matches the transcript's. An explicit generation is honoured as written; the
    `[1m]` suffix rides along. An unrecognized name is passed through with a warning
    rather than rejected — a model newer than this binary must not be blocked by it.
  - **Alias drift is detected, because resolving means a stale table picks the wrong
    model.** `ape costs coverage` compares the `aliases:` block against the
    generations the local Claude Code is actually emitting and reports any family
    where a **strictly newer** generation is in use than the alias names. Drift fails
    `--strict`, so `make check-prices` and the release gate block on it, and `ape
    doctor` warns. The condition is "newer generation in use", not "target absent" —
    pinning `claude-opus-4-8` everywhere means `claude-opus-5` never appears, and
    flagging that would fail the gate over a deliberate choice. Ordering parses the
    numeric segments of `claude-<family>-<major>[-<minor>]`, so `claude-opus-5`
    outranks `claude-opus-4-8` (the pair a string sort gets backwards); an id that
    does not parse yields no comparison and no claim.
  - **The price table is data, not Go literals.** It moved to an embedded
    `internal/cost/prices.yaml` in the same schema `ape costs update --from` accepts,
    so a correction can be applied locally without waiting for a release.
  - `ape costs reprice` dedupes the `latest` symlink every pipeline/task name
    carries. Without it the manifest glob matched each recent run twice and the
    dry-run preview reported double the real delta — misleading the very decision
    it exists to inform.
  - A build-time invariant (`TestAliasPointsAtNewestTabledGeneration`) fails when
    `prices:` gains a newer generation than `aliases:` names. That combination is
    otherwise invisible: the new model is exactly priced so coverage stays clean,
    drift only fires on a machine that has run it, and meanwhile every spec saying
    `model: opus` quietly selects the older model.
  - **A typo'd `model:` in a spec is no longer silent.** `Spec.ModelWarnings()`
    reports every pipeline/stage/step model ape cannot attribute to a known family;
    `ape pipeline`, the apescript pipeline runner, and the `pipelines.project` doctor
    check all surface it. Previously a `--model` typo warned instantly while the same
    typo in a checked-in spec stayed hidden until claude rejected it mid-run. Still
    not fatal — claude, not ape, decides which models exist.
  - **`ape doctor` is fast again.** The coverage check was 2342 ms of a 2346 ms
    doctor run — 99.8% of the command. Two fixes: a `bytes.Contains` pre-filter
    skips lines that cannot name a model before the JSON decode (2342 → 1028 ms,
    and the sweep is now I/O-bound at ~240 MB/s over ~242 MB), and doctor reads a
    1-hour cache keyed on the price table's own stamp, so an ape upgrade invalidates
    it immediately (→ 2 ms warm). A cached verdict always says so in the message.
    `ape costs coverage`, `--strict`, and `make check-prices` never use the cache.
  - `ape costs coverage` now lists every distinct raw spelling seen for a model
    (a dated snapshot alongside the bare id), not just the first one encountered —
    the interesting case is exactly when one model arrives under two names.
  - **`ape costs update --from` no longer discards `effective_from`.** It was
    validated and then dropped, so a dated override persisted as unconditional —
    silently repricing history the file explicitly excluded. The load → save
    round-trip now carries the date.
  - **A zero or negative price row is rejected rather than applied.** A misspelled
    key (`base_imput:`) unmarshals to zero, which would price those tokens at $0 —
    the same silent zero this release fixes, arriving through a typo. The built-in
    table refuses to load such a row; an override file has the row **dropped** and
    the built-in rate used instead (an override outranks the table, so applying it
    would be the dangerous direction), with the rejection reported by
    `ape costs coverage` and `ape doctor`. A `<synthetic>`-style sentinel id is
    still allowed a genuine zero. **If `~/.ape/prices.yaml` has a zero-rate row it
    now stops taking effect** — run `ape costs coverage` to see it named.
  - Docs: the [pipeline-spec reference](docs/reference/pipeline-spec.md) described `model`
    as "passed to claude as `--model {value}`" — no longer true now that ape canonicalizes
    before spawning. Corrected, with a new **Model values** section covering every accepted
    spelling, why the bare family word is the recommendation, and that `opus[1m]` is
    redundant on current models (their 1M window is the default) and meaningless on `haiku`.
    Examples across the docs moved off `opus[1m]`.
  - Docs: the [run-manifest reference](docs/reference/pipeline-run-manifest.md)
    now states that a zero `cost_usd` beside non-zero `tokens_*` means *unpriced*,
    not free, and what `telemetry_note` can say; `ape costs` joins the README
    command table; `internal/cost/` joins the CLAUDE.md directory map.
  - New: [How to keep cost pricing current](docs/how-to/keep-cost-pricing-current.md).

## v0.0.50 (2026-07-26)

- **feat: a workspace runs the `ape` that provisioned it, not the one its image was
  built with (PLAN-23)** — `ape` is no longer baked into the sandbox image. `aped`
  mounts the binary installed beside it **read-only at `/opt/ape/bin`**, first on
  `PATH`. The old arrangement was a lag in the wrong direction: the image carried
  whatever release was current when it was built, and since project work happens
  *inside* workspaces, an `ape` upgrade made to unblock that work never reached the
  place the work happens. Delivery removes it by construction — the two binaries ship
  in one release archive, so "the `ape` beside this `aped`" is the matching build with
  no fetch and no pin.
  - **Verified by reading the binary, never by running it.** `debug/buildinfo` gives
    the main package path (which catches a *different program* named `ape` — no version
    string could), `GOOS`/`GOARCH` (so an unusable binary is refused at create instead
    of failing as `exec format error` inside the VM), the ldflags version, and
    `vcs.revision`. Checked at daemon start *and* re-checked per create, because the
    file can be replaced under a running daemon — which is what a redeploy does.
    World-writable is fatal; group-writable warns, since `go build` under a `002` umask
    emits `0775` and refusing that would make `--ape-binary` unusable in development.
  - **The binary is staged into a directory of its own** under the state dir rather than
    mounted from where it is installed. `/usr/local/bin` holds 48 entries on a real node
    (containerd, the Kata shims, `aped` itself), and mounting that directory into every
    workspace first on `PATH` would both expose all of it and shadow the image's own
    tooling with the host's — a workspace's `bingo` and `asdf` would silently become the
    host's copies instead of the versions the image pins. The copy also pins what a
    running workspace executes, so replacing the host binary cannot swap it underneath.
  - **Your bingo pins are untouched.** bingo installs version-stamped names and calls
    them by absolute path, so a project's pinned `ape` never resolves through `PATH`;
    bare `ape` is the delivered one. Don't `bingo get -l ape` in a workspace, though —
    the unstamped link lands in `$GOBIN`, which is a node-wide shared cache, so two
    projects pinning different versions would contend for one name.
  - `ape sandbox ls` gains an `APE` column and `up` prints the delivered version beside
    the client's, because a laptop driving a remote node gets the **node's** `ape`.
    `ape update` inside a workspace now explains that instead of failing on a read-only
    mount. `ape doctor` gains `sandbox.ape-delivery`. `/opt/ape` is a reserved mount
    subtree, so a committed `.apesandbox.yaml` cannot choose its own workspace's `ape`.
  - The delivered mount is treated as a **system mount** by policy, like the framework:
    exempt from `mount_roots` only when read-only and on its own reserved destination, so
    an operator never has to allow-list `aped`'s state dir.
  - Retires the in-guest version floor (a delivered `ape` cannot predate its own
    daemon) and the image's dependency on an `ape` release entirely.
- **fix: `go`/`asdf` in an ssh or VS Code Remote session used the ephemeral rootfs
  instead of the durable caches** — a bug that predates the above and had nothing to do
  with it. The per-workspace toolchain env (`GOPATH`, `GOBIN`, `GOMODCACHE`, `GOCACHE`,
  `ASDF_DATA_DIR`, the egress proxy) is *container* environment, which `exec` and
  `attach` inherit — and `sshd` does not, because it builds a fresh environment per
  session. So anyone working over ssh silently lost PLAN-22's offline-after-warmup
  property, in a way that reads as "the cache isn't working". `aped` now also writes
  that env to a file the image's `/etc/profile.d` entry sources. It carries an
  **allowlist**: credential material legitimately lives in the process env (mode B
  injects `ANTHROPIC_API_KEY`) and has no business in a host-side file.
  Two further holes in the same path, both measured in a real workspace rather than
  reasoned about: the **egress proxy** was absent, so an ssh session had no network at all
  in a workspace that HAD been granted egress (it would have read as the egress policy
  denying traffic); and **`go` itself** was missing, because the image adds
  `/usr/local/go/bin` via `ENV` only — so it reached `exec`/`attach` and not ssh. Image
  `v1.1.1` re-establishes every `PATH` entry it adds for login shells.
- **`ape` and `aped` derive their version identically** (`internal/buildident`) —
  previously `ape` backfilled from Go build info and `aped` did not, so a locally built
  pair reported a pseudo-version beside a bare `"dev"`. Harmless until the daemon began
  comparing the two, at which point a perfectly matched pair would have refused itself.
- **`framework setup`/`status` now say WHICH git failure they hit** — a framework
  directory whose files are all present but which git will not read as a repository
  failed with `<dir> is not a git repository`. That reads as a broken mount, and in an
  `ape sandbox` workspace the mount is usually fine: git is *refusing* it, because a
  read-only host-owned checkout looks "dubiously owned" to a guest running as root. The
  message now separates **absence** (no `.git` — the files were copied rather than
  materialized, so `ape sandbox framework materialize <ref>` is the fix) from **refusal**
  (git's own `dubious ownership`), quotes git verbatim instead of paraphrasing it, and
  names the in-guest `ape` floor — **v0.0.49**, the release that scoped the
  `safe.directory` exemption — as the cheapest cause to rule out. This cannot change what
  an *older* baked `ape` prints, since that binary contains none of this code; it ends the
  guessing from here on, which matters now that variant images can bake any `ape`.
- **The default sandbox image is digest-pinned** — `sandbox.DefaultImage` and the
  `images:` allow-list in `deploy/policy.yaml` both name
  `ghcr.io/exoport/ape-sandbox:v1.1.1@sha256:b45a0674…`. A tag is mutable: re-pushing it
  would silently change what every workspace runs, and nothing in the pipeline would
  notice. The tag is kept alongside the digest so the version stays legible in errors and
  `ape doctor` output — containerd reduces `tag@digest` to the digest when it resolves, so
  the tag is documentation and the digest is the pin. The digest is the OCI image **index**,
  so it still resolves per architecture. Both places move in one commit: the policy check is
  an exact string match, so a mismatch surfaces as a policy **denial** rather than a pull
  error, and a test now asserts the shipped policy allows the compiled-in default.
- **fix: a node could not provision at all with the delivered `ape`** — the staged binary
  lives under `aped`'s own state dir, and the policy's mount check treated it like a
  caller-supplied host path, so **every** create was refused as a policy denial. The
  framework mount already had exactly the exemption this needed — read-only, on its own
  reserved destination, a source the daemon resolved itself — so the two are now uniform.
  The alternative, making operators allow-list `aped`'s state dir in `mount_roots`, fails
  closed in the most confusing way available.
- **A pull that cannot succeed now says why** — `aped`'s root executor is network-less by
  design (AF_UNIX only), so a registry pull from inside it fails with
  `socket: address family not supported by protocol`, which reads like a broken host rather
  than a deliberate restriction. A create failing on a network error now prints the
  pre-pull command with the namespace and the exact ref, including the part that costs a
  second attempt: containerd stores an image under the name it was pulled with, and the
  lookup is an exact match on the **digest** form, so pulling the tag leaves it missing.

### Upgrading a node

Two steps, both root, both new with this release:

1. **Install `ape` and `aped` together.** `aped` delivers the `ape` beside it into every
   workspace and refuses to start without one — or with one that does not match its own
   build. The release archive ships both; `deploy/dev-host.sh redeploy` installs both.
2. **Pre-pull the workspace image in digest form**, because the executor cannot:
   `sudo nerdctl --namespace aped pull ghcr.io/exoport/ape-sandbox@sha256:b45a0674…`

`ape doctor` reports both (`sandbox.ape-delivery`, `sandbox.image`).

## v0.0.49 (2026-07-26)

- **`ape sandbox` is now usable out of the box** — `sandbox.DefaultImage` pointed at
  `ghcr.io/exoport/ape-sandbox:v0`, a placeholder tag that was never published, so any
  `up` without an `--image` override or a profile failed on the pull. It now points at the
  published image, and `deploy/policy.yaml`'s allow-list matches it (they are checked
  independently, so a mismatch turns every default create into a policy denial).
  The image carries its **own version line**, independent of ape's: it changes for reasons
  ape does not — a new base, a newer asdf/bingo, a Playwright bump — so tying the two
  would mean either cutting a meaningless ape release to ship an image fix, or a tag that
  lies about what changed. The two directions are ordinary dependency pins, one each way
  (`ARG APE_VERSION` in the image, `DefaultImage` here).
- **README documents the sandbox** — the workspace flow, the committed descriptor, and the
  three properties that matter (allowlisted egress, a shared Claude session, declared
  toolchains into durable caches), with the Linux + KVM + Kata prerequisite stated up
  front.
- **A re-published credential can no longer lose to a token from the session it
  replaced** — the "publication was replaced" check compared inode identity only
  (`os.SameFile`), and filesystems RECYCLE inode numbers: `credentials publish`
  unlinks the path and re-creates it, and the create can be handed straight back the
  inode the unlink freed. A `claude /login` then looked like no change at all, and a
  workspace that had refreshed from the *old* session won on modification time —
  overwriting the operator's fresh credential with a dead one. Detection now also
  compares the published bytes against what the previous tick converged them to,
  which no inode allocation can fool. The credential sharing this belongs to is new
  in this release, so nothing shipped broken; it was caught by CI, on a runner whose
  filesystem reused the inode where the development host never did.
- **The Windows CI job runs only the portable packages** (`make test-portable`). It
  still *builds* everything — that is what catches portability breaks — but
  `internal/aped` and `internal/netd` assert POSIX mode bits, `/run/netns` paths and
  systemd units, so running them on Windows reports on the runner rather than on ape.
  Assertions elsewhere were made platform-aware rather than skipped: `%q`-quoted paths
  (which escape a Windows separator), 8.3 short names versus `EvalSymlinks`
  canonicalization, and the `setfacl` that Git for Windows ships — which is not the
  POSIX tool and silently defeated a `LookPath`-based skip.

- **feat: `ape sandbox` workspaces get allowlisted, audited network egress
  (PLAN-21 D1–D4)** — workspaces were `--network none` because aped's root
  executor cannot create container networking (empty capability set, AF_UNIX
  only, `RestrictNamespaces`, `@mount` denied) and widening it is forbidden by the
  ape/aped split's charter. Egress now comes from two actors *outside* the
  executor, which only ever handles a namespace **path**: a new narrow privileged
  helper (`aped netd` / `aped-netd.service` — two verbs over a root-only AF_UNIX
  socket, `CAP_NET_ADMIN` + `CAP_SYS_ADMIN`, no containerd access, no policy)
  wires one netns + veth per workspace onto a host bridge with **bridge port
  isolation**, and the de-privileged `aped-front` runs one **deny-by-default
  CONNECT proxy per workspace** (never decrypting TLS) whose every decision is
  audited to `egress-audit.jsonl` and `ape.audit.<node>.egress`. `policy.yaml`
  gains `egress:` (`enabled`, `allowed_domains`, `max_domains`); a project's
  request is intersected with it front-side and **re-checked by the executor**, so
  a project can narrow what a node permits and never widen it. Fail-safe by
  construction: the resolved spec stays networkless and only the helper-created
  netns grants a network, so a failed wire-up yields a networkless workspace
  rather than an open bridge. New `aped-netbr.service` (bridge + host nftables
  wall) and `deploy/dev-host.sh` (one idempotent root script for the host config)
  ship with it. Live Tier-2 validation is still open.
- **feat: a general mount model + committable `.apesandbox.yaml` (PLAN-20)** — the
  single host-fs project mount is replaced by a uniform, policy-checked list.
  A project declares its **repos** (each mounted at `/workspace/<name>`, one
  flagged `main`, which sets the working directory), extra **mounts**, its
  **egress** request, and its **toolchain** in one committed file, with repeatable
  `--mount-path` flags merging on top (CLI wins by destination). Everything there
  is a *request*: aped re-canonicalizes every source against `mount_roots`, adds
  read-only-only roots (`mount_roots_ro`) and a `limits.max_mounts` ceiling, and
  **refuses reserved destinations** (`/workspace`, `/opt/apex-framework`,
  `/sandbox/home`) even from a hand-crafted wire request — so a committed file can
  never redirect, shadow, or make-writable a system mount. See
  [docs/reference/apesandbox-yaml.md](docs/reference/apesandbox-yaml.md).
- **feat: the APEX framework is a read-only runtime mount, not a baked image layer
  (PLAN-20 D5)** — the `ape-sandbox` image becomes public and framework-free, so
  no node (or laptop) needs a registry credential. `ape sandbox framework
  materialize <ref>` copies one pinned ref out of your local checkout, with your
  own git credentials, into the node's framework root; aped mounts
  `<root>/<ref>` read-only at `/opt/apex-framework` and **errors with the exact
  command** when a ref is absent — it never fetches. Inside a workspace,
  `ape framework setup --no-fetch` installs from the mount.
- **feat: declared toolchains + durable caches + `stop`/`start` (PLAN-22)** —
  `.apesandbox.yaml`'s `toolchain:` section references the project's native
  `.tool-versions` / `.bingo` files, and `ape sandbox setup <name>` runs an
  idempotent `asdf install` + `bingo get` inside the workspace (`--dry-run` prints
  the script). Toolchain state lives in **durable host caches** mounted at
  `/cache/<tool>`, with the environment that points each tool at its cache derived
  server-side from a closed table — a caller picks a cache *name*, never a
  `GOPATH`. `ape sandbox stop`/`start` are now exposed (they free guest RAM while
  keeping the rootfs, unlike `freeze`, and survive a reboot), and aped
  reconciles its registry with containerd at startup so a workspace destroyed
  out-of-band stops haunting `ape sandbox ls`. An idle reaper / TTL is still open.
- **feat: `ape sandbox egress set` re-points a RUNNING workspace's allowlist** — the
  CONNECT proxy is host-side on a fixed port, so a new allowlist is a proxy restart on
  that port: the workspace keeps running, its `HTTPS_PROXY` stays valid, and the netns
  is untouched. The request goes through the same policy intersection as at create
  time, so it can narrow or re-shape a grant but never exceed the node's policy (and
  `set` with no `--domain` revokes egress entirely). Mounts deliberately cannot change
  this way — they are fixed in the container's OCI spec — so those need `down` + `up`,
  which is cheap because repos, caches and the framework live in durable host mounts.
- **feat/fix: `start` rebuilds a workspace's egress namespace** — covering two cases
  that need the same answer. After a **host reboot** the namespace is gone (it lives in
  `/run`) while the container still references it, so starting failed deep in the Kata
  shim. After **`stop`** it survives but is *dirty*: Kata's default
  `internetworking_model=tcfilter` adds a tc qdisc to the veth on boot and does not
  remove it when the task is killed, so `start` failed with "Failed to add qdisc for
  network index N : file exists" — a persistent namespace is ape's design choice, so
  cleaning up after the previous boot is ape's job. The namespace is rebuilt at the same
  path, address and proxy port (read from the container's own spec, because a guest
  about to start already has that address baked in). A workspace that is already running
  is returned before anything touches its namespace.
- **feat: `ape sandbox ls` reports AGE and LAST-USED (`--idle 24h`)** — last use is
  stamped on exec/attach/start, so "is this workspace still in use?" is answerable from
  data. There is deliberately **no automatic reaper**: use is not idleness (a workspace
  running a long job with nobody reaching in looks untouched), so an automatic reaper
  built on this signal would be an age-based killer wearing a policy's name. ape reports
  it and leaves `stop`/`down` to the operator.
- **feat: ONE Claude OAuth session shared by the host and every workspace
  (`ape sandbox credentials`)** — the hard part is not access to the credential but
  identity of session: OAuth refresh tokens **rotate**, so the moment any party refreshes,
  every other party's token is dead. Two obvious designs are therefore both broken, not
  merely weaker — bind-mounting one shared file means a workspace can never write the token
  it just refreshed (`claude` replaces the file by *rename*, and a single-file bind cannot
  be renamed over: EBUSY, measured), and independent per-workspace copies die at the first
  rotation. What ships instead: each workspace gets a real writable copy (so login and
  refresh work in-guest) and `aped-front` keeps every copy **converged** — a refresh or
  `/login` anywhere is written *in place* to the published credential, a hard link to your
  real `~/.claude/.credentials.json`, and out to every other workspace within a sync tick
  (`--cred-sync-interval`, default 3s). The in-place write is load-bearing: a
  temp-file-plus-rename would create a new inode and silently sever that link. It never
  creates a credential where none exists (a revoked one stays revoked) and never propagates
  invalid JSON (a torn read must not reach every workspace).
  Access is granted with a **POSIX ACL for exactly the `aped` user** (`setfacl -m
  u:aped:rw`) — read-only cannot replace it, because a workspace's refreshed token has to
  be written *back* through that file. A group grant was rejected deliberately: group `ape`
  is also the priv-socket gate, so it would share the credential with every operator added
  there, and `chgrp` additionally fails with EPERM in any shell opened before you joined
  the group. There is **no fallback** — `ape doctor` reports a host without `setfacl` as
  `sandbox.credential-acl`, `publish` fails with that reason, and a workspace whose
  credential the daemon cannot read fails naming the ACL as the cause. (`ls -l` will show
  `-rw-rw----+`: those group bits are the ACL *mask*, not group access.)
  Because a host `/login` **replaces** the file — which aped cannot notice, running as
  another user under `ProtectHome=yes` — every `ape sandbox` command re-publishes as a side
  effect, and `ape sandbox credentials watch --install-unit` installs a `systemd --user`
  service for the case where none is run (`loginctl enable-linger` for boot start). A
  re-published credential is **authoritative for one sync pass**, overriding timestamps: a
  login starts a new session, so a token a still-running workspace refreshed from the old
  one is dead however recently it was written.
  Validated live in all four directions, including a real host `/login` propagating to two
  running workspaces hands-off in ~1s. **Known race:** a workspace refresh in the same
  instant as a host login leaves one side holding the losing token for a tick, recovering
  on its next attempt.
- **fix(sandbox/aped): six defects found by live-validating egress on a Tier-2 host**
  — the deploy script wrote ExecStart drop-ins using flags the installed binary
  lacked (now capability-probed); the netns helper needed the `mnt` namespace for
  `ip -n`; `/run/netns` had to be a shared host mount; a unit with any
  private-mount-namespace option is a *slave* of the host peer group, so the helper
  now runs in the host mount namespace; a CONNECT client that half-closes had its
  upstream dial cancelled (now detached from request cancellation); and restarting
  `aped-front` silently stripped egress from running workspaces (proxies are now
  restored on the same port, re-intersected with current policy). The per-workspace
  egress audit trail is also operator-readable (`0640`, with the parent directories
  traversable — `UMask=0077` strips group bits from both).
- **fix(sandbox): `Proxy.Close` now closes its listener deterministically** —
  `http.Server.Close` only closes listeners its `Serve` goroutine has already
  registered, so stopping a just-started proxy could return while the socket was
  still accepting. The per-workspace egress supervisor stops a proxy and rebinds
  its port immediately, which surfaced it.
- **fix(framework): git calls carry a scoped `-c safe.directory=<repo>`** — inside
  a workspace the framework repo is a read-only host mount owned by the host user
  while the guest runs as root, and git refuses "dubiously owned" repos, which
  would have broken `ape framework setup` against the mount.

## v0.0.48 (2026-07-24)

- **feat: configurable reasoning `effort` (default `xhigh`)** — `ape pipeline`,
  `ape task`, `ape prompt`, and `ape chat` now all accept an `--effort` flag
  (`low|medium|high|xhigh|max`), and pipeline YAML files accept an `effort:`
  field at the pipeline, stage, and step levels. Effort resolves as
  `step ?? stage ?? pipeline ?? --effort ?? "xhigh"` (pipelines) or
  `--effort ?? "xhigh"` (task/prompt) — so those autonomous paths always run at
  an explicit effort. `ape chat` is interactive: it applies `--effort` only when
  given and otherwise keeps claude's native effort. ape exports the resolved
  value to the spawned
  `claude` via the `CLAUDE_CODE_EFFORT_LEVEL` env var (injected after the
  `CLAUDE_CODE_*` scrub, so ape's value is authoritative and not shadowed by the
  parent session's inherited level), which also makes it **propagate to
  sub-agents** — a batch skill's per-item sub-agents inherit the same effort.
  Like `--model`, effort is applied when the `claude` process launches. For
  pipelines/tasks the step-level value is recorded in the run manifest
  (`steps[].effort`) and the per-step event log.

## v0.0.47 (2026-07-24)

- **fix(sessiondriver): the `--max-duration` ceiling is now per batch item, not
  per batch** — the hard wall-clock ceiling (PLAN-19 D2, default 3h) previously
  measured from step start, so a sequential batch skill —
  `apex-story-batch-dev` / `-create` / `-review`, `apex-lift-project` — that
  spawns one sub-agent per item was killed mid-batch once the *whole* batch
  crossed 3h, even while each item was progressing. The ceiling clock now
  **resets on every sub-agent boundary** (`SubagentStart` / `SubagentStop`) — a
  completed sub-agent is unambiguous real progress, a stronger signal than the
  transcript-growth bytes the idle anchor already trusts — so the cap bounds an
  individual item rather than the batch. A step that spawns no sub-agents is
  unchanged: a flat cap from step start. Automatic on `ape pipeline`, `ape task`,
  and `ape prompt`; no flag or pipeline-spec change (the reset applies wherever
  sub-agents appear). The idle window (`--idle-timeout`, default 60m) still
  catches a step that goes genuinely silent.

## v0.0.46 (2026-07-24)

- **feat(service): report `last_event_at` on `job.status` / `job.list`** — the
  `ape service` job daemon now reports a `last_event_at` timestamp for each job:
  it equals `started_at` for a just-accepted job and advances to the `job-end`
  time once the job goes terminal. The timestamp is stamped **atomically with the
  terminal-state transition** inside the registry, so a consumer polling
  `job.status` can never observe a terminal state paired with a stale
  acceptance-time `last_event_at`. Additive to the wire contract (WireVersion
  stays 1).
- **docs(reference): `ape service` endpoint-contract reference** — new
  `docs/reference/service-api.md` documents all nine `ape.svc` endpoints
  (`pipeline.run` / `task.run` / `prompt.run` / `script.run`, `job.status` /
  `job.list` / `job.stop`, `status` / `health`): subjects, request/reply shapes,
  the field→argv mapping, the stable error codes, keyed exclusivity, and the
  allowlist trust boundary. Cross-linked with the event-stream reference
  (`events.md`).
- **chore(sandbox): public, framework-free `ape-sandbox` image** — the official
  workspace image is now **public and framework-free**, built from the separate
  public `exoport/ape-sandbox` repo (`ghcr.io/exoport/ape-sandbox`). The private
  APEX framework is no longer baked; `aped` mounts a pinned host-side checkout
  read-only at `/opt/apex-framework` at runtime (PLAN-20). `sandbox.DefaultImage`
  and the aped policy allow-list reference the public image.
- **docs:** PLAN-16 narrative + D4 egress reconciled to the shipped `ape`/`aped`
  split; the sandbox mount / egress / toolchain roadmap captured as PLAN-20 /
  PLAN-21 / PLAN-22.

## v0.0.45 (2026-07-14)

- **feat(pipeline): activity-aware step completion — progress-anchored idle
  window + hard cap (PLAN-19)** — an interactive step (pipeline stage step,
  `ape task`, `ape prompt`) is now cancelled only when it genuinely stops making
  progress, not merely because the bridge emitted no hook for a fixed window.
  The idle anchor resets on any of three signals: bridge hooks (unchanged), the
  active claude transcript growing (size/mtime, plus the transcript directory's
  mtime so a `/clear` session rotation counts as activity), and — on the
  `ape prompt` path — PTY output bytes. A new hard wall-clock ceiling
  `--max-duration` (default **3h**, `0` disables) bounds a stuck-but-noisy step
  and reports a distinct `max-duration exceeded` termination. `--idle-timeout`
  is now wired onto `ape pipeline` (the field existed but had no flag), and
  `--max-duration` onto `ape pipeline` / `ape task` / `ape prompt`. Terminations
  now carry a structured diagnostic — which limit tripped, the child claude
  process's liveness, and each progress source's age — instead of the old bare
  "idle for X without Stop hook". The poll cadence is 30s for the first hour of
  a step, then 60s. The smart wait loop lives once in `internal/sessiondriver`;
  both `interactiveCore` (pipeline/task) and the prompt Driver inherit it. The
  termination diagnostic reports the claude process's liveness on the
  pipeline/task path too (not just `ape prompt`) — `interactiveCore` installs a
  child-liveness probe from the stage's PTY session, so a killed step reads
  `child pid N alive|exited` instead of `child liveness unknown`. (The
  PTY-output progress anchor stays `ape prompt`-only by design: on the
  pipeline/task path transcript growth carries the anchor, and a raw-PTY anchor
  would risk masking a real stall behind the REPL's cosmetic repaints.)

- **feat(service): `ape service` now dispatches `prompt.run` and `script.run`
  (PLAN-14)** — the two remaining job-daemon endpoints spawn real headless `ape`
  children now that `ape prompt` (PLAN-12) and `ape script` (PLAN-15) exist.
  `prompt.run` maps `{prompt | handoff, agent?, model?, workflow?}` to
  `ape prompt` (exactly one of `prompt`/`handoff`). `script.run` maps
  `{script_path | script_source, script_args?}` to `ape script`, gated by two
  `service.yaml` flags (both default off): `script_path` must resolve to a file
  **inside an allowlisted root**; inline `script_source` (piped to `ape script -`
  on stdin, never onto the argv) is rejected `VALIDATION` unless
  `allow_script_source: true`; `force_script_sandbox: true` forces
  `ape script --sandbox` onto every script job. Field-to-flag mapping stays
  strict and typed — request fields are never concatenated into a shell string.
  Docs: `docs/how-to/run-ape-as-a-service.md`, `docs/reference/events.md`.

- **feat(script): new `ape script <file.go>` — yaegi-interpreted orchestration
  scripts (PLAN-15)** — `ape script ops/nightly.go -- args…` runs a plain Go
  file in-process under the [yaegi](https://github.com/traefik/yaegi)
  interpreter with a new public library, `apescript`
  (`github.com/exoport/apex_process_ape/apescript`), injected. A script defines
  `func Main(ctx context.Context) error`; ape evaluates the file then calls
  `Main`. SIGINT cancels `ctx`; a returned error or a recovered panic (reported
  with the yaegi source-position stack) exits 1; a compile error reports
  `file:line` and exits 1 **before any claude spawns**. `ape script -` reads the
  source from stdin; everything after `--` is exposed as `apescript.Args()`.
  The v1 `apescript` surface: `RunPipeline`/`RunTask`/`RunPrompt` (thin
  PTY-backed facades over the exact runners the CLI uses), `ReadManifest`,
  `ScanTranscript`, `Skills`, `Log` (respects `--quiet`), `Args`,
  `PublishEvent` (identity-stamped `ape.evt.<user>.<project>.script.<run-id>.<event>`
  only) and `PutBlob`. Default mode is unrestricted (full stdlib, arbitrary
  trusted code); `--sandbox` uses yaegi's restricted symbol set — blocking
  `os/exec`, `os.Exit`, `syscall`, `unsafe` — while the apescript orchestration
  functions stay available in both modes. `--output-format json|yaml` wraps the
  invocation in `{result, duration, cost_usd}`. The interpreter symbol table is
  generated (`make apescript-symbols`) and committed under
  `internal/apescriptsym/`. Docs: `docs/how-to/write-ape-scripts.md`,
  `docs/reference/apescript.md`. **Binary size** grows ~13.5 MB (yaegi + its
  stdlib symbols): the release `ape` binary went from ~37.6 MB to ~51.2 MB —
  accepted for a single-binary tool per PLAN-15.

- **feat(prompt): new `ape prompt` — prompt/handoff-driven Claude session** —
  `ape prompt [<text>]` (or `ape prompt --handoff <file>`) drives one
  unattended Claude Code session end-to-end through the in-process PTY: it
  spawns claude under the ape bridge, delivers the prompt, detects completion
  via the Stop hook (with `--idle-timeout` and process-death backstops),
  scans per-model telemetry, copies the transcript, and writes a
  `prompt.yaml` session record under `_output/ape/prompts/<prompt-id>/`.
  Exactly one of the positional `<text>` or `--handoff` must be given.
  `--agent A` fronts the session with the PAT-25 `/A --autonomous -- …`
  prefix; `--ultracode` prepends the ultracode keyword and `--workflow`
  appends a run-via-workflow directive (independent, composable). It makes
  no commits of its own. `--output-format human|json|yaml` emits a
  `{prompt_id, status, duration, cost_usd, per_model, transcript_paths,
  session_id}` envelope. Exit codes: 0 completed · 1 idle-timeout/failed ·
  2 preflight (no `_apex/config.yaml`, unresolved `--agent`, missing
  `--handoff`) · 3 REPL never ready · 4 claude died before Stop. Prompt
  sessions fold into a new `prompts` bucket in `ape costs` (readable per
  session with `ape costs prompt <id>`).
- **refactor(sessiondriver): extract the reusable interactive-session slice** —
  the transcript-binding + telemetry-scan machinery (main-session delta,
  sub-agent sessions, the v0.0.34 double-count guard, the dropped-SubagentStop
  robustness sweep, durable snapshots) plus Stop-hook step-done signalling
  with an idle backstop now live in `internal/sessiondriver`, shared by the
  pipeline interactive runner and `ape prompt`. Pipeline behavior is
  unchanged.

## v0.0.44 (2026-07-12)

- **feat(update): cosign-verify self-updates and drop `golang.org/x/crypto/openpgp`** —
  `ape update` now downloads the platform archive plus the release's signed
  `ape_checksums.txt` and its Sigstore bundle, and **verifies before applying**:
  the bundle is cosign-verified fully offline against an embedded Sigstore
  public-good trusted root, pinning this repo's `release.yml` workflow identity
  and the GitHub Actions OIDC issuer for the resolved tag; then the archive's
  SHA-256 is checked against the now-trusted manifest. Any failure — or a
  release with no signature bundle — aborts before the running binary is
  touched. Previously the updater linked
  `github.com/creativeprojects/go-selfupdate` (which hard-imports the frozen
  `golang.org/x/crypto/openpgp`) but set no validator, so `ape update` verified
  nothing. The self-update path now uses `github.com/sigstore/sigstore-go`
  (pure-Go verification) + `github.com/minio/selfupdate` (binary swap); openpgp
  is no longer in the compiled binary. `ape update`, its `--output-format`, the
  background update-available check, `ape doctor`'s update check, and
  `ape rollback` are unchanged.
- **build(release): ship `ape_checksums.txt.bundle`** — releases now sign the
  checksums file as a self-contained Sigstore bundle (`cosign sign-blob
  --new-bundle-format`: Fulcio cert + signature + certificate-transparency SCT
  + Rekor inclusion proof in one file), so `ape update` and `cosign verify-blob
  --bundle … --new-bundle-format` verify it offline. Supersedes the detached
  `ape_checksums.txt.sig` + `ape_checksums.txt.pem` shipped through v0.0.43 (see
  [docs/how-to/verify.md](docs/how-to/verify.md) for verifying older releases).
- **chore(ci): remove the `GO-2026-5932` govulncheck allow-list** — with openpgp
  out of the build graph the advisory is no longer flagged, so
  `scripts/govulncheck-gate.py` now runs with **zero** allow-listed exceptions.

## v0.0.43 (2026-07-12)

- **chore(security): bump Go toolchain to 1.26.5** — clears `GO-2026-5856`
  (crypto/tls ECH privacy leak) and `GO-2026-4970` (os symlink root-escape) from
  the shipped binaries.
- **chore(ci): scope the govulncheck gate with a documented allow-list** —
  `GO-2026-5932` (`golang.org/x/crypto/openpgp`, "unsafe by design", `Fixed in:
  N/A`) is pulled transitively by `github.com/creativeprojects/go-selfupdate`;
  `ape update` sets no PGP validator so it is linked-but-unused. A new
  `scripts/govulncheck-gate.py` allow-lists exactly that advisory (with
  justification) while still failing CI on any other or new vulnerability.
  Tracked follow-up: replace the self-update path with a cosign-verifying
  updater to drop openpgp entirely — **done in v0.0.44**, which removed
  go-selfupdate and retired this allow-list entry.
- **fix(portability): green the Windows `go test` job** — the first CI run over
  the full Phase-2 history surfaced Windows-only test failures in Linux-only
  components. Guest bind paths now use `path.Join` (POSIX) instead of
  `filepath.Join` (which emitted backslashes on Windows); the `ape service`
  allow-list missing-entry error is portable (`fs.ErrNotExist` → "does not
  exist") rather than asserting OS-specific stat text; and three inherently
  Linux tests (systemd ProtectHome hint, egress-proxy `os.Pipe` deadline, Unix
  0600 mode bits) skip or guard on Windows.

- **feat(framework): always-on operating-rules fragment + managed `CLAUDE.md`
  block (PLAN-47 Workstream C)** — `ape framework setup`/`update` now install a
  framework-maintained `_apex/apex-operating-rules.md` fragment and ensure the
  repo-root `CLAUDE.md` imports it inside a marker-delimited managed block
  (`<!-- apex:managed:begin -->` … `<!-- apex:managed:end -->`), so every Claude
  Code session in the project loads the APEX discipline rules with no hooks and
  no manual playbook hand-over. Content outside the markers is preserved
  byte-for-byte; an absent `CLAUDE.md` is created silently (headless-safe); the
  refresh is idempotent (a steady-state `update` leaves `CLAUDE.md`
  byte-identical). The `apex-orchestrator` persona skill installs via the
  existing generic skill path.

  **Version-skew safe (option B):** if the framework checkout predates the
  fragment, setup/update skip fragment + block management with a warning rather
  than failing — so this ape release is independent of the framework release
  order. `ape doctor` gains required-but-self-gating checks
  (`operating_rules.fragment` / `.import` / `.orchestrator_skill`): they hard-fail
  only when a managing install has lost the artifacts, and degrade to INFO/WARN
  outside a project, on legacy installs, or against an older framework — so a
  plain repo stays green. `framework.yaml` records `sources.operating_rules.managed`.
  An opt-in live smoke test (`APE_OPRULES_LIVE=1`) spawns a real `claude -p`
  session to prove the managed `@import` actually resolves at runtime — the
  semantic guarantee the syntactic doctor check can't give.

## v0.0.42 (2026-07-12)

Large feature drop — everything on `feat/plan-18-phase2-aped` since v0.0.41
(PLAN-10 / 13 / 14 / 16 / 17 / 18). Headline: hardware-isolated Kata VM
workspaces driven by a new rootful daemon (`aped`), plus NATS telemetry /
reporting and a job daemon.

- **feat(aped): rootful Kata-QEMU VM-management daemon (PLAN-18 Phase 2)** — a
  new single-binary daemon (`cmd/aped`) that provisions and operates
  hardware-isolated Kata microVMs. It runs as a **two-process split**: a root
  **executor** that is "root without power" (empty `CapabilityBoundingSet`,
  `RestrictAddressFamilies=AF_UNIX`, `@mount` denied, `ProtectHome`/
  `ProtectSystem=strict`), reachable only over an AF_UNIX **SO_PEERCRED-gated**
  priv socket; and a de-privileged **front** that embeds NATS and serves the
  frozen `ape.vmm.<node>.>` micro-service contract. A **default-deny policy**
  authorizes every fully-resolved command — allowed image refs, host-fs mount
  roots re-checked after symlink resolution, resource ceilings, and a device
  allow-list. Every privileged op is audited to an append-only log and forwarded
  on `ape.audit.<node>.>` (open **and** completion records for interactive
  sessions). Ships with hardened systemd units, tmpfiles, policy, auditd rules,
  and `deploy/tier2-setup.sh` (an idempotent Kata host-stack provisioner). See
  `docs/how-to/run-aped.md`.

- **feat(sandbox): `ape sandbox` is now a thin `aped` client** — the PLAN-16
  daemonless runner is retired; `ape sandbox up/ls/inspect/exec/attach/freeze/
  unfreeze/suspend/down` drive `aped` over the `ape.vmm` NATS contract and `ape`
  never runs as root. Interactive `exec`/`attach` stream stdio over per-session
  NATS subjects with credit-based flow control (`internal/vmmstream`): a real
  guest PTY, SIGWINCH resize, exact exit-code propagation, and abandoned-session
  reap (client keepalive + server idle watchdog + guest-exec kill). An opt-in
  `aped run --driver containerd` provisions through the containerd Go client
  **without** a client-side rootfs mount, so the full lifecycle runs through the
  hardened executor; a host-fs mount under `ProtectHome` now fails with an
  actionable error (use a root outside `/home`, a `BindPaths=` drop-in, or
  `--mount ephemeral|volume`).

- **feat(sandbox): Kata VM workspace mechanics (PLAN-16 Phase 1)** — the
  server-side building blocks `aped` composes: per-workspace `~/.claude` +
  git/authorized_keys composition, a CONNECT egress-proxy supervisor, an OCI/
  nerdctl command builder, and the official `ape-sandbox` workspace image
  (digest-pinned base, offline framework).

- **feat(reporting): NATS telemetry + reporting (PLAN-13 / PLAN-17)** — an
  optional NATS connection with credential-derived identity (`internal/natsconn`);
  a fire-and-forget progress-event publisher (`internal/eventing`) and
  content-addressed transcript blob upload (`internal/blobstore`); runs publish
  progress events and upload transcripts; and `ape event | log | metrics |
  transcript` report over NATS with a four-step Claude-session resolver. Per-VM
  telemetry credentials reuse the existing `ape.{evt,log,metrics}` roots.

- **feat(service): `ape service` job daemon (PLAN-14)** — a NATS-micro job daemon.

- **feat(cost): date-aware pricing + per-turn dedup (PLAN-10)** — cost accounting
  with per-turn deduplication and date-aware model pricing.

## v0.0.41 (2026-07-08)

- **chore: module + repo rename to `exoport/apex_process_ape`** — the Go module
  path and all repo references moved to `github.com/exoport/apex_process_ape`.
- **feat(release skill): autonomous mode** — the `/release` skill gained an
  autonomous mode that drives the pre-tag flow end-to-end.

> Retro-filled: v0.0.41 was tagged without a CHANGELOG entry.

## v0.0.40 (2026-07-05)

- **feat(cli): `ape task --handoff <file>`** — a shorthand for `--prompt`
  that points a task at a resumable handoff/context document instead of
  requiring the caller to type its prompt inline. It checks the file
  exists and derives the prompt `Read <abs-path> and follow the Resume
  Protocol inside it.` — the same continuation prompt the `/handoff`
  skill already suggests when it writes one. It forwards a pointer to
  the file rather than inlining its contents: the prompt is typed into
  the `claude` REPL as literal PTY keystrokes, so a multi-line value
  would risk submitting early. Mutually exclusive with `--prompt`, and
  still requires `--prompt-flag` to actually reach the skill (exit code
  2 on either misuse, or when the file doesn't exist). Docs updated:
  `docs/how-to/run-a-single-skill.md`, `docs/reference/cli.md`.

## v0.0.39 (2026-07-04)

- **fix(cli): `ape chat` no longer fails silently** — its `RunE` returned
  a bare `error` on the two preflight checks (unresolvable cwd, missing
  `_apex/config.yaml`), and `cmd/ape/main.go` discarded whatever
  `Execute()` returned without printing it. Combined with the root
  command's `SilenceErrors: true`, the result was `exit 1` with zero
  output — e.g. running `ape chat` outside an APEX project gave no clue
  why. Fixed at both layers: `chat.go` now prints the message and exits
  with the correct code from the shared table (`ExitUsage` for the two
  preflight checks, `ExitRunFailed` for a `runChat` failure), matching
  the `task`/`pipeline` convention; `main.go` now prints any error that
  still escapes `Execute()` as a last-resort net, so a future command
  that forgets to self-report can't fail silently either.

Framework repo layout: `ape framework setup` / `update` / `status --repo`
now read the **released** framework layout, where `.claude/` and `_apex/`
sit at the repo root — matching the shape a project consumes. The
earlier build layout that nested these under a `framework/` subfolder
(`framework/_claude`, `framework/_apex`) is no longer supported.

- **fix(framework): target root layout of `apex_process_framework`** — the
  four `Subtree*` path constants in `internal/framework/layout.go` now
  point at `.claude/skills`, `_apex/pipelines`, `_apex/config.yaml`, and
  `_apex/config.local.example.yaml` (previously `framework/_claude/…` /
  `framework/_apex/…`). This follows the framework's move from a build
  repo (assets nested under `framework/`) to a released repo (assets at
  the root, alongside the usage docs). Against a released checkout,
  `ape framework setup`/`update`/`status --repo` previously failed layout
  validation with `framework_layout_invalid: missing or non-directory:
  <repo>/framework/_claude/skills`; they now install correctly. **Breaking
  for anyone still pointing `$APEX_FRAMEWORK_REPO` at an old build-layout
  checkout** — repoint it at a released checkout. Verified end-to-end
  against `apex_process_framework` HEAD (89 skills, 8 pipelines). Docs and
  the generated CLI reference updated to match.

## v0.0.37 (2026-07-04)

Telemetry: ephemeral cache-write split (PLAN-10 D1), shipped **additively
under manifest `schema_version: 2`** — no schema bump, so the eval reader
and every archived v2 manifest keep working unchanged. Plus docs
completion for PLAN-9 F4 (generated CLI reference, first tutorial, docs
link-check gate).

- **feat(cost): ephemeral cache-creation 5m/1h split (PLAN-10 D1)** — the
  run manifest, per-step and per-model `model_usage` blocks, and the `ape
  task --output-format json` envelope now carry
  `tokens_cache_creation_5m` / `tokens_cache_creation_1h` (envelope:
  `cache_creation_5m_input_tokens` / `cache_creation_1h_input_tokens`)
  alongside the existing summed `tokens_cache_creation`. The two tiers
  price differently (5m ≈ 1.25× base input, 1h ≈ 2.00×); the transcript
  scanner already parsed and priced them per-tier, this just stops
  collapsing the breakdown on the way out. **Additive and non-breaking:**
  the summed field is byte-for-byte unchanged and stays equal to 5m + 1h,
  `schema_version` remains `2`, and consumers that only track total cache
  creation need no change. The eval's manifest reader
  (`apex_process_framework_eval`) hard-rejects any `schema_version`
  outside `[1,2]` but tolerates unknown fields, so this was deliberately
  shipped as added v2 fields rather than a v3 bump (see PLAN-10 D5).
- **docs: generated CLI reference (`docs/reference/cli.md`)** — a new
  hidden `ape gen-docs` command renders a single-file reference for every
  visible command, flag, and default straight from the cobra command
  tree; `make docs-cli` regenerates it. Hand-rolled (no `cobra/doc`
  dependency — avoids pulling in `go-md2man`) and deterministic (no
  timestamps), so the checked-in file can't drift from the code. The
  command is hidden: it stays out of `ape --help` and changes no other
  command's behaviour.
- **docs: first tutorial (`docs/tutorials/first-pipeline.md`)** — a
  guided greenfield walk-through: install → `ape doctor` → `ape framework
  setup` → `ape pipeline design` → read `_output/pipelines/<run>/` →
  `ape costs`.
- **build: docs link-check gate** — `scripts/check-docs-links.py`
  verifies every file under `docs/` is reachable from `docs/README.md`
  and that no relative link is dead (skips fenced code blocks; resolves
  directory links to their `README.md`). Wired into `make ci-local` as a
  `docs-check` step, with a standalone `make docs-check` target. Fixed
  what it surfaced on first run: a dead `authoring-pipelines.md` link and
  four orphaned docs (`run-artefacts`, `interactive-vs-programmatic`,
  `cli`, `first-pipeline`), now indexed across the docs README files.

## v0.0.36 (2026-07-03)

PTY-only consolidation + CLI hygiene (PLAN-9) and per-model cost
reporting (PLAN-10 D5).

- **feat(cli)!: remove the programmatic exec axis — interactive PTY is
  the only way ape runs claude** (BREAKING). Deleted the non-PTY
  `claude -p` path (`pipeline.runStages` / `buildArgv` / `runClaude` and
  the stream-json stdout parser) and the flags `-P`/`--programmatic`,
  `-I`/`--interactive`, and `--eval`. Every pipeline and `ape task` run
  now executes claude as a REPL inside an in-process PTY. The pipeline
  invocation matrix collapses from (UI × Exec) to a single UI axis:
  `--tui` (default), `--web`, `--no-tui`. Passing a removed flag errors
  with a pointer to `docs/explanation/why-pty-only.md` and exits 2. The
  removed `--eval` path was the eval harness's old entry point; the eval
  already migrated to `ape task --output-format json` + `ape pipeline
  --no-tui` (audited 2026-07-03), so nothing depends on it. The resolved
  rendering mode now prints on every pipeline start.
- **fix(cli): `ape --version`** — the root command exposed no version
  flag; it now prints the build version (set at Execute time so it
  reflects the build-info backfill).
- **fix(cli): `ape update` human output** — `printUpdateResult`
  type-asserted against an anonymous struct that never matched the
  function-local result type, so the human path always fell through to a
  raw `%v` dump. The result type moved to package scope; the switch now
  renders the `current:` / `latest:` / `message:` lines. Regression test
  added.
- **feat(cli): output-format + help consistency (PLAN-9 F3)** —
  `--output-format human|json|yaml` added to `pattern list`/`validate`,
  `adr list`/`validate`, `trait validate`, `sessions`, and `sessions
  prune`. Unimplemented stubs (`pattern sync`, `sync patterns`, `sync
  adrs`) are hidden so `ape --help` no longer advertises "not yet
  implemented" commands. `bootstrap --no-tui` is renamed `--no-picker`
  (old flag kept hidden as a one-release deprecated alias). New
  `internal/apecmd/exitcodes.go` is the single source of truth for exit
  codes (0 ok · 1 run failed/idle · 2 usage/preflight · 3 REPL never
  ready). The background update check is skipped for hidden/utility
  commands (`mcp-bridge`, `notify`). Root help now leads with
  pipelines/task/chat; `Example:` blocks added across the touched
  commands.
- **feat(cost): per-model rollup + `ape costs run`/`chat` (PLAN-10 D5)**
  — the cost rollup and `ape costs` gain a per-model breakdown: the
  human output has a new "by model" table and `--output-format json`
  includes a `per_model` map (project-wide and per pipeline/task bucket).
  `ape costs run <run-id>` (reads a run's manifest, with per-model
  detail) and `ape costs chat <chat-id>` (reads session.yaml) are now
  implemented and registered — both were advertised in the help but
  never wired.
- **fix(cost): `sumTotals` dropped `NumTurns`** — rollup and per-day
  totals summed cost and the four token fields but silently omitted turn
  counts. Fixed so rollup totals carry turns.
- **docs: PTY-only rationale + reference refresh** — new
  `explanation/why-pty-only.md` (linked from the removed-flag error);
  claude-spawn-modes / invocation-matrix / exec-modes updated for the
  collapsed UI axis; manifest reference documents the additive per-model
  fields (`totals.num_turns`, `totals.model_usage`, step `model_usage` /
  `sessions[]`; schema stays v2).
- **note:** PLAN-10 D1 (per-turn `TurnRecord`, requestId/stop_reason
  dedup, 5m/1h cache-tier split) and D3 (date-aware pricing) remain
  deferred — D1 would perturb the v0.0.35-validated telemetry math and
  D3 depends on D1's per-turn timestamps; the standard-rate price table
  is the documented conservative fallback. The
  `cost-discrepancy-20260521` investigation is updated with the v0.0.35
  sub-agent finding (partially explains the gap; tier falsification still
  needs D1).

## v0.0.35 (2026-07-03)

- **fix(telemetry): capture real sub-agent transcripts; end the
  2×-main double-count** — a step that spawned sub-agents recorded
  exactly twice the main session's usage while the actual sub-agent
  turns were never scanned. Root cause: the `SubagentStart/Stop` handler
  tracked a "sub-session" using the hook's `transcript_path`, which is
  the **parent** session, keyed by `session_id`, which is **also** the
  parent's (a sub-agent's internal `sessionId` equals its parent's) — so
  every sub collapsed into one phantom capture pointing at the main
  transcript, folding main a second time (the precise 2×-main
  signature). The real sub-agent transcripts
  (`<sid>/subagents/agent-<id>.jsonl`) were never read; their path is
  the `agent_transcript_path` field, which ape did not parse. Measured
  on a 6-sub-agent batch-dev run: ape reported 72 turns / 5.67M tokens;
  the true total is 267 turns / 22.9M tokens (main 36 + six subs 231).
  Fix: parse `agent_transcript_path` + `agent_id`; capture sub-sessions
  on `SubagentStop` via `agent_transcript_path`, keyed by `agent_id`
  (the only distinct per-sub identifier), each folded exactly once; the
  step total is now main + Σ subs, and the main `sessions[]` record
  reports main-only usage. A **double-count guard** never folds a sub
  whose resolved transcript equals the main/active transcript (belt-and-
  suspenders against future hook-shape drift — the exact bug signature).
  A **robustness sweep** at step end enumerates the main session's
  `subagents/` dir (mtime-scoped to the step) and folds any sub a dropped
  `SubagentStop` would have lost. Sub transcripts are copied into the run
  dir alongside the main one (durable through `~/.claude` rotation).
- **test: sub-agent regression lock** — the integration-shaped guard the
  CI never had: fixtures plant a main transcript + N `SubagentStop`
  events whose `transcript_path` all point at main (the real claude
  shape) with distinct `agent_transcript_path`s, asserting the step total
  equals main + Σ subs, is **not** 2×main, and `sessions[]` lists the
  main plus one entry per sub with distinct ids (not the parent id).
  Additional tests pin the double-count guard and the dropped-Stop sweep.

## v0.0.34 (2026-07-02)

- **refactor(telemetry): collapse the misdiagnosis-era capture layers**
  — with the v0.0.33 env scrub in place the spawned claude is a
  top-level session and its transcript persists at the normal path, so
  the v0.0.28 parent-side scan is sufficient. Removed the machinery
  built to copy a file the env-leak prevented from existing (~760
  lines net): the v0.0.32 hook-side capture (`APE_SNAPSHOT_DIR`
  injection, per-hook file I/O in `ape notify`, dual-source scan
  logic, per-run temp dir), the v0.0.30 tool-hook incremental
  snapshots (`AppendTranscript`/offset tracking in FeedHook), the
  v0.0.31 `syncStopCopy`, and the snapDiag counters that instrumented
  those paths. The telemetry path is now: scrub env → claude persists
  transcript → parent scans it → per-model/per-session telemetry.
  One source, one scan, one diagnostic — with less per-hook overhead.
  Kept: the env scrub (root-cause fix), the transcript scan with
  model_usage + per-session records, the `telemetry_note` breadcrumb
  (simplified to source path / presence / line-count / scan error —
  enough to name any future zero), and the durable per-step transcript
  copy into the run dir (one local copy per step, post-scan).
- **fix(manifest): `totals.num_turns`** — per-step `num_turns` now
  sums into run-level totals like the token fields (previously
  per-step showed turns while totals stayed unset).
- **test: guards realigned to the single-path design** — the
  bridge-delivered integration guard now runs under the nested context
  (`CLAUDECODE=1`, the environment every zero-telemetry run had) and
  asserts non-zero telemetry + model_usage + no note + the durable
  run-dir copy through the real notify→IPC→dispatch path. The
  CLAUDECODE-scrub guards (unit + session-spawn) stay; tests
  exercising the removed snapshot machinery are deleted; new
  missing-source-note test pins the diagnostic that would catch any
  genuine future persistence regression.

## v0.0.33 (2026-07-02)

- **fix(repl): scrub `CLAUDECODE`/`CLAUDE_CODE_*` from the spawned
  claude's environment** — the verified root cause of the entire
  zero-telemetry saga. `repl.NewSession` never set `cmd.Env`, so the
  child claude inherited ape's full environment; when ape itself runs
  inside a Claude Code session (ubiquitous in dev — and the context of
  every zero-telemetry run), the inherited `CLAUDECODE=1` and
  `CLAUDE_CODE_*` markers (notably `CLAUDE_CODE_CHILD_SESSION`) make
  claude-code treat the spawn as a nested/child session and **suppress
  session-transcript persistence** — the
  `~/.claude/projects/<cwd>/<sid>.jsonl` that all telemetry machinery
  (v0.0.28–32) scans was never written. Proven: strip the markers →
  transcript persists and usage is captured; keep them → zero. Fix:
  `repl.ScrubClaudeCodeEnv` removes `CLAUDECODE`, the whole
  `CLAUDE_CODE_*` family, and `CLAUDE_EFFORT` (so the child's effort
  comes from ape's flags, not the parent session), keeping
  `ANTHROPIC_*` auth and everything else. Applied at the single PTY
  spawn point (`NewSession`, covering pipeline `--no-tui`/`--tui`/
  `--web` and `ape task`) and at `ape chat`'s inherited-stdio spawn.
- **feat(manifest): stamp `claude_version`** — the resolved
  `claude --version` is recorded on every run manifest (best-effort,
  additive to schema v2). claude-code auto-updated 2.1.198→2.1.199 mid-
  investigation and its trust-dialog/transcript behavior shifts across
  versions; telemetry and repro are now attributable to the exact
  version that ran.
- **test: nested-context regression guards** — CI never runs under
  `CLAUDECODE`, which is why three green suites shipped alongside
  failing live runs. New guards reproduce the nested context
  explicitly: a unit test pins the scrub (markers removed,
  `ANTHROPIC_API_KEY` and prefix-lookalikes retained), and a
  session-spawn test sets `CLAUDECODE=1`/`CLAUDE_CODE_CHILD_SESSION`
  on the test process and asserts the spawned session's `cmd.Env`
  contains none of the stripped keys — the leak cannot reach the child
  regardless of how ape was launched.

## v0.0.32 (2026-07-02)

- **fix(telemetry): capture the transcript HOOK-SIDE, in `ape
  notify`** — the definitive fix for zeroed interactive telemetry.
  Five prior attempts read the session file from the wrong process at
  the wrong time: claude's Stop hook only blocks until `ape notify`
  exits, so even v0.0.31's "synchronous" parent-side Stop copy raced
  turn-end deletion (the IPC frame is processed asynchronously). Now
  the capture happens inside `ape notify` itself — the hook process
  that runs in claude's turn, the only context where the transcript is
  guaranteed resident:
  - the hook commands gain `APE_SNAPSHOT_DIR=<dir>` alongside
    `APE_BRIDGE_PORT` (settings builder), pointing at a per-run
    capture directory;
  - `Stop`/`SubagentStop` (sync, claude blocks): a **full atomic
    copy**, guaranteed complete before claude proceeds — the primary
    guarantee;
  - UPS / Pre/PostToolUse / SubagentStart: **incremental append**
    anchored on the destination size (no per-tool-call re-copy; the
    accumulated copy survives mid-turn deletion); the Stop-time full
    copy rewrites atomically, so the post-Stop scan always sees a
    clean artifact;
  - capture runs BEFORE the bridge dial, so the snapshot lands even
    when the bridge is unreachable.
  `StepTelemetry` reads the hook-written snapshot (keyed by
  session_id) in preference to the ephemeral source, for main and
  sub-agent sessions, and persists it into the run dir's
  `transcripts/` as the durable artifact. The v0.0.31 parent-side
  capture and snapDiag diagnostics remain as fallback + residual-edge
  reporting. Wired for `--no-tui`, `--tui`, and `--web` interactive.
- **test: deletion-at-dispatch-time regression guard** — the previous
  integration test deleted the source only after parent delivery, so
  it passed while real runs failed. The new guard deletes the source
  immediately after each hook dispatches (before the parent handles
  the frame) with the parent-side writer disabled entirely — the
  hook-side snapshot is the only possible artifact — and asserts it
  exists, is complete, and yields non-zero telemetry with model_usage
  and no note. A second guard proves the Stop-hook full copy alone
  suffices even when the bridge is unreachable and all incremental
  appends never ran.

## v0.0.31 (2026-07-02)

- **fix(telemetry): guaranteed synchronous transcript copy at Stop** —
  v0.0.30's tool-hook snapshots never landed in live runs
  (`snapAttempts=0`) despite the session file demonstrably existing and
  growing at every hook. The Stop hook is synchronous (claude blocks on
  it), so the transcript is guaranteed resident while FeedHook handles
  Stop: ape now performs a full `SnapshotTranscript` copy inline in the
  Stop case — independent of the incremental tool-hook path — for the
  main session and every tracked sub-session, re-anchoring the append
  offsets afterward. The Stop envelope itself can seed the transcript
  path, so the copy lands even if UPS and every tool hook failed to
  deliver a parseable path.
- **fix(telemetry): the snapshot path is now self-reporting** — the
  failure survived four releases because `AppendTranscript` errors were
  swallowed. Every guard, routing decision, and copy on the snapshot
  path is now counted per step (`snapDiag`: tool-hook cases seen,
  no-path/no-step guard failures, routed main/sub/new, append
  successes/attempts, stop-copies, last error) and the summary is
  folded into `telemetry_note` whenever telemetry is zero or missing —
  one run yields an exact diagnosis in the artifact, no debug build.
- **fix(telemetry): tool hooks adopt unmatched sessions instead of
  dropping them** — a tool hook whose session doesn't match the
  UPS-time main session no longer falls through silently: an unset main
  session is adopted (UPS may fire before the transcript exists), and
  an unseen session_id is auto-tracked as a sub-session (its
  SubagentStart may have been missed).
- **test: bridge-delivered integration guard** — the prior unit tests
  called FeedHook directly and passed while real runs failed; the new
  guard drives the REAL delivery pipeline (`ape notify` → TCP → IPC
  framing → BridgeRuntime dispatch → FeedHook) through the observed
  lifecycle (UPS before the file exists → tool hooks while it grows →
  Stop while present → deleted before the deferred scan) and asserts
  snapshots landed, telemetry is non-zero with model_usage, and no
  telemetry_note. A second guard pins the Stop-envelope-seeds-path
  fallback.

## v0.0.30 (2026-07-02)

- **fix(telemetry): capture the interactive transcript during its live
  window (Pre/PostToolUse), not just at UPS/Stop** — interactive steps
  still zeroed with `telemetry_note: transcript unavailable at scan
  time (… gone, no snapshot)`. Root cause: claude removes the
  interactive session's JSONL from `~/.claude/projects/` before the
  Stop hook, so both the UPS-time and Stop-time snapshots found nothing
  to copy and the run dir had no `transcripts/` copy at all. Fix: the
  run now snapshots the session transcript on **PreToolUse /
  PostToolUse** hooks — the only window the file demonstrably exists
  and is being appended — so the accumulated copy survives the source's
  mid-turn deletion. Sub-agent sessions snapshot on their tool hooks
  the same way (matched by session_id).
- **perf(runlog): incremental append-copy for transcript snapshots** —
  new `Writer.AppendTranscript` copies only the bytes past a tracked
  per-session offset (full re-copy only on truncation/rotation), so a
  step with dozens of tool calls over an MB-scale transcript doesn't
  produce O(n²) copy bytes. The snapshot accumulates across hooks and
  survives source deletion between them.
- **fix(telemetry): richer zero-capture diagnostics** — the
  `telemetry_note` now reports whether the scan used the live source or
  the snapshot, the line count, and the snapshot-attempt count, so a
  partial capture (missed only the final post-last-tool turn) is
  distinguishable from a total miss. Still warns on stderr; never a
  silent zero.
- **test: deletion-race regression guard** — a golden test simulates
  the real lifecycle (transcript present and growing across
  Pre/PostToolUse, deleted before Stop and before the deferred scan)
  and asserts a `transcripts/` snapshot exists, telemetry is non-zero
  with populated `model_usage`, and no `telemetry_note`. The
  incremental-copy primitive is pinned separately (accumulates without
  gap/overlap; survives source deletion). The v0.0.28/29 guards
  (unpriced-model-still-counts-tokens, verifier-skips-non-slash-UPS,
  bare-Enter dismissal) remain.

## v0.0.29 (2026-07-02)

- **fix(repl,bridge): folder-trust accept keystroke no longer breaks
  the interactive run in an untrusted dir** — a v0.0.28 regression.
  The trust-dialog was dismissed by typing `1` then Enter; in the
  interactive runtime that `1` surfaced as the first
  `UserPromptSubmit`, and its async hook could race past `BeginStep`
  into the step-contract window, where the verifier consumed it as the
  skill prompt (`got "1"`) and failed the stage in ~1.4s. Any pipeline
  or task run started in a fresh (untrusted) directory hit this. Two
  independent fixes:
  - **Dismiss with a bare Enter.** Option 1 ("Yes, I trust this
    folder") is preselected and the dialog shows "Enter to confirm",
    so Enter alone accepts it — no `1` selection keystroke that could
    leak as a prompt. Eliminates the leak at the source, timing-
    independent.
  - **Verifier skips non-slash-command UserPromptSubmit events.** A
    skill invocation always begins with `/`; a dismissal keystroke or
    menu artifact does not. The step-contract verifier now ignores any
    in-window UPS whose prompt isn't a slash command (defense in depth
    against any future onboarding-screen leak) and keeps waiting for
    the real skill prompt. Genuinely malformed (unparseable) payloads
    still hard-fail.
  - Regression guards: the verifier skips `1` / empty / `y`
    keystrokes and still matches the subsequent skill command; the
    trust-dialog dismissal asserts a bare Enter with no selection
    keystroke.

## v0.0.28 (2026-07-02)

- **fix(telemetry): interactive per-step metrics were zero on live
  runs** — `ape task` and `ape pipeline --no-tui` manifests showed
  `cost_usd: 0, tokens_input: 0, num_turns: 0`, tripping the eval's
  capture guard. Two distinct root causes, both fixed:
  - **P0a — transcript-read race.** The transcript was captured as a
    symlink into `~/.claude/projects/…`, and the source session file
    was observed gone/rotated by post-Stop scan time — a symlink to a
    removed target scanned after the fact yields nothing. Fix:
    transcripts are now **snapshotted (copied)** into the run dir on
    `UserPromptSubmit` and refreshed from the Stop-hook path while the
    child session is still resident; the telemetry scan prefers the
    live source and falls back to the snapshot. A step that still
    yields zero turns stamps a `telemetry_note` breadcrumb on the
    manifest and warns on stderr — never a silent zero.
  - **P0b — stale price table.** `claude-opus-4-8`, `claude-sonnet-5`,
    `claude-fable-5`, `claude-mythos-5` were missing from the price
    table, so even a successful scan cost $0 on current models. Added;
    `Lookup` now normalizes `[1m]`-style context-window suffixes and
    claude's short spawn aliases (`opus`, `sonnet`, …) onto the base
    model id.
- **feat(telemetry): per-model + per-session attribution** — the
  transcript scan now returns a per-model breakdown
  (`cost.ScanSession`), recorded as `model_usage` on each manifest
  step and on run totals (additive to schema v2), and as
  `model_usage` on the `ape task` JSON envelope. Sub-agent sessions
  (Agent tool) are captured via the `SubagentStart`/`SubagentStop`
  hooks — each sub-session's transcript is snapshotted and scanned,
  emitted as per-session records (`sessions: [{session_id,
  parent_session_id, model_usage, …}]`) and folded into the step's
  aggregate, restoring true per-agent token totals.
- **test: permanent eval-regression guards** — a checked-in golden
  transcript fixture in the exact live claude-code shape (nested
  `cache_creation`, `requestId`, duplicate `message.id`, `[1m]`
  model suffix) asserts non-zero tokens/turns/cost; an unpriced-model
  fixture locks in `Totals.Add`'s price independence (the exact
  invariant the zero violated); snapshot-fallback, sub-agent-session,
  manifest round-trip, and envelope tests pin the whole path the eval
  consumes so the zero cannot silently return.

## v0.0.27 (2026-07-02)

- **fix(repl): dismiss the folder-trust dialog; harden `WaitForReady`
  against modal false-ready** — claude-code (observed on 2.1.198)
  renders a folder-trust modal on first launch in an untrusted
  directory, and `--dangerously-skip-permissions` does not suppress it
  in interactive mode. The modal's menu item prints the `❯` glyph —
  exactly what the old `WaitForReady` treated as "REPL ready" — so the
  step prompt was typed into the modal and the run idled for the full
  60-minute timeout. Fix: (1) a modal registry in `internal/repl`
  (`blockingModals`) with a dismiss helper — the trust dialog is
  accepted by selecting option 1 explicitly, and future onboarding
  screens are a one-line registry addition; (2) `WaitForReady` now
  requires a real-REPL ready signal a menu item cannot satisfy (the
  `bypass permissions on` footer, or an empty `❯` prompt line); (3) on
  timeout the error is a `*repl.NotReadyError` carrying the last pane
  snapshot, so an unknown blocking modal fails fast at 30s with a
  readable diagnosis instead of a silent 1-hour stall.
- **feat(apecmd): `ape task <skill>` — single-skill interactive runs
  without pipeline YAML** (PLAN-11) — runs one framework skill through
  the same interactive PTY runner a pipeline step uses (preflight,
  bridge hooks, Stop-hook completion, transcript telemetry, manifest),
  with every parameter passed as a flag: `--agent`, `--model`,
  `--args`, `--prompt`/`--prompt-flag`, `--idle-timeout`. Commit
  control is two-layered: `--no-commit` maps to the framework skill's
  own no-commit functionality (slash-line layer, byte-parity with the
  pipeline convention), while the new opt-in `--task-commit ["<msg>"]`
  commits the complete task at the end (bare flag derives
  `ape:task/<skill>`; default off). `--output-format json` emits a
  stable result envelope on stdout (progress moves to stderr) shaped
  to replace the stream-json `result` event for eval-harness
  consumers: success, exit_code, duration, cost_usd, usage
  (input/output/cache tokens, num_turns), commits made during the run,
  and manifest path. Exit codes: 0 success · 1 run failure / idle
  timeout · 2 usage or preflight · 3 REPL never ready (last pane on
  stderr). Run artifacts land under `_output/tasks/<skill>/<run-id>/`;
  `ape costs` gains a `task:<skill>` bucket (rebuilt via
  `ape costs roll`). New exported seam:
  `pipeline.NewSingleStepSpec` synthesizes a one-stage/one-step spec so
  the runner, manifest, and commit machinery are reused unchanged.

## v0.0.26 (2026-07-01)

- **fix(bridge/mcp): negotiate the MCP `protocolVersion` instead of
  hard-coding it** — the `initialize` handler always answered with
  `protocolVersion: "2024-11-05"`. Current claude-code builds (2.1.197)
  send `initialize` with `"protocolVersion":"2025-11-25"`; receiving the
  stale/mismatched version back, claude-code never advanced to
  `tools/list` and the whole pipeline stalled at the first step. Fix:
  echo the client's requested `protocolVersion` back (standard MCP
  server behaviour — agree to the version the client asked for), with a
  `"2024-11-05"` fallback for clients that omit it. The bridge's tool
  surface is version-agnostic for this flow, so echoing the negotiated
  version is safe.

## v0.0.25 (2026-06-07)

- **fix(version): backfill version info from VCS when built via
  `go install` or `bingo`** — binaries built outside goreleaser showed
  `dev / unknown / unknown` for version, build date, and git commit
  because ldflags were never injected. Fix: `init()` in `version.go`
  calls `runtime/debug.ReadBuildInfo()` and backfills each field that
  is still at its default value from the embedded VCS settings
  (`vcs.revision`, `vcs.time`) and the module version
  (`info.Main.Version`). Goreleaser builds are unaffected — ldflags
  win and the defaults are never seen. The `v` prefix is stripped from
  `info.Main.Version` to match the goreleaser format (`0.0.25`, not
  `v0.0.25`).

## v0.0.24 (2026-06-01)

- **fix(pipeline): detect claude process exit immediately instead of
  idling for the full timeout** — when the claude REPL process exited
  unexpectedly between `WaitForReady` and the Stop hook (e.g., due to
  an API error or OOM on startup), `waitStepDone` had no mechanism to
  detect it and would block until `interactiveStepIdleTimeout` elapsed.
  Root cause: `repl.HasSession` and the session's `done` channel existed
  but were never wired into the pipeline runner. Fix: added
  `repl.SessionDone` (returns the session's `done` channel) and wrapped
  the stage context with `context.WithCancelCause` in
  `runStageInteractive`; a goroutine watches `SessionDone` and cancels
  the context immediately when the process exits, producing a
  `"claude process exited without Stop hook"` error in seconds rather
  than minutes. Also adds `APE_INTERACTIVE_DEBUG` pane capture on step
  failure so error output from crashed claude sessions is preserved.
- **fix(pipeline): raise interactive step idle timeout from 15m to
  60m** — skills that perform long single-turn generation with no
  intermediate tool calls (no Pre/PostToolUse hooks) were hitting the
  15-minute ceiling before the session-exit fix. The new ceiling gives
  legitimate heavy steps enough runway while the session-exit watcher
  handles the actual hung/crashed case fast.
- **feat(pipeline): `--from <stage>` flag** — skip all pipeline stages
  before the named one and start execution there. Useful for resuming a
  failed run without re-running already-completed stages. Invalid stage
  names are rejected before any stage executes, with an error listing
  the available names. Supported across all execution modes (TUI,
  no-TUI, web, programmatic).

## v0.0.23 (2026-05-28)

- **Release flow: rc-tag cycle removed** — earlier releases used a
  `vX.Y.Z-rcN` pre-release tag as the remote CI gate. When the rc
  and final annotated tags landed on the same commit, goreleaser's
  `git describe`-based tag resolution misrouted the build artifacts
  to the rc prerelease and no final GitHub Release was ever created;
  v0.0.20 and v0.0.21 both hit this. The new gate is the regular
  push-to-`main` CI run on the SHA you're about to tag — the final
  tag is the only annotated tag on the commit, so goreleaser cannot
  pick the wrong one. Updated:
  - `.github/workflows/ci.yml` — dropped the `v[0-9]+.[0-9]+.[0-9]+-*`
    push-tag trigger.
  - `.github/workflows/release.yml` — kept the job-level
    `if: !contains(github.ref_name, '-')` guard as belt-and-suspenders
    against the un-anchored push-tag glob.
  - `.claude/skills/release/SKILL.md` — rewrote phases 3–4 around
    push-to-`main` CI; added pre-flight check 1g rejecting HEAD with
    any pre-release tag; phase 6 also rejects releases landing on the
    wrong tag name (defense in depth).
  - `CLAUDE.md` and `docs/how-to/pre-tag-release.md` — rewrote the
    remote-gate step; added a short history blockquote explaining
    why the rc cycle was dropped.

## v0.0.22 (2026-05-28)

> v0.0.21 was abandoned mid-release: goreleaser running on the
> `v0.0.21` tag resolved the target tag to `v0.0.21-rc1` (both
> tags pointed at the same commit), uploaded the build artifacts
> to the rc prerelease, and no `v0.0.21` GitHub Release was ever
> created. The same misrouting affected `v0.0.20`. The release
> workflow and skill will be reworked in a follow-up so that the
> rc-tag CI gate cannot share a SHA with the final tag (or so
> goreleaser is pinned to the workflow's trigger tag).

- **TUI final error block now line-limited** —
  `internal/tui/pipeline.go` previously rendered the whole
  `m.finalErr.Error()` string as a single truncated line in the
  stages panel, so multi-line errors (e.g., wrapped failures from a
  pipeline step) collapsed into one ellipsised row. The final report
  now splits the error on newlines, renders up to 30 lines styled as
  failures, and appends `… (N more line(s))` when more remain. Cap
  lives in a new `maxFinalErrLines` constant.
- **`release.yml` job-level guard against rc tags** — v0.0.20
  narrowed the workflow tag filter to `v[0-9]+.[0-9]+.[0-9]+`, but
  GitHub's tag-filter globs are not end-anchored, so `v0.0.20-rc1`
  still matched and goreleaser ran against the rc tag. Added an
  `if: !contains(github.ref, '-')` guard on the release job so it
  is skipped for any tag containing a dash, leaving final-semver
  tags as the only trigger.
- **Release how-to skill** — new
  `.claude/skills/release/SKILL.md` automating the full
  pre-flight → `make ci-local` → rc tag → poll CI → final tag →
  poll release → cosign-verify flow documented in
  `docs/how-to/pre-tag-release.md`.

## v0.0.20 (2026-05-23)

### Pre-tag verification workflow

Two new gates make it possible to catch the v0.0.18 → v0.0.19 class of
failure (Windows-only test breakage) before a release tag fires.

- **`make ci-local`** — new Makefile target that runs every gate CI and
  the release workflow would run: `make test`, `make lint`,
  `make govulncheck`, `make snapshot`, plus a new
  `make xcompile-windows` target that cross-compiles + cross-vets for
  `GOOS=windows GOARCH=amd64` and builds the per-package test binary
  to `/dev/null`. Catches `//go:build` regressions and per-platform
  compile errors before push, in ~30–60 s.
- **`make snapshot` now passes `--skip=sign`** — local snapshot runs
  no longer trigger the cosign OIDC device-flow that times out without
  a browser. Real releases continue to sign via `release.yml`, which
  uses the runner's ambient OIDC token to mint a Fulcio cert.
- **Release tag filter narrowed** — `release.yml` now triggers only on
  final-semver tags (`v[0-9]+.[0-9]+.[0-9]+`). Pre-release tags such
  as `v0.0.20-rc1` no longer produce GitHub Releases; they're reserved
  for pre-release verification.
- **CI tag filter broadened** — `ci.yml` now also triggers on
  pre-release tags (`v[0-9]+.[0-9]+.[0-9]+-*`), re-running the full
  Linux + Windows matrix against the exact tagged SHA before a final
  tag is pushed.
- **`docs/how-to/pre-tag-release.md`** — new how-to documenting the
  two-step verify-then-release flow with worked examples, the
  rc-tag retry pattern, and the tag-filter table. Indexed from
  `docs/how-to/README.md` and `docs/README.md`.

## v0.0.19 (2026-05-23)

Windows CI follow-up to v0.0.18. Two test failures on `windows-latest`,
both pre-existing latent portability bugs that v0.0.18's new test
surface area happened to expose.

- **`internal/apecmd/doctor_test.go`** — `TestCheckClaudeBinary_PathInjection`
  wrote a synthetic `claude` shim with no extension. Windows's
  `exec.LookPath` only resolves names whose extension is on `PATHEXT`
  (`.EXE`, `.CMD`, …), so the resolver returned "not found" and the
  test asserted OK against FAIL. Fix: name the shim `claude.exe` when
  `runtime.GOOS == "windows"`.
- **`internal/pipeline/preflight_test.go`** — three `PreflightSkills`
  tests pointed the user-scope fallback at a temp dir via
  `t.Setenv("HOME", …)`. Windows's `os.UserHomeDir` reads
  `%USERPROFILE%`, not `$HOME`, so the override was silently dropped
  and the resolver fell back to the runner's real home. Same fix
  shape as v0.0.16's `internal/cost/overrides_test.go` — a new
  `setFakeHome(t, dir)` helper sets both vars.

No production-code change; this is a test-portability patch. Linux
and macOS behaviour was already correct.

## v0.0.18 (2026-05-23)

### `ape doctor` — environment health probe

New top-level command that probes the local environment and reports a
per-check verdict. Designed to catch missing prerequisites *before* a
pipeline burns tokens on a misconfigured host, and to give CI a clean
JSON gate.

- **`internal/apecmd/doctor.go`, `doctor_checks.go`, `doctor_test.go`** —
  cobra command plus 12 probes: `claude.binary`, `git.binary`,
  `node.binary`, `npx.binary`, `playwright.host_supported`,
  `playwright.cache`, `framework.metadata`, `skills.project`,
  `skills.user`, `pipelines.project`, `permissions.home_claude`,
  `ape.update_available`. Output: human (default), `json`, `yaml`.
  Flags: `--strict` (treat WARN as failure), `--skip <names>`,
  `--cwd <path>`.
- **Playwright probe (`playwright.host_supported`)** — flags Ubuntu
  versions outside a small allowlist (currently 20.04 / 22.04 / 24.04)
  and emits the `PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS=1`
  workaround as a `fix_command`. Catches the Ubuntu 26.04 / Playwright
  incompatibility that silently degrades Excalidraw-rendering skills.
  The allowlist is hard-coded with a `LAST UPDATED:` source comment;
  bump it when Playwright adds new OS support.
- **Outside-project behaviour** — `framework.metadata`,
  `skills.project`, `pipelines.project` degrade to INFO when the user
  is not in a project, so doctor remains useful on fresh installs.
- **`internal/framework/skills_discover.go`** — new shared helper
  (`ResolveSkill`, `ListInstalledSkills`, `ProjectSkillsPath`,
  `UserSkillsPath`, `IsFrameworkSkill`) consumed by both doctor and
  the existing `pipeline.PreflightSkills`. The skill-resolution lookup
  order (project → user) now lives in one place.
- **`docs/how-to/run-doctor-in-ci.md`** — new task-oriented guide with
  a GitHub Actions snippet, exit-code semantics, jq examples, and a
  table of common CI-runner findings.
- **Colourised human output** — when stdout is a terminal and
  `NO_COLOR` isn't set, the human renderer prefixes each row with a
  status emoji (✅ ⚠️ ❌ ℹ️ ⏭️) and colours the STATUS label, the
  summary counts, and the `$` glyph in fix commands. Pipes, redirects,
  and CI logs (anything where stdout isn't an `*os.File` TTY) fall
  back to the existing plain output byte-for-byte — preserves
  scriptability and `jq` / grep workflows.

## v0.0.17 (2026-05-22)

Final post-PLAN-8 CI cleanup. All matrix jobs (Linux + Windows + Lint
+ Vuln) now green.

- **`internal/cost/jsonltail_test.go`** — `TestTailer_AppendedLinesProcessed`
  and `TestTailer_PartialLineRejoined` close their own test-owned
  `os.Create(path)` handles via `t.Cleanup(func() { _ = f.Close() })`.
  On Linux/macOS, `unlink` on an open file is fine (the inode lingers
  until the last handle closes), so the test passed coincidentally.
  Windows `RemoveAll` errors with "file is being used by another
  process" if any handle is still open. v0.0.16's synchronous
  `Tailer.Stop` had closed the Tailer's reader; this one closes the
  test's writer.

## v0.0.16 (2026-05-22)

CI follow-up: surface and fix Windows-specific test failures exposed by
PLAN-8's matrix expansion, drop macOS from the matrix (user request —
the macOS leg surfaced no new diagnostic value that the Windows leg
didn't already cover for cross-platform validation).

### Production fixes (cross-platform correctness)

- **`internal/pipeline/manifest_writer.go`** — `OpenStepLog` now returns
  the per-step events_path via `filepath.ToSlash` so manifest entries
  use forward slashes regardless of host OS. Manifests produced on
  Windows are now byte-portable to Linux/macOS consumers (web view,
  the eval harness).
- **`internal/cost/jsonltail.go`** — `Tailer.Stop` is now synchronous:
  it waits for the polling goroutine to drain and close its file
  handle before returning. Fixes `TestTailer_AppendedLinesProcessed`
  / `TestTailer_PartialLineRejoined` on Windows where
  `testing.TempDir`'s cleanup hit `unlinkat: file in use` against the
  open session.jsonl. Linux/macOS behaviour is unchanged because
  pumps already cleaned up on `defer` order.
- **`internal/tui/event_renderer.go`** — `relativizePath` now compares
  via `filepath.ToSlash` instead of `os.PathSeparator`. The pre-PLAN-8
  implementation worked coincidentally on Linux (where `/` is both
  the separator and the path-start byte) but failed on Windows when
  raw is a Unix-style absolute (`/tmp/...`) because the prefix used
  `\`. Fixes 5 TUI tests on Windows.

### Test-isolation fix

- **`internal/cost/overrides_test.go`** — `TestLookup_OverrideWinsOverBuiltin`
  now sets both `HOME` and `USERPROFILE` via `t.Setenv`. Windows's
  `os.UserHomeDir` reads `USERPROFILE`, not `HOME`, so the test's
  $99/$200 opus override was leaking into the real
  `C:\Users\runner\.ape\prices.yaml` and poisoning subsequent tests
  (1M × $99 + 0.5M × $200 = exactly the $199 the Aggregates test
  surfaced).

### POSIX-only tests skipped on Windows

- **`internal/framework/copy_test.go`** — `TestCopyFile_PreservesMode`
  and `TestAtomicWriteFile_WritesAndCleansUpTemp` skip on Windows.
  Windows reports `0o666` for any user-readable file regardless of
  the requested mode bits; the 0o600 round-trip assertion is
  POSIX-only.
- **`internal/sessions/registry_test.go`** — `TestPrune_DropsDeadPIDs`
  skips on Windows. The current `pidAlive_windows` probe (Signal nil
  via os.FindProcess) can return false-positive "alive" for recycled
  PID slots. A real Windows fix would use OpenProcess +
  GetExitCodeProcess via `golang.org/x/sys/windows`; deferred.
- **`internal/pipeline/runner_commit_test.go`** and
  **`runner_manifest_test.go`** — both files gain
  `//go:build !windows`. Every `TestRun_*` here drives a bash shim
  script as the synthetic claude binary; Windows can't exec `.sh`
  files (`fork/exec …: %1 is not a valid Win32 application`). The
  production paths these tests cover are exercised on Windows via
  the smoke step in CI.

### CI

- **Matrix shrunk from {ubuntu, macos, windows} to {ubuntu, windows}**.
  PLAN-8's FD originally added macOS as defense-in-depth — the user
  decided that adding macOS coverage wasn't worth the per-OS CI run
  cost given that Linux already exercises the POSIX path identically.

## v0.0.15 (2026-05-22)

Post-release follow-up for v0.0.14 CI breakage. The release itself was
green and signed; CI on `main` after the v0.0.14 merge was red on three
of four matrix jobs.

- **Bump `golang.org/x/crypto` to v0.52.0.** Fixes `govulncheck` finding
  `GO-2026-5018` (DoS via pathological RSA/DSA parameters in
  `crypto/ssh.ParsePublicKey`). The reachability chain was
  `orchestrator.BridgeRuntime.Serve → sync.Once.Do → ssh.ParsePublicKey`;
  we don't call the SSH path in practice but govulncheck is conservative.
- **Restrict the FE grandchild-reaper test to Linux.** Move
  `TestKillSession_ReapsGrandchildren` from `repl_test.go` (no build
  tag) to `repl_linux_test.go` (`//go:build linux`). Fixes the
  windows-latest CI leg where the test file referenced
  `procGroupKillGrace` (defined under `linux || darwin`) without a
  matching tag, breaking compilation. Production reaper code in
  `proc_unix.go` still covers both Linux and macOS; only the
  pgrep-based test moved.

CI on macOS (`Test (macos-latest)`) was also red on v0.0.14; details
weren't surfaced in the failure output. v0.0.15 reduces the test
surface on darwin (no reaper test); if it stays red, follow-up
investigation needs CI log access.

## v0.0.14 (2026-05-22)

### Interactive runner — PLAN-8 tmux → in-process PTY migration

- **Migrate from external `tmux` to in-process PTY.** `ape pipeline <name> --tui` / `--no-tui` / `--web` (interactive exec) and `ape chat` no longer shell out to a `tmux` binary. The pipeline interactive runner now allocates a pseudo-terminal in-process via `github.com/aymanbagabas/go-pty` (Unix PTY on Linux/macOS, ConPTY on Windows incl. Git Bash); slash-command prompts are delivered as real REPL keystrokes by writing bytes to the PTY master end — the same delivery shape `tmux send-keys -l` used. `ape chat` switches to direct `exec.CommandContext(claude, …)` with stdio inheritance; the bridge runtime is unaffected (TCP MCP hooks, no terminal handoff).
- **`tmux` is no longer required.** `internal/tmux/` is deleted; replaced by `internal/repl/` with the same package-level API (NewSession / KillSession / HasSession / CapturePane / SendText / SendEnter / SendCommand / WaitForReady). The migration's value proposition: native Windows / Git Bash support, no PATH-time external dependency for interactive exec.
- **`CapturePane` now returns a rendered VT grid.** PTY output is parsed through `github.com/hinshun/vt10x`; capture-pane output is plain text — no ANSI escape sequences, no cursor-positioning noise. Matches tmux's `capture-pane -p` semantics (visible-grid only; tmux's 2000-line history buffer is not reproduced).
- **Grandchild reaper on Unix.** `KillSession` now SIGTERMs the child's whole process group (go-pty Setsid makes the child a session leader so pgid=pid), with a 500ms SIGKILL escalator. Closes the orphan-grandchild gap the programmatic runner already had via PLAN-2 / F1.
- **Dropped features.** External live attach (`tmux attach -t ape-<stage>-<pid>`) is gone — the session lives and dies with ape. `ape chat` Ctrl-B-D detach is gone — the chat session is bound to the terminal for its lifetime; wrap `ape chat` in an external `tmux` / `screen` if persistence past terminal disconnect is required.

PLAN-8 commits: F0 (repl package + interactive.go swap) → FA (vt10x VT-grid emulator) → FB (chat rewrite + delete `internal/tmux`) → FC (doc + comment sweep) → FD (CI matrix expansion: Linux/macOS/Windows) → FE (process-group hardening). Detail in `development/planning/plan-8_tmux-to-pty-migration.md` and `_output/implementation-notes.html`.

### CLI

- **`ape planning` / `ape help planning` color refinements.** Action IDs in the swimlanes view now render in **blue** (was bright cyan) for better contrast against the bright-green agent personas. Fixed a regex bug where agent names (`ux`, `arch`, `dev`) were getting green-wrapped inside hyphenated skill descriptors (`create-ux`, `create-arch`, `data-arch`, `story-batch-dev`) in the legend — Go's RE2 `\b` treats `-` as a word boundary, so the new regex requires non-word AND non-hyphen surrounds. Locked in by new test `TestPlanningDiagram_AgentNamesNotColoredInsideHyphenatedSkills`.

## v0.0.13 (2026-05-21)

### Pipeline TUI

- **Per-step elapsed time in interactive mode.** PLAN-7 follow-up. The running-step indicator in the status strip (`▸ step N/M (skill)`) now carries the active step's elapsed time, and `Stop` / `SubagentStop` hook rows in the event panel are annotated with the just-completed step's duration. Wiring: `BridgeObserver.OnStepStart` / `OnStepEnd` no longer no-op — they forward `stepStartMsg` / `stepEndMsg` the same way the programmatic observer does, populating `stepRow.startedAt` / `endedAt` so `elapsedFor()` can compute on them. The Stop annotation reads `startedAt` at receive time (Stop arrives before `OnStepEnd` fires, by design — the runner waits on `stepDoneCh` which Stop itself signals) and freezes the duration into the rendered event body. New `stepIdxFromHookStep` parses the 1-based on-wire step idx out of `<stage>/<idx>-<skill>` for stage-step lookup.

### CLI

- **`ape planning` / `ape help planning`** prints an ASCII swimlanes view of the greenfield planning pipeline. Lanes are agent personas; rows are topological depth; each node is rendered `◉ <ID>←<parent>,<parent>` with the action ID in cyan and parents left uncolored so action and dependencies read apart at a glance. Agent personas (analyst, pm, ux, arch, modeler, sm, dev) are green. Colors honor `NO_COLOR` and fall back to plain text when stdout is not a TTY. Source of truth for the dependency edges is `docs/explanation/pipelines/planning-pipeline.md` in `apex_process_docs` — keep both in sync when the planning skill set changes.

## v0.0.12 (2026-05-21)

### Pipeline TUI

PLAN-7: Unified pipeline TUI. The interactive (`--tui`) and programmatic (`--tui -P`) Bubble Tea models converge on a single implementation — interactive mode gains dual-panel rendering (left = rich event feed, right = stages list), cursor + scroll navigation (`↑↓`, `Enter`, `L`, `PgUp/PgDn/Home/End`), render-style cycling (`r` → human / raw / both), narrow-layout fallback under 90 columns, and the final-report completion row that programmatic mode has had since PLAN-2. The await-message reply modal remains interactive-only (the stream-json source has no `await_message` MCP frames to surface).

- **Hook-event renderer.** New `tui.RenderHookEvent` adapts bridge `HookEvent` payloads (`PreToolUse`, `PostToolUse`, `Stop`, `UserPromptSubmit`, `Notification`) to the same `RenderedEvent` shape the stream-json renderer produces. Interactive mode's left panel now shows `🔧 Read foo.md` / `↳ result excerpt` / `? prompt` / `✓ skill complete` rows tagged to the correct stage via `stageFromHookStep`, instead of the bare `15:04:05 PreToolUse <step>` hook timestamps the legacy `InteractiveModel` produced.
- **Border misalignment fix under scroll.** The left and right panel borders drifted by one row when the event panel overflowed its visible window — visible since PLAN-2 / F3's `styleBoth` (2 lines per event vs 1-line-per-event slice math) and amplified by `renderEventPanel`'s trailing-newline overhead. New `composePanelBody(header, body, budget)` helper enforces an exact line budget; `renderEventPanel` now caps output at `height` lines across all three render styles. Second-pass fix surfaced under live sandbox testing: long stage-list rows (e.g., `"> ✓ create-architecture 7m36s"` at 29 visual cells against a 28-cell content area) were getting soft-wrapped by lipgloss inside the right panel, growing it past `panelHeight + 2`. `renderStageList(width int)` now truncates each row to fit the content-area width via the new `truncateForVisualWidth` helper (rune-aware, emoji-safe); `MaxHeight(panelHeight + 2)` on both panels acts as a defensive belt-and-suspenders cap. Regression covered by `TestRenderSmoke_DesignPipelineWrap`.
- **Single-model carve-out.** `tui.NewPipelineModel` grows `WithEventSource(SourceStreamJSON|SourceHookEvents)` and `WithAwaitReplySender(fn)` functional options. The default constructor (no opts) reproduces PLAN-2 behavior byte-for-byte — every existing call site (and the test suite) is unchanged. `InteractiveModel` (PLAN-6 / Phase E) is deleted; `InteractiveObserver` is renamed to `BridgeObserver` and lives in `bridge_observer.go`.
- **Hook events get their step backfilled before reaching the TUI.** `ape notify` can't populate `HookEvent.Step` under tmux (no step-bind plumbing on the wire). `interactiveCore.FeedHook` was filling it locally for the runlog only — leaving the observer immediately downstream to see `Step=""`, drop the event when routing by stage, and produce a permanently-empty event panel. The `OnHook` callback now backfills `h.Step` from a new `interactiveCore.ActiveStep()` getter before forwarding to the bridge observer.

### CLI

- **`ape version` shows a mascot in interactive terminals.** ASCII-art ape prints above the version block when stdout is a TTY and the output format is `human`; pipes, redirects, and `--output-format=json|yaml` stay clean.

## v0.0.11 (2026-05-21)

### Post-PLAN-6 interactive-mode parity fixes (2026-05-20)

Discovered during the sandbox invocation-matrix sweep. The interactive runner shipped without four pieces of parity with the programmatic runner; the docs already described the correct behavior, only the code was wrong.

- **Per-step commits now fire in interactive mode.** `runStageInteractive` was missing the `performStepCommit` call that `runStages` makes after every step. Result before fix: a full `design` pipeline run produced zero commits despite `commit: "specs: …"` directives on every step. The interactive path now matches `runStages`' PLAN-4 / C4 commit boundary semantics — commit on dirty tree after step success, skip on step failure, abort the pipeline on commit failure.
- **Hook events tagged with the active step.** `ape notify` cannot populate `step` on the hook frame (no step-bind plumbing under tmux); `interactiveCore` now tracks `activeStep` (set on `OnStepStart`, cleared on `OnStepEnd`) and `FeedHook` injects it when the frame's `step` is empty. `/clear` between steps still gets `step:null` correctly (fires outside the active-step window).
- **`stages/<NN>-<stage>/step-NN-<skill>.ndjson` populated in interactive mode.** Each per-step file gets a `step-start` line (slash command prompt + agent + model) and a `step-end` line (duration). `events_path` on the step record now matches programmatic mode.
- **Web-mode hook-event de-duplication.** `pipeline_web.go`'s Hub `OnHook` callback was writing to runlog twice — once directly and once via `core.FeedHook`. The bug was latent before the step-tagging fix (both writes produced `step:null`). The direct write is now gated on `core == nil` so the interactive core owns the write when active.
- **Interactive step timeout switched from wall-clock to idle-based.** `WaitStepDone`'s 10-min wall-clock cap was killing legitimately busy steps (e.g., a slow `apex-create-architecture` run that was still emitting tool-use hooks). Now resets on every hook event; trips after **15 min idle** with `interactive step idle for <duration> without Stop hook`. Polling tick is 30s; detection latency ≤ poll interval beyond the threshold.
- **Negative per-step telemetry deltas in multi-step interactive stages.** Validating the dedup fix on `governance --tui` exposed a separate bug: per-step cost/turns were going negative (e.g., `apex-pattern-generate: cost=$-0.56, turns=-6`). Cause: `interactiveCore.StepTelemetry` subtracted the previous step's cumulative `cost.Totals` from the new step's scan. The assumption was that all steps in a stage share one claude session transcript — but `/clear` between steps in claude REPL **rotates the session_id**, so each step gets its own fresh transcript file (verified: governance's 18 steps produced 18 distinct session UUIDs under `transcripts/`). The subtraction compared totals from two different files, producing negative deltas whenever step N+1's absolute usage was less than step N's. Fix: `interactiveCore` now tracks `cumulativeFor` (the path the cumulative was computed against); when `activeTranscript` moves to a new path, the baseline resets to zero before computing the step's delta. The `NoClear: true` path still works correctly (same path → same baseline → incremental delta). New `TestInteractiveCore_StepTelemetry_ResetsBaselineOnPathChange` locks in the behavior.
- **Cost over-counting from duplicate assistant messages in the claude session JSONL.** Investigating an interactive-mode `--tui` run (`design`, $18.57 total) against the prior `--tui -P` baseline ($4.97) surfaced a pre-existing bug in `cost.ScanSessionJSONL` and `cost.Tailer`: claude's session transcript logs the same assistant message multiple times under distinct top-level `uuid` values when a tool turn re-renders or the conversation tree branches. Same `message.id`, different `uuid`. The cost scanner counted every line, inflating totals 2–4×. Fix: extract `message.id` on `AssistantLine`, dedupe by ID in both `ScanSessionJSONL` and `Tailer.consumeLines`. Real run impact: `apex-create-architecture` step over-counted 80 logged lines as 80 turns (true: 26 unique); cost dropped from $13.65 to ~$3.65 after dedup, in line with the programmatic baseline. New `TestScanSessionJSONL_DedupesByMessageID` locks in the behavior. Affects `ape chat` cost totals too (PLAN-5 / C7 wrote them).
- **Interactive-mode telemetry capture from the claude session transcript.** Pre-fix, every step in `--tui` / `--no-tui` / `--web` had `cost_usd: 0` and `tokens_*: 0` on its manifest record because claude in REPL mode emits no stream-json on stdout (the runner's pane-snapshot scan returns nil). The fix taps into the session JSONL written under `~/.claude/projects/<hash>/<sid>.jsonl`. On each `UserPromptSubmit` hook the interactive core captures `transcript_path` from the payload; after `WaitStepDone` returns, `interactiveCore.StepTelemetry` waits a 500ms flush window, scans the transcript via `cost.ScanSessionJSONL`, and returns the delta from the previous step's snapshot. The runner adapts this to the existing `resultEvent` shape via a new exported `pipeline.StepTelemetry` struct + `RunOptions.StepTelemetryFn` callback so the manifest writer's `recordStep` path stays uniform across exec modes. `cost.Totals.NumTurns` field added so the manifest's `num_turns` populates too. Per-stage cumulative reset via `OnStageStart` → `core.ResetStageTelemetry`. Programmatic cells (`-P`, `--eval`) are unaffected — their stream-json `result` event already provides the same numbers directly.
- **Claude session transcripts symlinked into `transcripts/`.** Pre-fix, the runlog package created `<run-dir>/transcripts/` but nothing populated it. The interactive core's `FeedHook` now calls `runlog.Writer.LinkTranscript(<stage>-<idx>-<skill>.jsonl, transcript_path)` on every `UserPromptSubmit` (idempotent on same target). The `--web -P` hub callback does the same in its direct-write branch. Interactive mode produces N symlinks per stage all pointing to the stage's single session JSONL; `--web -P` produces one per step (each step is its own `claude -p` subprocess with its own session). `--eval` is unaffected (no runlog writer).
- **Step-tag label format includes step index.** Was `<stage>/<skill>`; now `<stage>/<idx>-<skill>` (1-based to match the manifest's step numbering). Resolves the collision exposed by `governance` where `apex-adr-adoption` runs at step 3 AND step 6 of the `adr-governance` chain — they now appear as `adr-governance/3-apex-adr-adoption` and `adr-governance/6-apex-adr-adoption` instead of sharing one label. Touched both the interactive-mode `interactiveCore.activeStep` and the `--web -P` `stepTaggingObserver` label-set sites, plus the `commit-made` checkpoint label emitted by both runners. New `pipeline.StepLabel(stage, idx, skill)` helper centralizes the format.
- **Hook event step-tagging in `--web -P`.** The legacy PLAN-5 web programmatic path was writing every `hook-events.jsonl` entry with `step: null` because (a) `ape notify` cannot populate the Step field and (b) no in-process tracker existed for the programmatic web flow (the interactive core's `activeStep` only fires in interactive cells). Added `webHookStepTracker` + `stepTaggingObserver` in `pipeline_web.go`: the observer wraps the plain stdout observer and updates the tracker on `OnStepStart` / `OnStepEnd`; the hub's `OnHook` direct-write branch consults the tracker when `h.Step` is empty. `--web -P` hook events now carry `step: "<stage>/<skill>"` matching the interactive cells.
- **PLAN-6 / C2 Phase D — stage-boundary commits now actually fire.** Before this change, the runner computed `EffectiveCommit{Boundary: CommitBoundaryStage, ...}` via `Spec.Effective()` but never consumed it; per-step `step.Commit.Resolve(...)` ran instead, so pipelines that set `commit:` at the stage or pipeline level got per-step derived `ape:<pipeline>/<stage>/<skill>` commits anyway. Phase D wires the plan through both `runStages` and `runStageInteractive`: stage-level / pipeline-level `commit:` produces exactly one commit per stage capturing the chain's accumulated diff, attributed to the last step in the chain; earlier steps record `commit_status: deferred-to-stage`. Step-level `commit:` continues to fire at the step boundary. `commit: false` at any level suppresses as documented. New `Spec.PlanStageCommits(stage)` is the entry point; `DerivedStageCommitMessage(pipeline, stage)` produces the `ape:<pipeline>/<stage>` default message when stage-boundary mode is enabled without an explicit message.

---

PLAN-6: Interactive pipeline exec + orthogonal UI/exec modes. `ape pipeline <name>` is now **tui + interactive** by default — one persistent `claude` REPL per stage running inside a per-stage `tmux` session, prompts delivered as real REPL keystrokes via `tmux send-keys`. UI choice (`none` / `tui` / `web`) is orthogonal to exec choice (`interactive` / `programmatic`). Pipeline YAML grows pipeline-level and stage-level defaults for `commit` / `model` / `agent` with precedence `step > stage > pipeline > default`. The hooks bridge enforces a per-step contract on the agent-prefixed skill prompt; `/clear` between steps is runner-driven (skip with `no-clear: true`).

### Features

- **`ape pipeline <name>` default flips to `--tui` + `--interactive`.** Old default (per-step `claude -p`) is now `--tui -P` or `--no-tui -P`. `--web` defaults to interactive; `--web -P` is the legacy PLAN-5 web programmatic mode (byte-equivalent preserved).
- **New `-P` / `--programmatic`** modifier to opt back into per-step `claude -p` spawning.
- **`--eval` is locked.** Byte-equivalent stdout for the eval harness at `apex_process_framework_eval`; no bridge, no hooks, no per-stage spawn. Admits no exec modifier. Renamed from `--print` so the byte-equivalence contract is visible at the call site.
- **`--no-tui` is now a UI selector**, not an alias for the locked path. Means "no UI, but still interactive exec".
- **Pipeline YAML grows pipeline-level + stage-level defaults** for `commit`, `model`, `agent`. Precedence: `step > stage > pipeline > default(skip)`. Default commit unit is the stage boundary.
- **Per-step `no-clear: true`** opts a step out of the inter-step `/clear` for multi-step chains that need shared context (e.g., `apex-create-prd`'s elicit/respond loop).
- **`ape chat` rewritten** as a thin `tmux` spawn-and-attach helper. ape spawns `claude` in a named tmux session with the bridge wired (hooks captured to `_output/ape/chats/<id>/`), prints attach instructions, and `exec`s `tmux attach`. The Bubble Tea chat TUI from PLAN-5 was removed.
- **Live debugging via tmux attach.** During an interactive run, `tmux attach -t ape-<stage>-<pid>` shows the live claude session at any time. Detach with Ctrl-B D.
- **Diataxis docs reorg.** New tutorials, how-tos, and reference docs at `docs/{tutorial,how-to,reference,explanation}/` covering invocation matrix, exec modes, step contract, pipeline YAML schema, run artefacts.

### Breaking changes

- **`--print` renamed to `--eval`.** Hard rename, no deprecation alias. Pre-existing scripts that pass `--print` will error with `unknown flag`; migrate to `--eval`. The byte-equivalent output shape is unchanged.
- **`tmux` is now required for interactive exec.** ape errors clearly if `tmux` is not on `PATH`. Programmatic exec (`-P`, `--eval`) has no tmux dependency.
- **`--no-tui` no longer aliases the locked path.** Use `--eval` explicitly for byte-equivalent stdout, or `--no-tui -P` for "no UI + programmatic exec".
- **`ape chat` no longer hosts a TUI surface.** The web/Bubble Tea chat UI was removed; `ape chat` is now `tmux attach` to a bridged claude. Pre-existing automation that screen-scraped the chat UI must migrate.
- **`creack/pty` dependency removed.** Interactive surfaces (pipeline + chat) all route through tmux now.

### Design pivot — tmux send-keys (2026-05-20)

PLAN-6 originally specified interactive exec as PTY + `--system-prompt` + MCP `await_message` / `reply` for prompt delivery. Sandbox bring-up showed that shape was structurally broken: the PAT-25 skill prompt (`/<agent> --autonomous -- <skill> --autonomous`) is a claude-CLI-level slash command; delivered to the model as a tool-result string via `await_message`, the model receives the text but cannot invoke it (the CLI never sees the leading `/`). The fix: spawn `claude` inside a per-stage tmux session and deliver each step's prompt as real REPL keystrokes via `tmux send-keys -l <text>` + Enter, so claude's CLI parses the slash command the normal way. The bridge stays useful for hook observability (`UserPromptSubmit`, `Stop`) but no longer carries prompt delivery. The `/model X` contract rule was dropped (CLI-level switch the model can't self-invoke). Full record at `development/planning/plan-6_interactive-exec-and-orthogonal-modes.md` § "Implementation pivot — tmux send-keys".

---

PLAN-5: `ape chat` + `ape pipeline` web mode (MCP bridge). Brings the
validated PoC at `claude_mcp_bridge_poc@4e542d0` into ape as the new
default UX path. Web UI runs via HTMX 2.x + stdlib `html/template`,
vendored under `internal/web/assets/vendor/` — no JS toolchain on
either side.

### Features

- **New `ape chat` subcommand.** One bridged interactive Claude
  session, web UI is the only surface. Localhost-only bind; the URL
  prints on stderr at startup. `--open` runs `xdg-open`.
  `--ignore-project-settings` skips project + local `.claude/settings*.json`.
  Exit 137 on browser-side Stop, 0 on natural exit, 130 on ctrl-C.

- **New `ape sessions` subcommand.** Lists live sessions tracked in
  `~/.ape/registry.json` across all projects. `ape sessions prune`
  drops rows whose PID is gone. `ape sessions open <prefix>`
  xdg-opens the URL of the matching session.

- **New `ape costs` subcommand.** Reads `_output/ape/cost-rollup.json`
  and prints per-pipeline + per-day totals. `--output-format json`
  for machine consumption.

- **Hooks observability.** Six Claude Code hooks wired via inline
  `--settings`: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`,
  `SubagentStart`, `SubagentStop`, `Stop`. Hooks fire only in web
  mode (zero overhead in `--tui` / `--print`). PreToolUse gating
  uses the new `hookSpecificOutput.permissionDecision` schema —
  wiring lands here; rule-set is OUT of this release.

- **Run artefacts.** Pipeline runs continue to land at PLAN-3's path
  (`<project>/_output/pipelines/<name>/<run-id>/`); PLAN-5 adds
  `hook-events.jsonl`, `bridge-calls.jsonl`, `checkpoints.jsonl`,
  and `transcripts/` symlinks alongside `manifest.yaml`. Chat
  sessions land separately at `<project>/_output/ape/chats/<chat-id>/`
  with a small `session.yaml` in place of the manifest. Run-id
  collisions fail loud.

- **Cost rollup.** `<project>/_output/ape/cost-rollup.json` aggregates
  pipeline runs and chat sessions into per-name / per-day buckets.
  Per-step cost data comes from per-message `usage` blocks in the
  session JSONL (no schema bump — populates PLAN-3 v2 fields).

### New CLI flags on `ape pipeline`

- `--tui` — currently inert (default). Reserved as the explicit
  opt-in form so a future release can flip the default with one
  line.
- `--print` — plain stdout (the explicit name for what `--no-tui`
  used to do). Routes through the same code path as today's
  `--no-tui` so byte equivalence is structural.
- `--no-tui` — deprecated alias for `--print`. Prints a stderr
  warning when used; remove after one minor version.
- `--ignore-project-settings` — reserved for web mode; no-op until
  pipeline web mode lands.
- Multiple mode flags simultaneously is an error (`ape pipeline foo --tui --print` → exit 2).

### Internal-only commands (hidden from `ape --help`)

- `ape mcp-bridge` — MCP stdio server, spawned by Claude Code when
  ape is declared in the inline `--mcp-config` blob.
- `ape notify --event <EventName>` — hook forwarder. Reads JSON
  envelope from stdin, dials `APE_BRIDGE_PORT`, NDJSON-encodes a
  `TypeHook` frame, exits 0.

### Breaking UX change: `ape pipeline <name>` defaults to the web UI

**`ape pipeline <name>` now spawns a browser by default.** The Bubble
Tea TUI moves behind `--tui`; plain stdout moves behind `--print`.
This is the no-flag surface change PLAN-5 set up. Three migration
paths for callers that relied on the old behaviour:

- **Want the TUI back?** Add `--tui` to your invocation.
- **Want plain stdout (CI / eval capture)?** Add `--print`.
- **Stuck on `--no-tui`?** It still works, but prints a deprecation
  warning. Move to `--print` before the next minor version.

The eval consumer (`apex_process_framework_eval`) pins `--print`
explicitly; its capture path is unaffected.

### Cost-table caveat

`internal/cost/prices.go` ships with starter Anthropic Claude 4.x
prices marked TODO. Confirm them against the current Anthropic
price list before the cost path becomes load-bearing for billing
decisions.

### Docs

- `docs/reference/bridge-ipc.md` — IPC wire schema.
- `docs/reference/bridge-security.md` — threat model + bind contract.
- `docs/how-to/run-artefacts.md` — `_output/` layout reference.
- `docs/explanation/bridge-architecture.md` — design narrative.

## v0.0.10 (2026-05-11)

PLAN-4: per-step boundary commits, on by default. Every successful
pipeline step now lands as its own git commit with a deterministic
message, building on PLAN-3's manifest infrastructure. The pipeline-
spec YAML gains a `commit:` field for per-step overrides; the user
opts out of all commits with the new `--no-commit` CLI flag.

This is a default-behavior change — pre-v0.0.10, ape never ran git
commit during a pipeline run. Use `--no-commit` to preserve the old
shape.

### Behavior changes

- **Pipeline runs commit per step by default.** After each successful
  step ape runs `git add -A && git commit -m "ape:<pipeline>/<stage>/<skill>"`.
  Empty diffs are recorded as `no-op` (no empty commits emitted).
  Failed steps and cancelled runs skip the commit boundary. A failed
  `git commit` (e.g., pre-commit hook rejection) aborts the pipeline
  with `commit_status: failed` recorded for the offending step.
  PLAN-4 / C2–C4.

- **Pre-run dirty-tree gate.** When commits are enabled, ape refuses
  to start if `git status --porcelain` is non-empty in the project
  root. The actionable error message lists both bypass options.
  PLAN-4 / C5.

- **End-of-run summary now shows commit count** when at least one
  commit was made: `📌 commits: N (run \`git log --oneline --grep '^ape:<pipeline>/'\` to inspect)`.
  PLAN-4 / C8.

### New CLI flags

- `ape pipeline <name> --no-commit` — pipeline-level kill switch.
  Suppresses every `git commit`, overrides any per-step `commit:` in
  the YAML, sets every step's `commit_status` to `skipped-by-flag`.
- `ape pipeline <name> --commit-allow-dirty` — bypass the dirty-tree
  gate. The first committing step's diff will include any pre-
  existing uncommitted changes. Use with caution.

### Pipeline spec schema

- New optional `commit:` field per step accepting `bool` or `string`:
  - omitted / `true` / `~` → commit with derived message.
  - `false` → skip this step's commit.
  - `"explicit message"` → commit with the given message verbatim
    (no `ape:` prefix added).
  - Multi-line strings, empty strings, mappings, sequences, and
    integers are rejected at spec-load with line-number errors.

### Manifest schema (v1 → v2)

- Bumped `schema_version: 1` → `schema_version: 2`. Forward-compatible:
  v2 readers accept v1 manifests (new fields are `omitempty`).
- Added to `StepRecord`: `commit_sha`, `commit_message`, `commit_status`,
  `commit_error`.
- Added to `Manifest.totals`: `commits_made`.
- `commit_status` is a closed enum with 7 values; see
  `docs/reference/pipeline-run-manifest.md` § Commits during a run.

### Internals

- New `internal/pipeline/commit.go` — `CommitDirective` + `CommitMode`
  + `DerivedCommitMessage`.
- New `internal/pipeline/git.go` — thin wrappers around `git status
  --porcelain`, `git add -A`, `git commit -m`, `git rev-parse`. Never
  passes `--no-verify`; hooks run as configured.
- `pipeline.RunOptions` gains `NoCommit` and `AllowDirty`.
- `pipeline.CommitsMadeFor` exposes the latest run's commit count to
  the CLI.

### Docs

- `docs/reference/pipeline-spec.md` — new "Commits" section + `commit`
  row in the Step fields table.
- `docs/reference/pipeline-run-manifest.md` — "Commits during a run"
  rewritten for the default-on shape, new fields documented, manifest
  schema version bumped.

### Verification

- `make lint` zero issues. `go test ./...` clean across all packages.
- New tests: 4 unit suites covering YAML shapes / message derivation
  / resolve logic; 9 integration suites covering default-commit /
  explicit-message / skip-by-spec / skip-by-flag / no-op / dirty-tree
  refusal / allow-dirty bypass / dirty-tree-ignored-when-no-commit /
  failed-step-no-commit.

### Coordination

- The framework's mode-aware Commit Policy (drafted at
  `apex_process_framework_eval/_output/framework-prompt-anti-self-commit-clause.md`)
  is the natural complement: leaf skills won't auto-commit, and ape
  takes over the commit boundary deterministically. Pipelines work
  correctly against pre-Commit-Policy framework versions too — if a
  leaf skill auto-commits, ape sees an empty diff and records `no-op`.

## v0.0.9 (2026-05-11)

PLAN-3: every `ape pipeline <name>` invocation now writes a structured
on-disk record of the run. The artifact unblocks the eval-side
per-skill metrics work (apex_process_framework_eval PLAN-9) and gives
real-project users a "what did that run cost" answer that survives the
TUI closing.

### Behavior changes (no CLI flag breakage)

- **Pipeline runs now leave a manifest on disk.** Every invocation —
  TUI mode, `--no-tui`, eval-harness mode — writes
  `<project_root>/_output/pipelines/<name>/<run_id>/manifest.yaml`
  alongside per-step `.ndjson` captures of the raw claude
  stream-json events and a human-readable `pipeline-report.md`.
  Per-step `cost_usd`, `tokens_*`, `num_turns`, and `duration_seconds`
  are extracted from the terminal `result` event in claude's stream;
  totals roll up at the run level. A `latest` symlink at
  `<pipeline_name>/latest` points at the most recent `<run_id>` for
  easy tailing. PLAN-3 / M1-M5.

- **End-of-run summary prints the report path.** Both the TUI and the
  plain-printer (`--no-tui`) finish a run with a stable
  `📊 report: _output/pipelines/<name>/<run_id>/pipeline-report.md`
  line on stdout. CI logs can link straight to the artifact. PLAN-3
  / M6.

### New CLI flags

- `ape pipeline <name> --manifest-dir <path>` — override the manifest
  root (default: `<project>/_output/pipelines`). Used by the eval
  harness to redirect manifests into its own results tree; available
  to anyone who wants pipeline runs in a non-default location.
  PLAN-3 / M6.

### Internals

- `pipeline.RunOptions` gains `ManifestDir`, `DisableManifest`, and
  `ApeVersion` fields. `DisableManifest` is a library-only escape
  hatch for tests / embedded use; it is not surfaced on the CLI.
- Manifest types live in `internal/pipeline/manifest.go`; the
  on-disk YAML schema is the external contract (the eval reads it).
  Schema is versioned (`schema_version: 1`); future additions are
  forward-compatible.
- `runClaude` accepts an optional `io.Writer` to tee the
  stream-json events to disk in parallel with the Observer.
- `pipeline.ReportPathFor(projectRoot, pipelineName, manifestDir)`
  exposes the most recent report path for embedding callers.

### Docs

- New: [docs/reference/pipeline-run-manifest.md](docs/reference/pipeline-run-manifest.md)
  — schema, status enum, metric provenance, forward compatibility,
  cleanup guidance.
- Updated: [docs/reference/pipeline-spec.md](docs/reference/pipeline-spec.md)
  cross-links the manifest reference.

### Verification

- `make lint` zero issues. `go test ./...` clean across all
  packages, including the three new manifest tests
  (`TestRun_EmitsManifest`, `TestRun_FailedStepCaptured`,
  `TestRun_DisableManifestSkipsTree`).

## v0.0.8 (2026-05-11)

A focused follow-up to v0.0.7 that closes every gap the v0.0.7 smoke
surfaced or that PLAN-1 deferred. Eight independently-shippable
items; see [development/planning/plan-2_pipeline-ux-followups.md](development/planning/plan-2_pipeline-ux-followups.md)
for the full rationale.

### Behavior changes (no CLI flag breakage)

- **Confirmed quit now kills the whole `claude` subprocess tree.**
  Pre-v0.0.8, pressing `q` then `y` (or double-Ctrl+C) SIGKILLed the
  immediate `claude` child but any sub-agents it had spawned via the
  `Task` tool were reparented to PID 1 and continued running until
  they exited naturally — burning Anthropic API budget for minutes
  after the user thought the pipeline was dead. v0.0.8 makes the
  child a process-group leader (`Setpgid=true`) and rewires
  `Cmd.Cancel` to deliver SIGTERM to the whole group, with a
  detached escalator goroutine that SIGKILLs the group after a
  500ms grace period. Linux + darwin only; Windows falls back to
  the existing direct-child SIGKILL. PLAN-2 / F1.

- **The pipeline TUI no longer auto-quits when the pipeline finishes.**
  Pre-v0.0.8, the TUI tore down on the last stage's `OnStageEnd` and
  the user dropped back to the shell with no chance to scroll
  through events. v0.0.8 transitions the model into a
  `phaseCompleted` state instead: a synthetic `📊 final report` row
  appends to the stage list, a completion banner replaces the
  keybind hint (`✓ pipeline complete: N/N stages OK` or
  `✗ pipeline failed: M/N FAILED`), and selecting the report row
  populates the event panel with a per-stage summary (glyph · name ·
  duration · event count · last error). Navigation, scroll, and
  render-style cycling all stay wired; `q` exits directly (no
  confirmation modal — there's nothing to cancel). PLAN-2 / F7.

- **Tool-call event lines render paths relative to the project root.**
  Pre-v0.0.8, sandbox prefixes like `/tmp/ape-v007-smoke-c70b/...`
  ate the event-panel column and the actually-informative suffix
  was truncated. v0.0.8 strips the project-root prefix from
  `Read` / `Edit` / `Write` / `Grep` / `Glob` path arguments at the
  renderer; system paths, `$HOME`-relative paths, and
  framework-source paths pass through unchanged. The TUI and
  `--no-tui` plain mode both apply the same logic. PLAN-2 / F6.

- **`PgUp` / `PgDn` scroll works in any mode.** Pre-v0.0.8, the
  scroll keys were gated behind `Pinned` mode and were no-ops in
  the default `Live` mode. v0.0.8 adds a `userScrolled` flag on the
  model: any scroll key suspends auto-tail, new events arrive
  silently in the background, pressing `End` (or paging back to the
  bottom) rejoins the tail. `Enter` (pin) seeds the scroll offset
  to the tail so the pinned panel opens on the latest events.
  PLAN-2 / F8.

- **`r` cycles event-render style: human → raw JSON → both.**
  Documented in `docs/reference/tui-keybindings.md` since v0.0.7,
  finally wired in v0.0.8. Each rendered event now carries the
  original NDJSON line so the raw / both views are zero-cost
  re-renders. The keybind-hint footer surfaces the active style
  label. PLAN-2 / F3.

- **Single-column layout under width 90.** Narrow terminals (tmux,
  kitty splits, side-by-side editors) drop the right-side stage
  column; the stages collapse to a one-row horizontal stepper
  above the event panel, the event panel takes the full width, the
  cursor stage gets wrapped in `[ ]` for visibility. Widens back
  on `WindowSizeMsg` above the threshold. PLAN-2 / F4.

- **`ape pipeline --no-tui --quiet` suppresses the per-event stream.**
  For CI runs where humans only read the failure summary, the
  per-event stream from v0.0.7's `plainObserver` was noise. v0.0.8
  adds `--quiet`, which returns the plain observer to its
  pre-PLAN-1 / I4b shape: stage / step start+end markers and
  failure summaries print, `OnStepLine` is a no-op. Combining
  `--quiet` with the interactive TUI is refused with an actionable
  error. PLAN-2 / F5.

- **30 Hz render throttle on TUI event flushing.** Pre-v0.0.8 the
  per-event re-render cadence was implicit (no measured pain on
  current workloads, but unbounded as terminal multiplexers
  introduce per-frame latency). v0.0.8 buffers incoming stepLines
  into a queue and flushes them in a single Update pass every
  33ms (~30 Hz), independent of incoming line rate. PLAN-2 / F2.

### Notes

- The `docs/explanation/why-streaming-events.md` § "What it cost"
  caveat about orphan sub-agents surviving Ctrl+C is closed by
  F1. The new escalator-goroutine cancel path is exercised by
  `internal/pipeline/runner_unix_test.go` —
  `TestRunClaude_KillsProcessGroupOnCancel` builds a shell shim
  that forks a SIGTERM-trapping grandchild and asserts both PIDs
  are reaped within 1.5s of context cancellation.
- `NewPipelineModel` gains a trailing `projectRoot string`
  parameter for F6. Out-of-tree callers will need a one-line
  update.

## v0.0.7 (2026-05-10) ⚠️ BREAKING

A pipeline-UX pass driven by real v0.0.6 use. The CLI surface and the
Go-API surface both move; see [docs/how-to/framework-setup.md](docs/how-to/framework-setup.md)
for the new install flow and the planning doc
[development/planning/plan-1_pipeline-ux-and-framework-setup.md](development/planning/plan-1_pipeline-ux-and-framework-setup.md)
for the full rationale.

### Breaking changes 💥

- **`ape framework update` is now refresh-only.** The first-install path
  is `ape framework setup`. Strict refusal semantics either way:

  ```text
  $ ape framework update            # fresh project
  Error: framework metadata not found at <path> — run "ape framework setup" to install

  $ ape framework setup             # already-installed project
  Error: framework already installed at <path> — run "ape framework update" to refresh,
  or "ape framework setup --force" to re-bootstrap (resets project_name and extensions)
  ```

  Scripts and CI tooling that call `framework update` for first-time
  installs must branch based on `_apex/framework.yaml` presence. The
  apex_process_framework_eval harness does this in
  `apex_eval/runner.py:_invoke_ape_framework_update`.

- **`pipeline.Observer` gains `OnStepLine(stage, idx, line)`.** Any
  external Observer implementation must add the method. The only known
  implementations live in this repo (`PipelineTUIObserver`,
  `plainObserver`); both are updated.

- **`NewPipelineModel(spec)` → `NewPipelineModel(spec, cancel)`.**
  The TUI model takes a `context.CancelFunc` that the confirmed-quit
  modal invokes to SIGKILL the spawned `claude` subprocess. A `nil`
  cancel is tolerated (test paths) — the modal still renders, but
  confirmed quit exits without subprocess teardown.

- **`internal/framework.Update(ctx, opts)` semantics changed.** It is
  now refresh-only and refuses if `_apex/framework.yaml` is absent.
  Use `framework.Setup(ctx, opts)` for first-time installs. Both
  share an internal `installCore` helper.

### Features ✨

- **`ape framework setup`** — one-time install. Skills + pipelines +
  bootstrap `_apex/config.yaml` via the existing Bubble Tea TUI (or
  `--project-name` + `--extensions` flags, or `--no-bootstrap`).
  Refuses to re-run unless `--force` is passed (which resets the
  bootstrap values).

- **Live pipeline TUI streaming.** `OnStepLine` plumbs newline-delimited
  events from the spawned `claude` subprocess into the TUI as they
  arrive. `internal/tui/event_renderer.go` parses each
  `claude --output-format stream-json` event and renders a one-line
  human summary (`🔧 Read foo.md`, `✎ Drafting ADR table`,
  `↳ ⚠ validation failed`, `✓ skill complete`). No more frozen output
  between stages.

- **Three-panel pipeline TUI.** Top-left ~70% width streams events for
  the cursor's stage; top-right ~30% lists all stages with status
  glyph + duration + cursor; bottom status row summarizes the cursor
  stage. Modes:
  - `Live` (default) — cursor auto-follows the running stage,
    auto-scrolls.
  - `Pinned` — `Enter` pins to the cursor's stage; `PgUp`/`PgDn`
    scroll. `L` or `Esc` returns to Live mode.

  See [docs/reference/tui-keybindings.md](docs/reference/tui-keybindings.md)
  for the full key map.

- **`--no-tui` mode streams the same rendered events.** Timestamped,
  prefixed with stage + skill. Log captures and CI runs get the same
  human-readable feed as the interactive TUI.

- **Quit-confirmation modal.** `q` or `Ctrl+C` mid-run opens a
  `Stop pipeline?` overlay. `y` confirms (cancel + SIGKILL the in-flight
  `claude` subprocess); `n` / `Esc` dismisses. Two Ctrl+C within 1s
  force-quit. Closes the v0.0.6 "TUI quits but subprocess keeps
  running" gap.

### Fixes 🐛

- **`ape framework status` not-installed error.** Previously leaked Go's
  `open …: no such file or directory` trailer underneath the otherwise
  actionable message. Now: single-line, trailer-free. Typed
  `*framework.NotInstalledError` for programmatic callers
  (`errors.As`); still satisfies `errors.Is(err, fs.ErrNotExist)`.

### Internal

- New exit codes: `exitCodeAlreadyInstalled` (6), `exitCodeNotInstalled` (7).
- `internal/framework/install.go` shared `installCore(ctx, opts, doBootstrap)`.
- `internal/pipeline/runner.go` `runClaude` uses `StdoutPipe`/`StderrPipe`
  + `bufio.Scanner` goroutines instead of a captured-string `cmd.Run`.
  Scanner buffer ceiling raised to 1 MiB to accommodate long
  `tool_result` bodies.
- `.goreleaser.yaml` migrated to v2 schema (`version: 2`; `formats:`
  in `format_overrides`) — silences deprecation warnings from
  goreleaser v2 and avoids hard failures in v3.

### Tests

- `internal/framework/install_test.go` — full TestSetup\_\* / TestUpdate\_\*
  rewrite plus refusal-case tests.
- `internal/pipeline/runner_test.go` — TestRunClaude_StreamsLineByLine,
  InterleavesStderr, PropagatesNonZeroExit.
- `internal/tui/pipeline_test.go` — quit-modal state machine + nav
  (cursor moves, Pin freezes, L returns to Live).
- `internal/tui/event_renderer_test.go` — 25+ cases covering every
  tool, every result shape, schema-drift fallback, host extraction,
  truncate helper.

## v0.0.6 (2026-05-10) ⚠️ BREAKING

This release moves pipeline specs out of the binary and adds a first-class
install path for the framework's skills + pipelines + project bootstrap.

### Breaking changes 💥

- **Pipeline specs are no longer embedded into the ape binary.** They now live
  at `<project>/_apex/pipelines/*.yaml` and must be installed before
  `ape pipeline <name>` will work. Existing v0.0.5 installs that ran
  `ape pipeline design` against bare projects will break with:
      pipeline "design" not found at <project>/_apex/pipelines/design.yaml — run
      "ape framework update" to install pipelines from the framework repo

  Migration is one command:
      export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
      ape framework update

  See [docs/how-to/framework-update.md](docs/how-to/framework-update.md).

- **`LoadSpec(name string)` → `LoadSpec(name, projectRoot string)`.** Internal
  API change in `internal/pipeline`; only relevant if you've imported the
  package directly. Callers that pass an empty `projectRoot` get an explicit
  error with the resolved path.

- **`ape pipeline list` (introduced earlier on this branch) is now `ape pipeline`
  with no positional arg.** `--output-format human|json|yaml` works in list mode
  (no positional). With a name, `ape pipeline <name>` runs the pipeline as
  before. Tab completion still surfaces installed pipelines.

### Features ✨

- **`ape framework update`.** Installs/refreshes the framework's `apex-*` skills
  into `<project>/.claude/skills/` and the canonical pipelines into
  `<project>/_apex/pipelines/`. On first run, opens an interactive Bubble Tea
  prompt to seed `_apex/config.yaml` (project_name + extensions). Headless
  contexts use `--project-name`, `--extensions`, or `--no-bootstrap`.
  Refuses to clobber tracked-but-modified `apex-*` skills without `--force`;
  untracked apex-\* leftovers are safe to overwrite.

- **`ape framework status`.** Reads `<project>/_apex/framework.yaml` and prints
  the installed framework version. With `--repo` or `$APEX_FRAMEWORK_REPO` set,
  fetches the framework HEAD and emits a drift report (hash + tag).

- **`<project>/_apex/framework.yaml`.** New metadata file generated on every
  `framework update` run. Records framework SHA + tag, the ape version that
  performed the install, and the list of installed assets. Should be committed
  alongside the project. Schema:
  [docs/reference/framework-yaml.md](docs/reference/framework-yaml.md).

- **`ape pipeline` (no args).** Lists pipelines installed at
  `<project>/_apex/pipelines/`, with `--output-format human|json|yaml`.

### Internals ⚙️

- New `internal/framework` package implementing the install/status flow:
  copy primitives, git CLI wrappers, metadata schema, two-phase Bubble Tea
  bootstrap TUI, full `Update(ctx, *UpdateOptions)` orchestration. Test
  coverage via `testify/require`: copy primitives, git wrappers against
  ephemeral repos, metadata roundtrip, full Update flow happy path,
  idempotent re-run, stale-skill removal, dirty-framework refusal,
  modified-skill refusal, untracked-skill safe-clobber, missing-subtree
  error, drift detection.

- `internal/pipeline/spec/` (the embedded yaml directory) is gone. The
  three canonical pipelines now live in `apex_process_framework` at
  `framework/_apex/pipelines/` (introduced in framework v0.0.71).

### Documentation 📚

- New [how-to/framework-update.md](docs/how-to/framework-update.md).
- New [reference/pipeline-spec.md](docs/reference/pipeline-spec.md) — formalizes
  the on-disk pipeline YAML schema.
- New [reference/framework-yaml.md](docs/reference/framework-yaml.md).
- New [explanation/why-project-local-pipelines.md](docs/explanation/why-project-local-pipelines.md).
- [how-to/install.md](docs/how-to/install.md) updated with a "next step"
  pointer to `framework update`.

### Compatibility envelope

ape v0.0.6 requires a framework with `framework/_apex/pipelines/` populated
(framework v0.0.71 or later).

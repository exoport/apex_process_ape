# Work with project data

The APEX framework's skills read and write a project's own records: ADRs,
patterns, features, capabilities, stories, the sprint tracker, team memory
and the deferred-work ledger. `ape` gives them deterministic commands for
the parts that are mechanical, so a skill does not have to load a corpus
into a context window to answer a question a directory scan can answer.

Every command here follows the same shape: **measure or assert, report
findings, and leave judgment to the caller.** None of them decides what a
defer means, which of two divergent statuses is right, or whether one
memory entry supersedes another.

## Start here: does `ape` see your project?

```bash
ape config resolve
```

This walks up for `_apex/config.yaml`, overlays `_apex/config.local.yaml`
key-wise, and prints the seventeen folder/name variables, the framework's
two declared **optional** variables, the four derived `ext_*` flags, the
absolute paths those folders denote, and the run's `date` and `timestamp`.

Run it first when anything below reports "no records". Before it existed,
`ape adr list` printed `no ADR directory found (looking for
development/adrs/)` on a project with 64 ADRs in
`development/governance/adrs/` — because nothing in `ape` read
`governance_folder`. Every command below resolves its paths through this
one, so if `ape config resolve` is wrong, everything is wrong in the same
direction.

| Exit | Means |
| ---- | ----- |
| 0 | resolved |
| 2 | a config file exists but is malformed — the message names it |
| 4 | no `_apex/config.yaml` here or in any parent |

A malformed `config.local.yaml` is a hard failure on purpose. This
resolution is the first act of every skill, so a silent fall-back to base
values would let one typo'd override run a whole pipeline against folders
nobody chose.

### The two optional variables

`model_profile` and `evidence_folder` are the framework's **declared
optional** variables. Both can be set in `config.yaml` and overridden in
`config.local.yaml`, and both are emitted **exactly as written**.

`ape` supplies **no default for either, and derives no path from
`evidence_folder`.** That is the contract, not a gap: each key ships with a
documented default its framework consumers apply when the resolver omits it
(`strong` for `model_profile`, and a three-step fallback for
`evidence_folder`). Defaulting them here would make `ape config resolve`
assert a value nobody chose, behind a key whose whole point is that the
framework resolves it.

A config template that never mentions them is correct rather than drifted,
which is why they are exempt from the template-drift check that otherwise
requires every resolved variable to be declared.

### `timestamp` never moves backwards

The `timestamp` this command emits — local wall-clock `YYYYMMDDHHMMSS`, the
value skills copy into `updated_at`, `generated_at`, `frozen_at` and their
own report headers — is issued monotonically. Every issue returns
`max(now, last_issued)`, so a machine whose clock is behind cannot write a
field backwards.

The floor persists in `{output_folder}/ape/timestamp.state`. When that file
is absent — a fresh clone, a cleaned `_output` — it seeds from the newest
`updated_at` already in `sprint-status.yaml`, so a clean checkout cannot
silently reset it. `date` derives from the same clamped instant, so a
record can never carry a date of one day and a timestamp of the next.

A skewed clock is **clamped, never fatal**: keeping the floor is a
deterministic repair that needs no judgement, and failing instead would
trade a corrupt field for a dead run. `ape sprint verify`'s exit-5
backwards-write check remains the detector of last resort. If the output
folder is unwritable the floor is simply not persisted — the command still
resolves.

## Registries: ADRs, patterns, features, capabilities

Each family carries the same four verbs, and answers to its plural
(`ape adrs verify` is `ape adr verify`):

```bash
ape adr verify                  # four checks, exit 0 with findings
ape adr sync --check            # what reconciling would change
ape adr sync                    # reconcile the index against disk
ape registry verify --all       # every family at once
```

`verify` does **exactly four checks and no others**: set equality between
the directory and `index.yaml` in both directions, every index `file:`
resolving against the index's own directory, duplicate ids on both sides,
and whether a record parses as frontmatter at all. No schema validation, no
field drift, no tag comparison, no `updated_at` comparison — those are
judgment, and a verifier that wanders into them stops being trustworthy.

An `index.yaml` that is absent while records exist is one finding
(`registry.index_missing`), not one per record.

`sync` is the repair for what `verify` reports, and nothing more: it copies
what a record's own frontmatter already states and invents no titles or
statuses. A renamed record keeps its authored index entry rather than being
dropped and re-added with less.

### A headerless record, and the removal `sync` will not make

A record with no frontmatter block claims no id. `verify` therefore reports
it twice — `registry.record_unparseable` for the record, and
`registry.phantom_entry` for the index entry that now looks unclaimed — and
those two findings always arrive together.

That entry is the last copy of the record's id, title, type, status,
version and dates. Removing it turns a missing header into an unrecoverable
loss, on a file that was sitting on disk the whole time. So `sync`
**withholds** the removal and says why:

```text
WITHHELD 1 removal(s) — an index entry here may be the only copy of a
record's metadata, and removing it is not reversible short of git:
  keep  adrs  ADR-0032  the record at adr-0032_….md is on disk but its
                        frontmatter is unreadable, so it claims no id —
                        this entry is its metadata, not a phantom
```

Two rules, narrowest first: an entry whose `file:` names an unreadable
record is that record's entry; and while *any* record in the family is
unreadable, no entry in it can be shown to be a phantom, because the
unreadable one may claim that very id. Adds and file-repointing still run.

The repair is `sync` in the other direction:

```bash
ape registry restore-headers --all --check    # what it would write
ape registry restore-headers --all
```

It gives a headerless record the frontmatter its index entry already
states — every value copied verbatim, sequences like `tags` included, minus
the two index-bookkeeping keys (`file`, `slug`) and plus the
`output_document` self-reference. Both findings then dissolve and `sync`
has nothing left to withhold.

It will **not** touch a record that has a frontmatter block, even one that
fails to parse: overwriting a header someone authored to satisfy a checker
is a different and far worse operation. That case is reported and left for
a person.

`ape <family> update --updates -` applies field deltas to entries that are
already listed, and fails before writing anything if it is handed an id
that is not — creating an entry is the job of the skill that authors the
document it points at.

## Stories

```bash
ape story fields --select story_id,epic,status,features
ape story verify                                  # corpus report, exit 0
ape story verify --file path/to/1-1_thing.md      # gate, exit 0/2/3/4
ape story verify --fix --check                    # derivable repairs, dry
ape story verify --fix                            # apply them
```

`fields` reads at most 8 KiB per file, stops at the closing `---`, and
never opens a body. A file counts as a story only if it has a `story_id`,
which is what keeps retrospectives, epic briefs and the deferred-work stub
out of the result.

The trailer is part of the answer: `files_scanned`, `stories_matched`,
per-field presence counts and `bytes_read`. "This field is absent
everywhere" and "this field was never looked for" are different results,
and only the trailer distinguishes them.

`verify` has two modes with deliberately different contracts. The corpus
mode is a **report** — exit 0 even with findings, `--strict` to make it 1.
`--file` is a **gate**, with the exit codes its callers already branch on:
0 valid, 2 parse failure, 3 a required or extension-conditional key absent,
4 the keys are fine and the **body** is not.

> `--strict` must never be set from inside `apex-review-story`,
> `apex-code-review` or `apex-epic-batch-review`. A non-zero exit on those
> paths converts a defer into a patch, raises `unfixed_patches`, and demotes
> the story.

### The story-shape classes (`--file` only)

Corpus mode checks frontmatter. `--file` also checks the **body**, which is
what lets the framework delete the structural prose four skills each
restate in their own words.

| Check | What it catches |
| ----- | --------------- |
| `story.section_missing` | a section the derived set requires is absent |
| `story.file_list_marker` | a File List entry with no marker, an unknown one, or the em-dash-prose form standing in for one |
| `story.placeholder_residue` | `_(populated during dev)_` surviving at `status: review` |
| `story.compliance_table_header` | a compliance table header that is not the declared shape |
| `story.gcc_line_form` | a GCC line that is not `- [ ] **[ID]** {instruction} -- {scope}` |
| `story.adrs_considered` | `adrs_applicable` not binding against the recomputed tag-match count |
| `story.adr_unresolved` | a `governance.adrs` id resolving to no ADR at HEAD |
| `story.adr_not_accepted` | a cited ADR that is not `accepted` — **exit 0**, reported under `flagged` |

**The derived section set is a function of the resolved config alone.** No
writer stamps a story type into frontmatter, so there is nothing else to
read: `## Story`, `## Acceptance Criteria`, `## Tasks / Subtasks`,
`## Dev Notes`, `## Dev Agent Record` with its four subsections and
`## Change Log` are always members; `### Governance Compliance Criteria`
and `## Governance` join on `ext_adrs` **or** `ext_patterns`, the two
compliance tables on their own flags, and `## Feature Scope` on
`ext_features` alone. `## UX Specification` is **not** a member — the
template calls it a frontend-story section, which is exactly the
type-dependent judgement no key records.

An empty section is accepted; a missing header is not. `## Feature Scope`
carrying `_No features apply to this story._` is "no matches", never a
missing section.

Two details that differ from the prose they replace:

- **The File List marker vocabulary is all five the template declares** —
  `(created)`, `(modified)`, `(deleted)`, `(planned)`, `(deferred)`. The
  last two are `apex-lift-project`'s, and rejecting them would report a
  finding on every lifted story. Backticks around the path are canonical
  but not required by this check: a bare path is still read as an entry,
  so the commonest defect — no marker at all — cannot slip past.
- **`### Debug Log References` carrying `_No issues encountered._` is a
  terminal convention, not residue.** Its writer emits it as a complete
  answer, so it is never a finding.

`story.adrs_considered` recomputes the tag-match candidate count from the
body and binds the story's declared `governance_pass.adrs_applicable`
against it as a **bound, never an equality**: the findings are
`adrs_applicable > adrs_considered` and `adrs_applicable == 0` against a
non-zero candidate set. Applicability itself is the producing agent's
judgement, so a recomputed-applicability mismatch is deliberately not a
finding — a deterministic CLI reproducing a judgement would fire on every
legitimate keep-or-drop. The candidate set counts only ADRs that could
produce a compliance criterion at all (`status: accepted`,
`type != pattern`).

The two ADR classes need the project's corpus, which `--file` finds by
walking up from the story's own path. Against a story outside any project
they **skip and say so** under `skipped_checks`; the mode keeps working
there, and a governance class that says nothing when it could not run
would read as one that passed.

### `--fix`, and the two classes it refuses

`--fix` repairs exactly one class: a `depends_on` item that YAML decoded as
a number. `- 53.1` has one right answer — the same characters, quoted —
derivable from the finding alone with no second source. It handles both the
flow and block shapes and leaves an already-quoted item as authored.

Two other classes look just as mechanical and are not:

- **`features:` items must be `{id, contribution}` objects.** The
  contribution is not in the finding. Its only source is the prose
  `## Stories` table inside the feature record — and where that table has
  no row for the story, the disagreement between the two documents *is* the
  defect. Coercing a value here would fabricate a lifecycle edge.
- **A missing `features:`/`capabilities:` key needs a value.** `[]` is a
  claim that the story contributes to nothing, true only if no record lists
  it. That is a registry question, not a frontmatter one.

Both are reported as remaining rather than dropped, and both belong to
`apex-frontmatter-repair`, the framework skill that may read documents and
judge. This is the same split as `ape deferred verify` and
`apex-deferred-repair`: deterministic work here, judgment in a versioned
skill.

The repair is lexical, so the rest of the file — key order, quoting style,
the hand-wrapped flow sequences a node round-trip would reflow — is
byte-identical afterwards. That is also how a fixer corrupts a file while
reporting success, so every touched file is re-verified and rolled back if
its finding count did not fall.

## Sprint tracker

```bash
ape sprint check                                  # divergence, always exit 0
ape sprint verify --file … --key 1-1 --expected done
ape sprint reconcile --epic 12
```

`check` compares tracker rows against story files and **reports both sides
of every divergence without picking a winner**. It always exits 0 and has
no `--strict`: which side is right is judgment, and wiring it into a build
loop would stop runs over something no tool can fix. It belongs in
`ape doctor` and nowhere else.

`reconcile` also refreshes a **`Sprint` tab on the project's board**, if the
project has one — story and epic counts, what is in flight, what is blocked.
See [Watching a run on the board](use-the-board.md#watching-a-run-on-the-board).

The join is the **story key** — the story file's stem, which is what the
tracker rows on. A story's frontmatter `story_id` is a *different* string
(`1-1_greet-a-name` vs `"1.1"`); it travels in the finding for legibility
and is never correlated on.

### `sprint.nonstandard_row_key`, and why it exists

A story row keyed `7-3`, with no separator and slug, is reported on its own
rather than as a missing story file:

```console
$ ape sprint check
sprint.nonstandard_row_key  7-3  done  done
  row key has no separator and slug, so it names no story file — and
  `ape sprint reconcile` counts it toward epic 7 while the
  reconcile-epic-status.py it replaces does not. rename the row `7-3` to
  `7-3_payment-retry` to match the story file, or rename
  `7-3_payment-retry.md` to `7-3.md`
```

Two things are wrong with such a row, and the second one is invisible
without this check. It names no story file, because
`apex-create-story` writes `{story_key}.md` and a story key carries a slug.
And **the two live implementations of the epic projection disagree about
it**: `reconcile-epic-status.py` matches story rows with `^(\d+)-\d+[-_]`,
which requires the separator, so a bare row contributes nothing to its
epic; `ape sprint reconcile` counts it. Until the Python is retired the
same tracker reconciles differently depending on which one ran.

**One misnamed row is one finding.** When exactly one story file could be
the row — its stem is the row key plus a separator and a slug —
`sprint check` names that rename outright and suppresses both of the
findings it stands in for: no `row_without_story` for the row, and no
`story_without_row` for the file. Either one would send the reader off to
create something that should not exist. The finding carries the file's
`path`, `story_id` and status so both sides are still visible.

Two candidate files is a different problem, not a worse one: the row cannot
be renamed to both, so the message names them and picks neither, and each
file keeps its own `story_without_row`. Whichever branch it lands in, the
candidates are in the finding's `candidates[]` field — not deciding which
file a row meant is deliberate, but making the decider parse prose to learn
what the options were is not.

When nothing on disk matches, the finding says exactly that — "no story file
matches this row" — and names the two real options: the story is filed under
an unrelated name, or the row is stale. It does **not** fall back to the
generic advice, which ends by pointing at `ape sprint check` and would be
this command sending the reader to re-run itself.

`ape sprint reconcile` reports the same rows on its own output — including
on a run that changed nothing, because the divergence is in what was
*counted*, not in what was written — and carries them as `bare_row_keys`
plus a `bare_row_key_remediation` string in its JSON. Its remediation is
the **generic** form (`N-M` → `N-M_slug`), because reconcile is
tracker-only by design and never reads a story file, so it cannot know what
the row should have been called. It says so, and points at `sprint check`.

`verify` asserts one row landed as written, including against the last
committed value — the comparison that actually catches a backwards write,
since `updated_at` against `created_at` passes trivially when both are
written in the same operation. Exit 5 means "deterministically repairable":
re-write the field as the reported clamp value or later, re-run, report the
clamp, and never stop the run.

> **The lock sidecar is gitignored for you.** `reconcile` takes an advisory
> lock on a `sprint-status.yaml.lock` sidecar and never unlinks it —
> releasing a lock and deleting the file are different acts, and deleting one
> another process may be waiting on is how the mutual exclusion is lost. So
> the file stays, the framework reconciles at six boundaries, and an
> untracked artifact sits beside the tracker waiting to be swept up by a
> `git add -A`.
>
> `ape framework setup` and `ape framework update` append
> `sprint-status.yaml.lock` to the project `.gitignore` — the entry is that
> narrow deliberately, since a blanket `*.lock` would also ignore
> `Cargo.lock`, `flake.lock` and friends, which belong in history. They skip
> the write when git already ignores the sidecar by any rule of your own.
> `ape doctor`'s `sprint.lock_ignored` row reports the state for a project
> that has not been updated yet, and reports it more loudly once the file is
> already committed — ignoring a tracked file changes nothing until it is
> untracked too.

`reconcile` projects an epic's row from its story rows. It changes exactly
two lines — the `epic-N:` value and the body `updated_at` — leaving every
comment, the row ordering and the sync-generated header byte-identical, and
it takes an exclusive lock because concurrent per-epic sub-agents reconcile
the same file.

## Team memory

```bash
ape memory index          # ordinal, section, date, size, title
ape memory show 3,7,12    # verbatim bodies
ape memory check          # size against two budgets
```

`team-memory.md` outgrew whole-file reading: at 431,950 bytes a `Read`
fails outright, including for the retrospective that is told to re-read it
before editing it.

`index` is structurally lossless and carries no filter or ranking, because
most call sites sit inside `## On Activation` — which runs *before* the
story is identified, so no predicate keyed on "this story's domain" could
work there.

`check` never reads the file; it stats it. That is what makes it cheap
enough to run on every retrospective:

| state | means |
| ----- | ----- |
| `absent` | no `team-memory.md` yet — a fresh project, not a problem |
| `ok` | under the soft budget |
| `over-soft` | compaction is due at the next epic close |
| `over-hard` | approaching the 256 KiB Read cap; the soft gate was missed |

**It exits 0 whatever the state.** The verdict is the `state` field, not
the exit code — a failing exit would abort the retrospective at exactly the
moment compaction is most needed. `--fail-at soft|hard` opts CI into a
non-zero exit, and `ape doctor` fails on `over-hard` so a real breach stays
non-ignorable either way.

## Deferred work

```bash
ape deferred ingest --story 54-1 --skill apex-review-story --body-file /tmp/d.txt
ape deferred list --owner platform
ape deferred verify
ape deferred close DW-20260822-a1b2c3 --by "54-2"
```

One file per record under `{development_folder}/deferred/`. The store sits
outside `implementation_folder` deliberately: ten skills glob
`{implementation_folder}/**/*.md` across 17 sites, and record files under
that folder would feed every one of them.

Two files there are prose about the store rather than records in it, and
the loader skips both by name: `README.md`, and `PREAMBLE.md` where a
migration parks the ledger text that is not a deferred item.

`ingest` takes bullets from `--body-file` or stdin, never argv — 108 of 109
real bodies contain backticks, which shell-expand inside an argument. It
**never exits non-zero for a content reason**: an unrecognised bullet is
stored verbatim as free-form with a warning, and empty input is a no-op.
Only an unwritable store fails.

A bullet whose own text already announces its discharge — an appended
`RESOLVED` clause, a `[Closed: <sha>]` or `[Superseded: <artifact>]`
companion line — is stored in `closed/` rather than the working set, with a
warning saying so. The open set is a *directory*, not a status filter, so a
`status: closed` file written beside the open records would sit in `list`
forever. `migrate` reads the same markers, which is what makes the
closure-marker check below a fact.

`verify` (alias `lint`) tags each finding:

- `confidence: certain` — a fact. Schema problems; `related[]` /
  `supersedes[]` pointing at records that do not exist; an open record whose
  own body says it was discharged.

An open record that says it was discharged is **two** checks, not one,
because the two mean opposite things and need opposite remediations —
branch on the check name rather than re-reading the body:

| Check                                       | The record's text says           | Remediation             |
| ------------------------------------------- | -------------------------------- | ----------------------- |
| `deferred.closure_marker_in_open_record`    | the work was **done**            | an operator `close`s it |
| `deferred.superseded_marker_in_open_record` | it was **overtaken**, never done | `ape deferred discard`  |
- `confidence: candidate` — a heuristic, never auto-actionable. A dead
  anchor, a trigger naming a now-done story, a near-duplicate title, a
  free-form record.

The closure-marker check is a fact rather than a heuristic because no write
door can produce it: `migrate` and `ingest` both read the discharge markers
before they write, so a record that says it is closed and is sitting in the
working set got there by a later append or a hand edit. It is reported, never
acted on — discharging a record is judgment, and `repair` is forbidden to
close one.

`free_form: true` means **nothing on that record was interpreted** — the
body is the text verbatim, the title is its first line, and no field was
parsed from it. That is the contract `repair` relies on: it can treat the
body as the only evidence instead of having to distrust half-filled
fields.

Nothing here ever closes a record. Closing requires re-verifying the
premises against HEAD, and on the reference ledger an unbiased sample of 20
records found 5 already delivered.

`close` moves a record to `closed/` and never deletes it. Closed records
leave the working set but stay on disk, which is what stops an LLM
re-filing work it already did.

### Three statuses, and why `discard` is not `close`

```bash
ape deferred close   DW-… --by "54-2"
ape deferred discard DW-… --reason "superseded by the 91-2 rewrite" \
                          --evidence "pkg/a.go:12 is gone at HEAD"
ape deferred list --status discarded
```

A record is `open`, `closed` (the work was done) or `discarded` (the work
was never needed). Those last two are different claims and are not
interchangeable:

| | records | body |
| --- | --- | --- |
| `close` | `resolved_by`, `resolved_at` | gains a discharge marker |
| `discard` | `discard_reason`, `discard_evidence` | untouched |

Using `close` for a discard asserts work happened that did not, and it
mutates the body — breaking the byte-identity the migration's whole
verification design exists to protect. `verify` cannot tell the two apart
afterwards, because it accepts `resolved_by` **or** `discard_reason`, so
the mistake is silent. That is why the third status has its own writer.

`--status closed` matches discarded records too, since both have left the
working set; `--status discarded` narrows to just the discards.

### Migrating an existing ledger

```bash
ape deferred migrate --dry-run              # parse and verify, write nothing
ape deferred migrate                        # history recovery is ON by default
ape deferred migrate --no-recover-deleted   # skip it — a one-way choice
ape deferred recover                        # recover into an ALREADY-migrated store
```

The migration verifies before it writes, **against the ledger**: every
significant source line must come back out in something the migration
writes — a record body, a record's `source_heading`, or the `PREAMBLE.md`
that holds ledger prose which is not a record — N records parsed must equal
N files written, and every body must survive a round trip, or nothing
lands. Line endings are the one normalisation: a CRLF ledger yields LF
bodies.

The against-the-ledger half matters more than it sounds. Checking only that
records round-trip through the serialiser proves the parser is consistent
with itself, which a badly wrong parse can be — the first field migration
inherited stale section context, turned headings into records and swallowed
the ledger's preamble, and reported clean success on 124 records.

It is idempotent from disk state, never deletes the source (the ledger
becomes a short signpost), and **commits nothing**: it prints the paths and
the `git add` line, and you group the change into however many commits you
want.

The completion output reports how many bytes of cited text changed folders.
A repo-wide gate scoped to `implementation_folder` — an anchor count, a
citation ratchet — will drop the moment this lands, with nothing actually
regressed. Re-baseline it rather than chasing it.

**History recovery runs by default, and only on the first migration.** The
ledger's only eviction mechanism was deletion, so its git history holds
records that exist nowhere else; the migration mines them into `closed/` as
tombstones. It defaults on because the choice is not symmetric — recovery
only ever writes to `closed/` and cannot touch the working set, the cost is
one `git show` per revision, and skipping it forfeits that history
permanently. After the migration, `migrate` reports `already migrated` and
returns before the recovery step, so the flag is inert; it now says so
rather than exiting 0 in silence.

Use **`ape deferred recover`** for a store that has already been migrated.
It reads history *through* the stub now sitting at the ledger path, and
de-duplicates against what is on disk rather than a fresh parse, so it is
safe to re-run. It is the only route that does not cost you every `close`,
`discard` and repair edit made since the migration.

**Re-migrating after a parser fix.** Record ids are derived from the
ledger, so a corrected parse yields different ids and the two sets cannot
be merged. Restore the ledger from git and delete the store directory; the
migration refuses to run into a store that still holds records rather than
interleaving two migrations on disk.

`ape framework update` runs it for you; `--no-migrate` leaves it pending,
and `ape doctor` reports that so the state is visible rather than silent.

`ape deferred repair` is the judgment half: it dispatches a framework skill
on opus to complete or retire the free-form records. It is opt-in, refuses
without a TTY (it spends real money), and asserts the on-disk record count
did not fall afterwards.

## Documents

```bash
ape doc verify prd.md              # gate: duplicate heading slugs
ape doc shard prd.md prd/          # split, rewriting relative links
ape doc assemble prd/ prd.md       # concatenate back
ape doc analyze _output/handoffs   # sizes, groups, routing
```

`shard` always writes an `index.md` listing every section file — the
calling skill treats its absence as proof the command did not complete. It
refuses when two headings slugify to the same value rather than writing
`foo-2.md` siblings that consumers cannot distinguish.

## Checking all of it at once

```bash
ape doctor
```

Eight rows report project data: `config.resolved`, `registry.drift`,
`story.frontmatter`, `sprint.divergence`, `sprint.lock_ignored`,
`output.ape_ignored`, `memory.size` and `migration.pending`. All degrade to
INFO outside a project. `memory.size` and `config.resolved` are the only two that can fail
the run — see [Run doctor in CI](run-doctor-in-ci.md).

An eighth project-scoped row, `runs.legacy_layout`, reports on ape's own
artifacts rather than the project's records: run history still sitting at
the pre-`{output_folder}/ape` paths. `ape framework update` relocates it —
see [How to read the output folder](run-artefacts.md).

## Related

- [Framework update](framework-update.md) — where migrations run
- [Run doctor in CI](run-doctor-in-ci.md) — what each check means on a runner
- [CLI reference](../reference/cli.md) — every flag

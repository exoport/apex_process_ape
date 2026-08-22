---
plan_id: PLAN-25
created_at: 2026-08-21
status: draft
tags:
  - cli
  - config
  - python-retirement
  - governance
  - registries
  - stories
  - memory
  - deferred-work
  - migration
  - framework
summary: >
  Give `ape` the deterministic project-data commands the APEX framework skills need,
  so the skills stop doing mechanical work by hand — and so `ape` can see the project's
  artifacts at all. Today no `.go` file in this repo reads `_apex/config.yaml`'s folder
  variables, so `ape adr list` reports "no ADR directory found" against a project with
  64 ADRs on disk. Path resolution (D1) is therefore the gate on everything else: a
  validator shipped without it finds zero records, reports OK, and reproduces the exact
  failure it was built to catch. On top of it: registry verification and index writing
  for the four governance families, story frontmatter projection and verification,
  sprint divergence reporting, bounded team-memory reads, a one-file-per-record
  deferred-work store with a verified, reviewable migration, epic-status reconciliation,
  and markdown sharding. Ten of the framework's seventeen Python scripts (3,267 of 6,011
  lines — one triplicated, one already forked into two divergent copies) are replaced and
  retired, which removes PyYAML from every review path — where a missing interpreter
  dependency currently downgrades a verified post-write check to an eye-check nobody records.
  Zero new Go modules;
  stdlib + cobra + `gopkg.in/yaml.v3` + `golang.org/x/sys`, all already direct requires.
origin:
  - Upstream: `apex_process_framework_eval/_output/apex-implementation-plan.md`
    (2026-08-21, framework base v0.10.2 `e0d7aab`, ape base v0.0.52). That document's
    Part B is the `ape` CLI requirement list; its Part D adds three more commands, three
    flags on an existing command, and turns `ape framework update` into something that
    mutates project data and commits. This plan is the `ape`-side half of both, restated
    against what is actually in this repo.
  - 2026-08-21 review of that plan against this tree. Ten claims did not survive the
    check; two of them were missing deliverables rather than wording (see "Findings that
    reshaped the work"). Command naming and flag conventions were unified in the same
    pass, because the upstream plan proposes 16 subcommands in a shape that splits this
    CLI's surface in two.
  - Owner's calls during that review: the store is `ape deferred`, not `ape defer`;
    noun-first command naming throughout, with plural aliases on the four governance
    families; malformed `config.local.yaml` is a loud failure, not a silent fallback; the
    Python verifiers are replaced and retired, not kept alongside.
  - APEX-framework-side review of this plan, 2026-08-21 (third pass). Every quantitative
    claim about the framework repo checked out; ten changes requested, all applied. The
    substantive one: **the PyYAML failure mode is a silent degrade, not a demotion** — three
    of the four call sites carry an explicit non-HALT fallback, so the retirement's case is
    consolidation and testability, and D12's promotion into Wave 2 was reverted. Also added:
    D13's `index.md` contract (a missing deliverable), the corrected caller sets for D3/D5/D13,
    the `apex-distillator` edit collision, the three-bump landing rule, and the two fallback
    deletions the framework owes. Three of the review's own counts were low; the plan uses
    the verified figures.
  - Framework-side re-review, 2026-08-21 (fourth pass). Two count errors in the third-pass
    corrections: D13's caller figure conflated `shard-doc.py` (the script, which is replaced)
    with `apex-shard-doc` (the skill, which is not) — the product of a substring grep — and
    D5's "4 skills" was 4 files in 3 skills. Both corrected against verified greps. The
    conflation mattered beyond arithmetic: six `- skill: apex-shard-doc` pipeline dispatch
    entries sit inside that same substring, so a grep-scoped retirement edit would break
    three pipelines.
  - Owner's calls 2026-08-21 (second pass): Decision 1 (flags) and Decision 2 (memory
    check) accepted as written. The three Python scripts this plan first deferred —
    `reconcile-epic-status.py`, `shard-doc.py`, `analyze_sources.py` — are promoted into
    scope as D12–D14. Every commit `ape` makes is to be stated in one place — the "Commit
    policy" section — and **no command in this plan may commit**: `ape` writes the files, the
    operator commits them as one commit or two, whichever they prefer.
---

# PLAN-25: Project-data commands — one path resolver, ten retired scripts, and the deferred-work store

## Goal

Make `ape` the deterministic half of an APEX run. Every command here answers a question
a skill currently answers by reading files into an LLM context window — or answers
wrongly, because it cannot find the files at all.

The shape of every command in this plan is the same: **measure or assert, report
findings, and leave judgment to the caller.** Nothing here decides what a defer means,
which of two divergent statuses is right, or whether one memory entry supersedes another.

## Why now

Three facts, each independently sufficient:

**`ape` cannot see the project.** `findADRDir()` (`internal/apecmd/adr.go:150-164`) probes
`development/adrs` and `$APE_PROCESS_REPO/development/adrs`. A real project sets
`governance_folder: development/governance`, so:

```
$ ape adr list ; echo exit=$?
no ADR directory found (looking for development/adrs/)
exit=0
$ ls development/governance/adrs/*.md | wc -l
64
```

No `.go` file in this tree mentions `governance_folder` or `config.local.yaml`.
`findPatternsDir()` (`pattern.go:104-107`) has the same defect, and `ape bootstrap` writes
to a **third** location (`bootstrap.go:231-233`, `<out>/governance/adrs`), so `ape`
already disagrees with itself about where an ADR lives.

**The framework's own artifacts have outgrown a whole-file read.** `team-memory.md` is
431,950 B on the reference project and a whole-file `Read` now fails outright (`exceeds
maximum allowed size (256KB)`) — including for the retrospective that is instructed to
re-read it before editing it. `deferred-work.md` is 456,144 B with 109 live records, and
its only eviction mechanism is deletion.

**A missing PyYAML silently downgrades a verified gate to an eye-check.**
`verify-sprint-status-row.py` exits 1 with `ERROR: PyYAML is not installed` (line 47), and
the framework anticipated that: `apex-review-story/steps/step-04-present.md:830` and
`apex-code-review/steps/step-04-present.md:360` both say *"If the script itself cannot run
(no `python3` or PyYAML), fall back to re-reading `{sprint_status}` and comparing the row
value by eye."* So the failure mode is not a HALT — it is a **silent degrade**: the
post-write verification that exists because "a row write can silently fail to land" becomes
a model eyeballing a value, with nothing in the run recording that the real check never
executed. A statically-linked Go binary removes the degrade path entirely, which is the
whole point of C2's "no fallback branches".

One site does not carry that fallback: `step-04-present.md:713`, the cross-story reopen
path (`--key {owner_key}`). A verification that still fails after its one re-write routes
the finding to **branch 2** — `fixed: false` — which parks a patch and holds the story
*under review* at `in-progress`. Even there it is explicitly "FALLBACK — never HALT". One
site, on the reopen path, not the defer path.

## Non-goals

- **Judgment.** No `contribution` vocabulary, no enum checks, no JSON Schema engine, no
  closing a defer, no picking a winner between a tracker row and a story file, no
  deciding that memory entry 47 supersedes entry 12. Upstream C5 holds throughout.
- **New Go modules.** stdlib + cobra + `gopkg.in/yaml.v3` + `golang.org/x/sys` (D12's file
  lock), all already direct requires. `CGO_ENABLED=0` across six release targets, so nothing
  needing cgo.
- **An API client in `ape`.** D9's judgment phase dispatches a framework skill through
  the existing PTY task runner. No HTTP client, no model constants, no credentials.
- **Framework text edits.** Upstream Part A is the framework repo's work. This plan
  ships the commands those edits call, and names the call sites so the two land in the
  right order.
- **Writing git history on the operator's behalf.** Nothing here commits. The three commit
  sites that already exist (pipeline step, pipeline stage, `ape task --task-commit`) are
  untouched and gain no siblings — see the Commit policy.
- **The `epic-close.yaml` pipeline** (upstream A12). Pipelines are read from
  `<project>/_apex/pipelines/` and installed from the framework repo — verified: nothing
  in this tree embeds a pipeline spec. That deliverable needs no `ape` change at all.
- **Rewriting `ape` self-update, `pipeline`, `task`, `chat`, `prompt`, `costs`,
  `sandbox`, `service`.** Untouched.

## Findings that reshaped the work — corrections to the upstream plan (U-series)

Ten checks of the upstream plan against this tree. **U1** and **U10** found missing
deliverables — one command that is depended on but never specified, and three more scripts
that belong in the same wave. The rest are corrections that change what gets built.

**U1 — `ape memory check` is required but never specified.** Upstream D2 and D5 both
depend on it (two thresholds, the retrospective's compaction gate, an `ape doctor` row).
Part B specifies only `memory index` and `memory show`. It is a missing Part B
deliverable on D2's critical path — the whole compaction ceremony hangs off it. Spec'd
here as D6, with the exit-code question settled in Decision 2.

**U2 — The migration command has two spellings.** Upstream D3 step 1 says
`ape defer migrate --from … --recover-deleted`; its D3.1 table and its D6 escape hatches
say `ape migrate defer`. Settled by the owner's call to `ape deferred migrate` — one noun
owns the whole store's vocabulary, and there is no second migration to justify a top-level
`migrate` group.

**U3 — `--json` and `--project` would split the CLI surface.** Every shipped command
uses `--output-format human|json|yaml` and `--cwd`, `ape doctor` — the command the
upstream plan tells us to imitate — included, and `CLAUDE.md` mandates it. `--json`
exists exactly once, as a **hidden deprecated alias** on `ape task` (`task.go:166,170`).
Settled in Decision 1.

**U4 — `ape defer` cannot be a Go package.** `defer` is a keyword. Renamed to
`ape deferred` by the owner's call, which resolves the package name to
`internal/deferred` for free.

**U5 — "`ape adr validate` keeps exiting 0, this is not a breaking change" is half
true.** The exit code survives; the JSON payload does not. Today it emits
`{dir, count, files[]}` (`validate_util.go:12-19`); the replacement emits `findings[]`.
Any caller parsing that JSON breaks. Recorded in the changed-commands table as the one
genuinely breaking surface.

**U6 — There is a third stub.** `ape pattern sync` (`pattern.go:87-96`) is a hidden
"not yet implemented" stub, separate from `ape sync patterns`. Two paths to one verb.
Resolved by the naming decision (D0).

**U7 — `ape framework update` currently makes zero commits.** Nothing in
`internal/framework/` commits to the project; `gitcmd.go` reads only (`IsClean`,
`HeadSHA`, `SkillsPorcelain`). Upstream D6's sample output ends "3 commits: framework
files, migration, repair", which silently changes that too. This plan keeps the property
intact by adding **no commit anywhere** — see the Commit policy.

**U8 — The clean-tree gate has an ordering trap.** Upstream D6 requires a clean tree for
the migration, but the framework file install *itself* dirties the tree, so a check
evaluated afterwards is meaningless. D10 resolves it by scoping the gate — and the
staging — to the paths the migration touches, which are disjoint from the install's, rather
than by imposing an ordering constraint a reviewer cannot see. See the Commit policy.

**U9 — Upstream D6's default spends money and needs a TTY.** The repair phase spawns an
opus session by default, with `--no-repair` as the opt-out, on a command whose current
contract is "copy some files". D9/D10 invert it: `--repair` is opt-in, and it refuses
without a TTY the way `pickBootstrapper` already refuses to seed silently
(`framework.go:310`).

**U10 — The Python overlap is larger than two scripts.** The upstream plan's B5/B6 name
`verify-story-frontmatter.py` and `verify-sprint-status-row.py`. A full inventory of the
framework's 17 Python scripts (6,011 lines) found `verify-sprint-status-row.py`
**byte-identical in three skills** (`md5 60115b36…`) and `render-index-update.py`
**already forked into two divergent copies** (189 vs 231 lines, list-shaped vs
mapping-shaped). Retirement scope, and what is deliberately kept, are in D15.

## Design

### D0 · Command surface and flag conventions

The current surface is overwhelmingly **noun-first, verb-second**: `ape adr list`,
`ape pattern validate`, `ape framework update`, `ape sandbox forward`, `ape sessions …`.
Exactly one command inverts it — `ape sync adrs|patterns` — and two are single-verb
commands about `ape` itself (`ape update`, `ape rollback`), which are not counter-examples.

**The rule: `ape <noun> <verb>`, always.** The canonical noun is singular (`adr`,
`pattern`, `feature`, `capability`, `story`, `sprint`, `memory`, `config`, `registry`,
`doc`); `deferred` is not a count noun and stays as it is.

**The four governance families also answer to their plurals.** `adrs`, `patterns`,
`features` and `capabilities` are registered as cobra `Aliases` on the family commands, so
`ape adrs verify` and `ape adr verify` are the same command. Three reasons this is worth
the two lines of code: the framework's own vocabulary is plural everywhere it names a
collection (`{governance_folder}/adrs/`, the index's `adrs:` / `patterns:` /
`capabilities:` list keys, `extensions: [ext-adrs, ext-features]`), so a skill author
writing `ape adrs sync` is following the directory they are pointing at; `ape adrs sync` is
one word away from the retired `ape sync adrs`, which makes the muscle-memory transition
free; and an alias costs nothing at runtime — cobra resolves it before dispatch, so there
is one implementation, one help page, and one docs entry.

The canonical singular is what the help text, `docs/reference/cli.md` and every example
use. Aliases are a courtesy, not a second surface: no flag, output shape or exit code
differs, and nothing in the framework is *required* to use either spelling.

`writeCommandSection` in `internal/apecmd/gendocs.go:77-99` does not currently emit
`Aliases`, so `make docs-cli` would silently drop them from the reference. One line there,
in the same deliverable.

**Package map.** Cobra files go in `internal/apecmd/<noun>.go`, one noun per file, per the
house convention; the logic lives in six new packages, all of them in the portable set:

| Package             | Owns                                    | Deliverables    |
| ------------------- | --------------------------------------- | --------------- |
| `internal/apexcfg`  | config resolution + the 17 variables    | D1              |
| `internal/registry` | the four governance families            | D2, D3          |
| `internal/story`    | frontmatter projection + verification   | D4, D5          |
| `internal/sprint`   | tracker compare, row verify, projection | D5, D12         |
| `internal/memory`   | entry index, bodies, size check         | D6              |
| `internal/deferred` | the record store, migration, repair     | D7, D8, D9      |
| `internal/apexdoc`  | sharding, assembly, source survey       | D13, D14        |

`internal/apexdoc` rather than `internal/doc` — `doc` shadows nothing in the stdlib but
reads as a package of documentation, and `apexcfg`/`apexdoc` keep the two framework-shaped
helpers recognisable next to the domain packages.

Three verbs, with contracts that are the point of the naming:

| Verb     | Contract                                                                 | Exit                                          |
| -------- | ------------------------------------------------------------------------ | --------------------------------------------- |
| `list`   | project the corpus; no judgment                                          | 0                                             |
| `verify` | assert invariants; emit `findings[]`                                     | 0 by default; `--strict` → 1                  |
| `check`  | measure against a budget, or report a divergence with no resolvable winner | documented per command; never fails by default |

`validate` is dropped as a synonym for `verify` — it exists on two shipped commands and
is kept there as a hidden alias for one release.

The full surface. **Bold** is new in this plan; *italic* is an existing command whose
behaviour changes:

```
ape config      resolve                                        # D1

ape adr         list | verify* | new | sync+ | update+          # D2, D3   alias: adrs
ape pattern     list | verify* | sync+ | update+                # D2, D3   alias: patterns
ape feature     list+ | verify+ | sync+ | update+               # D2, D3   alias: features
ape capability  list+ | verify+ | sync+ | update+               # D2, D3   alias: capabilities
ape registry    verify+ | sync+          [--all | --family]     # D2, D3 fan-out

ape story       fields+ | verify+                               # D4, D5
ape sprint      check+ | verify+ | reconcile+                   # D5, D12
ape memory      index+ | show+ | check+                         # D6
ape deferred    ingest+ | list+ | close+ | verify+              # D7
                | migrate+ | repair+                            # D8, D9
ape doc         verify+ | shard+ | assemble+ | analyze+          # D13, D14

ape framework update  [--dry-run+ | --no-migrate+ | --repair+]  # D10
ape doctor            (+6 checks)                               # D11

  +  new in this plan
  *  existing command whose behaviour changes (see the change table)
```

Retired or aliased:

| Was                    | Becomes                | How                                                            |
| ---------------------- | ---------------------- | -------------------------------------------------------------- |
| `ape sync adrs`        | `ape adr sync`         | top-level `ape sync` kept hidden for one release, then deleted  |
| `ape sync patterns`    | `ape pattern sync`     | same                                                            |
| `ape pattern sync` (stub) | `ape pattern sync`  | same name, but the stub becomes the real implementation         |
| `ape adr validate`     | `ape adr verify`       | `validate` hidden for one release, then deleted                 |
| `ape pattern validate` | `ape pattern verify`   | same                                                            |

Two verbs on the registry families need distinguishing, because they are different
operations that the framework currently does with two different tools:

- **`sync`** reconciles the index against disk — adds orphan records, drops phantom
  entries, refreshes `generated_at`. `--check` makes it a dry-run diff, which is the
  semantic the parent `sync` command's existing `--check` flag already declares
  (`sync.go:11,19`).
- **`update`** applies entry-level field deltas to an index that already lists the
  entry, and fails fast on an unknown id — the exact contract of
  `render-index-update.py`, whose docstring says so: *"creating index entries is the job
  of the skills that author the documents they point at."*

#### Decision 1 — `--output-format`/`--cwd` vs `--json`/`--project` · **ACCEPTED 2026-08-21**

This is settled first because it lands on every new subcommand and is nearly free to get
right now, expensive later.

**Option A — the house convention: `--output-format human|json|yaml` and `--cwd <dir>`.**
Mandated by `CLAUDE.md`; used by every shipped command; `ape doctor` — the registry
pattern the upstream plan tells us to copy — uses both (`doctor.go:210,213`).
`internal/output`'s `Format`/`Print` already give all three formats for free, so the
implementation cost is zero. Costs: `--output-format json` is verbose in skill prose.

**Option B — the upstream plan's shorthand: `--json` and `--project <dir>`.** Terser,
and `--project` arguably reads better than `--cwd` (which sounds like an instruction to
change directory). Costs: it splits this CLI's flag vocabulary in two — 30-odd shipped
commands one way, 33 new ones the other; `yaml` output is lost for the new half; it
contradicts `CLAUDE.md`; and `--json` is currently a *hidden deprecated alias* on
`ape task`, so blessing it re-opens a decision this repo already closed.

**Option C — Option A plus a `--json` shorthand alias on the new commands.** Skills type
`--json`, scripts keep `yaml`, docs show one canonical form. Costs: two ways to say one
thing, and it makes `ape task --json` (hidden, deprecated) inconsistent in the opposite
direction — a reader would reasonably conclude `--json` is the modern spelling.

**Option D — Option A, but the data commands default to `json` when stdout is not a
TTY.** Removes the verbosity problem entirely for skills. Costs: implicit format
switching is a footgun (the same command produces different output in a pipe than at a
prompt), it would be the only command family in the CLI doing it, and upstream D6's own
sample output shows humans read these too.

**Decision: Option A, verbatim, with no aliases.** The verbosity objection is weak
because the callers are 91 generated skill files, not humans at a prompt — prose has no
character budget, and every one of those call sites is being written fresh in this wave
anyway. Everything else in the objection column is a real cost. Concretely, every new
subcommand takes:

```
--cwd <dir>                      project root (default: cwd; resolved per D1)
--output-format human|json|yaml  default human
--strict                         on every `verify` only: promote findings to exit 1
```

Diagnostics go to **stderr**; stdout carries only the payload — the stdout discipline
PLAN-13/17 already established, and the reason a skill can pipe any of these into `jq`.
Where the upstream plan gives a command a second format axis (`ape defer list --format
brief|full|json`), the two axes are separated: `--output-format` picks the encoding,
`--detail brief|full` picks how much the human rendering says.

### D1 · `ape config resolve` — build first; everything else is inert without it

```
ape config resolve [--cwd <dir>] [--output-format human|json|yaml]
```

Walk up from `--cwd` for `_apex/config.yaml`; key-wise overlay `_apex/config.local.yaml`.
Emit the 17 variables the framework's On-Activation block resolves (verified against
`_apex/config.yaml` in the framework repo and the identical list in every
`SKILL.md`), the four derived `ext_*` booleans, the resolved absolute root,
`local_overlay_applied`, and `date` / `timestamp`.

`timestamp` is **local wall-clock**, never UTC unless the host is — the framework is
explicit about this, and every skill writing `updated_at` reads it from here.

| Exit | When                                                                |
| ---- | ------------------------------------------------------------------- |
| 0    | resolved (with or without a local overlay)                          |
| 2    | `_apex/config.yaml` or `_apex/config.local.yaml` is malformed        |
| 4    | no `_apex/config.yaml` in `--cwd` or any parent                     |

**Malformed `config.local.yaml` is a loud failure** — exit 2, naming the file and the parse
error. Owner's call, and the right one: this is the first act of every skill, so a silent
fallback to base values would let one typo'd override run an entire pipeline against the
wrong folders, writing real files to a path nobody chose. C2 says a broken setup must fail
loudly and early; this is that.

**These are command-local exit codes**, in the style `ape framework` (3–7) and
`ape bootstrap` (2–4) already use — *not* the shared table in `exitcodes.go`, where 4 means
"claude exited before the Stop hook fired". That table governs the run commands (`task`,
`pipeline`, `prompt`); a project-data command that never spawns claude cannot produce its
codes and should not borrow its meanings. Worth stating because a reader who checks
`exitcodes.go` will otherwise think exit 4 here is a bug. Every new command in this plan
documents its own codes in its `Long` help.

`governance_repository_path` gets an explicit resolution answer in the payload:
when set, it names an external governance repo, and the emitted ADR/pattern roots say
which tree won. Without that, D2 would happily audit the wrong one and report it clean.

**Also in this deliverable:** repoint every path resolver at the result —
`findADRDir()` (`adr.go:150-164`), `findPatternsDir()` (`pattern.go:104-107`),
`ape adr new`'s `development/adrs` fallback (`adr.go:103`), and `writeArtifacts()`'s
`<out>/governance` (`bootstrap.go:231-233`). Four resolvers, one answer.

**Tests.** Base only; base + local key-wise overlay (only present keys replace); local
absent → `local_overlay_applied: false`, exit 0; malformed local → exit 2 naming the key;
no config anywhere → exit 4; `--cwd` nested three levels deep; walk-up terminates at the
filesystem root without looping; all 17 keys present; four `ext_*` booleans derived from
`extensions`; `date`/`timestamp` formats against an injected clock; three output formats;
`governance_repository_path` empty vs set. Regression, the one that matters: a temp
project with `governance_folder: development/governance` and *N* ADR files makes
`ape adr list` return *N*.

### D2 · `ape <family> verify` + `ape registry verify` — exactly four checks

```
ape adr|pattern|feature|capability verify [--cwd] [--strict] [--output-format]
ape registry verify [--all | --family adrs,patterns,…] [--strict] [--output-format]
```

**Exactly four checks. No schema validation, no field drift, no tag comparison, no
`updated_at` comparison.**

1. Set-equality directory ↔ `index.yaml`, both directions → `registry.orphan_record` /
   `registry.phantom_entry`
2. `os.Stat` every index `file:` against the index's own directory →
   `registry.file_unresolved`
3. Duplicate ids, in the index and on disk → `registry.duplicate_id`
4. Record parses as frontmatter at all → `registry.record_unparseable`

One `internal/registry` package with a per-family descriptor
(`{name, dir, listKey, extFlag, indexShape}`), so the four families are one code path and
`ape registry verify --all` is a fan-out, not a fifth implementation. Families whose
`ext_*` flag is false are skipped, not failed.

**An absent `index.yaml` with records on disk is one finding, not one per record.** It is
the degenerate case of check 1 — one side of the set comparison is missing entirely — so it
reports a single `registry.index_missing` and stops there. Emitting 64 `orphan_record`
findings would be technically correct and practically useless: the noise hides the one fact
that matters, and the repair (`ape adr sync`) is the same either way. This is not a fifth
check; it is check 1 refusing to spam.

Exit 0 by default; exit 1 only under `--strict`. This is what makes it safe to call from
anywhere: the gates that can fail are `ape doctor` and a pipeline stage, neither of which
is a pre-existing contract.

**Replaces** `runMarkdownDirValidate` (`validate_util.go:24-51`), which does `os.ReadDir`,
prints `OK: <file>` for every `.md`, and returns nil. It never calls `os.Open`.

**Tests.** Orphan record (on disk, `status: accepted`, absent from the index — the
ADR-0050 shape: cited by 42 story files and missing from `index.yaml` across all 57
commits that touched it); phantom entry; `file:` unresolved and `file:` resolved relative
to the index's own dir; duplicate id in the index; duplicate id across two files on disk;
unparseable record; clean corpus → zero findings, exit 0; `--strict` → 1 with findings,
0 when clean; each of the four families plus `--all`; an `ext`-disabled family skipped;
`index.yaml` absent with records present → exactly one `registry.index_missing`;
`findings[]` JSON golden.

The **false-positive** tests are the ones that decide whether this is trustworthy, so the
clean fixture deliberately contains everything that is *not* a record: `changelog/*.yaml`
sidecars (the real corpus has 54), `index.yaml` itself, a `README.md`, and a non-`.md`
file. "Zero false positives on the other 63" means exactly this. The <10 ms target is
asserted on the 64-record corpus that produced it; a generated 500-record corpus is a
separate scaling check, so a regression in either is attributable.

### D3 · `ape <family> sync` and `ape <family> update` — fill the stubs, retire the fork

```
ape adr|pattern|feature|capability sync   [--check] [--cwd] [--output-format]
ape adr|pattern|feature|capability update --updates <file|-> --generated-at <ts>
ape registry sync [--all|--family …] [--check]
```

`sync` reconciles the index against disk — the repair for D2's findings — and `--check`
makes it a dry-run diff. This fills `ape sync adrs`, `ape sync patterns` and the
`ape pattern sync` stub, all three of which currently print "not yet implemented".

`update` applies entry-level field deltas from JSON on stdin (`{"<id>": {"<field>":
"<value>"}}`), refreshes top-level `generated_at`, writes block-style YAML with 2-space
indent preserving insertion order, then round-trip-parses the result — and exits non-zero
on any error, including an id that is not already in the index. That is
`render-index-update.py`'s contract, kept verbatim so the calling prose needs no rewrite
beyond the command name.

**The caller set is five skills and seven invocation lines**, not the three the first draft
of this plan named (framework review, verified):

| Skill                     | Invocation                       | Reaches into                  |
| ------------------------- | -------------------------------- | ----------------------------- |
| `apex-feature-update`     | `step-02-apply.md`               | its own `resources/`          |
| `apex-pattern-update`     | `step-04-apply.md` (×2)          | its own `resources/`          |
| `apex-adr-update`         | `step-02-apply.md:134,215`       | `../apex-pattern-update/`     |
| `apex-capability-update`  | `step-02-apply.md:159`           | `../apex-pattern-update/`     |
| `apex-feature-refresh`    | `step-03-write.md:138`           | `../apex-feature-update/`     |

That right-hand column is the structural fact worth keeping: **two script copies serve five
skills**, three of them by reaching across a sibling skill's directory. Of ~345 sibling-skill
references across 91 skills, these are read-only invocations rather than writes, so they do
not trip the rule upstream A3 exists to enforce — but they are exactly why the fork went
unnoticed, since no single skill owns both copies. One binary makes the sibling reach
unnecessary.

It must handle **both** index shapes — list-shaped (`adrs`, `patterns`, `capabilities`)
and mapping-shaped (`features`) — because that difference is precisely why the Python
forked into two divergent copies. One descriptor field, one implementation, no drift.

**Tests.** Reconcile adds orphans and drops phantoms; `--check` writes nothing and reports
the same diff; `generated_at` refreshed on mutation only; unknown id → non-zero *before*
any write (fail-fast, asserted by checking the file is byte-identical after the failure);
both index shapes; insertion order and comments preserved where the Python preserved them;
round-trip parse failure surfaces rather than writing corrupt YAML; a golden byte-compare
against `render-index-update.py`'s output for the same input, on both shapes — the
retirement gate.

### D4 · `ape story fields` — the 377× read reduction

```
ape story fields --select k1,k2,… [--cwd] [--output-format]
```

Walk `{implementation_folder}`, read at most 8 KB per `.md`, stop at the closing `---`,
count a file as a story only if `story_id` is present, emit only the named top-level keys
plus their nested blocks. **Never open a body.**

Trailer: `{files_scanned, stories_matched, fields, per_field_present_count}`. A field
present in zero stories is exit 0 with `present: 0` and an empty column — the caller
asked a legitimate question and got a true answer.

This is what lets `apex-feature-refresh` stop doing "load each story file completely"
(23.5 MB / 5.9M tokens against a 1M ceiling) to read four frontmatter fields.

**Tests.** A file whose *body* contains a `story_id:` line at 100 KB is not misparsed —
this proves both the 8 KB cap and stop-at-closing-`---`, and it is the test that would
catch the naive implementation; no `story_id` → not a story; nested block extraction;
zero-presence field → exit 0; one malformed frontmatter → that file skipped with a stderr
warning, rc 0, every other file returned; frontmatter over 8 KB → reported, never
silently truncated; CRLF and BOM; empty or unknown `--select` → exit 2; trailer counts.
Then a generated 465-file corpus read through a counting reader, asserting total bytes
read ≈ 8 KB × files: that asserts the *mechanism* behind the 377× claim rather than a
wall-clock number that varies by machine. Retros, epic files and `deferred-work.md` are
excluded — the same test that proves the deferred store can live anywhere without
poisoning story discovery.

### D5 · `ape story verify`, `ape sprint check`, `ape sprint verify`

```
ape story  verify [--cwd] [--strict] [--output-format]          # corpus: report
ape story  verify --file <path> [--active-extensions a,b]       # one file: gate
ape sprint check  [--cwd] [--output-format]                     # divergence: report
ape sprint verify --file <sprint-status.yaml> --key <k> --expected <status>
```

**`ape story verify` (corpus)** — three check classes, no more:

1. Extension-gated required-key **presence** (`governance.adrs`, `governance.patterns`,
   `features`, `capabilities`, gated on D1's resolved `ext_*` flags)
2. **Type** checks as Go type switches: `features` items must be objects, not bare
   strings; `depends_on` items must be strings, not YAML floats
3. **Referential integrity**: every `governance.adrs` / `features` / `capabilities` id
   resolves to a record on disk, via D2's loader

No enum checks, no `contribution` vocabulary, no JSON Schema engine. Exit 0 by default.
`--strict` is documented as never settable from inside `apex-review-story`,
`apex-code-review` or `apex-epic-batch-review`.

**`ape story verify --file`** is the per-file replacement for
`verify-story-frontmatter.py` and keeps that script's exit codes exactly — 0 valid,
2 parse failure, 3 missing required or extension-conditional key — so the calling prose
in `apex-create-story`, `apex-story-amend` and `apex-lift-project` changes only the
command name — 4 invocation lines (`apex-story-amend/steps/step-02-apply.md:82`,
`apex-create-story/SKILL.md:456`, `apex-lift-project/steps/step-07b-backfill.md:140` and
`:285`) plus 6 prose references — a 10-line framework edit across 4 files in **3 skills**
(`apex-create-story`, `apex-story-amend`, `apex-lift-project`; `apex-story-amend` carries it
in both its `SKILL.md` and its step file).
It keeps the Python's `--active-extensions` flag too, rather than resolving
them through D1: the caller already has the list in hand, the flag makes the check
reproducible against a file outside any project, and a drop-in replacement that quietly
changed where its gating came from would be the one difference nobody tested for. Absent
the flag, it falls back to D1. Those three are not C1-protected skills, so a non-zero exit there is safe;
verified against the call sites (`apex-create-story/SKILL.md:95,456`,
`apex-story-amend/steps/step-02-apply.md:82`,
`apex-lift-project/steps/step-07b-backfill.md:15`).

**`ape sprint check`** set-compares `sprint-status.yaml` rows against story files on disk
(classifying out `epic-*` and `*-retrospective` rows) and compares each row's status
against that story's own frontmatter, normalising the tracker's `drafted` to
`ready-for-dev`. It **reports divergence, never picks a winner, and always exits 0.**
Wired into `ape doctor` only — not the build loop, because it produces findings no tool
can resolve.

**`ape sprint verify`** replaces `verify-sprint-status-row.py` and preserves its exit
codes exactly — 0 match, 2 unreadable/malformed, 3 key missing or value differs, 4
`updated_at` precedes `created_at`, 5 `updated_at` earlier than the last committed value.
Those codes are load-bearing: `apex-review-story` and `apex-code-review` branch on 5
specifically ("the repair is to re-write the field as the committed value and re-run,
never to stop the run"). The improvement is not the codes — it is that **exit 1 stops
existing as a class**, which is what lets the framework delete the two "compare the row by
eye" fallbacks at `apex-review-story/step-04-present.md:830` and
`apex-code-review/step-04-present.md:360`. Those are the silent-degrade branches C2 forbids,
and they are only deletable once the check cannot fail for an environment reason. The git
comparison against the last committed revision stays; `internal/framework/gitcmd.go`'s
`runGit` is the existing pattern for it.

**Tests.** *Corpus*: reproduce the known failure set exactly on a fixture mirroring
53-1..53-6, 54-1..54-5, 55-1, 55-2 (missing `features`) plus 28-1 (missing
`capabilities`) — 14 findings, no more; 15 bare-string `features` entries; three
`depends_on` values that yaml.v3 decodes as `float64` (reported, never coerced); ext
gating (`ext_features: false` → no presence findings); referential integrity against a
records fixture; exit 0 default, 1 under `--strict`; **a negative test asserting
`contribution: contributes` produces no finding** — that one locks C5 against a future
"helpful" enum. *Per-file*: each of exit 0/2/3, and a byte-compare of the message text
against the Python for a fixture set, which is the retirement gate. *Sprint check*: row
without story file; story file without row; status divergence; `drafted` normalised (not
a finding); `epic-*` and `*-retrospective` classified out; **exit 0 even with findings**,
as a lock test, because that is a contract and not an accident; malformed and missing
`sprint-status.yaml`. *Sprint verify*: all five exit codes, including a git fixture whose
committed `updated_at` is later than the working copy's (exit 5), and the
`created_at == updated_at` case that passes trivially under a naive comparison.

### D6 · `ape memory index` / `show` / `check`

```
ape memory index [--cwd] [--output-format]
ape memory show  <n>[,<n>,…] [--cwd]
ape memory check [--cwd] [--soft <bytes>] [--hard <bytes>] [--fail-at never|soft|hard]
```

`index` emits one line per `### ` entry of `{development_folder}/team-memory.md` —
ordinal, section code, date, byte size, title — plus a total. **Structurally lossless. No
filter, no ranking, no predicate.** That constraint is not aesthetic: most call sites sit
inside `## On Activation`, which runs *before* the story is identified, so no predicate
keyed on "this story's domain" can work there. Selection has to be possible from ordinal,
section, date, size and title alone.

`show` returns verbatim bodies for the named ordinals. Exit 2 on out-of-range.

One hazard the upstream plan does not name: **a `### ` line inside a fenced code block.**
Retrospectives paste commands and diffs into memory entries. A naive `^### ` scan counts
those as entries, silently shifting every ordinal after it — and `show <n>` then hands a
skill the wrong entry with no way to notice. The scanner is fence-aware, with a test.

#### Decision 2 — `ape memory check`: thresholds, flags, exit codes · **ACCEPTED 2026-08-21**

Upstream D2 gives the intent (soft ~40 KB = "compaction is due"; hard 200 KB =
"approaching the 256 KB Read cap, the file is about to become unreadable by its own
writer") and says `check` **fails** at the hard ceiling. It leaves the mechanics open,
and the mechanics are where this can go wrong.

The consumers pull in different directions. The retrospective needs a machine-readable
gate — *should I spawn `apex-distillator`?* — evaluated on every run, cheaply.
`ape doctor` needs a status it can map to WARN/FAIL. CI would like a failing exit.

**Exit-code options.**

*Option A — the exit code carries the verdict: 0 under soft, 1 over soft, 2 over hard.*
Cheapest to consume (`if [ $? -ne 0 ]`). But the framework's universal prose convention
is *"On non-zero exit: Guided: HALT"* — that is literally how
`render-index-update.py`'s call sites are written. Under that convention, a hard-ceiling
exit 1 makes the retrospective **abort at exactly the moment compaction is most needed**,
which is the opposite of the intent. The gate would break the ceremony it exists to
trigger.

*Option B — always exit 0; the verdict is a field.* `state: absent|ok|over-soft|over-hard`
in the payload, and the same word in the human line. Cannot be mistaken for failure by
any generic wrapper. Costs: the skill gates on a field rather than `$?`, and `ape doctor`
maps state → WARN/FAIL itself (trivial — it already does that per check).

*Option C — Option B plus an opt-in failing exit.* Safe by default; CI and `--strict`
consumers can still get a non-zero.

**Decision: Option C, with `--fail-at never|soft|hard`, default `never`.** One flag,
three values, no ambiguity, and it reads as what it does. `ape doctor` calls the library
rather than the CLI so it is unaffected; CI uses `--fail-at hard`; the retrospective calls
it bare and reads `state`. This deliberately deviates from upstream D2's "check **fails**"
wording, for the reason in Option A — and the deviation is safe because the thing upstream
wanted from a failing exit (a visible, non-ignorable hard-ceiling breach) is delivered by
`ape doctor` failing on that state, which is the second half of what D2 asks for anyway.

**Threshold options.** Hardcoded constants are unconfigurable — a project with a
different context budget cannot move them. Flags-only means every caller repeats them and
the retrospective's numbers drift from doctor's. Config keys in `_apex/config.yaml`
(`memory_soft_budget_bytes`, `memory_hard_ceiling_bytes`) resolved through D1 are the
right long-run home, but they are a framework template change and would make this
deliverable wait on one.

**Decision: defaults in Go — soft 40 KiB, hard 200 KiB — overridable by `--soft`/`--hard`,
with config keys deferred** (F1 in Deferred). The flags exist primarily so the thresholds
are testable without writing 200 KB fixtures; nothing in the framework passes them.

**Units are bytes**, because that is what `os.Stat` returns and what the 256 KB Read cap
is measured in. The human line also prints an estimated token count (chars/4) explicitly
marked as an estimate. Nothing ever gates on the estimate.

**`check` never reads the file.** `os.Stat` only. That is what makes it free enough to run
on every retrospective, which is what makes the gate reliable. An absent file is
`state: absent`, exit 0 — a fresh project has no `team-memory.md` and that is not a
problem.

**Tests.** `index`: emitted count equals `grep -c '^### '` on a fixture with H2 sections
and a preamble; byte spans sum to the file minus H2 headings and preamble; a `### ` inside
a code fence is not counted; empty file → zero entries, exit 0; CRLF fixture (needs
`.gitattributes -text`, per the byte-exact-fixture lesson from the `ape update` work).
`show`: byte-identical bodies; out-of-range → exit 2; duplicate and unsorted ordinals.
`check`: absent / under-soft / over-soft / over-hard states; `--fail-at` across all three
values × all four states; and a file **larger than the 256 KB Read cap** handled
correctly, which is what proves it stats rather than reads.

### D7 · `ape deferred` — the record store

```
ape deferred ingest --story <key> --skill <s> [--cycle N] [--body-file <f>]   # else stdin
ape deferred list   [--status open|closed|all] [--owner] [--story] [--path] [--group]
                    [--detail brief|full] [--output-format]
ape deferred close  <id> --by "<story|reason>"
ape deferred verify [--strict] [--output-format]
```

**Store:** one file per record under `{development_folder}/deferred/`, named
`<id>_<slug>.md`, YAML frontmatter plus verbatim markdown body — the same shape
`development/governance/adrs/` already uses. Derived `index.yaml` is gitignored and
rebuilt on read. `closed/` holds tombstones, invisible without `--status closed|all`.

The directory sits **outside `{implementation_folder}`** deliberately: ten skills across
17 sites glob `{implementation_folder}/**/*.md`, and putting 227 records under that folder
would feed every one of them. Siting it one level up costs zero skill edits and cannot
collide. D4's story-detection test (`story_id` present or it is not a story) is the belt
to that braces.

**Body arrives on stdin or `--body-file`, never as an argv string** — 108 of 109 real
bodies contain backticks, which shell-expand inside an argument.

**`--body-file` is the form the framework calls, and stdin is the form a human calls.** Not
a style preference: `apex-review-story/steps/step-04-present.md:13` bans shell expansion
(`$()`, `$$`, `$?`) and chaining (`&&`, `|`) inside code blocks. Input redirection is not
named there, so upstream A8's `ape defer ingest … < /tmp/defer-{story_key}.txt` sits in a
grey zone that would need either a carve-out or an argument about what "shell expansion"
covers. `--body-file <path>` is unambiguously a plain argument and needs neither. The skill
writes the scratch file with the **Write tool** (never a heredoc — 79 of 82 measured bodies
contain backticks) and passes its path. Recorded here so nobody "simplifies" the call site
back to a redirect.

Two non-negotiable properties:

- **`ingest` never exits non-zero for a content reason.** It is reachable from
  `apex-review-story`'s emit path, where a non-zero exit converts a defer into a patch,
  raises `unfixed_patches`, and demotes the story to `in-progress`. A malformed line
  warns on stderr and is stored verbatim. Empty stdin is a rc-0 no-op. Only a genuine
  setup failure — unwritable directory — is allowed to fail, which is C2's distinction.
- **One malformed record on disk loses exactly that record.** Warn on stderr, return the
  rest, rc 0. This is why one-file-per-record was chosen: the single-store alternative
  returned *zero* records when handed one bad file.

`verify` replaces the upstream plan's `lint`. It carries both halves — hard invariants
(schema, `related[]`/`supersedes[]` targets exist) and heuristics (duplicate candidates by
title similarity, anchors whose `file:line` no longer resolves, a `trigger:` naming a story
that is now `done`) — with the heuristic findings tagged `confidence: candidate` and
documented as never auto-actionable. One word, one command, and the tag carries the
distinction that a second verb name would have. `lint` stays as a hidden alias.

Relations are a `group: <slug>` field plus optional `related[]` / `supersedes[]`.
**Not a DAG** — 0 of 109 records assert an ordering constraint, the largest related family
is 4, and there are no chains of length 3.

**Tests.** `ingest`: body byte-identical through backticks, double quotes and the
`— defer:` em-dash tail; the `- [ ]` checkbox survives (the current path strips it in 100%
of the 109 live records); deterministic id and slug with a same-title-same-day collision;
`--cycle N`; empty stdin → rc 0 no-op; malformed line → warning, rc 0, stored verbatim;
unwritable dir → non-zero (the one allowed failure). `list`: every filter; `closed/`
invisible by default; `--detail` × `--output-format` matrix. `close`: writes
`- [x] [Defer] … — resolved: <story_key> (<date>)`, never deletes, idempotent on a second
close. `verify`: dangling `related[]`/`supersedes[]`, duplicate titles, dead anchors, a
fired `trigger:`, `confidence` tagging, exit 0 default and 1 under `--strict`. Store:
**one malformed record loses one record, rc 0, others returned**; derived index rebuilt
from disk; a stale index ignored; slug-derived filenames Windows-safe (no `:` or `?`
surviving from a title).

### D8 · `ape deferred migrate` — verified, reviewable, once

```
ape deferred migrate [--from <legacy.md>] [--recover-deleted] [--dry-run] [--cwd]
```

Converts the legacy single-file ledger into record files. The mapping is fixed, because
this is the only place losslessness can be lost:

| Legacy source                                               | Becomes                                                    |
| ----------------------------------------------------------- | ---------------------------------------------------------- |
| `## Deferred from: story review of <key> (<date>)`          | `source_story`, `source: story-review`, `created`           |
| `## Deferred from: apex-correct-course reconciliation of …` | `source: correct-course`                                   |
| `- [Defer] <title>` / `- [ ] [Defer] <title>`               | `title` (checkbox discarded; `status` carries it)           |
| `[<file>:<line>]` citation bracket                          | `anchors[]`                                                |
| `— defer: outside-story=… ; cross-cycle=… ; non-blocking=…` | three fields, verbatim, unparsed                           |
| `owner=…`                                                   | `owner`                                                    |
| `trigger: …`                                                | `trigger`                                                  |
| `next-batch brief: …`                                       | `next_action`                                              |
| everything after the tail                                   | the markdown **body**, byte-identical                      |
| recovered from git history                                  | same mapping, written to `closed/` with `status: closed`    |

**Unparseable is not fatal.** A record whose tail does not match keeps its full text as
the body, takes `title` from the first line, and is flagged by `ape deferred verify`. It is
never dropped and never guessed at — 26 of 109 records are free-form, so this path runs on
every real migration.

Required properties: **verified before write** (N records in → N files out, bodies
byte-identical; assertion fails → nothing is written and it says why), **idempotent**,
detected from disk state (does `deferred/` exist, is the legacy file a stub) with no stored
version marker to drift, and **never deletes the source** — the legacy file becomes a
15-line stub pointing at the directory, and the content stays in git.

**It does not commit.** The files land in the working tree and the operator commits — see
the Commit policy. What `ape` owes in exchange is a truthful account of what it touched: the
run ends with the counts, the exact paths, and the `git add` line for them. The precondition
that makes that account trustworthy is a porcelain check **scoped to those same paths**
(not the whole tree), because with no commit to isolate ape's work, `git status` is the only
separation between it and the operator's WIP. `--dry-run` writes nothing.

**Tests.** A fixture ledger exercising every row of the table above; N-in/N-out with
byte-identical bodies; free-form record → verbatim body, title from first line, flagged by
`verify`; second run is a no-op (stub + directory detected); `--dry-run` writes nothing;
`--recover-deleted` against a git fixture whose history contains a commit that deleted
records; and the negative that matters — inject a count mismatch and assert **nothing is
written**, the exit is non-zero, and the message names the mismatch.

### D9 · `ape deferred repair` — judgment, in its own phase

```
ape deferred repair [--dry-run] [--cwd]
```

Free-form records cannot be completed deterministically: deciding whether a messy note is
real work, what it points at, and whether it is already dead is judgment. So an LLM does
it — in its own phase, never inside the verified migration.

|            | D8 `migrate`                    | D9 `repair`                    |
| ---------- | ------------------------------- | ------------------------------ |
| Does       | parses all, writes complete + verbatim | completes or retires the free-form ones |
| Nature     | deterministic                   | judgment                       |
| Idempotent | yes                             | no — a one-shot proposal       |
| Review as  | a byte-identity assertion       | a diff, read line by line      |

Keeping them apart is what preserves the migration's guarantees: a model's output can never
invalidate a byte-identity check. Neither phase commits, so how they are grouped in history
is the operator's decision — the plan's job is to make them separable, not to separate them
(see the Commit policy).

**Mechanism: reuse what exists.** `ape` already runs a framework skill through the PTY task
runner with a model override (`ape task <skill> --model opus`, `task.go:153`). `repair`
spawns `apex-defer-repair` on opus through that same path. The prompt therefore lives in
the framework as a versioned, reviewable skill rather than as a Go string literal, and
`ape` gains no HTTP client, no credentials and no model constant.

Two additions to the upstream design:

- **`repair` refuses without a TTY** unless explicitly forced, matching
  `pickBootstrapper`'s existing refusal to seed silently (`framework.go:310`). It spends
  money; it should not do that from a script that did not ask.
- **An `ape`-side post-condition:** the on-disk record count must not fall. Upstream D3.1's
  "`discard` never deletes" is an absolute rule that currently lives only in a prompt, and
  a prompt is not an enforcement mechanism. If the count drops, `repair` says so loudly and
  names the missing records, so the operator can `git restore` those paths before committing
  anything.

**Tests.** The invocation builder produces the expected skill/model/args (unit test on the
builder — the way `task` and `pipeline` invocation shapes are already tested here, not a
live run); `--dry-run` prints the plan and spawns nothing; non-TTY refuses; the
record-count post-condition trips on a fixture where records vanish.

### D10 · `ape framework update` performs the migration

```
ape framework update [--dry-run] [--no-migrate] [--repair] [--repo] [--cwd]
```

Project-data migrations run as part of `ape framework update`, not as a separate command a
skill has to police — so no skill ever meets an un-migrated project, and no skill needs a
migration failure path. This is the right transaction boundary: explicitly invoked by the
operator, at the moment framework expectations change, outside the build loop.

Three corrections to the upstream shape, all from U7–U9:

1. **The clean-tree gate is path-scoped, which dissolves the ordering trap.** Upstream's
   whole-tree gate has to be evaluated before the install, because the install dirties the
   tree — an ordering constraint that is easy to get wrong and impossible to see in review.
   Scoping the check to the paths the migration touches removes the constraint instead of
   documenting it: the install writes `.claude/skills/apex-*`, `_apex/pipelines/`,
   `_apex/framework.yaml`, `_apex/apex-operating-rules.md` and `CLAUDE.md`, while the
   migration writes under `development/` — **disjoint sets**, so order cannot matter. The
   disjointness gets an explicit test, because if a future framework install ever writes
   under `development/` the trap comes straight back. A dirty *migration* path still skips
   the migration, installs the framework files, and names the dirty paths.
2. **`ape framework update` commits nothing at all** — not the install, not the migration,
   not the repair. It has never committed and this plan keeps it that way; the whole result
   sits in the working tree for one `git diff`, and the operator groups it into however many
   commits they want. The run ends by printing the paths and the `git add` line for them.
3. **`--repair` is opt-in.** Upstream had `--no-repair` as the opt-out on a default that
   spawns a paid opus session.

`--dry-run` shows the framework diff *and* the pending migrations, writing nothing.
`--no-migrate` installs framework files only and leaves a visible pending state that
`ape doctor` reports.

**Tests.** The install's write set and the migration's write set are asserted disjoint (the
test that keeps the ordering trap dead); a dirty migration path → install proceeds,
migration skipped, dirty paths named; unrelated WIP elsewhere → migration proceeds anyway;
`--dry-run` writes nothing and lists both; `--no-migrate` → doctor reports pending;
`--repair` off by default and refused without a TTY; idempotency detected from disk state;
**`git log` is byte-identical before and after a full run** (the lock test for the
no-commit rule — it fails against any implementation that commits); the summary names every
path written and the `git add` line; non-git project (the migration still runs, since
nothing here needs git except `--recover-deleted`); migration summary present in all three
output formats.

### D11 · `ape doctor` wiring

Six checks appended to the `allChecks` registry (`doctor.go:123-148`):

| Check                  | Backed by | Required                              |
| ---------------------- | --------- | ------------------------------------- |
| `config.resolved`      | D1        | yes — nothing works without it        |
| `registry.drift`       | D2        | no (WARN)                             |
| `story.frontmatter`    | D5        | no (WARN)                             |
| `sprint.divergence`    | D5        | no (WARN)                             |
| `memory.size`          | D6        | **yes** — see below                   |
| `migration.pending`    | D8/D10    | no (WARN)                             |

`memory.size` must be `Required: true` or D6's hard ceiling can never surface: `runDoctor`
silently downgrades a non-required check's FAIL to WARN (`doctor.go:276-278`). It maps
`state: over-hard` → FAIL, `over-soft` → WARN, `ok`/`absent` → OK.

All six return INFO outside a project root, matching `checkFrameworkMetadata`
(`doctor_checks.go:168-177`). `docs/how-to/run-doctor-in-ci.md` carries a per-check table
that needs the new rows.

**Tests.** Each check's Required decision; each returns INFO outside a project;
`memory.size` FAILs on over-hard and WARNs on over-soft; a project with registry drift
stays exit 0 without `--strict`.

### D12 · `ape sprint reconcile` — the epic projection, with a real lock

```
ape sprint reconcile (--epic <N> | --all) [--cwd] [--output-format]
```

An epic's status is not a fact any skill asserts — it is a projection of the story rows
beneath it. `reconcile-epic-status.py` is the single implementation of that projection,
invoked at six boundaries that move a story. The rule is tracker-only (no story-file
reads) and is reproduced verbatim:

```
rows   = development_status keys matching ^{N}-\d+[-_]        (story rows of epic N)
active = rows whose status is not 'cancelled'

no rows              -> leave unchanged   (never close an epic that has no stories)
no active rows       -> leave unchanged   (all-cancelled is a scope decision)
all active 'done'    -> 'done'
all active 'backlog' -> 'backlog'
otherwise            -> 'in-progress'
```

`blocked` lands in the final clause, so a blocked story holds its epic open. An
unrecognised status is treated as neither `done` nor `backlog` — it can only ever hold an
epic open, never close it — and is **named in the output** rather than swallowed.

Three properties are load-bearing and easy to lose in a port:

- **Writes are targeted.** Only the matched `epic-N:` line and the body `updated_at` field
  change. Comments, ordering, story rows and the sync-generated header are untouched. That
  rules out the obvious Go implementation — unmarshal, mutate, re-marshal — because
  yaml.v3 discards comments and normalises formatting. It has to be a line-oriented edit
  plus an atomic same-directory temp-file rename.
- **`updated_at` never moves backwards.** Refreshed on mutation only, clamped, reported,
  never fatal.
- **The read-modify-write is locked.** Concurrent per-epic sub-agents reconcile the *same*
  file, and without a lock the last writer silently drops a sibling's update. The Python
  takes an exclusive advisory `flock` on a sidecar `<path>.lock` and degrades to a no-op
  where `fcntl` is missing. `ape` can do strictly better: `golang.org/x/sys` is already a
  direct require, so the lock is real on Windows too rather than silently absent.

**Exit policy.** The Python exits 1 on any error, missing PyYAML included, and it is called
from `apex-epic-batch-review`'s `epic-review-runner` — a mutation path, though one that
handles the failure: *"A non-zero exit is recorded, never a HALT and never an epic error"*,
with `{{epic_status}}` left at the row's unchanged value. So nothing breaks today; the epic
row just silently stops being re-derived at that boundary until `apex-sprint-sync` catches
up. `ape sprint reconcile` exits 0 for every *content* outcome (including an unrecognised
status, which is reported as a finding) and non-zero only for a genuine I/O failure such as
an unwritable tracker — which keeps the recorded-not-fatal contract meaningful instead of
making it a catch-all for a missing interpreter.

**Do not** emit an epic-close pipeline from here. `_reconcile_locked` returns at
`if not applied: return 0` (`:336-337`) before any insertion point, and on a mature project
every epic row is already `done`, so such a hook could never fire.

**Tests.** The projection truth table — all five branches, plus `blocked` and an
unrecognised value in each position; no rows; all-cancelled; `--epic N` vs `--all`. A
**full-file byte diff** asserting exactly two lines changed, with comments, ordering, story
rows and header preserved — that single test is what proves the targeted write. `updated_at`
clamped when the on-disk value is later, and the clamp reported rather than fatal. Two
concurrent reconciles of different epics in one file both landing (the lock test; it fails
against an unlocked implementation). Exit 0 on unrecognised status, non-zero on an
unwritable file. And the golden: same inputs through the Python and through `ape`, files
byte-identical, on POSIX and on Windows.

### D13 · `ape doc verify | shard | assemble` — markdown sharding

```
ape doc verify   <src> [--level N]                        # duplicate heading slugs
ape doc shard    <src> <dir> [--level N] [--numbered]     # explode
ape doc assemble <dir> <out>                              # reverse
```

`shard-doc.py`'s three commands, kept as three verbs on the `doc` noun. `shard` splits a
markdown document into section files at a heading level and **rewrites relative links to
account for the depth increase**; `assemble` reverses both the split and the rewrite;
`verify` is the pre-flight that refuses to explode a document with duplicate heading slugs
at the target level.

`ape doc verify` is a **gate**, not a report: exit 0 clean, exit 1 with the duplicate pairs
and their line numbers — the same documented exception to the verify contract as
`ape story verify --file`, and for the same reason. Its caller relies on the non-zero exit
to stop before writing anything.

**`shard` must also write an `index.md`.** This is a contract, not a nicety:
`apex-shard-doc/SKILL.md:111` states that *"`shard-doc.py explode` always produces an
`index.md` that lists and links all generated section files; its absence indicates the
command did not complete successfully"*, and the skill's Step 4 verifies it exists (`:110`,
`:121`). A replacement that splits and rewrites links perfectly but omits `index.md` passes
the round-trip golden and then **fails its real caller's own verification** — the most
expensive kind of near-miss, because the test suite says green. `assemble` must skip
`index.md` on the way back rather than treating it as a shard.

The slug function is the other compatibility surface. A slug that differs by one character
produces different filenames, and `assemble` then cannot find its own shards. It gets a
dedicated golden across a punctuation/unicode/collision fixture.

**The verb changes, so the caller edit is not just a command name.** `check|explode|assemble`
becomes `verify|shard|assemble` — D3's "no rewrite beyond the command name" does not transfer
here.

**And the edit must be scoped by `shard-doc.py`, never by `shard-doc`.** The string
`shard-doc` matches two different things, only one of which moves. An earlier revision of
this plan reported them as a single count of 11 across 7 skills; that number came from a
substring grep and is wrong. Verified split:

| | Count | Where |
| --- | --- | --- |
| **`shard-doc.py`** — the script, moves | 10 markdown lines, 5 skills | 5 in `apex-shard-doc/SKILL.md` (`:13,97,98,111,148`); 5 outside: `apex-orchestrator/resources/ape-execution.md:70`, `apex-create-architecture/steps/step-04-decisions.md:249` + `resources/ext-adrs.md:342`, `apex-lift-project/steps/step-07c-lifecycle-reconciliation.md:74`, `apex-create-data-architecture/steps/step-06-ripple.md:111` |
| **`apex-shard-doc`** — the skill, **does not move** | 15 lines outside its own directory | 8 in skill prose (`ape-execution.md:110,114,120`, `apex-create-epics-and-stories/steps/step-05-additive-from-proposal.md:47,109`, `apex-lift-project/steps/step-05b-shard.md:122` + `step-04b-ux.md:94`, `ape-execution.md:70`) and 7 in `_apex/pipelines/` |

The pipeline half is the reason this matters more than a miscount: **six of those lines are
`- skill: apex-shard-doc` dispatch entries** — `design.yaml:12,26,40`,
`design-headless.yaml:12,26`, `epics.yaml:28` — plus a prose comment at
`governance.yaml:2`. The skill keeps its name and keeps being dispatched; only the script it
calls internally is replaced. An edit scoped by a bare `grep -rl shard-doc` would rewrite
those dispatch lines and **break three pipelines** (four files, counting the comment), which
is a far worse outcome than a dangling prose reference.

`ape-execution.md:70` is the one line that carries both strings — a model-tiering note naming
the skill *and* its script — so it is the single site that needs reading rather than
substituting.

**Tests.** The round trip is the strongest test available and it is cheap: explode then
assemble a fixture corpus and assert the result is **byte-identical to the source**, at
several heading levels, with `--numbered` on and off, and with relative links at depth +1
and +2. **`index.md` present after every `shard`, listing and linking every section file,
byte-compared against the Python's** — and `assemble` ignoring it. Duplicate slugs detected
with correct line numbers; `--level` variants; a document with no headings at the requested
level; the slugify golden against the Python; exit codes 0/1 preserved. Stdlib-only in and
out — no new dependency for any of it.

### D14 · `ape doc analyze` — source survey for the distillator

```
ape doc analyze <path|folder|glob>… [--output-format] [--single-max-files N]
                [--single-max-tokens N] [--split-min-tokens N]
```

Enumerates files from paths, folders (recursively over `.md`/`.txt`/`.yaml`/`.yml`/`.json`)
and glob patterns, skipping `node_modules`, `.git`, `__pycache__`, `.venv`, `.claude`,
`.cursor`, `.vscode`. Emits per-file size, token estimate and detected document type; a
summary; suggested groupings by naming convention (a brief paired with its discovery notes,
each file tagged `primary`/`companion`/`standalone`); a routing recommendation
(`single` when ≤3 files **and** ≤15K estimated tokens, else `fan-out`); and a split
prediction (distillate estimated at ~1/3 of source, split `likely` above 5K tokens).

Two notes specific to this one:

- **It shares D6's token estimator.** Both are chars/4, both label the number an estimate,
  and neither ever gates a correctness decision on it. One helper, one place to change it.
- **This is the only retired script that wrote JSON to stdout.** Under Decision 1 the
  default is `human`, so the call site in `apex-distillator` gains `--output-format json` —
  a one-line prose edit — and the JSON payload is byte-comparable to the Python's for the
  golden.

The three thresholds are flags purely so the boundaries are testable without building
15K-token fixtures. Defaults match the Python exactly.

**This lands in a file that upstream is already rewriting — coordinate.**
`apex-distillator/SKILL.md` is simultaneously being reworked by upstream D2's compaction
dispatch (it needs `--autonomous`, `--no-commit`, in-place single-file editing, and no
splitting). D14 edits the same file at its Stage 1 call (`:79`) and its Stage 4 "Measure
distillate" call (`:175`). There is a third site neither review named: `:246` instructs the
operator to run pytest on `scripts/tests/test_analyze_sources.py`, which **disappears with
the suite** — leaving a dangling instruction if only the two call sites are updated. All
three should land as one framework edit alongside the compaction rework, not as a separate
pass over the same file.

**Tests.** Each input form (file, folder, glob) and the skip list; doc-type detection from
each naming convention; grouping, including a companion with no primary and a standalone;
the routing boundaries tested *on* the boundary (exactly 3 files, exactly 15K tokens — the
`≤` vs `<` bug this catches is the whole reason to test it); split prediction at exactly
5K; the JSON-shape golden against the Python. `tests/test_analyze_sources.py`'s 207 lines
of pytest cases are ported as Go table tests, which is the retirement gate — the Python
suite does not survive its subject.

### D15 · Python replacement and retirement

The framework ships 17 Python scripts, 6,011 lines. This plan retires **10 files, 3,267
lines (54%)**, removes PyYAML from every story-review mutation path, and takes the
framework's Python test tooling to zero.

| Script                             | LOC     | Copies                | Replaced by                    | Contract kept                  |
| ---------------------------------- | ------- | --------------------- | ------------------------------ | ------------------------------ |
| `verify-story-frontmatter.py`      | 291     | 1                     | `ape story verify --file` (D5) | exit codes 0/2/3               |
| `verify-sprint-status-row.py`      | 292     | **3, byte-identical** | `ape sprint verify` (D5)       | exit codes 0/2/3/4/5           |
| `render-index-update.py` (list)    | 231     | 1                     | `ape <family> update` (D3)     | fail-fast on unknown id        |
| `render-index-update.py` (mapping) | 189     | 1, **already forked** | `ape <family> update` (D3)     | same, mapping-shaped           |
| `reconcile-epic-status.py`         | 645     | 1                     | `ape sprint reconcile` (D12)   | the projection rule, verbatim  |
| `shard-doc.py`                     | 525     | 1                     | `ape doc` verbs (D13)          | explode/assemble round-trip    |
| `analyze_sources.py` + its tests   | 303+207 | 1                     | `ape doc analyze` (D14)        | JSON shape, routing thresholds |

**Total: 10 files, 3,267 lines.** What remains is 7 files, 2,744 lines — see "Keep as
Python" below.

Three things make this worth doing as one wave rather than script by script — none of them
urgency:

**The duplication is already costing.** `verify-sprint-status-row.py` is vendored
byte-identically into `apex-dev-story`, `apex-review-story` and `apex-code-review`
(`md5 60115b3680f14c29b6fbc85eb41b023d`), so every fix lands three times.
`render-index-update.py` has *already* diverged into two copies whose docstrings describe
each other. A single binary cannot drift from itself.

**PyYAML turns verified gates into unverifiable ones.** Five of the retired scripts exit 1
on a missing PyYAML (`verify-sprint-status-row.py:47`, `verify-story-frontmatter.py:31`,
both `render-index-update.py:36,39`, `reconcile-epic-status.py:76`). What that costs is
**not** a demotion — an earlier draft of this plan claimed it was, and the call sites say
otherwise:

| Site                                          | On an unrunnable script                                     |
| --------------------------------------------- | ----------------------------------------------------------- |
| `apex-review-story/step-04-present.md:830`    | falls back to comparing the row "by eye"                    |
| `apex-code-review/step-04-present.md:360`     | the identical sentence                                      |
| `epic-review-runner.md` (reconcile call)      | "A non-zero exit is recorded, never a HALT and never an epic error" |
| `apex-review-story/step-04-present.md:713`    | **no fallback** — routes to branch 2, parking a patch        |

Three of the four already degrade gracefully, and only the fourth — the cross-story reopen
path — holds a story at `in-progress`, and even that is written as "FALLBACK — never HALT".
So the case for retirement is **consolidation and testability**, the same category as D13
and D14: one implementation instead of four vendored copies, one Go test suite instead of
three `--self-test` harnesses plus a pytest run, and exit 1 removed as a class so the
degrade branches can be deleted (see the last paragraph of this section). That is a real
gain. It is not an emergency, and this plan should not sequence work as though it were.

**The framework's Python test tooling disappears.** `tests/test_analyze_sources.py` is the
framework's only pytest suite; retiring D14's target retires it too. Three `--self-test`
harnesses embedded in `verify-story-frontmatter.py`, `verify-sprint-status-row.py` and
`reconcile-epic-status.py` go with them, replaced by Go table tests that run in the same
`make test` as everything else.

Every retirement is gated on a **golden byte-compare against the Python** for the same
inputs — across both index shapes, every exit code, and (for D13) a full
explode→assemble round trip plus the `index.md` contract. Retirement itself is a
framework-repo edit on the framework's own cadence; this plan ships the replacements and the
goldens that prove equivalence. Until the deletion lands, both implementations exist and the
Python is what the skills call.

**The framework edit is not a binary swap — two fallback branches have to go with it.**
`apex-review-story/step-04-present.md:830` and `apex-code-review/step-04-present.md:360`
both end with *"If the script itself cannot run (no `python3` or PyYAML), fall back to
re-reading `{sprint_status}` and comparing the row value by eye."* Those exist because the
Python **can** fail for an environment reason. `ape sprint verify` cannot, so under C2's
no-fallback-branches rule they must be **deleted** in the same edit that repoints the call.

This is the one place in the whole retirement where behaviour actually changes: today an
environment problem silently downgrades a verified post-write check to a model eyeballing a
value; afterwards it is a loud setup failure that `ape doctor` reports. Swapping the binary
and leaving the degrade in place would keep the worse of the two behaviours while claiming
the improvement — so the deletion is part of the deliverable, not a follow-up. The same
applies in weaker form to `epic-review-runner`'s "a non-zero exit is recorded, never a HALT"
around the reconcile call (D12): the recording stays useful, but the reason it was written
that way goes away.

**Keep as Python — 7 files, 2,744 lines.** The two `apex-create-mockups` scripts
(`apply-theme.py` 411, `theme-merge.py` 409) and the five `apex-create-wireframes` scripts
(`validate-wireframe.py` 587, `export-wireframes.py` 426, `auto-fix-wireframe.py` 317,
`run-report.py` 310, `theme-backstop.py` 284) do HTML, theme and colour-space work with
judgment in the middle. They are not deterministic project-data operations, and porting
them would move a rendering pipeline into a CLI that has no business owning one.

## Deliverables

- [ ] **D0 — Command surface + flag conventions.** Noun-first naming, the three verb
      contracts, `--output-format`/`--cwd`/`--strict` on every new subcommand, plural
      `Aliases` on the four governance families, `ape sync` and `validate` retired to
      hidden aliases, and `writeCommandSection` taught to emit `Aliases`
      (`gendocs.go:77-99`) so `make docs-cli` does not drop them. Landed as the shape of
      D1–D15, not as a separate change.
- [ ] **D1 — `ape config resolve`** + repoint all four path resolvers. **Gate on
      everything else.**
- [ ] **D2 — `ape <family> verify` + `ape registry verify`**, exactly four checks,
      `internal/registry` with a per-family descriptor. Replaces
      `runMarkdownDirValidate`.
- [ ] **D3 — `ape <family> sync` + `ape <family> update`.** Fills three stubs; retires
      both `render-index-update.py` copies; handles both index shapes.
- [ ] **D4 — `ape story fields`**, `internal/story`, ≤8 KB per file, never opens a body.
- [ ] **D5 — `ape story verify` (corpus + `--file`), `ape sprint check`, `ape sprint
      verify`.** Retires `verify-story-frontmatter.py` and all three copies of
      `verify-sprint-status-row.py`.
- [ ] **D6 — `ape memory index|show|check`**, `internal/memory`, fence-aware scanner,
      `--fail-at never|soft|hard`.
- [ ] **D7 — `ape deferred` store** — `ingest|list|close|verify`, `internal/deferred`,
      one file per record, ingest never fails for content, one bad record loses one record.
- [ ] **D8 — `ape deferred migrate`** — fixed mapping, verified before write, idempotent
      from disk state, never deletes the source.
- [ ] **D9 — `ape deferred repair`** — dispatches `apex-defer-repair` on opus through the
      existing task runner; refuses without a TTY; record-count post-condition.
- [ ] **D10 — `ape framework update` integration** — `--dry-run`, `--no-migrate`,
      `--repair`; path-scoped clean gate; commits nothing, prints the paths and the
      `git add` line.
- [ ] **D11 — `ape doctor` wiring** — six checks, `config.resolved` and `memory.size`
      Required.
- [ ] **D12 — `ape sprint reconcile`** — the epic projection verbatim, targeted line-level
      write, real advisory lock on POSIX *and* Windows, exit 0 for every content outcome.
      Retires `reconcile-epic-status.py` (645 lines, 6 call sites; the one on a mutation
      path records a non-zero exit rather than halting, so this is consolidation, not a fix).
- [ ] **D13 — `ape doc verify|shard|assemble`** — heading-level sharding with link
      rewriting and its exact reverse, **plus the `index.md` the caller verifies**;
      `verify` is a gate (exit 1 on duplicate slugs). Retires `shard-doc.py` (525 lines)
      and repoints 10 script references across 5 skills — **scoped by `shard-doc.py`, not
      `shard-doc`**, or the edit hits 6 pipeline dispatch entries for the skill, which stays.
      Stdlib-only.
- [ ] **D14 — `ape doc analyze`** — source survey with grouping, routing and split
      prediction; shares D6's token estimator. Retires `analyze_sources.py` and the
      framework's only pytest suite (510 lines).
- [ ] **D15 — Python retirement goldens** — byte-compare fixtures proving equivalence for
      all ten retired files, plus the ported pytest cases from D14. Names the two
      framework-side fallback deletions that make the retirement a behaviour improvement
      rather than a binary swap.

## Existing commands that change

Eleven commands. Everything else in the CLI is untouched.

| Command                | Change                    | How                                                                                                                                                            |
| ---------------------- | ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ape adr list`         | **behaviour**             | `findADRDir()` drops its two hardcoded probes and asks D1 for `{governance_folder}/adrs`. Same output shape; the "no ADR directory found" path stops firing.     |
| `ape adr validate`     | **renamed + payload**     | Becomes `ape adr verify` (`validate` hidden alias). Repointed off `runMarkdownDirValidate` onto D2. Exit stays 0; `--strict` added. **JSON changes** — U5.       |
| `ape adr new`          | **write location**        | The `development/adrs` fallback (`adr.go:103`) becomes the resolved governance dir, so `new` and `list` stop disagreeing.                                        |
| `ape pattern list`     | **behaviour**             | As `adr list`, via `findPatternsDir()`.                                                                                                                        |
| `ape pattern validate` | **renamed + payload**     | As `adr validate`.                                                                                                                                             |
| `ape pattern sync`     | **stub → real**           | The hidden stub becomes D3's reconcile.                                                                                                                        |
| `ape sync adrs`        | **moved**                 | → `ape adr sync`. Top-level `ape sync` hidden for one release, then deleted. `--check` becomes meaningful.                                                      |
| `ape sync patterns`    | **moved**                 | → `ape pattern sync`. Same.                                                                                                                                    |
| `ape doctor`           | **new rows, new failures**| Six checks appended. JSON grows rows (additive). A project over the 200 KiB memory ceiling now FAILs where it used to pass.                                     |
| `ape framework update` | **scope**                 | Gains `--dry-run`/`--no-migrate`/`--repair`; runs D8 then optionally D9; skips the migration when its own paths are dirty. Goes from "copy files, exit" to "copy files, migrate data, exit" — still **zero commits**. |
| `ape bootstrap`        | **write location**        | `writeArtifacts` (`bootstrap.go:231`) routes through D1 like the rest. Not in the upstream plan — found in review.                                              |

## Commit policy — every commit `ape` makes

`ape` writes git commits into the **user's project**, not into its own repo, so the set is
worth stating in one place. The answer for this plan is short — it adds none — but that is
a decision, not an omission, and it is only defensible next to the three sites that do
commit.

### What exists today

| # | Site                          | Message                            | Default | Gate                                                              |
| - | ----------------------------- | ---------------------------------- | ------- | ----------------------------------------------------------------- |
| 1 | `ape pipeline` step boundary  | `ape:<pipeline>/<stage>/<skill>` or the spec's literal `commit: "msg"` | **on** | `--no-commit` kill-switch; `commit: false` per step; dirty-tree gate with `--commit-allow-dirty` |
| 2 | `ape pipeline` stage boundary | `ape:<pipeline>/<stage>`           | **on**  | same — used when a chain folds into one commit                     |
| 3 | `ape task --task-commit`      | `ape:task/<skill>`, or the flag's value | **off** | opt-in only; `--commit-allow-dirty` bypasses the dirty-tree gate |
| — | `ape framework setup/update`  | —                                  | —       | **makes no commits at all** (U7)                                  |
| — | `ape update` / `rollback`     | —                                  | —       | replaces the `ape` binary; never touches a repo                    |

Four rules already hold across all of them, and this plan does not change any:

- **The `ape:` prefix marks a machine-made commit.** It is a namespace, deliberately not
  Conventional Commits — those are for humans writing in `ape`'s own repo. `ape:` is
  greppable, and `sanitizeFsName` makes every component filesystem- and git-tooling-safe
  (`commit.go:89-119`).
- **Never `--no-verify`.** A failing pre-commit hook fails the commit and aborts the run
  (`git.go:43-57`, PLAN-4 / C4.4). A repo's own quality gates outrank `ape`'s desire to
  finish.
- **A dirty tree blocks a committing run**, because the first commit's `git add -A` would
  otherwise fold the operator's unrelated WIP into an `ape` commit
  (`runner.go:313-330`).
- **`ape` reports commits it did not make.** `gitCommitSubjectsSince`
  (`task.go:495-514`) captures the run's *whole* commit trail — commits the framework skill
  made inside the claude session included. Anything in the run envelope's `commits[]` is an
  observation, not a claim of authorship.

### What this plan adds — nothing

**No command in this plan commits.** Not the migration, not the repair, not
`ape framework update`, not `ape sprint reconcile`, not the index writer. `ape` writes
files and reports what it wrote; the operator commits, as one commit or two or five,
however they like. Upstream D6's sample output ends "3 commits: framework files,
migration, repair" — this plan makes it zero.

The reasoning, since this deviates from upstream and from what a reader might expect after
seeing sites 1–3:

**The three existing sites earned their commits; these have not.** A pipeline step boundary
commits because it is a *boundary* — the run continues, and the next step needs a clean
starting point it can be diffed against. A migration is a single terminal act. Nothing runs
after it that needs a commit to exist, so committing buys nothing that `git status` does not
already give.

**`ape framework update` has never committed, and now never will.** That property is worth
more than the convenience: an operator who runs it knows the entire result is sitting in
the working tree, reviewable in one `git diff`, and that no history was written on their
behalf. Adding a commit — even a good one — would make it the first `ape` command that
writes history from a verb that only ever copied files.

**Whether the migration and the repair belong in one commit is the operator's call, not
ape's.** They are genuinely different in kind — D8 is a verified deterministic transform,
D9 is non-idempotent judgment — and the earlier draft of this section argued from that to
"therefore two commits". That is a real argument for *reviewing* them separately, and no
argument at all for `ape` deciding it. Someone who wants the whole update as one atomic
change is not wrong.

**The absence of a commit makes the path-scoped clean gate more important, not less.** With
no commit to isolate ape's work, `git status` and `git diff` are the *only* separation
between what ape wrote and what the operator had in flight. So the gate stays, and its
scope stays narrow: the paths the migration touches must be clean — the legacy
`deferred-work.md` above all, because reading an uncommitted version breaks the "the content
stays in git" promise the stub relies on. Unrelated WIP elsewhere is fine and does not block
anything. When it refuses, it names the dirty paths.

**So the output has to carry what a commit message would have.** Each write-performing
command ends with the counts, the exact paths it touched, and an explicit statement that
nothing was committed:

```
migration: 109 records -> development/deferred/*.md
           118 recovered from git history -> development/deferred/closed/
           26 free-form records kept verbatim, flagged for verify
  verify:  109 in -> 109 out, bodies byte-identical ... OK
           development/implementation/deferred-work.md -> stub

nothing committed. review and commit when you are happy:
  git add development/deferred development/implementation/deferred-work.md
```

`--dry-run` remains the option that writes nothing at all. `--no-commit` is **not** a flag
on any of these — there is no commit to suppress, and offering the flag would imply there
was.

## Steps

**Wave 1 — the gate, and the two things that need nothing else**

1. **D0/D1** — `internal/apexcfg` + `ape config resolve`, then repoint all four resolvers.
   The regression test (`ape adr list` returns *N* against a `governance_folder` project)
   is the definition of done.
2. **D2** — `internal/registry`, four checks, four families, `--strict`. The ADR-0050
   orphan shape and the false-positive fixture are the two tests that matter.
3. **D3** — reconcile + delta writer, both index shapes, goldens against both Python
   copies.

**Wave 2 — the commands the skills are waiting on**

4. **D6** — `ape memory index|show|check`. Unblocks the framework's A11 (15 unpredicated
   whole-file reads across 13 skills) and upstream D2's compaction gate.
5. **D4** — `ape story fields`. Unblocks A9 (`apex-feature-refresh`) and A10
   (`apex-capability-refresh`).
6. **D5** — `ape story verify`, `ape sprint check|verify`. Retires four Python files.
7. **D7** — `ape deferred` store. Unblocks A8, which is already validated end-to-end
   upstream (8 runs, 24 reviews, 2 of 2 defers routed, zero direct ledger writes, zero
   demotions).

**Wave 3 — migration and scheduling**

8. **D11** — doctor wiring, once there is something to check.
9. **D8** — `ape deferred migrate`, standalone and verifiable before anything calls it.
10. **D9** — `ape deferred repair`.
11. **D10** — fold both into `ape framework update`.

**Wave 4 — the Python consolidation, independent of everything above**

D12, D13 and D14 all take file or path arguments and depend on nothing in waves 1–3, so
they can run in parallel with any wave or slip without blocking anything. Their case is
consolidation and testability, not urgency: every one of their call sites either degrades
gracefully today or records the failure without halting (see D15's table).

> **D12 was moved into Wave 2 and back out again.** The move was made on the belief that
> `reconcile-epic-status.py` removed a live demotion hazard at
> `apex-epic-batch-review`'s `epic-review-runner`. It does not — that site says "A non-zero
> exit is recorded, never a HALT and never an epic error". The framework review caught it.
> Recorded here so the same argument is not made a third time.

12. **D12** — `ape sprint reconcile`. Do the lock and the byte-diff test first; the
    projection is the easy half.
13. **D13** — `ape doc verify|shard|assemble`. Slugify golden before anything else, then
    the `index.md` contract.
14. **D14** — `ape doc analyze`, porting the pytest cases as it goes. Coordinate with the
    `apex-distillator` rework (see D14).
15. **D15** — retirement goldens consolidated; the framework-side deletions — including the
    two fallback branches — land in the framework repo's own version bump.

**Landing rule.** `ape` ships on its own release cadence and every deliverable here is
additive. The framework side cannot be one bump: its call sites fall into three groups with
different `ape` prerequisites, and C4 allows only one framework bump per eval-capture
window — so this is **at least three bumps**, not one.

| Framework bump           | Call sites                                | Needs first        |
| ------------------------ | ----------------------------------------- | ------------------ |
| Text edits only          | upstream A1–A7                            | nothing            |
| Command adoption         | A8–A11                                    | D4, D6, D7         |
| Python retirement        | the 10 script call sites + D15's fallback deletions | D3, D5, D12, D13, D14 |

The rule, restated: **each framework bump lands after the `ape` release its own call sites
depend on.** A skill calling a command that does not exist yet is a hard failure by C2's
design, with no fallback branch — and the retirement bump is the one that also *deletes*
fallbacks, so it must not land before the binary that makes them unnecessary.

## Acceptance

- `ape adr list` returns 64 records in the reference project. Today it returns none and
  exits 0.
- `ape registry verify --all` rediscovers ADR-0050 (on disk, `accepted`, cited by 42
  stories, absent from `index.yaml` across all 57 commits that touched it), reports zero
  false positives on the other 63, and runs in <10 ms.
- `ape adr update` and `ape feature update` produce byte-identical output to both
  `render-index-update.py` copies across the golden fixture set, and fail fast on an
  unknown id without touching the file.
- `ape memory index` emits an entry count equal to `grep -c '^### '` on the real file —
  180 — and on a fixture that fences a `### ` inside a code block it emits **one fewer than
  the grep**, which is the point: the grep is the baseline only where no fence exists, and
  where one does the grep is the thing being corrected.
- `ape memory check` runs on the 431,950 B file without reading it, reports
  `state: over-hard`, and exits 0; `--fail-at hard` exits 1; `ape doctor` FAILs.
- `ape story fields --select story_id,epic,status,features` emits ~67 KB from a
  25,252,775 B corpus, with total bytes read ≈ 8 KB × files scanned.
- `ape story verify` reproduces the 14 known frontmatter failures exactly, plus the 15
  bare-string `features` entries and the 3 numeric `depends_on` values — and reports
  nothing for `contribution: contributes`.
- `ape sprint verify` reproduces `verify-sprint-status-row.py`'s five exit codes on the
  golden set, and exit 1 is no longer reachable.
- `ape deferred ingest` round-trips a body containing backticks, double quotes and the
  `— defer:` tail byte-intact, keeps the `- [ ]` checkbox, and exits 0 on a malformed line
  and on empty stdin.
- One malformed record in the store loses exactly that record: warning on stderr, every
  other record returned, rc 0.
- `ape deferred migrate` on the reference ledger: 109 in → 109 out, bodies byte-identical,
  26 free-form kept verbatim and flagged, 118 git-recovered into `closed/`, legacy file
  left as a stub, and **nothing committed** — `git log` unchanged, every written path named
  in the summary. A second run is a no-op.
- `ape framework update --dry-run` lists the framework diff and the pending migration and
  writes nothing. Without `--repair`, no paid session is spawned.
- `ape adrs verify` and `ape adr verify` produce byte-identical output, and
  `docs/reference/cli.md` lists the alias.
- `ape sprint reconcile --epic N` changes exactly two lines of `sprint-status.yaml` —
  the `epic-N:` status and the body `updated_at` — with every comment, the row ordering and
  the sync header byte-identical; and two concurrent reconciles of different epics in the
  same file both land.
- `ape doc shard` then `ape doc assemble` returns a fixture corpus byte-identical to the
  source, at three heading levels, with relative links restored — and every `shard` leaves
  an `index.md` byte-compared against the Python's, which `assemble` ignores.
- `ape doc analyze --output-format json` is byte-comparable to `analyze_sources.py` on the
  golden set, and all of that script's pytest cases pass as Go table tests.
- `make ci-local` green, including `make xcompile-windows` and `make test-portable` —
  every new package is in the portable set, D12's Windows lock path included.

## Risks / notes

- **D1 is a single point of failure for every other deliverable.** A wrong resolution
  order or a mishandled `governance_repository_path` makes every verifier audit the wrong
  tree and report it clean — the exact failure mode the upstream plan warns about. This is
  why the D1 regression test asserts a record *count* against a real folder layout rather
  than asserting that resolution "worked".
- **The `ingest` exit-code contract is load-bearing and invisible.** Nothing in `ape` will
  ever show that a non-zero exit here demotes a story two skills away. The C1 comment goes
  on the function, not in this plan, and the lock test asserts rc 0 for a malformed line.
- **`--strict` is a loaded gun on a review path.** Every `verify` gets it; three skills
  must never set it. `ape` cannot enforce that — the constraint lives in the framework's
  prose. The command help says so explicitly, which is the most `ape` can do.
- **Retiring the Python is a framework-repo edit, not an `ape` edit.** The goldens prove
  equivalence, but the deletion lands elsewhere and on a different cadence. Until it does,
  both implementations exist, and the Python is the one the skills call.
- **`shard-doc` is the only script stem that is also part of a live skill name.** Checked
  all six stems against `.claude/skills/`: `verify-story-frontmatter`,
  `verify-sprint-status-row`, `render-index-update`, `reconcile-epic-status` and
  `analyze_sources` collide with nothing, so a substring-scoped edit is safe for nine of the
  ten files. `shard-doc.py` vs `apex-shard-doc` is the single exception, and it is the one
  whose collision reaches into `_apex/pipelines/`. The retirement edit for D13 must match
  `shard-doc\.py`; every other retirement can be scoped loosely. This plan reported the wrong
  number twice before the split was made explicit, which is itself the argument for writing
  the grep into the deliverable rather than leaving it to whoever does the edit.
- **A green test suite is not evidence the caller still works.** D13's `index.md` contract is
  the case in point: a replacement can pass a byte-identical round trip and still fail
  `apex-shard-doc`'s own Step 4 verification. Every retirement golden must be written against
  what the *calling skill checks*, not only against what the script returns — the framework
  review found this one, and it is the class of defect an ape-side test cannot see.
- **The migration's byte-identity assertion is the only real guarantee in D8.** Everything
  else about that deliverable is recoverable from git; a lossy conversion that passed
  silently would not be. Assert before write, never after.
- **`ape doctor` gains a way to fail on a real project.** `memory.size` FAILing at the hard
  ceiling is intended (it is how D6's deviation from upstream stays honest), but it will
  turn red on the reference project the day it ships, before any compaction has run.
  `docs/how-to/run-doctor-in-ci.md` needs to say so.
- **D12's obvious Go implementation is wrong.** Unmarshal → mutate → re-marshal destroys
  the comments, key ordering and sync-generated header that the Python goes out of its way
  to preserve, and yaml.v3 gives no way to keep them. The line-oriented edit is not an
  optimisation, it is the requirement — and the full-file byte-diff test is what catches
  anyone who "simplifies" it later.
- **D12's lock is the only place `ape` needs platform-specific file locking.**
  `golang.org/x/sys` is already a direct require, so this costs no dependency, but it does
  mean two build-tagged files and a Windows path that CI compiles but cannot exercise
  behaviourally. The POSIX path gets the real concurrency test; the Windows path gets a
  compile gate plus a single-process lock/unlock test.
- **An uncommitted migration is a state an operator can forget.** Nothing commits, so a
  migrated-but-uncommitted tree survives only until the next `git stash`, `git checkout` or
  careless `git restore`. `ape doctor`'s `migration.pending` reads *disk* state, so it will
  correctly report the migration as done and will not nag about the missing commit. The
  mitigation is the summary line — every written path, plus the `git add` command — and it
  is the reason that output is a requirement of D8/D10 rather than a nicety.
- **This plan adds the fourth and fifth copy of "run git, capture output".** There are
  already three: `internal/pipeline/git.go` (unexported), `internal/framework/gitcmd.go`
  (`runGit`, partly exported), and `task.go`'s `gitHeadFull`/`gitCommitSubjectsSince`. D5's
  last-committed-value comparison and D8's `--recover-deleted` both need one. Two new
  consumers is the point at which a small shared internal helper is cheaper than a fourth
  variant — decide that in D5, before D8 copies whatever D5 does.
- **The plural aliases must not become a second surface.** They are `Aliases` on one cobra
  command, so divergence is structurally impossible — but examples, help text and
  `docs/reference/cli.md` all use the singular, and a reviewer should reject any patch that
  starts documenting both.
- **`make docs-cli` regenerates `docs/reference/cli.md` (2,218 lines) from the cobra tree**
  — this plan adds **33 leaf subcommands under 9 new command groups** (`config`, `feature`,
  `capability`, `registry`, `story`, `sprint`, `memory`, `deferred`, `doc`), so that file
  grows by 42 sections and the regenerated diff is large enough to hide a mistake. Review it
  as content, not as generated noise. `make docs-check` separately requires every new doc be
  reachable from `docs/README.md`.
- **Windows portability is a CI gate, not a nicety.** `internal/apecmd` is in the portable
  set: no `/` literals in paths, no `syscall`, and byte-exact fixtures need
  `.gitattributes -text` or CRLF conversion shifts every byte span in D6's tests.

## Deferred — with the event that unparks each

> The **F-series** below is the deferred list, matching the house convention in PLAN-16/18/22/24.
> The corrections to the upstream plan are the **U-series** above; the two are separate numberings.

- **F1 — Memory threshold config keys** (`memory_soft_budget_bytes`,
  `memory_hard_ceiling_bytes` in `_apex/config.yaml`). Unparks when a second project wants
  different thresholds, or when the framework template changes for another reason and this
  can ride along. Until then: Go defaults plus `--soft`/`--hard`.
- **F2 — `ape deferred` as a general work-item store.** This plan builds it for defers
  only. Nothing here generalises to patches, escalations or spec gaps, and it should not
  until a second record class actually exists.
- **F3 — An `epic-close` pipeline stage that calls D12.** Upstream A12 sequences
  `feature-refresh → capability-refresh → epic-retrospective → generate-project-context`
  as a framework pipeline; whether epic reconciliation belongs in that chain is a framework
  decision, not an `ape` one. Unparks when A12 lands and the chain needs a tracker step.

## Closed — settled, with the reason, so they are not reopened

- **Retiring the wireframe / mockup Python** — 7 files, 2,744 lines
  (`apply-theme.py`, `theme-merge.py`, `validate-wireframe.py`, `export-wireframes.py`,
  `auto-fix-wireframe.py`, `run-report.py`, `theme-backstop.py`). Judgment in the middle,
  rendering at the edges. Closed, not deferred: porting them would move a rendering
  pipeline into a CLI that has no business owning one, and none of them carries the PyYAML
  hazard or the duplication that justified the other ten.
- **A `promote-parent-epic` command.** `reconcile-epic-status.py` already subsumed that
  script upstream — the general projection produces its backlog → in-progress promotion,
  without its special case refusing to touch a `done` epic (which made an epic a one-way
  door and turned an additive back-fill into a HALT). D12 inherits the subsumption; there
  is nothing separate to build.

## Dependencies

- **Upstream framework work** (`apex-implementation-plan.md` Part A) calls these commands.
  A1–A7 are text edits with no `ape` dependency; A8 needs D7, A9 needs D4, A10 needs D4,
  A11 needs D6, A12 needs nothing from this repo.
- **`ape` releases before the framework bump that depends on it.** C2 gives skills no
  fallback branch.
- **No new Go modules.** Verified against `go.mod`: `gopkg.in/yaml.v3`,
  `github.com/spf13/cobra`, `github.com/spf13/pflag` and `golang.org/x/sys` (D12's file
  lock, `v0.46.0`) are already direct requires.

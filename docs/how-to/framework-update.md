# How to refresh the framework in a project

`ape framework update` installs the framework's newest **release** into your project and commits it. It exports the release tag's files from your clone of the framework's ship repo, re-copies skills, pipelines and the framework's tables, runs pending migrations, and refreshes `_apex/framework.yaml`. It does **not** touch `_apex/config.yaml` — that's the one-time bootstrap from [`ape framework setup`](framework-setup.md).

Run it whenever the framework publishes a release. Since ape v0.4.0 it installs tagged releases only, never whatever happens to be on the framework's `main`. The design is in [Framework updates install releases](../explanation/framework-updates-from-releases.md).

## Prerequisites

- ape `v0.0.7` or later — `ape version` to confirm.
- The project must have been set up first via `ape framework setup`. Update refuses to run if `_apex/framework.yaml` is absent.
- A local clone of `apex_process_framework`. Either pass `--repo PATH` on every invocation, or set `$APEX_FRAMEWORK_REPO` once:

  ```bash
  export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
  ```

- ape uses that clone only to read git objects: it fetches tags, exports the chosen tag's files, and never checks anything out. The clone may be dirty or on any branch. (`--from-worktree` installs the clone's working tree instead, and then the clone must be on `main` and clean; `--force` bypasses that.)
- To commit, which is the default, the project must be a git repository on a branch with no modified or staged tracked files. Untracked files are fine and are never committed. Pass `--no-commit` to leave the result in the working tree instead.

## Quickstart

```bash
cd /path/to/your/project
ape framework update
```

What happens:

1. Validates that `_apex/framework.yaml` is **present** (the project is set up). If absent, exits with `Error: framework metadata not found at <path> — run "ape framework setup" to install`.
2. When committing, checks the project tree is clean (exit 4, listing the paths, if not). Nothing has been written yet.
3. Fetches tags (`git fetch --tags --force origin`; skipped with `--no-fetch`; a failed fetch warns and uses the tags already there), then picks the release (see [Which release is installed](#which-release-is-installed)) and exports its files.
4. Checks this ape against the release's `min_ape_version` (see [ape's minimum version](#apes-minimum-version)). Below it, nothing is written.
5. Records the release's tag and commit for the metadata file, and warns loudly if the tag installed last time now names a different commit.
6. Removes any existing `<project>/.claude/skills/apex-*` (so removed-from-framework skills disappear locally).
7. Copies all `apex-*` skill directories into `<project>/.claude/skills/` (including `apex-orchestrator`).
8. Copies all framework pipeline YAMLs into `<project>/_apex/pipelines/`.
9. Refreshes the framework's [`ape aboard` recipe library](use-the-board.md#recipes) in `<project>/_apex/aboard/recipes/`.
10. Ensures the project has a [board](use-the-board.md#the-board-is-already-there): creates `<project>/.aboard/` and seeds `.aboard/.gitignore` if either is missing. An existing board is never overwritten.
11. Refreshes the operating-rules fragment (`_apex/apex-operating-rules.md`) and the managed block in the repo-root `CLAUDE.md`. Skipped with a warning if the framework repo predates the fragment.
12. Copies the framework-owned tables the runner reads: `_apex/terminal-contracts.csv`, `_apex/ape-commands.yaml`, `_apex/commit-owners.csv`, `_apex/effort-defaults.yaml` (the per-model reasoning-effort table; see `ape config effort`), and the upgrade list `_apex/migrations/*.md`. Each is optional in the framework repo — one that predates a file installs none of it, and the runner then reports that state rather than assuming it. **`commit-owners.csv` is reported in both directions**, because an absent roster silently disarms the per-dispatch commit-ownership assertion described in [Run a single skill](run-a-single-skill.md).
13. Ensures `.gitignore` ignores `sprint-status.yaml.lock`, appending the entry only when git does not already ignore the sidecar. This is the verify-and-fix pass: a project set up before the entry existed gains it here, without having to know it was missing.
14. Relocates any run artifacts still at the pre-`{output_folder}/ape` paths (`_output/pipelines/`, `_output/tasks/`, and on a project that renamed `output_folder`, `_output/ape/prompts/` and `_output/ape/chats/`). Skipped with `--no-migrate`; reported without writing by `--dry-run`. See [What the relocation does](#what-the-relocation-does).
15. Rewrites `<project>/_apex/framework.yaml` — preserving the `sources.config` block recorded by the original `setup` so `project_name` + `extensions` stay intact.
16. Runs the pending migrations (see below), then commits everything the install and the migrations changed as `chore(framework): update APEX framework to vX.Y.Z` — see [Commits](#commits).

## Which release is installed

- **By default, the highest tag shaped exactly `vX.Y.Z`**, compared as semver, so `v0.10.0` beats `v0.9.0`. A release candidate (`-rc.N`) or any other suffix is never picked, and a commit on the framework's `main` that no release tag names is never installed.
- **`--version vX.Y.Z`** installs exactly that release, and **`--version vX.Y.Z-rc.N`** installs a candidate. That is the only way to install one.
- **`--from-worktree`** installs the clone's working tree as it stands, which was the behaviour before ape v0.4.0. It's for framework developers and scratch installs.
- **A default update never goes backwards.** If you installed a candidate that is newer than the newest final release, `update` keeps it and says so. `--version vX.Y.Z` goes back when you mean it.

The release must use the framework's released layout (`_apex/` and `.claude/` at the root of the tag). A tag from the framework's build repo nests them under `framework/` and is refused (exit 3): point `--repo` at the ship repo.

## ape's minimum version

A framework release can declare the oldest ape it works with, as `min_ape_version:` in its `_apex/ape-commands.yaml`. `update` reads it from the release **before writing anything**:

- **Meets it** (an rc of exactly that version counts, so `0.4.0-rc.2` meets `0.4.0`): the install goes ahead. If the update check knows a newer ape, one line says so.
- **Below it, at a terminal:** you are asked whether to update ape first. The update goes through bingo when the project pins ape (`.bingo/ape.mod`), and otherwise through `ape update`. Then the same command re-runs on the new ape.
- **Below it, without a terminal:** nothing is written. ape prints the command to run (`run: …`) and exits 11.
- **A `dev` build or Go pseudo-version** cannot be compared: a warning, and the install goes ahead. A release that declares no minimum has none.

The two update routes trust different things. `ape update` installs the cosign-verified release binary. bingo compiles the tagged source through the Go module proxy, which is checksummed but is not the signed binary. A bingo-built ape reports its version correctly, and its git commit as `unknown`.

## Commits

Until ape v0.4.0, `update` committed nothing. Now each install is a release with an identity, and the commits record it:

- **`chore(ape): update ape to vA.B.C`**, only when ape was updated through bingo, touching only `.bingo/`.
- **`chore(framework): update APEX framework to vX.Y.Z`**, holding the install and the migrations it ran, with these trailers:

  ```
  Framework-Version: vX.Y.Z
  Framework-Commit: <the release's full commit sha>
  Framework-Migrations: <ids, when any were applied>
  Generator: ape framework update
  ```

Details:
- An update that changed nothing commits nothing.
- Your hooks run. If one fails, everything stays staged and the command exits 12.
- If ape was updated and committed and the install then fails, the ape commit stays: it is valid on its own.
- `--no-commit` restores the old behaviour exactly: the whole result stays in the working tree for one `git diff`, and only modified `apex-*` skills block the run.

## What gets touched

| Path                              | Behavior on update                 |
| --------------------------------- | ---------------------------------- |
| `.claude/skills/apex-*/`          | Wiped + reinstalled                |
| `_apex/pipelines/*.yaml`          | Overwritten                        |
| `_apex/apex-operating-rules.md`   | Overwritten (if framework ships it) |
| `_apex/terminal-contracts.csv`    | Overwritten (if framework ships it) |
| `_apex/ape-commands.yaml`         | Overwritten (if framework ships it) |
| `_apex/commit-owners.csv`         | Overwritten (if framework ships it). **Absent = every dispatch's commit-ownership assertion skips** — the run says so either way |
| `_apex/migrations/*.md`           | Refreshed, **not synced**: an entry the framework pruned stays put, because the applied-id ledger records entries by id. No empty directory is left when the framework ships none |
| `_apex/aboard/recipes/*.md`       | The framework's own recipe files are overwritten; **anything else in that directory is left alone** — see below |
| `.aboard/`                        | Created if missing; an existing board is **never** overwritten |
| `.aboard/.gitignore`              | Seeded if missing; an edited one is **NOT** rewritten |
| `CLAUDE.md` (repo root)           | Managed block refreshed in place; content outside the markers untouched |
| `_apex/config.yaml`               | **NOT touched** (that's setup)     |
| `_apex/config.local.example.yaml` | **NOT touched**                    |
| `.gitignore` (repo root)          | One entry APPENDED if the lock sidecar is not already ignored; never rewritten, never reordered |
| `_apex/framework.yaml`            | Rewritten (config block preserved) |
| `{output_folder}/ape/`            | Run artifacts MOVED here from the legacy paths, once. Never overwritten — see below |
| `{output_folder}/` (elsewhere)    | **NOT touched** — the framework's handoffs, briefs and reports are not ape's |

Recipes are **refreshed, not synced**: a file the framework no longer ships stays where it is, and a recipe you wrote yourself is never removed. The reason is whose directory it is — `_apex/aboard/recipes/` is also where a workspace keeps its own recipes, so deleting what ape did not install would destroy work the framework never owned. Framework-named files are overwritten in place; if you edited one, your edit is replaced, so keep local changes under a different name.

Non-`apex-*` entries under `.claude/skills/` are never touched. In `CLAUDE.md`, only the bytes *between* the `<!-- apex:managed:begin -->` / `<!-- apex:managed:end -->` markers are ape-owned — everything else is preserved byte-for-byte. Do not hand-edit inside the markers; `update` refreshes that region wholesale, so in-marker edits are discarded.

## Idempotency

Running `update` twice on a steady-state project is safe and cheap:

- Skills: wiped + reinstalled. Net effect identical when the framework HEAD is unchanged.
- Pipelines: overwritten. Net effect identical.
- Operating rules: fragment overwritten; the `CLAUDE.md` managed block is rewritten only when its bytes actually change, so a steady-state `update` leaves `CLAUDE.md` byte-identical.
- `.gitignore`: appended to only when the sidecar is not already ignored, so a steady-state `update` leaves it byte-identical. Unlike `CLAUDE.md` this is **not** a managed block — ape appends one line and never rewrites the file, because an ignore file's whole job is to be hand-curated and managing a region of it to own a single line is a bad trade. Delete the line and the next `update` puts it back; that is the cost of the simpler contract.
- Run artifacts: relocated only if a legacy tree still holds runs. A project already on the current layout has nothing to read, so a steady-state `update` moves nothing and reports nothing.
- `framework.yaml`: rewritten, but byte-identical when nothing about the install changed. `installed_at` moves only when the framework commit, the ape version, the installed files or the ledger did (since v0.1.1). So a steady-state `update` leaves the whole tree clean.

The destructive operation that matters — wiping `apex-*` skills — is git-safe: if the project is a git repo and you have uncommitted edits to a tracked `apex-*` skill file, the command refuses without `--force`. Untracked `apex-*` paths are treated as leftovers and get clobbered.

## What the relocation does

`update` is where a project moves onto ape's current output layout. ape owns
`{output_folder}/ape/` and nothing outside it; run artifacts written by older
versions sit at `_output/pipelines/` and `_output/tasks/`, beside the
framework's own directories.

This moves your run history — the manifests `ape costs` reads and the run ids
your reports quote — so it is deliberately conservative:

- **Whole run directories only.** A run is never half at the old path and half
  at the new one.
- **A destination that already exists is a conflict.** The source is left
  untouched and named in the output; nothing is overwritten or merged. Two runs
  sharing an id is not something to resolve by guessing. A conflict does not
  stop the other runs from moving.
- **`latest` symlinks are recreated** pointing at the same run.
- **Emptied legacy directories are pruned** — but never your live output
  folder, even when empty. That directory is the framework's.
- **Idempotent.** Nothing left to move means nothing reported.

Look before you leap:

```bash
ape framework update --dry-run     # reports what would move, writes nothing
ape framework update --no-migrate  # installs framework files only, defers this
```

`ape doctor` reports a project that still needs it as `runs.legacy_layout`.
Until it runs, cost rollups and the hook-contract check read a project with no
history — nothing is lost, the records are just somewhere ape no longer looks.

Full layout: [How to read the output folder](run-artefacts.md).

## The framework's upgrade migrations

A framework release changes what project data must look like. The framework
ships that change as a list of entries under `_apex/migrations/`, one file per
migration, and `ape framework update` runs it after the install.

```bash
ape framework update --plan            # print the plan, do nothing else
ape framework update --plan --no-check # …and run no entry's check: command
ape framework update                   # install, then apply the derivable ones
ape framework update --no-migrate      # install only; the list stays pending
```

`--plan` is readable against a project in any state and it is the whole
command: no install, no fetch, no migration.

It shows the list the project will hold **after** the update: what's installed
now, overlaid by the repo's `_apex/migrations/` by file name, which is exactly
what the install's copy does. It also runs each entry's `check:` against the
project as it is now. A `SOURCE` column marks each row `installed`,
`incoming (new)` or `incoming (replaces installed)`, so a migration arriving
with this update can be read before it runs. The repo is read as it stands,
unfetched. The update itself fetches first unless you pass `--no-fetch`, so
use `--no-fetch` on both if you want the preview and the run to match
exactly. Before v0.1.1, `--plan` showed only the installed list, so an
incoming entry was invisible until it had run.

### What ape may run, and what it may not

`kind:` is the authority model, not a hint.

| `kind:` | what happens |
| ------- | ------------ |
| `derivable` | ape executes the entry's `command:` unattended |
| `judged` | listed with the skill from `skill:` to dispatch, and **never executed, under any flag** — an entry that also carries a `command:` still does not run it |
| anything else, or unset | treated as `judged`; ape not understanding an entry means ape does not run it |

### The four states

The **ledger** in `_apex/framework.yaml` decides applied-ness — an ordered list
of `{id, version, applied_at}`, a list rather than a set because "which
migrations ran, in what order" is what you ask when one half-applies. It is
also what makes a second run a no-op and a failed run resumable, independent of
whatever any command does.

| state | means |
| ----- | ----- |
| `applied` | the ledger records it — or it does not, and the entry's check says the shape is already there. `source:` says which; ape never backfills the ledger for work it did not do |
| `half-applied` | the ledger says it ran and the check says the post-condition does not hold |
| `pending` | not in the ledger, and either the check answered "no" or no check is declared |
| `cannot-tell` | not in the ledger, and the check **could not run**. Unapplied *and* unverifiable, so it is never run — treating it as pending is how a runner re-applies things |

### Writing a `check:`

A check **reports and never gates**, and its exit code is read the way `grep -q`,
`jq -e` and `test` already work:

- **`0`** — applied.
- **`1`** — not applied. This is the only non-zero code read as an answer.
- **`2` and above** — the check itself failed, so the entry is `cannot-tell`.

That split matters more than it looks. A shell reports a missing binary as exit
**127**, so a check piping into a `jq` that is not installed comes back
non-zero; reading every non-zero code as "not applied" would make the entry
pending and ape would apply it. Write checks that exit 1 for "no" —
`test -f x && grep -q y x`, not `grep -q y x`, which exits 2 when `x` is absent.

Checks are bounded at two minutes. A `command:` is not, because a derivable
entry may dispatch a whole session.

### `ape` inside a check means *this* `ape`

Checks and commands run with a PATH in which `ape` resolves to the binary
running the migration, not to whatever `ape` the machine has installed.

This is not a convenience. The framework's first migration entry checks
with `ape sprint check --output-format json | jq -e …`, and on a machine
with an older `ape` on PATH that older binary answers: it has
`sprint check --output-format json`, it emits perfectly valid JSON, and
its `findings` is `[]` because the check class did not exist yet. `jq`
says `true`, the check reports satisfied, the entry is recorded as
applied, and the ledger makes that permanent — on a project that was never
migrated.

The shadow directory holds one entry, so nothing else on PATH changes
resolution order. If it cannot be built, the run says so in a line
beginning `migrations:` rather than proceeding silently — running against
the wrong `ape` without saying so is the failure this prevents.

### Ordering, and what stops a run

Entries are ordered **semantically**: `version` as semver, then `seq` as an
integer, then `after:`. Never by filename — `v0.9.0` sorts *after* `v0.10.0`
lexically, which is the bug the rule exists to prevent. The filename is for
uniqueness and humans, and one that disagrees with its frontmatter is reported.

An `after:` **cycle** leaves the order undefined, so none of the entries in it
runs; an invented order is how a migration runs before what it depends on. An
`after:` naming an entry that is not in the list is a finding, not a cycle.

A failed entry **stops the sequence** — later entries may depend on it — and
the rest are reported as not attempted. The command still exits 0: the
framework install succeeded, and collapsing a migration failure into a non-zero
exit would make the install look like it had not happened. Re-running picks up
where it stopped.

`ape doctor` reports the state as `migrations.pending`, as a warning rather
than a failure: a project that owes a migration is behind, not broken. That row
runs no checks — it answers from the ledger alone, so it stays cheap and
predictable in `--strict` CI.

> `migrations.pending` and `migration.pending` are **different rows**. The
> singular one is ape's own project-data conversion, detected from disk state;
> the plural one is the framework's authored upgrade list, tracked in a ledger.

## Inspecting drift

```bash
ape framework status
```

Compares the installed release (from `_apex/framework.yaml`) with the newest release in the framework clone, which is what `update` would install. It also reports, loudly, when the installed tag now names a different commit than the one installed: release tags are meant never to move, so ape reports this and never follows it. `--from-worktree` compares against the clone's working-tree HEAD instead, as before ape v0.4.0.

## Output formats

All flags work with `--output-format human|json|yaml`:

```bash
ape framework update --output-format json | jq '.summary'
# { "skillsInstalled": 86, "skillsRemoved": 0, "pipelinesInstalled": 3, ... }
```

## Troubleshooting

### `framework metadata not found at <path> — run "ape framework setup" to install`

You haven't run `setup` on this project yet. Update is refresh-only; use [`ape framework setup`](framework-setup.md) for the first install.

### `framework repo path not set`

Pass `--repo /path/to/apex_process_framework` or export `APEX_FRAMEWORK_REPO`.

### `framework repo has uncommitted changes (pass --force to bypass)` (`--from-worktree` only)

The framework repo must be clean. Either commit/stash the framework-side changes, or pass `--force` to clobber-install from a dirty tree (recorded in `framework.yaml` so the divergence is auditable).

### `framework repo is on branch X (expected main)` (`--from-worktree` only)

The command refuses to install from a non-`main` branch unless `--force` is passed. This is to prevent accidentally pinning a project to an experimental branch's HEAD.

### `uncommitted changes under .claude/skills/apex-*`

You have local edits to one or more committed framework skills. Either:

- Commit the edits in your project (they'll get clobbered on the next update but at least you have the diff in git).
- Pass `--force` to clobber them now.
- Stash them somewhere outside `.claude/skills/apex-*`.

Untracked `apex-*` paths don't trip this check — only tracked-and-modified ones do.

### `operating-rules: framework repo predates _apex/apex-operating-rules.md`

Your framework checkout is older than the operating-rules feature. `update`
skipped installing the fragment + `CLAUDE.md` managed block (a version-skew
suppression, not a failure). Upgrade the framework repo to a version that ships
`_apex/apex-operating-rules.md`, then re-run `ape framework update`. Until then
`ape doctor` reports the operating-rules checks as a WARN nudge, not a failure.

### `framework branch "main" diverged from origin` (`--from-worktree` only)

`git merge --ff-only` failed because the framework repo's local main has commits not on the remote. Either rebase manually or pass `--no-fetch` to skip the pull.

## Project-data migrations

`update` also runs any pending **project-data** migration — converting a
legacy single-file `deferred-work.md` into one record file each. Migrations
run here rather than in a command a skill has to police, so no skill ever
meets an un-migrated project and no skill needs a migration failure path.
This is the right transaction boundary: explicitly invoked, at the moment
framework expectations change, outside the build loop.

The migrations `update` runs are committed together with the install (see
[Commits](#commits)). `--repair`'s output is never committed. With
`--no-commit`, nothing is: the whole result sits in the working tree for
one `git diff`.

```bash
ape framework update --dry-run     # framework drift AND pending migrations; writes nothing
ape framework update               # install, then migrate
ape framework update --no-migrate  # install only; migrations stay pending
ape framework update --repair      # also run the opus judgment phase (spends money)
```

`--repair` is opt-in, not opt-out. It spawns a paid session, and a verb
whose contract has always been "copy some files" should not start doing
that by default. It also refuses without a TTY.

A migration is **skipped, never forced**, when its own paths have
uncommitted changes — the message names them. The gate is scoped to those
paths rather than the whole tree: they are disjoint from what the install
writes (`.claude/skills`, `_apex/*`, `CLAUDE.md`), so the install and the
migration are order-independent, and unrelated work in progress elsewhere
does not block anything.

With `--no-migrate`, `ape doctor`'s `migration.pending` check reports the
outstanding work, so the state is visible rather than silent. See
[How to work with project data](work-with-project-data.md) for what the
migration does and how to run it on its own.

## Related

- [How to work with project data](work-with-project-data.md) — the commands the migration uses.
- [How to set up the framework (first install)](framework-setup.md) — run this before `update`.
- [Pipeline spec reference](../reference/pipeline-spec.md) — the YAML shape of `_apex/pipelines/*.yaml`.
- [framework.yaml reference](../reference/framework-yaml.md) — the metadata file written on every install.
- [Why setup and update are separate](../explanation/why-setup-and-update-are-separate.md) — design rationale for the split.

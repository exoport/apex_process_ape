# How to refresh the framework in a project

`ape framework update` installs the framework's newest **release** into a project that already has it, runs the release's pending upgrade migrations, and commits the result. Run it whenever the framework publishes a release. It installs tagged releases only, never whatever is on the framework's `main`; the design is in [Framework updates install releases](../explanation/framework-updates-from-releases.md). It is also how an APEX v0 project becomes an APEX v1 project: see [Upgrading a v0 project to v1](#upgrading-a-v0-project-to-v1).

It does **not** touch `_apex/config.yaml`.

## Prerequisites

- The project was set up with [`ape framework setup`](framework-setup.md). `update` refuses without `_apex/framework.yaml` (exit 7).
- Access to the framework repository: `--repo PATH`, else `$APEX_FRAMEWORK_REPO`, else ape's own clone under the user cache directory (`~/.cache/ape/framework/…` on Linux), cloned from `$APEX_FRAMEWORK_URL` or the framework's GitHub repo the first time it is needed. ape brings the clone to the release it installs: a clone of yours only ever moves forward, and one with local changes that would move is refused (exit 3) unless you pass `--force-clone`. ape's own clone is always overwritten. See [The framework and governance clones are caches](../explanation/framework-and-governance-clones.md).
- To commit, which is the default, the project is a git repository on a branch with no modified or staged tracked files (exit 4 otherwise). Untracked files are fine and are never committed. `--no-commit` leaves the result in the working tree instead.

## Quickstart

```bash
cd /path/to/your/project
ape framework update --dry-run   # what would change, and the pending migrations; writes nothing
ape framework update
```

What happens:

1. Checks that `_apex/framework.yaml` exists, and, when committing, that the tree is clean. Nothing has been written yet.
2. Fetches the clone's tags (`--no-fetch` skips it; a failed fetch warns and uses the tags already there), then picks the release (see [Which release is installed](#which-release-is-installed)).
3. Checks this ape against the release's `min_ape_version` (see [ape's minimum version](#apes-minimum-version)). Below it, nothing is written.
4. Records the release's tag and commit, and warns loudly if the tag installed last time now names a different commit.
5. Replaces `.claude/skills/apex-*` with the release's skills, so a skill the release no longer ships disappears, and installs `_apex/ape-commands.yaml`, `_apex/migrations/*.md` and `_apex/standards/adr-0001.md`.
6. For an APEX v1 release: removes the project's APEX v0 migration entries (`_apex/migrations/v0.*`), and puts the `.claude/` shape in the root `.gitignore`, as [setup does](framework-setup.md#the-claude-shape-in-gitignore).
7. Creates the [board](use-the-board.md) at `.aboard/` if it is missing. An existing board is never overwritten.
8. Rewrites `_apex/framework.yaml`, carrying forward the migration ledger and what setup recorded.
9. Runs the pending upgrade migrations (see [The framework's upgrade migrations](#the-frameworks-upgrade-migrations)).
10. In an APEX v1 project, installs or updates the plugins `_apex/config.yaml` lists, at project scope, and writes them into `.claude/settings.json`. A plugin that cannot be put right is a warning naming the command to run, not a failed update.
11. Commits the install and the migrations it ran as `chore(framework): update APEX framework to vX.Y.Z`. See [Commits](#commits).

## Upgrading a v0 project to v1

An APEX v0 project becomes an APEX v1 project when `ape framework update` installs a v1 release into it. The release's first migration entry is the lift, `ape project lift`, which rewrites the project to the v1 layout in one commit. The entry is **blocking**: if the lift is refused or fails, the update exits 1 and commits nothing (see [When a blocking migration is not applied](#when-a-blocking-migration-is-not-applied)). [How to lift a v0 project](lift-a-v0-project.md) covers what the lift does and how to review its result.

1. **Update ape, and move the project's pin.** The update must run on a v1 ape (see *Which ape runs*, below):

   ```bash
   ape update --pin    # updates this ape, moves .bingo/ape.mod to it, commits chore(ape)
   ```

2. **Give the branch an upstream**, if it has none. The lift checks the tag on the current branch's upstream remote:

   ```bash
   git rev-parse @{u} || git push -u origin "$(git branch --show-current)"
   ```

3. **Tag the commit to lift, and push the tag.** The tag is the archive of everything the lift deletes: v0 files stay readable as `git show apex-v0-final:<path>`. It must point at HEAD.

   ```bash
   git tag -a apex-v0-final -m "apex-v0 final"
   git push origin apex-v0-final
   ```

4. **Read the lift's plan.** `--dry-run` lists every pre-flight problem, not only the first, and writes nothing:

   ```bash
   ape project lift --dry-run
   ```

5. **Update the framework**, which runs the lift:

   ```bash
   ape framework update
   ```

**Which ape runs.** The update must run on a v1 ape. A project whose `.bingo/ape.mod` pins a v0 ape hands `ape framework update` to that pin, and the v0 ape is below the v1 release's `min_ape_version` 1.0.0: without a terminal it exits 11, and at one it offers to move the pin in a `chore(ape)` commit, which then makes the lift refuse (below). Either run the update as `APE_TOOLCHAIN=local ape framework update`, or move the pin with `ape update --pin` (step 1) **before** you tag. The lift itself always runs on the binary you invoke, and its commit moves the pin to that ape when it is a release and bingo is on PATH (otherwise it prints the command that does). See [How to pin ape in a project](pin-ape-in-a-project.md).

**Commit nothing after the tag.** A commit on top of `apex-v0-final`, including a `chore(ape)` pin move, makes the lift refuse with `lift.tag-not-head`. Tag the new HEAD and push the tag again, replacing the old one (`git tag -fa apex-v0-final -m "apex-v0 final"`, then `git push --force origin apex-v0-final`), and re-run.

The lift's commits (`apex: lift …`, and `apex: lift regression verdict` when the regression command ran) land before the update's own commit. The update then lists the judged entries it owes a person, such as the lift disposition for `apex-plan`. `ape project lift --help` and `ape project codes` list every refusal code.

## Which release is installed

- **By default, the highest tag shaped exactly `vX.Y.Z`**, compared as semver, so `v1.10.0` beats `v1.9.0`. A release candidate (`-rc.N`) or any other suffix is never picked, and a commit on the framework's `main` that no release tag names is never installed.
- **`--version vX.Y.Z`** installs exactly that release, and **`--version vX.Y.Z-rc.N`** installs a candidate. That is the only way to install one.
- **A default update never goes backwards.** If you installed a candidate newer than the newest final release, `update` keeps it and says so. `--version vX.Y.Z` goes back when you mean it.
- **An APEX v1 project never takes an APEX v0 release.** When the newest release is a v0 one, `update` keeps what is installed and says so. A `--version` naming a v0 release is refused (exit 1) and nothing is installed.
- **`--from-worktree`** installs the clone's working tree as it stands. It is for framework developers and scratch installs, and needs the clone clean and on `main` (`--force` bypasses that).

The release must use the framework's released layout (`_apex/` and `.claude/` at the root of the tag). A tag from the framework's build repo nests them under `framework/` and is refused (exit 3): point `--repo` at the ship repo.

## ape's minimum version

A framework release declares the oldest ape it works with, as `min_ape_version:` in its `_apex/ape-commands.yaml`. `update` reads it from the release **before writing anything**:

- **Meets it** (an rc of exactly that version counts, so `1.0.0-rc.2` meets `1.0.0`), or the release declares no minimum: the install goes ahead. A newer ape is **opt-in, never automatic**:
  - `--update-ape` moves ape to the newest release first: the project's bingo pin, in a `chore(ape): update ape to vA.B.C` commit of its own, or `ape update`. The command then re-runs on the new ape.
  - At a terminal, without the flag, you are asked. The default is No.
  - Without a terminal and without the flag, one line says a newer ape exists. An unattended run never picks up an ape nobody measured.
- **Below it, at a terminal:** you are asked whether to update ape first, through bingo when the project pins ape (`.bingo/ape.mod`), else through `ape update`. Then the same command re-runs on the new ape.
- **Below it, without a terminal:** nothing is written. ape prints the command to run (`run: …`) and exits 11.
- **A `dev` build or Go pseudo-version** cannot be compared: a warning, and the install goes ahead.

The two update routes trust different things. `ape update` installs the cosign-verified release binary. bingo compiles the tagged source through the Go module proxy, which is checksummed but is not the signed binary. A bingo-built ape reports its version correctly, and its git commit as `unknown`.

## Commits

- **`chore(ape): update ape to vA.B.C`**, only when ape was updated through bingo, touching only `.bingo/`.
- **The commits a migration makes itself**, such as the lift's, land first.
- **`chore(framework): update APEX framework to vX.Y.Z`**, holding the install and the migrations it ran, with these trailers:

  ```
  Framework-Version: vX.Y.Z
  Framework-Commit: <the release's full commit sha>
  Framework-Migrations: <ids, when any were applied>
  Generator: ape framework update
  ```

  `Governance-Version:` and `Governance-Commit:` follow when the project's governance clone was synced. An update where only that clone moved is titled `chore(framework): sync governance vX.Y.Z`.

Details:

- An update that changed nothing commits nothing.
- Your hooks run. If one fails, everything stays staged and the command exits 12.
- If ape was updated and committed and the install then fails, the ape commit stays: it is valid on its own.
- `--no-commit` leaves the whole result in the working tree for one `git diff`, and then only modified `apex-*` skills block the run.

## What gets touched

| Path | Behaviour on update |
| --- | --- |
| `.claude/skills/apex-*/` | Wiped and reinstalled. Other entries under `.claude/skills/` are never touched. |
| `_apex/ape-commands.yaml` | Overwritten. |
| `_apex/migrations/*.md` | Refreshed, **not synced**: an entry the framework pruned stays, because the ledger records entries by id. A v1 install removes the `v0.*` entries; the ledger keeps their history. |
| `_apex/standards/adr-0001.md` | Overwritten. |
| `.aboard/`, `.aboard/.gitignore` | Created if missing. An existing board, or an edited ignore file, is never rewritten. |
| `.gitignore` (repo root) | v1 release: the `.claude/` shape, as setup writes it. |
| `.claude/settings.json` | v1 project: the plugins `_apex/config.yaml` lists. |
| `_apex/framework.yaml` | Rewritten; the ledger and setup's record carried forward. |
| `_apex/config.yaml` | **Not touched.** |

The install never edits a skill you wrote: only `apex-*` directories are framework-owned. If a tracked `apex-*` skill file has uncommitted edits, `update` refuses without `--force`; untracked `apex-*` paths are treated as leftovers and replaced.

## Idempotency

Running `update` twice on a steady-state project changes nothing and commits nothing. Skills are reinstalled byte for byte, `.gitignore` is rewritten only when its `.claude/` shape is missing, and `framework.yaml` keeps its `installed_at` unless the framework commit, the ape version, the installed files or the ledger changed. A second run applies no migration twice, because the ledger records what ran.

## The framework's upgrade migrations

A framework release changes what a project must look like. The framework ships that change as entries under `_apex/migrations/`, one file per migration, and `ape framework update` runs them after the install.

```bash
ape framework update --plan            # print the plan, do nothing else
ape framework update --plan --no-check # …and run no entry's check: command
ape framework update                   # install, then apply the derivable ones
ape framework update --no-migrate      # install only; the list stays pending
```

`--plan` is the whole command: no install, no migration. It shows the list the project will hold **after** the update: what is installed now, overlaid by the release's `_apex/migrations/` by file name. It also runs each entry's `check:` against the project as it is now. A `SOURCE` column marks each row `installed`, `incoming (new)` or `incoming (replaces installed)`, and a `BLOCKING` column marks the entries the install cannot stand without. The repo is read as it stands; the update itself fetches first unless you pass `--no-fetch`, so use `--no-fetch` on both if you want the preview and the run to match exactly.

### What ape may run, and what it may not

`kind:` is the authority model, not a hint.

| `kind:` | What happens |
| --- | --- |
| `derivable` | ape runs the entry's `command:` unattended. |
| `judged` | Listed with the skill from `skill:` to dispatch, and **never executed, under any flag**. An entry that also carries a `command:` still does not run it. |
| anything else, or unset | Treated as `judged`: ape not understanding an entry means ape does not run it. |

### The states

The **ledger** in `_apex/framework.yaml` decides applied-ness: an ordered list of `{id, version, applied_at}`, ordered because "which migrations ran, in what order" is what you ask when one half-applies. It is what makes a second run a no-op and a failed run resumable.

| State | Means |
| --- | --- |
| `applied` | The ledger records it, or it does not and the entry's check says the shape is already there. ape never adds a ledger row for work it did not do. |
| `half-applied` | The ledger says it ran, and the check says the post-condition does not hold. |
| `pending` | Not in the ledger, and either the check answered "no" or no check is declared. The only state the runner acts on, and only for a derivable entry. |
| `cannot-tell` | Not in the ledger, and the check **could not run**. Unapplied and unverifiable, so it is never run: treating it as pending is how a runner re-applies things. |
| `superseded` | A present entry names it in `supersedes:`. It is not run. |

### Writing a `check:`

A check **reports and never gates**, and its exit code is read the way `grep -q`, `jq -e` and `test` already work:

- **`0`**: applied.
- **`1`**: not applied. This is the only non-zero code read as an answer.
- **`2` and above**: the check itself failed, so the entry is `cannot-tell`.

A shell reports a missing binary as exit **127**, so a check piping into a `jq` that is not installed comes back non-zero; reading every non-zero code as "not applied" would make the entry pending and ape would apply it. Write checks that exit 1 for "no": `test -f x && grep -q y x`, not `grep -q y x`, which exits 2 when `x` is absent.

Checks are bounded at two minutes. A `command:` is not, because a derivable entry may run a long command.

### `ape` inside a check means *this* `ape`

Checks and commands run with a PATH in which `ape` resolves to the binary running the migration, not to whatever `ape` the machine has installed. The framework's checks call ape (`ape project operator-interface --check`, `ape decision standard --check`), so each is answered by the ape that would run the entry's command. An older `ape` answering instead could report the shape as already there, and the entry would never run.

The shadow directory holds one entry, so nothing else on PATH changes resolution order. If it cannot be built, the run says so in a line beginning `migrations:` rather than proceeding silently.

### Ordering, and what stops a run

Entries are ordered **semantically**: `version` as semver, then `seq` as an integer, then `after:`. Never by filename: `v0.9.0` sorts *after* `v0.10.0` lexically, which is the bug the rule exists to prevent. A filename that disagrees with its frontmatter is reported.

An `after:` **cycle** leaves the order undefined, so none of the entries in it runs. An `after:` naming an entry that is not in the list is a finding, not a cycle.

A failed entry **stops the sequence**, because later entries may depend on it, and the rest are reported as not attempted. The ledger records what succeeded, and re-running picks up where it stopped. A migration is **skipped, never forced**, when its own paths have uncommitted changes; the message names them. What happens next depends on whether the entry is blocking.

### When a blocking migration is not applied

An entry marked `blocking: true` is one the install is not valid without; the lift is the main one. When a blocking derivable entry fails or is not attempted (an earlier entry failed, its check could not run, or it cannot be run):

- the update **exits 1**, names the entry and why, and commits nothing;
- the install is reverted, so the tree is as the update found it and a re-run starts from the release again;
- the entry's own error is printed where it ran;
- a commit that migration, or an earlier one, made itself stays (`git log <start>..HEAD` shows them), and the re-run carries on after them.

With `--no-commit` the result is left in the working tree instead, for you to reset or fix. Fix what stopped the migration, then run `ape framework update` again.

A judged blocking entry never fails the update: it is a dispatch a person owes after the install, listed in the output and in `--plan`.

A failed migration that is **not** blocking is reported, the ledger keeps what succeeded, and the update still commits the install and exits 0.

`ape doctor` reports owed entries as `migrations.pending`, a warning rather than a failure. It runs each entry's `check:` by the same rule as the update and `--plan`.

## Inspecting drift

```bash
ape framework status
```

Compares the installed release (from `_apex/framework.yaml`) with the newest release in the framework clone, which is what `update` would install, and says where the framework and governance clones are. It reports, loudly, when the installed tag now names a different commit than the one installed: release tags are meant never to move, so ape reports this and never follows it.

## Output formats

Every form takes `--output-format human|json|yaml`. The structured forms carry the full metadata block and an install summary.

## Troubleshooting

### `framework metadata not found at <path> — run "ape framework setup" to install`

The project has no framework install yet. Use [`ape framework setup`](framework-setup.md) for the first install.

### `blocking migration <id> …`

A blocking migration was not applied, and the update was reverted. The lines after the error name each entry, why, and the command it runs; the migration's own error is printed above them. For the lift, `ape project lift --dry-run` lists every pre-flight problem.

### `uncommitted changes under .claude/skills/apex-*`

You have local edits to committed framework skills. Commit them (the next update replaces them, but the diff is in git), move them to a skill not named `apex-*`, or pass `--force` to replace them now.

### `framework repo has uncommitted changes` or `framework repo is on branch X (expected main)`

Only with `--from-worktree`. Drop it to install a release, or pass `--force`.

## APEX v0 projects

For a project that stays on an APEX v0 release, `update` also installs the release's pipelines and v0 tables (`terminal-contracts.csv`, `commit-owners.csv`, `effort-defaults.yaml`, operating rules and the `CLAUDE.md` managed block, board recipes), adds the `sprint-status.yaml.lock` line to `.gitignore`, runs ape's own project-data migration (`--repair` adds its paid judgment phase; `ape doctor` reports it as `migration.pending`), and moves run artifacts left at the pre-`{output_folder}/ape` paths (`runs.legacy_layout`). A v0 project is served by the v0 ape its `.bingo` pins; its docs are at https://github.com/exoport/apex_process_ape/tree/v0.10.0/docs.

## Related

- [How to set up the framework (first install)](framework-setup.md): run this before `update`.
- [How to lift a v0 project](lift-a-v0-project.md): the lift the v1.0.0 migration runs.
- [framework.yaml reference](../reference/framework-yaml.md): the metadata file, and its migration ledger.
- [Framework updates install releases](../explanation/framework-updates-from-releases.md): the design, and every exit code.
- [Why setup and update are separate](../explanation/why-setup-and-update-are-separate.md).

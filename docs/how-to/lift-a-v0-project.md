# How to lift an APEX v0 project to v1

This page takes an APEX v0 project (framework v0.15.0 to v0.31.x) to the v1 layout. The
mechanical half is `ape project lift`: one commit that deletes the v0 families, converts the ADRs,
lays down the v1 skeleton and writes a disposition file. The judgment half (which v0 rule lives
where, what M1 delivers) is apex-plan's, from that file, and then yours. Every flag and field
below is in [`ape project lift --help`](../reference/cli.md#ape-project-lift).

## Before you start

- **Update ape, and move the project's pin, before you tag.** The v1 release requires ape 1.0.0 or
  later (`min_ape_version`). In a project that pins ape with bingo (`.bingo/ape.mod`), a bare
  `ape framework update` runs the pinned ape, and a v0 pin cannot install a v1 release. Run
  `ape update --pin`: it updates this ape, moves the pin to it, and commits that as `chore(ape)`.
  Do it before step 1, because the lift refuses when HEAD has moved past the tag
  (`lift.tag-not-head`).
- **The ape you run is the one that lifts.** The lift is exempt from the project's
  `.bingo/ape.mod` pin, since a v0 pin names an ape with no v1 commands. Its first output line
  names the binary (version and path). When that ape is a release and `bingo` is on `PATH`, the
  lift commit moves the pin to it; otherwise it prints the `bingo get` command that does. Until the
  pin moves, every other v1 command in the project needs `APE_TOOLCHAIN=local`, including
  `git commit` (the pre-commit hook runs ape) and Claude Code (start it as
  `APE_TOOLCHAIN=local claude`). See [How to pin ape in a project](pin-ape-in-a-project.md).
- **A clean tree,** on the branch you lift.
- **Nothing the v1 project commits may be ignored by git**, above all `.claude/settings.json` and
  `.claude/skills/`. The lift rewrites the root `.gitignore`'s `.claude/` rules itself. An ignore
  it does not rewrite (a nested `.gitignore`, `.git/info/exclude`, `core.excludesFile`) is refused
  as `lift.claude-ignored`: remove it, commit, and tag again.

## 1. Tag the commit to lift, and push the tag

The tag `apex-v0-final` is the archive of everything the lift deletes: v0 files stay readable as
`git show apex-v0-final:<path>`. The lift checks the tag on the current branch's upstream with
`git ls-remote` (its one network call), so the branch needs an upstream:

```bash
git rev-parse @{u} || git push -u origin "$(git branch --show-current)"
git tag -a apex-v0-final -m "apex-v0 final"
git push origin apex-v0-final
```

## 2. Read the plan

```bash
ape project lift --dry-run
```

`--dry-run` prints the whole plan and writes nothing. It lists **every** pre-flight problem, not
just the first, as `would refuse` lines, so one run tells you everything to fix. On a project
whose tag is not pushed yet it reads, in part:

```text
would refuse  lift.tag-not-pushed — apex-v0-final is not on origin — `git push origin apex-v0-final`, then lift
delete    project-context.md  (project context)
adr       (none) -> ADR-0001  development/decisions/0001-record-architecture-decisions.md  [accepted]  APEX's standard ADR (ape's own copy)
gates     2 command(s) from project-context.md:3; regression: . ./env.sh && ./check.sh
gates     written for /bin/sh: `source ./env.sh` → `. ./env.sh` (ape runs checks with sh, which has no source where it is dash)
...
dry run: nothing written
```

The pre-flight refusals (exit 1 on a real lift, `code` in `--json`):

| Code | Meaning |
| --- | --- |
| `lift.not-v0` | Not a v0 install, or already lifted (a v1 layout or an earlier lift). |
| `lift.unsupported-layout` | The v0 version is outside v0.15.0–v0.31.x, or `_apex/config.yaml` does not resolve. |
| `lift.dirty` | The tree is not clean. |
| `lift.no-tag` | `apex-v0-final` does not exist. |
| `lift.tag-not-head` | The tag is not on HEAD. |
| `lift.tag-not-pushed` | The tag is not on the branch's upstream remote at the same commit. |
| `lift.remote-unreachable` | `git ls-remote` failed (action `retry`). |
| `lift.claude-ignored` | git ignores something the v1 project commits, by a rule the lift does not rewrite. |

Read the `hashed` lines too. A file that a test, CI script or lock file hashes goes red once the
lift edits a comment in it. Keep such files out of the lift with `--exclude`, a glob, repeatable:

```bash
ape project lift --dry-run --exclude 'schema/core/**' --exclude deploy/init/00-bootstrap.sh
```

This is a heuristic: a copy compared byte for byte without a hash needs its own `--exclude`.

## 3. Run the lift

There are two ways, and they produce the same commit.

**Through `ape framework update`** to a v1 release. The release's first migration,
`v1.0.0_seq-01`, runs `ape project lift --from-update`. Read what the update will do first:

```bash
ape framework update --plan      # the migration plan only: no install, no migration
ape framework update --dry-run   # the framework drift and the pending migrations, nothing written
ape framework update
```

`--plan` lists the migrations the project will hold after the update, with a `SOURCE` column
marking the ones the update brings, and their state (pending, applied, half-applied,
cannot-tell). The lift entry is `blocking: true`: when it fails or is not attempted, the update
exits 1, names it and why, and commits nothing (the install is reverted). The lift's own error is
printed where it ran. On success the lift's commits land first, then the update commit,
`chore(framework): update APEX framework to vX.Y.Z`. The update also runs the release's other
derivable entries (archive layout, operator interface, the ADR-0001 standard, `next.md`); each
one that has something to do makes its own `apex: …` commit. See
[How to refresh the framework in a project](framework-update.md).

An ape below 1.0.0 offers to update itself first on a terminal, and exits 11 without one. If it
also moves the pin, that commit lands after the tag, and the lift then refuses: move the pin
before tagging, as [Before you start](#before-you-start) says.

**By hand, before the update,** when you need `--exclude`:

```bash
ape project lift --exclude '<glob>'
ape framework update
```

The migration's check then finds the `apex: lift` commit, so the update counts the entry as
applied and does not run it again. It is applied by its check, not by the ledger: no row is
written to `_apex/framework.yaml` for it, and `ape doctor` says "by check, not in the ledger".

What the lift commit (`apex: lift from APEX v0 <version>`) does, in short (the help lists every
rule):

- deletes the v0 families (the skills the v0 install manifest lists, the v0 `_apex` tables and
  pipelines, stories, the sprint tracker, planning, ADR and pattern records, registries, deferred
  records, team memory, `project-context.md`), and keeps docs, CHANGELOG, evidence, research, code
  and `_apex/framework.yaml`;
- writes ADR-0001, APEX's standard ADR, and converts every other ADR into
  `development/decisions/NNNN-<slug>.md`, renumbered from 0002 (see
  [How to record a decision](record-a-decision.md)). Accepted and superseded ones are recorded in
  `_apex/state/decisions.json`, so their text is fixed from here on. Canon ADRs the project cites but
  has no record of are copied from the governance clone, when `_apex/config.yaml` resolves one;
- rewrites ADR citations to the new numbers, and strips story, epic and deferred ids from code
  comments only;
- carries `## Code Validation` from `project-context.md` into `AGENTS.md` `## Commands`, and the
  runnable commands, joined with `&&`, into `regression:` in `_apex/config.yaml`;
- lays down the v1 skeleton as `ape project init --brownfield` does, and the `.claude/` shape in
  the root `.gitignore`;
- writes `development/lift-disposition.md` for apex-plan.

The exit code is 0 whether the regression verdict below is green or red: the commit stands either
way.

## 4. Read the regression verdict

The `regression:` command runs twice: at the tag, before anything is written, and at the lift
commit. The second run is judged against the first. `--no-verify-run` skips both.

| Verdict | What it means | What follows |
| --- | --- | --- |
| `green` | Green after the lift. | Nothing. |
| `red-at-tag` | Red at the tag too, failing as it did there. | Pre-existing: apex-plan puts it in Next or a fix milestone. With the note "the output changed after the lift", compare the two logs first. |
| `red-at-tag-new-failures` | Red at the tag too, but tests fail that did not fail there. | The lift broke those tests, named in `regression_new_failures`. Fix them first. |
| `red-after-lift` | Green at the tag, red after. | The lift broke it. Fix it in a commit of its own, or `git reset --hard apex-v0-final` and lift again with `--exclude`. |
| `cannot-run` | Exit 126 or 127 (or "command not found" with make's `Error 127`) at both points, and no failing test named. | A tool is missing here. It is not a red. |
| `red-no-baseline` | Red after the lift, and the run at the tag could not run. | Nothing says whose red it is. apex-plan reruns it at the tag in a temporary worktree. |
| `flaky` | A lift-caused red that passed when ape ran it once more. | Not the lift's. Record it and plan no fix. |

A `red-after-lift` or `red-at-tag-new-failures` command is always run a second time at the lift
commit. Green that time makes it `flaky`. A `red-at-tag-new-failures` command whose re-run fails
only as it did at the tag becomes `red-at-tag`. Red again keeps the verdict, and its note says so.

A command that exits 0 but whose output names failing tests counts as red. Failing tests are read
from `go test`, gotestsum, pytest, cargo, jest, and flutter or dart test output. A command that is
an `&&` chain runs command by command at both points, past a red one, so a red lint cannot hide
the tests after it. Each command is judged on its own, and the overall verdict is the worst one.

The verdict is recorded in three places:

- the output, and `--json` (`regression_verdict`, `regression_note`, `regression_new_failures`,
  and `regression_commands[]` for a chain);
- `regression` in `_apex/state/lift-disposition.json` (verdict, note, `regression_new_failures`,
  commit, and one entry per command with `exit_at_tag`, `exit_after` and the logs);
- a `## Regression` section of the disposition file, committed after the lift commit as
  `apex: lift regression verdict` (`regression_commit` in `--json`).

The logs are under `_apex/state/logs/lift/`. A real `red-after-lift` reads:

```text
regression at apex-v0-final exit 0 (0.1s)
regression exit 1 [red-after-lift] — LIFT FAILURE: red at the lift commit (exit 1), green at apex-v0-final — the lift broke it ...
regression verdict recorded in development/lift-disposition.md (## Regression, commit 67707c4 apex: lift regression verdict) and _apex/state/lift-disposition.json (regression)
```

### `source` lines and exit 127

ape runs every check, gate and regression command with `sh`, which is dash on Debian and Ubuntu
and has no `source`. A line of the `## Code Validation` block that is exactly `source <file>` is
written as `. <file>`. The lift lists each rewrite in `gates.rewrites` (JSON), in the disposition
file and in the output:

```text
gates     written for /bin/sh: `source ./env.sh` → `. ./env.sh` (ape runs checks with sh, which has no source where it is dash)
```

Any other bash or zsh builtin is left as written. When a command exits 127 and its output says a
bash or zsh builtin was not found (`sh: 2: source: not found`), the result carries a `shell_note`
naming the builtin, instead of reading as a missing tool. The same note appears on a check, a gate
or a regression run anywhere in ape. Write that line in POSIX sh, or run it with `bash -c '…'`.

## 5. Dispose of the disposition file

`development/lift-disposition.md` is the lift's hand-off. It holds every binding-rule candidate
(from `project-context.md`, `CLAUDE.md` and team memory), with an empty **Disposition** column. It
also lists the gate commands, the code and tests that read deleted v0 state, the converted ADRs,
the ids left in code and the regression verdict. The full lists are in
`_apex/state/lift-disposition.json`, which git ignores: it exists only in the checkout that
lifted.

While the file exists, `ape project check` reports `lift.disposition-pending` (action `person`).
That is the one finding allowed: everything else must pass.

The work is the framework's judged migration entry (`kind: judged`, skill apex-plan).
`ape framework update` lists it and never runs it. You start it: run apex-plan with a request that
names the entry and states the next real delivery. You can do this from `/apex-orchestrator` in
Claude Code, or from a terminal:

```bash
ape task apex-plan --args "<request naming the lift-disposition migration, and the next delivery>"
```

apex-plan fills every Disposition cell with one of `kept in AGENTS.md`, `kept in CLAUDE.md`,
`development/project.md`, `gated by <cmd>`, `ADR-NNNN`, `docs/<page>` or `dropped: <reason>`. It
reads the regression verdict as the table above says, and gives each v0-state reader a re-point
or a drop. It commits all of it as `plan: lift <summary>`. It approves no milestone.

Then:

1. A fresh read-only session reviews the filled file.
2. You correct any row, and delete `development/lift-disposition.md` in its own commit. Delete only
   the `.md`: the JSON is never committed.
3. When apex-plan wrote a fix request for code that reads deleted v0 state (the first dated line
   in `development/next.md`), run that fix first, before any delivery work:
   `ape task apex-change --args "<request>"`.

From here the project is an ordinary v1 project: see [How to run a milestone](run-a-milestone.md).

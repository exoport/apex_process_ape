# How to run a single skill with `ape task`

Run one framework skill without authoring a pipeline YAML — everything a
pipeline step gets (framework-agent prefix, preflight, bridge hooks,
manifest, telemetry), with the parameters passed as flags. Execution is
interactive-PTY only: claude runs as a REPL, the prompt is typed as
keystrokes, and completion is detected via the bridge Stop hook.

## Basic run

```bash
ape task apex-shard-doc --args "--doc prd"
```

The skill must resolve under `.claude/skills/<name>/SKILL.md` (project)
or `~/.claude/skills/` (user) — unknown skills fail preflight with exit
code 2 before any claude spawn.

## With a framework agent, model, and prompt

```bash
ape task apex-create-prd \
  --agent apex-agent-pm \
  --model opus \
  --prompt "a greeter CLI" --prompt-flag --prompt
```

`--agent` fronts the skill exactly like a pipeline step:
`/apex-agent-pm --autonomous -- apex-create-prd --autonomous …`.
`--prompt-flag` names the skill flag the `--prompt` value is forwarded
through (the `prompt_flag:` spec field equivalent).

## Resuming from a handoff file

```bash
ape task apex-create-prd \
  --agent apex-agent-pm \
  --handoff _output/handoffs/2026-07-05-x-handoff.md --prompt-flag --prompt
```

`--handoff <file>` is a shorthand for `--prompt`: it checks the file
exists and derives the prompt `Read <abs-path> and follow the Resume
Protocol inside it.` — the same continuation prompt the `/handoff`
skill itself suggests when it writes a handoff doc. It still needs
`--prompt-flag` to actually reach the skill, and is mutually exclusive
with `--prompt` (exit code 2 if both are given, or if the file doesn't
exist). It intentionally forwards a pointer to the file rather than
inlining its contents — the prompt is typed into the REPL as literal
keystrokes, and a multi-line value risks submitting early.

## Commit control — two independent layers

- `--no-commit` — **skill layer**: tells the skill/framework not to
  commit. The no-agent invocation shape already carries it by
  convention; with `--agent` it is injected into the skill invocation.
- `--task-commit ["<msg>"]` — **task layer**, off by default: ape
  commits the complete task working tree at the end of the run. A bare
  flag derives the message `ape:task/<skill>`.

They compose: `--no-commit --task-commit "feat: X"` suppresses the
framework's granular commits and produces exactly one whole-task
commit. The dirty-tree gate applies only when `--task-commit` is given
(bypass with `--commit-allow-dirty`).

## The commit-ownership assertion

Every dispatch is checked against the project's own declaration,
`_apex/commit-owners.csv` — **the framework ships it, and
[`ape framework update`](framework-update.md) installs it.** A project
that has not updated against a framework carrying the roster has no file,
and every dispatch's assertion is then skipped; the update run reports
which of the two happened rather than leaving an absent roster to look
like a deliberate choice.

```csv
skill,commit_kind,message_regex
apex-story-batch-dev,dev,^dev: story \d+\.\d+ [a-z0-9 ]+$
apex-story-batch-dev,review,^review: story \d+\.\d+ [a-z0-9 ]+$
```

Two rows for one skill is the schema's shape, not a duplicate. Each regex
carries its own anchors **in the file** and is compiled verbatim — `ape`
adds none — and is matched against a commit's **subject line only**.

| Skill | Assertion across the dispatch |
| ----- | ----------------------------- |
| **absent** from the CSV | HEAD unchanged, no path staged that was not staged before, and the stash unchanged |
| **present** in the CSV | at least one commit, and every commit in `pre..HEAD` matching one of that skill's rows |
| **no CSV in the project** | nothing is asserted; the verdict is `skipped` with a reason |
| **present, dispatched with `--no-commit`** | producing no commit is `skipped` with a reason — the dispatch told it not to. Committing **anyway** is `dispatch.committed_under_no_commit`, reported instead of the message format even when the subject matches |

HEAD alone is not the check, and that is the point: `git add` and
`git stash` both leave HEAD exactly where it was, and a stash silently
destroys the caller's working tree. The committer side is a **per-commit
predicate over the range**, not "HEAD advanced by one" — a batch dispatch
makes a dev and a review commit per story, and all of them must match.

Three things it deliberately does not do:

- **A project with no CSV is not asserted at all.** With nothing
  declaring which skills commit, neither assertion has a basis, so the
  verdict is `skipped` with a reason rather than a guess. Enforcing the
  non-committer branch there would convert "this project has not adopted
  the declaration" into "this project asserts nothing may commit" — a
  claim nobody made — and would fail every framework skill that
  legitimately commits, on every project that has not adopted the CSV
  yet. A CSV that exists but does not parse fails preflight instead:
  degrading a broken declaration to an empty one would turn every
  committer's assertion into the non-committer's, which is exactly the
  inversion that lets a suppressed commit through.
- **Rows naming a skill `ape` never dispatches are tolerated.** The
  conducting session's own rows carry `apex-orchestrator`, a persona that
  is adopted rather than dispatched, so the assertion never reaches them.
- **A path the caller staged before the run is the caller's.** Only paths
  the dispatch added are reported.

`--task-commit` is `ape`'s own commit, in `ape`'s own derived format, made
after the skill is done — it is not judged against a skill's declaration,
and the assertion is skipped when that flag is set.

**`--no-commit` does the same to the other half.** The roster is
`skill,commit_kind,message_regex` — it has no conditionality column, and
it declares the **shape** of a commit rather than its inevitability:
*when this skill commits, the subject looks like this*. Reading it as
"this skill always commits" would convict a declared committer for
obeying `--no-commit`, which several framework skills mandate in their own
text (`apex-sprint-planning`: "When `{no_commit}` is `true`: make NO
commits and NO `git add`/`git stash`"). So under that flag, producing no
commit is a **skip with a reason** rather than a violation — a suppressed
commit means *the skill was permitted to commit and produced none*, never
*the operator said not to and it complied*.

The flag excuses the absence of a commit and **forbids its presence**. A
skill that commits anyway reports `dispatch.committed_under_no_commit` —
reported *instead of* `dispatch.message_format`, including when the
subject matches its declared shape perfectly. When the commit should not
exist, its wording is not the defect, and naming the format would send you
to fix a commit whose fix is deletion.

A violation exits **6** and prints each finding on stderr:

```text
Error: dispatch.stash_changed: the stash changed across the dispatch
  (no stash, depth 0 → a1b2c3d4, depth 1) — a stash silently destroys the
  caller's working tree, which is why the operating rules forbid it outright
```

In a directory that is not a git repository the assertion cannot run. It
reports that as **skipped**, never as a pass: "we could not look" and "we
looked and it was clean" are different answers. The same applies when
HEAD advanced but the commit subjects could not be read — a failed read
is not evidence of a suppressed commit.

Every verdict, including a skip, is also written to the run's
`manifest.yaml` as `commit_contract`. The JSON envelope is ephemeral; a
consumer that parses it, sees failure and discards stdout would otherwise
leave nothing on disk saying why.

**Assert the verdict's shape, never the exit status.** A dispatch that
asserted and held, and one that asserted nothing, are BOTH exit 0 — the
skip is not a failure and must not fail a run. The two are told apart by
the payload:

```json
"commit_contract": {"skill":"apex-help","declared":false}
"commit_contract": {"skill":"apex-help","declared":false,"skipped":true,
                    "skip_reason":"--task-commit: ape makes this dispatch's commit itself, …"}
```

The first is a **held** assertion. The second asserted nothing. A missing
`skipped` key is the held verdict rather than an absent one — `omitempty`
drops a false bool by design — so a check written against `$?` cannot
tell them apart, and neither can one that only looks for the key's
presence.

> **Reading a failing run:** `totals.commits_made` counts **ape's own**
> boundary commits (`--task-commit`, per-step commits), never the ones the
> dispatched skill made itself. A `commits_made: 0` on a run whose skill
> committed six times is correct and expected — check `commit_contract`
> and `git log`, not that field.

## Machine-readable result

```bash
ape task apex-create-prd --agent apex-agent-pm --output-format json
```

Progress streams to stderr; stdout carries only the result envelope:

```json
{
  "skill": "apex-create-prd",
  "agent": "apex-agent-pm",
  "model": "claude-opus-5",
  "success": true,
  "exit_code": 0,
  "duration_seconds": 142.3,
  "cost_usd": 0.83,
  "usage": {
    "input_tokens": 15031,
    "output_tokens": 30682,
    "cache_read_input_tokens": 1703661,
    "cache_creation_input_tokens": 195953,
    "cache_creation_5m_input_tokens": 61200,
    "cache_creation_1h_input_tokens": 134753,
    "num_turns": 26
  },
  "commits": ["SKILL:create-prd"],
  "manifest_path": "_output/ape/tasks/apex-create-prd/20260702-120000-abc1234/manifest.yaml",
  "commit_contract": {
    "skill": "apex-create-prd",
    "declared": false
  },
  "error": null
}
```

`commits` lists every commit made during the run (framework commits
included), oldest first.

`commit_contract` is the per-dispatch commit-ownership verdict described
above. It is always present, including when the assertion passed and when
it had to be skipped — a consumer must be able to tell "asserted and
clean" from "could not assert", and a field that appears only on failure
cannot. `declared` says which of the two assertions ran; `violations` and
`skipped` / `skip_reason` appear only when they apply. Cost and usage come from the session
transcript scan — the same telemetry pipeline steps record.
`cache_creation_input_tokens` is the total ephemeral cache-write count;
`cache_creation_5m_input_tokens` and `cache_creation_1h_input_tokens`
break it into the two tiers (added in v0.0.37, additive — the total is
their sum). The same split appears on each `model_usage` entry.

## Artifacts

Each run writes `manifest.yaml`, per-step ndjson, and runlog streams
under `_output/ape/tasks/<skill>/<run-id>/` (a `latest` symlink tracks the
newest run). Task runs appear in `ape costs` under `task:<skill>` after
`ape costs roll`.

## Exit codes

| Code | Meaning                                                                  |
| ---- | ------------------------------------------------------------------------ |
| 0    | Success.                                                                 |
| 1    | Skill ran but failed, Stop-wait error, or a backstop fired — the progress-aware idle window (`--idle-timeout`, default 60m) or the hard ceiling (`--max-duration`, default 3h). |
| 2    | Usage or preflight error (unknown skill/agent, bad flags).               |
| 3    | REPL never became ready — trust-dialog dismissal failed or an unknown modal blocked; the last pane snapshot is on stderr. |
| 5    | The session's own turn failed against the API and nothing followed — a `529`/`522`/… carried verbatim. Upstream and retryable: the skill did not misbehave, so a caller that knows its budget can decide to re-run. Reported ~3.5 min in, rather than waiting out `--idle-timeout`. |
| 6    | The dispatch violated the project's declared commit ownership. Distinct from 1 because the skill's own work may have **succeeded**: the run finished and the repository is not in the state `_apex/commit-owners.csv` declared it would be. Only reported when nothing worse happened — a skill that crashed *and* left a stash is reported as the crash. |

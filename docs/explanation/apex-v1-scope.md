# What APEX v1.0 promises, and where it stops

APEX v1 replaces v0's story ceremony with a small loop: a person approves a milestone, the
orchestrator plans each change, applies it on its own branch, verifies it, has a separate session
review it, and archives it as one squash commit. ape owns every record in that loop, and refuses
when a record would be wrong. This page says what that buys you, and the shape of work v1.0 was
built and measured for. The reference for each command is `ape <command> --help` and
[cli.md](../reference/cli.md).

v1.1 keeps this promise and the shape below, and adds to it (usage-aware launching, waiting on
events, module fences for libraries, expert advise); the repository's `CHANGELOG.md` lists what.

## The shape v1.0 is built for

**One operator per checkout, on a local trunk.** Every project surveyed before v1 worked this way:
one person, a linear `main`, no merges. v1.0 states it rather than pretending otherwise:

- Change branches (`change/<id>`) are local. Attempts, limit stops, run logs and hook state live in
  the checkout's ignored `_apex/state/`. A second machine does not see them.
- ape never fetches. It compares the base branch with its upstream as last fetched, shows
  ahead/behind in `ape status`, and refuses `change start` and `change archive` while the base is
  behind (`base.behind-upstream`: pull first). Without that, an archive onto a stale base would
  diverge from the remote.
- Code that lands on the base outside the change flow (no `Change:` trailer, not an `apex:` record)
  is listed as `outside_flow` in `ape status --json`. It is shown, not failed. A "reconcile" change
  is how such work gets a spec.
- One v1 project per repository: every project shares the `change/` branch prefix and one HEAD.

A protected `main`, pull requests, and CI on the squash are not v1.0. PR mode
(`archive --to-branch`) is planned for v1.1.

## What reaches the base branch

D38's guarantee is about **code**: code reaches the base branch only as the single squash commit of
a verified, reviewed change, with the trailer `Change: <id>`. The work-in-progress commits, failed
verifies and review rounds stay on `change/<id>`. Archive keeps the branch tip as
`refs/apex/archive/<id>`, so the history behind a squash is never garbage-collected.

The base branch also receives **records**, which are not code:

- `apex: project init`, and the lift commit `apex: lift from APEX v0 …`;
- milestone records (proposed, approved, gate results, close), decisions accepted, tweaks approved;
- the change's draft and its plan (written by `apex-change` before the branch exists);
- `ape next add` lines in `development/next.md`, and acceptances of archived changes.

So "the base only receives verified changes" is true for code, not for every commit. A changelog
generated from commit history should filter `apex:` subjects. The squash subject follows
`commit.subject` in `_apex/config.yaml` (for example `{type}: {title}` with the change's `type:`).

## Tweaks are a queue, not an interrupt

A tweak (`milestone: none`) is a change a person asked for outside the roadmap. Its `type:` says
what it is (`fix` for a defect, `feat` for a small feature), so `ape change new --milestone none`
needs `--type`. A person approves it with `ape change approve`, and the orchestrator runs approved
tweaks first at its next change boundary. One checkout cannot hold two actors: the
orchestrator's working tree sits on `change/<id>` most of the time. So tweaks are planned and approved
**between runs**: wait for the run to end, or `ape run stop` it, plan and approve the tweak, and start
the run again: nothing queues a tweak into a running loop. If archive finds the base branch checked out in another
worktree, it stops with `git.base-checked-out`, naming the worktree. That is an environment problem,
so the orchestrator stops and does not re-plan.

## What only a person does

Some actions are a person's by design:
- approving a milestone or a tweak, including re-approving a milestone whose gate was fixed after
  its work was done (`ape milestone approve <M> --accept-green "<reason>"`);
- accepting a human-review task (`ape change accept`);
- archiving past an unaccepted human-review task (`--allow-pending-human`);
- answering a blocked change (`ape change answer`);
- accepting a decision (`ape decision accept`) or a changed config (`ape project accept-config`);
- closing a milestone on a red gate (`ape milestone close --accept-red "<reason>" --by <who>`).

ape refuses these when it runs under a model, which it detects by `APE_SESSION` (every session ape
starts) or `CLAUDECODE` (every Claude Code Bash tool call). A person does them in one of two ways:

- **From a plain terminal.** A `!` line typed inside Claude Code runs in that session's
  environment, so it counts as a model session.
- **In the person's own Claude Code session, by typing the act** (E2). Type the phrase in that turn,
  for example `approve M1` before the session runs `ape milestone approve M1`; ape then records
  your words as the approval. `ape hook user-prompt --help` lists the phrases. `ape project init`
  registers the hook that records them. Inside a session ape started, no phrase counts.

See [What only a person does](person-acts.md).

The same rule covers the guard overrides. The pre-tool hook denies `git tag`, `git push`,
`git rebase`/`merge`/`cherry-pick` on a change branch, and `pkill`/`killall` in v1 sessions. An
`APE_ALLOW_TAG`, `APE_ALLOW_PUSH` or `APE_ALLOW_KILL` override counts only when the person who
launched the session set it. A variable the model writes into its own command line does not count.

Human-review tasks are never archived away. The orchestrator leaves a change with pending
human-review tasks `reviewed` and lists it under `awaiting_human`. The milestone cannot close until
a person accepts the tasks (`ape change accept … --evidence <path|url>`). Accepting still works
after archive. Hardware runs, soaks and attested sign-offs are human-review tasks with evidence, never
`check:` commands.

A release with known defects is a milestone closed with `--accept-red`. The reason and the person
are recorded in `milestones.json`, in the gate evidence when `evidence: archive` is set, and on the
roadmap's done line. Tagging stays a person's act, after the close.

## ape and the framework must match

A v1 project installed beside an older ape would fail in the middle of a skill: in every v0 ape,
`ape change` is the old maintenance lane. The v1 framework release declares its floor as
`min_ape_version` in `_apex/ape-commands.yaml`. Every ape since v0.4.0 refuses `ape framework update`
to a release above it, and a v1 ape also refuses `ape task` when the installed floor is above it.
Apes older than v0.4.0 cannot see the floor, so **update ape first**: pin the new version in
`.bingo/ape.mod`, which every ape since v0.9.0 follows, then update the framework.

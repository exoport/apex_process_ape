# Security model and known limits

APEX runs much of your work unattended: the loop plans, applies, reviews and archives changes while you are away.
This page says what keeps that safe, where the limits are today, and what you can do about each limit until it is
closed.

## What APEX protects, and against what

APEX and `ape` are built to catch **mistakes**: a model that edits a spec directly, runs a gate script outside `ape`,
pushes, tags, kills processes it should not, commits a secret, or tries to approve its own work. These protections
are mechanical, so they do not depend on the model following instructions:

- the PreToolUse guard parses each command, so quoting, chains, `sh -c` and `env` wrappers do not hide it;
- the hooks fail closed: if `ape` cannot run, the session cannot edit or run commands;
- `ape`'s own records can be changed only through `ape`, and a forged one stops the next change command;
- every commit is scanned for secrets;
- the nine person acts (approving a milestone or tweak, answering a blocked change or an orchestrator's question,
  accepting a human-review task, accepting the config, accepting a red gate, allowing pending human work, accepting
  an ADR) are refused when a
  model runs them, unless you typed the act's words yourself in that turn
  ([What only a person does](person-acts.md));
- from 1.4.2, a conductor may carry out some of those acts for you, but only those your standing orders name, only in
  the project whose orders they are, only with a stated reason and the evidence it rests on, and only through one
  `ape` command that checks all of that first (see "Acts a conductor makes for you" below).

They hold against a model that makes an honest mistake. They are weaker against a model that has been **steered**:
one that read instructions planted in a file, an issue, a web page, a plugin's text or a tool's output, and follows
them. In the unattended loop that is the case to plan for. The limits below are all of this kind.

## Known limits

### A model can carry out a person act through a script

`ape` tells a model from a person by two environment variables that every model session carries. The guard reads the
command a model runs, but not the contents of a script file
([hooks.md](../reference/hooks.md) lists what it does not see). A model that writes a script which clears those
variables and then runs a person act gets past both checks, so the act is recorded as if a person had done it.

This needs a steered model. It has never been seen in APEX's measurements. A fix is not scheduled yet: telling a
person from a model reliably needs a confirmation the model cannot give, and that is a design question for a later
release.

**Meanwhile:**

- Read the record. Person acts are recorded in commits whose subject starts `apex:` (for example
  `apex: M2 approved`, `apex: <change> answered`, `apex: config accepted by <name>`), and an act granted in your own
  Claude Code session records your words: `by: <name> (prompt <time>: "<your words>")`.
  An approval, acceptance or answer you do not remember making is the sign to look for:
  `git log --grep '^apex: ' --format='%h %an %s'`.
- Keep text you have not read away from unattended runs: plugins you have not reviewed, pasted issue text, pages
  fetched from the web.
- Treat a change that adds a script calling `ape` or `git push` as worth a look in review.

### The push and tag guards have the same gap

The guard refuses every `git push` and tag write a model runs, in any form it can parse. The same script route gets
past it, and whether a push then succeeds depends on the credentials the session can reach: an `ssh-agent` socket, a
git credential helper, a token in the environment. The loop never pushes. From 1.4.2 a conductor may push a work
branch, and only a work branch: never `main`, the base branch or a tag, only through `ape conductor act`, and only
when your standing orders name it. Under the script route, the host's branch protection, not `ape`'s guard, is what
keeps `main` and your tags safe.

**Meanwhile:**

- Protect `main` and your release branches on the git host, and restrict who may create tags. A protected branch
  refuses the push whatever the guard missed.
- If your conductor pushes work branches, give its shell a token that can push branches but not `main` or tags.
- Start unattended runs from a shell without push credentials. `ape` never fetches or pushes for the loop, so
  nothing in the loop needs them. For example:
  `env -u SSH_AUTH_SOCK -u GITHUB_TOKEN -u GH_TOKEN ape …`. If git uses a credential helper, give that shell a
  configuration without it.

### Sessions inherit your whole environment

A session `ape` starts gets every environment variable of the shell that started it, except Claude Code's own.
Anything there (cloud keys, API tokens, a database password) is readable by the model and, on the host, can be sent
anywhere. The sandbox, once it is complete, is where this is meant to be closed.

**Meanwhile:**

- Start loops from a shell that holds only what the project needs. Keep admin and production credentials out of it.
- Prefer short-lived and narrowly scoped tokens for anything a project's checks must use.
- For a local development stack, prefer settings that need no secret (trust or passwordless authentication on a
  local database), or keep the value in a git-ignored file.

### Logs and session transcripts are not scanned for secrets

Commits are scanned; `ape`'s logs and run records under `_apex/state/`, and Claude Code's own
session transcripts (kept in your home folder), are not. They hold whatever a session read,
including environment output and the contents of files with credentials. They are not committed and
stay on your machine.

**Meanwhile:**

- Read a log or transcript before attaching it to an issue or sharing it. The conductor's decision log
  (`_apex/state/conductor-<you>.md`) is one of these local files. It quotes the reasons and evidence of each act.
- Keep real secrets out of the files a session is likely to read (see the previous limit).

### Plugins are pinned by a ref, not by their content

A plugin you list is checked at a ref: the version its marketplace serves, or the git ref you point
the marketplace at. A plugin's skills, rules and scripts run with your session's rights, and its
text is read as instructions. Nothing checks that the content behind a ref is the content you
reviewed.

**Meanwhile:**

- Install plugins only from sources you trust, and pin them: point the marketplace at a git ref you
  reviewed (a commit, which cannot be moved the way a tag or branch can).
- Before bumping a plugin's ref, read what changed.

### Acts a conductor makes for you (1.4.2)

You can let your conductor act for you while you are away. Without standing orders, it only relays: every act still
comes to you.

**Standing orders.** You write them in your own file in each project, `_apex/standing-orders/<you>.md`, committed with
it. Teammates can read them, and git keeps their history. The orders count only after you accept them from a terminal
with `ape orders accept --by <you>`. The conductor can draft them with you; `ape orders accept --from <draft>` shows
what `ape` will enforce, and anything it will read only as advice, before you confirm. A header line `ape` cannot
read is refused, never ignored. A commit that changes the file stops them counting until you accept it again, and
the edit tools are refused on the folder.

`--by` must name the orders' person, and `ape` cannot check who typed it: it trusts whoever sits at the terminal, as
for every person act today. It records who accepted; it does not prove it.

What the orders can and cannot allow:

| A conductor may | When |
| --- | --- |
| answer a blocked change; answer an orchestrator's question; approve a tweak; approve a milestone; accept an ADR; accept a human-review task whose task names a delegate; prepare a release for you; push a work branch and open a PR | only the acts your orders name |
| accept the config; accept a red gate; allow pending human work; push to `main` or the base branch; tag; release; merge; change your orders | **never** |

Preparing a release means the checks and the release notes, for a version the changelog already names: the conductor
hands you the result and the exact tag and push commands, and you run them. Your orders can also list categories that
always come to you even when the act is listed: a change that touches the gate, a change that edits the config, or a
milestone past a token budget you set. A work branch is never `main` or the base branch, and the conductor never
pushes tags; `ape` refuses both, and merging stays with you or your branch protection.

Your orders can narrow an act further with conditions `ape` checks: the kinds of tweak it may approve, how many changes
a milestone it approves may hold, the milestone's last gate result, and answers only to questions, not to changes
stopped by a limit. Anything else you write in the orders guides the conductor's judgment, but `ape` does not enforce
it.

**Each act.** A conductor acts only through `ape conductor act`. Any other way a model runs a person act is refused,
as before. Before it runs the act, `ape` checks:

- the orders are accepted and not revoked;
- they name the act;
- its conditions hold;
- the act gives a reason;
- the evidence it names exists, and a milestone approval names the latest gate run.

`ape` then re-runs the check behind the act itself (the gate's self-test for a milestone, the change's check for a
tweak) and refuses if it is red. Whether the evidence supports the act is still the conductor's judgment, and the
reason quotes it.

**The record.** Every act is recorded as `by: conductor under orders <you>@<commit> (reason)`, with its evidence, and
you are notified of each one. The conductor also keeps a decision log on that machine: each act with its reason and
evidence, and the questions and holds it is keeping for you. Your orders hold until you revoke them
(`ape orders revoke --by <you>`, from a terminal), and a revocation takes effect at the next act.

**Waking the conductor.** `ape conductor watcher` follows your projects' events. If your conductor runs in a tmux
pane, the watcher types each event into that pane, marked as `ape`'s, and only when the pane still runs the conductor's
Claude Code session, the session is idle and its input line is empty. If you are typing there, it waits. The
watcher records which Claude Code session the pane runs when it starts. If another session takes the pane later, the
watcher stops typing for good and notifies you; you restart it with `--pane`. If no acknowledgement comes within 15
minutes, you are notified. Without tmux you are only notified. A marked message is
never your words: a prompt that carries one anywhere cannot grant an act, whatever else it says.

For a project on another machine, the conductor runs the same commands there over ssh. You accept or revoke that
project's orders in a terminal on that machine.

**The limit to know:** a conductor that has been steered, by instructions planted in something it read, can still use
your orders for anything they allow, in every project that has them. `ape` stops it at the edge of the orders, not
inside them. A stated reason does not change this: a steered conductor can write a convincing one.

The edge of the orders is also only as firm as the person-act check. The first known limit above (a script that
clears the variables) lets a steered model accept orders of its own, or make a person act directly, whatever your
orders say. Until a person act can be confirmed somewhere a model cannot reach, this is how delegation works.

**Meanwhile:**

- Keep the orders narrow: name only the acts you need while you are away, in only the projects you need, and revoke
  them when you are back.
- Read the notifications. A delegated act you would not have made is the sign to revoke the orders and look at what
  the conductor read.
- You are notified whenever orders are accepted or revoked. An acceptance you did not make is the same sign; you can
  also list them: `git log --grep '^apex: orders' --format='%h %an %s'`.
- The never-delegable acts stay with you whatever the orders say.

## Planned: a security review in the loop

A security review of each milestone's work is planned, in this shape:

- It runs **after the milestone's full test run and before its gate**.
- `ape` first runs the mechanical checks it can decide by itself, such as known-vulnerable dependencies and changes to
  CI, hooks or settings. Then a model session reviews the milestone's code for the problems a check cannot see:
  injection, missing authorisation on a new entry point, unsafe defaults, credentials that would ship.
- A security issue it finds **must be handled before the gate and the milestone's close**: either **fixed**, or
  **accepted by you as an exception, with the fix postponed**. That acceptance is a person act, recorded like the
  others.

It will be built only once a measurement shows it helps: projects with planted vulnerabilities, run with and without
it, under rules fixed in advance. Whether it becomes its own skill, a mode of review or an expert plugin is decided
after that measurement.

Before `ape`'s sandboxes run APEX projects end to end, their isolation will get an outside security review.

## Fixed in 1.4.1

The sandbox's credential sync could copy a logged-out credential over your own and every workspace's. `ape` 1.4.1
ranks the copies by the token's expiry and never spreads a logged-out one.

## See also

- [What only a person does](person-acts.md)
- [hooks.md](../reference/hooks.md): what the guard parses and what it does not see
- [bridge-security.md](../reference/bridge-security.md): the session bridge's trust model

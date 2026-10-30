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
- the eight person acts (approving a milestone or tweak, answering a blocked change, accepting a human-review task,
  accepting the config, accepting a red gate, allowing pending human work, accepting an ADR) are refused when a
  model runs them, unless you typed the act's words yourself in that turn
  ([What only a person does](person-acts.md)).

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
git credential helper, a token in the environment. No model in APEX ever needs to push.

**Meanwhile:**

- Protect `main` and your release branches on the git host, and restrict who may create tags. A protected branch
  refuses the push whatever the guard missed.
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

Commits are scanned; `ape`'s logs and the session transcripts under `_apex/state/` are not. They hold whatever a
session read, including environment output and the contents of files with credentials. They are git-ignored and stay
on your machine.

**Meanwhile:**

- Read a log or transcript before attaching it to an issue or sharing it.
- Keep real secrets out of the files a session is likely to read (see the previous limit).

### Plugins are pinned by name and version, not by content

An expert or other plugin you list is installed by its name and version. A plugin's skills, rules and scripts run
with your session's rights, and its text is read as instructions. Nothing checks that the content behind a version
is the content you reviewed.

**Meanwhile:**

- Install plugins only from sources you trust, and pin a version.
- Before moving a plugin to a new version, read what changed.

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

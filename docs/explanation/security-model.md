# Security model and known limits

ape keeps a model to the APEX flow with guards: commands that refuse themselves in a model
session, a hook that reads every tool call before it runs, and records only ape writes. Those
guards hold against a model that follows its instructions. They are not a boundary against a
model that has been steered to get past them, for example by text in a file it read. This page
says where that line is, which gaps are known in ape 1.4.1, and what to do about each until it
is closed. The full threat model is the APEX security proposal of 2026-10-29 (findings SEC-1
to SEC-9); this page states the ones that bear on how you run ape today.

## What the guards are for

Each person act ([What only a person does](person-acts.md)) refuses itself when it runs in a
model session, and in your own Claude Code session the hook grants it only on the phrase you
typed in that turn. Every `git push` and tag write is denied in any form the hook can parse.
ape's records (specs, change state, the archive, `_apex/state/`) cannot be edited with Claude
Code's edit tools, and a write made another way is found by `ape project check` and refused by
the change commands until a person restores or accepts it. Every commit is scanned for secrets.

These make the honest path the only easy one: a model following the APEX skills never meets a
refusal it has to work around. What they do not do is stop a model that is trying to.

## Known limits

### A script can carry out a person act (SEC-1)

ape tells a model from a person by two environment variables, `APE_SESSION` and `CLAUDECODE`,
and the hook does not read script files (see [hooks.md](../reference/hooks.md): it does not see
`"$(…)"` inside double quotes, git aliases or script files). A model that writes a script which
clears both variables and runs, say, `ape milestone approve`, gets past both layers. This was
verified on ape 1.4.0. Nothing in the shipped skills does it; it needs a steered model.

A check of the process's ancestry (refuse when a Claude Code or ape session process is among
its parents) was designed and is not in 1.4.1: it raises the bar but a double fork escapes it,
so it would not be a boundary either. The boundary is a person act confirmed on a channel a
model cannot write to; that is v1.5 design work.

**Meanwhile:** read what the loop did before you rely on it. `ape status` and the milestone's
records show every approval, answer and acceptance with who recorded it and when; an act you did
not make is a reason to stop the loop and look. Do not run the loop on a project whose files you
have not read (a hostile `AGENTS.md`, spec or expert rules file is how a model gets steered).

### Pushes and tags go through the same route (SEC-2)

The push and tag guards are the same hook, so the same script route passes them. Whether a push
then succeeds depends on what the session can reach: your SSH agent and git credential helpers
pass into every session ape starts.

**Meanwhile:** protect the branches and tags that matter on the remote (GitHub branch and tag
protection), so a push from a session meets the remote's own rules. For unattended runs, prefer a
checkout whose remote credentials cannot push, or run the loop where `SSH_AUTH_SOCK` is not set.
No APEX skill ever pushes.

### Sessions inherit your environment (SEC-3)

ape removes Claude Code's own variables from the sessions it starts and passes everything else:
cloud keys, `GITHUB_TOKEN`, `ANTHROPIC_*`, `SSH_AUTH_SOCK`. A session on the host has no egress
control, so a steered model can send any of it out.

**Meanwhile:** start unattended runs (`ape task apex-loop`, review and delegate sessions start
from it) from a shell that holds only what the project needs, not your everyday login shell
with every token exported. An `ape sandbox` workspace runs with its own environment and a
deny-by-default egress proxy; sandboxes are not yet supported end to end for APEX projects.

### Transcripts and logs are not scanned for secrets (SEC-6)

Commits are scanned; session transcripts (under Claude Code's `~/.claude/projects/`) and ape's
logs under `_apex/state/` are not, and they hold whatever a session read: command output, `.env`
files, fixtures. Today they stay on your machine and git ignores them.

**Meanwhile:** treat transcripts and `_apex/state/` as secret-bearing. Do not attach them to
issues or share them without reading them first. (v1.5 plans to share transcripts across a
team; it scans and redacts them first.)

### Expert plugins run with the session's rights (SEC-8)

An expert plugin's skills, rules files and scripts are read and run by the session with
everything the session can do, and its text is read as instructions. ape pins a plugin by its
marketplace ref, not by the content's digest, so a plugin that changes under the same ref is
not noticed.

**Meanwhile:** add only plugins whose source you trust and have read, pin them to a tag or a
commit rather than a branch, and read a plugin's diff before you move its pin
(`ape plugins status` lists what is installed and at which ref).

## Fixed in 1.4.1

**`aped` credential sync (SEC-7).** In `credentials: oauth` mode the sync could copy a
logged-out credential over the host's and every workspace's. 1.4.1 ranks copies by the token's
expiry and never spreads a logged-out one. An outside review of `aped` is planned before
sandboxes run APEX projects end to end.

## See also

- [What only a person does](person-acts.md)
- [hooks.md](../reference/hooks.md): what the pre-tool guard parses and what it does not see
- [bridge-security.md](../reference/bridge-security.md): the session bridge's trust model

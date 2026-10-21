# What only a person does

An APEX v1 milestone runs unattended: the loop plans, applies, verifies, reviews and archives
each change without asking anyone. A few acts stay with a person whatever the loop is doing,
and ape refuses them when a model runs them. This page lists those acts, says why they are a
person's, and explains the two ways a person carries one out: from a plain terminal, or by
typing the act's phrase in their own Claude Code session. The reference for each command is
its `--help` and [cli.md](../reference/cli.md).

## The acts

Eight commands are a person's. The third column is what you type in your own Claude Code
session to grant the act (see [In your own Claude Code session](#in-your-own-claude-code-session)).

| Command | What it decides | Phrase |
| ------- | --------------- | ------ |
| [`ape milestone approve <M> --by <who>`](../reference/cli.md#ape-milestone-approve) | The milestone, its goal and its gate are what the loop works toward. `--accept-green "<reason>"` on a re-approval belongs to the same act. | `approve <M>` |
| [`ape change approve <tweak-id> --by <who>`](../reference/cli.md#ape-change-approve) | A tweak (`milestone: none`) may start. | `approve <tweak-id>` |
| [`ape change answer <change-id> --text "<answer>" --by <who>`](../reference/cli.md#ape-change-answer) | The answer to the question a blocked change stopped on. | `answer <change-id>: <answer>` |
| [`ape change accept <change-id> T<n> --by <who>`](../reference/cli.md#ape-change-accept) | A human-review task is met, with `--evidence <path\|url>`. | `accept T<n> of <change-id>` |
| [`ape project accept-config --by <who>`](../reference/cli.md#ape-project-accept-config) | A change to a guarded block of `_apex/config.yaml` stands. | `accept the config` |
| [`ape milestone close <M> --accept-red "<reason>" --by <who>`](../reference/cli.md#ape-milestone-close) | The milestone is done although its gate is red. | `accept red on <M>` |
| [`ape change archive <change-id> --allow-pending-human`](../reference/cli.md#ape-change-archive) | The change archives with human-review tasks not yet accepted. | `allow pending human on <change-id>` |
| [`ape decision accept <ADR-NNNN> --by <who>`](../reference/cli.md#ape-decision-accept) | A proposed ADR binds the project from now on. | `accept <ADR-NNNN>` |

The same commands without the person's part are not person acts:

- `ape milestone close <M>` without `--accept-red`;
- `ape change archive <id>` without `--allow-pending-human`;
- `ape change accept` with `--delegate` or `--verdict`.

The loop runs those itself.

## Why these are a person's

Each act either sets what the unattended work is held to, or vouches for something no check
proves:

- **Approving a milestone or a tweak** fixes the scope and the gate the loop runs against.
  Approval records the gate as it stands, and gate and close refuse a gate that changed since.
- **Answering a blocked change** decides what the model could not decide. A change blocks with a
  question only a person can answer: a product decision, credentials, an out-of-scope request.
- **Accepting a human-review task**, **allowing pending human work** and **accepting a red gate**
  are judgments a check cannot make: hardware runs, soaks, sign-offs, a release shipped with known
  defects.
- **Accepting the config** lets a loosened limit or check rule stand. Without the guard, a
  session could loosen its own limits with an Edit.
- **Accepting an ADR** binds every later change to it.

If a model could run these, the loop would approve its own scope and accept its own work, and
nothing a person decided would be left in the record. So the acts record who did them:
the `by:` field, the approval and close records in `milestones.json`, and the commit subjects
(`apex: <M> approved`, `apex: <M> done (accepted red)`, `apex: config accepted by <who>`).

## How ape tells a person from a model

ape refuses a person act when it runs in a model session. It knows a model session by two
variables:

- `APE_SESSION`, set in every session ape starts (`ape task`, `ape prompt`, `ape chat`,
  `ape change review --run`);
- `CLAUDECODE`, set in every Bash tool call Claude Code makes.

The refusal is exit 2, code `session.person-only`. A `!` line typed inside Claude Code runs in
that session's environment, so it counts as a model session too.

Two ways through remain, and they are the two ways a person acts.

## From a plain terminal

Run the command in a terminal that is not inside Claude Code, and name yourself with `--by`:

```bash
ape milestone approve M2 --by "$(git config user.name)"
ape change answer add-cache --text "use Redis; keep entries 10 minutes" --by alice
ape change accept add-cache T3 --by alice --evidence docs/review/cache-hit-rate.png
ape milestone close M3 --accept-red "ships with the known RF drift; fixed in M4" --by alice
```

`--by` is required there, except on `ape change archive --allow-pending-human`, which has no
`--by`.

## In your own Claude Code session

In a Claude Code session you started yourself (plain `claude` in the project, usually with
`/apex-orchestrator` open), you can grant an act with your own words. You type the act's
phrase, and the model runs the command in that turn. This is how
[`/apex-orchestrator`](operator-and-loop.md) works: it proposes an act and ends with the exact
words to type, for example: Say `approve M2` to approve it.

What happens:

1. The `UserPromptSubmit` hook, `ape hook user-prompt`, records your prompt under
   `_apex/state/prompts/` (ignored by git).
2. When the model runs one of the eight commands, the `PreToolUse` hook, `ape hook pre-tool`,
   looks for the phrase in the prompt that opened the turn. The prompt must also be your typed
   message in the session's transcript. When both match, the hook writes a grant under
   `_apex/state/grants/`. A grant is used once and expires after 120 seconds. The phrase may be
   its own message (`approve M2`) or a slash command's arguments (`/apex-orchestrator approve
   M2`): Claude Code writes a typed slash command into the transcript as its command name and
   arguments, and ape reads the typed line back from them.
3. The command consumes the grant and records `by: <git config user.name> (prompt <ts>: "<your
   sentence>")`. The quote is at most 160 characters, with secrets redacted.

The phrase grammar is printed by `ape hook user-prompt --help` and `ape hook pre-tool --help`.
The match is strict:

- The phrase's words must appear in order, with the target exact (`M2`, `T3`, the change id,
  `ADR-0012`; case aside). For an answer, the `--text` itself comes after the target.
- No negation word comes before them in their sentence, or among the three words after the
  target: not, no, never, don't (and every n't), wait, hold, stop, cannot, nope. The sentence is
  not a question.
- "yes", "ok", "do it" and "approve it" grant nothing.
- A prompt grants each act and target once.
- Text the model reads in a file, an issue or a tool output never counts. A prompt Claude Code
  generated itself (a task notification) is recorded as a notification and never grants.

The command the model runs must be a literal line: no `$` and no backquote anywhere, and one
person act per command. `--by` is optional there. Given, it must equal `git config user.name`,
else the command fails with `session.by-mismatch` (exit 1).

Some cases never grant:

- **A session ape started.** Under `APE_SESSION` nothing is recorded or granted, so `apex-loop`
  and every other `ape task` session can never carry out a person act.
- **A `!` line.** It fires no hook.
- **A missing hook.** Without `ape hook user-prompt` registered in `.claude/settings.json`, every
  person act from Claude Code is refused. `ape project check` lists the notice
  `hooks.prompt-absent`, `ape doctor` the row `hooks.prompt`, and `ape project init` merges the
  hook back.

A grant counts only in the session it was issued for: its session id must be the command's
`CLAUDE_CODE_SESSION_ID`. It also counts only while your prompt record and the transcript
message behind it still match.

Three acts are long runs: approve (which runs the gate), `--accept-red` and
`--allow-pending-human`. Running the same command line again while it runs (exit 75), or before
its result is printed, attaches to the run with no new words. Once its result is printed, the
same command is a new act and needs your words again.

What this does not protect against: a script the model writes and runs as your OS user can forge
the prompt record, the transcript line and the grant together. The typed phrase attests your
words; it is not a sandbox, and the pre-tool hook sees direct calls only.

## The guard overrides

The pre-tool hook denies `git tag` writes, every `git push`, and `pkill`/`killall` in the
sessions it guards. Three variables lift those denies:

- `APE_ALLOW_TAG=1` allows tag writes;
- `APE_ALLOW_PUSH=1` allows `git push` (a tag push needs both);
- `APE_ALLOW_KILL=1` allows `pkill`, `killall`, `killall5` and `skill`.

Each counts only in the environment the person launches the session with, which the hook
inherits:

```bash
APE_ALLOW_PUSH=1 claude
APE_ALLOW_PUSH=1 ape task <skill> --args <id>
```

The same variable set inside a command the model runs does not count. `APE_ALLOW_PUSH=1 git
push`, `env APE_ALLOW_PUSH=1 …` and `export APE_ALLOW_PUSH=1; …` change the command's own
environment, never the hook's, and the hook judges the command as if they were not there. The
hook also denies edits to `.claude/settings.json` and `.claude/settings.local.json`, where the
hooks and the session environment live. [Hooks](../reference/hooks.md) has every guard.

## Accepting the config

Fifteen blocks of `_apex/config.yaml` can loosen what ape enforces, and are guarded:

- `limits`, `checks`, `retries`, `requires`, `timeouts`, `caps`;
- `regression`, `commit`, `evidence`;
- `marketplaces`, `plugins`, `tools`;
- `human_review`, `release`, `secrets`.

Since the project's v1 epoch (the newest `apex: project init` or `apex: lift` commit), a commit
that changes one of them and is not ape's own (`apex: …`) is the finding `config.changed`
(action person). `ape change start`, `verify` and `archive` then refuse with
`change.provenance`. The guard compares whole blocks and is blind to direction: raising a cap or
a timeout loosens as surely as lowering one.

A person clears the finding in one of two ways:

- **Accept it.** `ape project accept-config --by <who>` commits an empty
  `apex: config accepted by <who>` on the base branch, and every guarded change before it is
  clean. A later change is judged again. It is refused on a change branch and with staged changes.
- **Revert it.** `git revert <sha>` of the change clears it too.

### What the guard reads

The guard reads git history. Who made a commit is told by its subject, and the project's v1
epoch is the newest `apex: project init` or `apex: lift` commit. A commit whose subject starts
`apex:` counts as ape's own, and an `apex: config accepted` commit counts as a person's
acceptance. So only ape may write such a subject, and three rules keep it that way:

- **No session commits one.** The pre-tool hook denies a commit whose subject would start
  `apex:`, however the message is given: `git commit -m`, `-F`, `-C`/`-c`, `--amend` keeping such
  a subject, `-t`, `git merge -m`, a cherry-pick of such a commit (so an old acceptance cannot be
  copied), `git commit-tree` and `git am`. ape's own commits never go through a session's Bash
  call, so there is no override. See [Hooks](../reference/hooks.md#bash-commands-it-denies).
- **Re-running init from a session starts no new epoch.** In a model session, on a project that
  is already v1, `ape project init` repairs but commits nothing (`commit_withheld`). The files stay
  for the session's own commit, under its own subject.
- **No change archives as one.** A change whose squash subject would start `apex:` (its title,
  under the default template with no `type:`) is refused at `change check`, `start` and
  `archive`, and by `project check` (`change.subject-reserved`).

What the hook cannot see is the limit of every Bash guard: a script or program the session writes
and runs, a git alias, a command substitution ape cannot read, a `rebase --exec` string, and
history plumbing (`git fast-import`, `filter-branch`, `replace`). A session that writes a
program to forge a commit is past any guard that parses commands. What catches that is the
change's review, which reads the change's diff, and a person reading the project's history.

Two blocks depend on that acceptance directly:

- **`secrets.allow`.** An entry lets a value through the commit secret scan only once a person
  has committed it and accepted the config.
- **`human_review`.** `ape change accept --delegate` refuses (`delegate.unaccepted-config`)
  while the block is a change nobody accepted, so a model cannot add a delegate and accept its own
  work.

The unguarded keys are `project_name`, `development_folder`, `models`, `effort`, `docs` and
`notify`. [`_apex/config.yaml`](../reference/config-yaml.md) has every key.

## What a delegate may do instead

A project can hand human-review tasks to a skill through the `human_review:` block: by default
`apex-accept`, in its own read-only session. A pass is recorded as `delegate:<skill>` with the
run's id, never as a person. Without the block, only a person accepts a task, with one default-on
exception: the docs check of a milestone's docs change.
[The operator and the loop](operator-and-loop.md#human-review-and-delegates) explains it.

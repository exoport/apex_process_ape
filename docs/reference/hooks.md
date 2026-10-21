# Hooks

ape enforces part of APEX v1 through hooks: four Claude Code hook handlers that guard every
session in the project, and git's pre-commit hook. This page lists what `ape project init`
registers, what each handler decides, and the `ape doctor` and `ape project check` rows that
report a missing or altered hook. Each handler's full text is in `ape hook <name> --help` and
[cli.md](cli.md#ape-hook).

## What `ape project init` registers

`ape project init` merges these entries into `.claude/settings.json`, keeping every other key and
hook:

| Event | Matcher | Command | Timeout |
| ----- | ------- | ------- | ------- |
| `SessionStart` | none | `ape hook session-start` | Claude Code's default |
| `Stop` | none | `ape hook stop` | 1800 s |
| `SubagentStop` | none | `ape hook stop` | 1800 s |
| `PreToolUse` | `Edit\|Write\|MultiEdit\|NotebookEdit\|Bash` | `ape hook pre-tool` | Claude Code's default |
| `UserPromptSubmit` | none | `ape hook user-prompt` | Claude Code's default |

Each is a `type: command` hook. An entry that was altered is removed and written again as init
writes it. Altered means one of:

- another matcher;
- a type other than `command`;
- `async` (an async hook cannot block);
- a stop timeout below 1800 s.

An intact entry is left alone, so running init again changes nothing. `ape project
operator-interface` uses the same merge.

`.claude/settings.json` is the team's file: init writes the `.claude/` shape into the root
`.gitignore` (`.claude/*`, `!.claude/settings.json`, `!.claude/skills/`, `CLAUDE.local.md`), so the
hooks reach every checkout.

**Inline copies.** A session `ape task` launches in a v1 project also gets `session-start`, `stop`
and `pre-tool` in its `--settings` (with `--inline`), so a hand-edited `settings.json` cannot drop
them. The `settings.json` copy then does nothing, and each event is decided once. `user-prompt` is
never inline: in a session ape launched it records nothing.

**A pin that cannot run.** When the project's ape pin (`.bingo/ape.mod`) cannot run, `pre-tool`
exits 2 with the pin's message, which Claude Code reads as a deny: the guard fails closed. That
covers a pin below the floor, and one that is not installed. `stop` and `session-start` exit 13,
which blocks nothing. `APE_TOOLCHAIN=local` in the session's environment runs the ape on `PATH`
regardless.

Every Claude Code hook exits 0 otherwise. A block or a deny is in its JSON output.

## Git's pre-commit hook

`ape project init` (and `ape project lift`) installs `<hooks dir>/pre-commit` when no pre-commit
hook is there. The hooks dir is what `git rev-parse --git-path hooks` names: `core.hooksPath` when
set, else `.git/hooks`. The script is a POSIX sh script marked by the line
`# ape: pre-commit secret scan`:

- it exits 0 at once when `APE_SCANNED=1`;
- it refuses the commit when `ape` is not on `PATH`;
- otherwise it runs `ape hook pre-commit`.

A pre-commit hook that is not ape's is never edited. `ape project check` then lists the notice
`hooks.pre-commit-absent`, naming the line to add to it: `ape hook pre-commit || exit 1`. git
never versions `.git/hooks`, so a fresh clone has no hook until `ape project init` runs again. Init
is idempotent and installs only what is missing.

## `ape hook session-start`

Adds `ape status --brief` (at most 25 lines) to the session's context as `additionalContext`.
Outside a v1 project it prints nothing.

When the session's shell is zsh (`$CLAUDE_CODE_SHELL`, else `$SHELL`), a paragraph follows the
brief: quote globs and variable expansions, never start an unquoted word with `=`, and take file
names from `find`, never `ls`.

## `ape hook pre-tool`

Runs before every `Edit`, `Write`, `MultiEdit`, `NotebookEdit` and `Bash` call, and denies with a
`permissionDecision: deny` and a reason. Anything it does not deny gets no output.

### Edits it denies

| Target | Why |
| ------ | --- |
| `development/specs/**` | Specs change only through `ape change archive`: a change puts the requirement in its `## Delta` |
| `development/changes/*/state.json` | Only ape writes a change's state |
| `development/archive/**` | Only `ape change archive` writes the archive |
| `_apex/state/**` | ape's own records: gate runs, logs, hook verdicts |
| A file that already exists under `development/briefs/**` | A brief is a plan's dated input. A changed request is a new dated brief, `development/briefs/YYYY-MM-DD-<slug>.md`, and writing a new file there is allowed |
| `.claude/settings.json`, `.claude/settings.local.json` | They hold ape's hooks and the session's environment: a person edits them |

Paths are resolved through symlinked folders, so a link into `specs/` is still `specs/`. The guard
sees the edit tools only. A Bash write to these paths is not seen here; `ape project check` reports
it from history (`spec.edited`, `state.forged`, `brief.edited`).

Two kinds of session are read-only, and the guard confines them:

- a **review session** (`ape change review --run`) may write only
  `development/changes/<id>/review.md` in the project;
- a **delegate session** (`ape change accept --delegate`) may write only
  `development/changes/<id>/assets/accept-*` notes.

Outside the project, both may write anything, such as a throwaway copy to break.

### While a check runs

ape runs checks during verify, a gate, a baseline, or a Stop hook's re-run. After each check it
undoes what the check left in the tree outside `development/`: files it created are removed, and
tracked files it changed are restored. A copy of each undone file, as it was when the check ended,
is kept under `_apex/state/strays/<timestamp>-<pid>-<hex>/`. The verify and gate results name the
undone paths (`stray_files`) and the folders holding the copies (`strays_kept`). Files that were
already dirty before the check are never touched.

git's view cannot tell a check's litter from another writer's edit made while it ran. So while a
check runs in the checkout, the hook denies an `Edit` or `Write` outside `development/` and
`_apex/state/`, naming the check, its run and its pid. An edit made from a shell or an editor in
that window is not seen by the hook: it may be undone, and its copy is under `_apex/state/strays/`.

The check's marker, `_apex/state/runs/checking-*.json`, counts only while the process that wrote it
lives (its pid and start time), so a check that crashed never blocks an edit.

### Bash commands it denies

| Command | Override |
| ------- | -------- |
| A git tag write: `git tag <name>` (with `-a`, `-s`, `-f`, `-m`, …), `git tag -d`; `git push --tags`, `--follow-tags`, `--mirror`; a push of a `refs/tags/…` ref, `tag <name>`, or a name the repository has as a tag and not as a branch; `git push --delete` of one | `APE_ALLOW_TAG=1` |
| Every `git push`, in any form: a remote, a refspec, `--dry-run`, `git -C`, `sh -c`, a chain | `APE_ALLOW_PUSH=1` (a tag push needs both) |
| `pkill`, `killall`, `killall5` and `skill`, in any form: `sudo`, `env`, `timeout`, `xargs`, `sh -c`, a chain, an absolute path | `APE_ALLOW_KILL=1` |
| `git rebase` and `git merge` while the repository has a change branch, `change/<id>`, checked out | none |
| A command that names `_apex/state/prompts` or `_apex/state/grants`, or runs `ape hook user-prompt`, `ape hook pre-tool` or `ape __run` | none |

Some commands stay allowed:

- listing tags: `git tag`, `-l`/`--list`, `-n`, `--contains`, `--merged`, `--points-at`, `--sort`,
  `--format`, `-v`;
- `kill <pid>`;
- `rebase --abort`/`--quit` and `merge --abort`/`--quit`, `git merge-base`, `merge-file` and
  `merge-tree`;
- a rebase or merge on any other branch.

ape catches a change up with a moved base itself: `ape change start|verify|archive <id>` rebase it.

The command is parsed, not grepped:

- **What it follows:** quotes; `&&`, `||`, `;` and `|` chains; subshells; `NAME=value` prefixes;
  `env`, `sudo` and `xargs` wrappers; `git -C` and `-c`; and `sh -c` and `eval` scripts. So
  `echo "git tag v1"` is not flagged.
- **What it does not see:** `"$(…)"` inside double quotes, git aliases and script files.

A command without `git` passes the tag, push and rebase rules at once.

**Overrides** count only from the environment the person launches the session with
(`APE_ALLOW_PUSH=1 claude`, `APE_ALLOW_PUSH=1 ape task …`), which the hook inherits. Set inside the
model's own command (`APE_ALLOW_PUSH=1 git push`, `env …`, `export …`), they do not count. See
[What only a person does](../explanation/person-acts.md#the-guard-overrides).

### Person acts

On a Bash command that runs one of the eight person acts, the hook grants it only on the person's
own words: the phrase typed in the prompt that opened the turn, as `ape hook user-prompt` recorded
it. A grant is a single-use file under `_apex/state/grants/`, valid for 120 s. Without one the hook
denies the command and says why. The phrase grammar and the matching rules are in
[What only a person does](../explanation/person-acts.md#in-your-own-claude-code-session). In a
session ape launched (`APE_SESSION`), nothing is granted.

## `ape hook stop`

Registered for `Stop` and `SubagentStop`. What it does depends on who started the session.

**In a session ape launched** (its process carries `APE_SESSION`), it blocks the stop with
`{"decision": "block", "reason": …}` in two cases:

1. **Background work.** On the main session's Stop (not a SubagentStop), it blocks while the
   payload's `background_tasks` lists anything: a backgrounded shell, a monitor, any type, on any
   branch. The reason starts "background work is still running", names each task, and tells the
   model to wait for it in this turn, and next time to run a long command in the foreground with a
   600000 ms timeout.
2. **Red checks on an in-progress change.** When HEAD is on `change/<id>` and that change is
   in-progress, it re-runs the checks that were red at the change's last verify of its current plan
   (recorded in `_apex/state/logs/<id>/last-verify.json`; before any verify, the task checks). It
   blocks when any of them still exits non-zero, with the failing checks' last lines.

It never runs a long check:

- A check whose last recorded run took over 60 s (or `timeouts.check.max`, if lower) is not re-run.
- No check is re-run once one hook run has spent 600 s.
- Such a check is judged from the last verify at HEAD: red stays red.
- When the work moved since that verify, the stop is blocked with the instruction to run
  `ape change verify <id> --wait 9m`.

It allows the stop, printing nothing, in these cases:

- HEAD is on no change branch;
- the change is not in-progress;
- the last verify had nothing red, or every re-run check is green;
- it has blocked 3 times in a row. Background blocks count toward the same 3, and the count resets
  when the payload's `stop_hook_active` is false.

**In a person's own Claude Code session** it never blocks and runs no check. On an in-progress
change whose last verify had red checks, or with background work still running, it allows the stop
with a `systemMessage` shown to the person ("APEX: change <id> is in progress, and at its last
verify … check(s) were red: …").

Each verdict is written to `_apex/state/hooks/<session>[-<agent>].json`, and the main session's to
`<session>.jsonl` beside it. `ape task` reads that log to learn whether a Stop ended the turn.
`ape change review --run` and `accept --delegate` read it to learn whether a session ended with
background work still running.

## `ape hook user-prompt`

Registered for `UserPromptSubmit`. It appends each prompt the person types (at most 64 KiB) to
`_apex/state/prompts/<session_id>.jsonl`, ignored by git. A record has `ts`, `session_id`,
`transcript_path`, `prompt`, `prompt_id` and `kind`. This record is what `pre-tool` grants a person
act on.

A prompt Claude Code generated (a `<task-notification>`, a `<system-reminder>`, tags only) is
recorded as kind `notification` and never grants. Nothing is recorded in a session ape launched, or
outside a v1 project. The hook never blocks a prompt: it prints nothing and exits 0.

## `ape hook pre-commit`

git's pre-commit hook, not Claude Code's: its exit code is the verdict. It runs the commit secret
scan, the same scan every commit ape makes goes through:

- **What it scans:** the added lines of what git is about to commit, from the index git hands its
  hooks, so `git commit -a` and `git commit <paths>` are scanned as they commit.
- **The rules:** betterleaks' default rules plus ape's `aws-access-key-id`.
- **What it allows:** the `secrets.allow` fingerprints in `_apex/config.yaml`, read at the guard's
  baseline, so an entry counts only once a person has committed and accepted it (see
  [`_apex/config.yaml`](config-yaml.md#secrets)). Inline `betterleaks:allow`/`gitleaks:allow`
  markers and `.betterleaksignore` are not honoured.
- **What it prints:** each hit as `<file>:<line> rule <id>: <redacted>`, with the placeholder fix
  first and the allow entry a person may paste. The files stay staged.

| Exit | Meaning |
| ---- | ------- |
| 0 | Clean (prints nothing), not a v1 project, or `APE_SCANNED=1` (ape's own commits that run hooks, such as the archive squash, already scanned) |
| 1 | A secret (`commit.secret`), or the scan could not run (`commit.failed`): git commits nothing |

`git commit --no-verify` skips the hook. That is a person's escape: the archive's scan of the
squash stays the backstop.

## Rows that check the hooks

| Row or code | Where | What it reports |
| ----------- | ----- | --------------- |
| `hooks.project` | `ape doctor` | ape's hooks missing from, or altered in, `.claude/settings.json`; `ape project init` restores them |
| `hooks.prompt` | `ape doctor` (WARN) | `ape hook user-prompt` missing or altered: a person act from Claude Code is then refused |
| `hooks.pre-commit` | `ape doctor` (WARN) | git's pre-commit hook does not run ape's scan: absent (a fresh clone), another tool's hook without the line, or not executable |
| `settings.shared` | `ape doctor` | `.claude/settings.json` or `.claude/skills/` ignored by git, so the hooks or skills reach no other checkout |
| `hooks.contract_drift` | `ape doctor --only hooks.contract_drift --cwd <project>` | Whether Claude Code still sends the hook fields ape reads, judged from the project's runlogs under `_apex/state/runs` |
| `hooks.missing`, `hooks.altered` | `ape project check` (findings) | The same as `hooks.project` |
| `hooks.prompt-absent` | `ape project check` (notice) | The same as `hooks.prompt` |
| `hooks.pre-commit-absent` | `ape project check` (notice) | The same as `hooks.pre-commit`, with the line to add |
| `settings.ignored` | `ape project check` (finding) | The same as `settings.shared` |

`ape project codes` explains every code.

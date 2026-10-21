# Reference — step contract

An `ape task <skill>` run is one step in one Claude Code session. This page is what ape types into that session's REPL, what it checks about the line the session received, and when it counts the step as done. The same contract holds for every session the `ape change` verbs and an `apex-loop` run start, because those are `ape task` runs too ([claude-spawn-modes.md](claude-spawn-modes.md)).

ape spawns `claude` inside an in-process PTY (`internal/repl/`) and delivers the step as keystrokes written to the PTY master. The bridge reports the session's `UserPromptSubmit` and `Stop` hooks back to ape; a `ContractVerifier` (`internal/bridge/orchestrator/contract.go`) checks the submitted line.

## What `ape task` does

1. **Spawns `claude` and waits for the REPL.** The session is launched with its model (`--model`) and, in an APEX v1 project, its effort (`--effort`), and ape waits up to 30 seconds for the REPL to be ready. A REPL that never comes up ends the run with termination `repl_not_ready` and exit 3. ape sends no `/model` later: the session runs on the model it was launched with.

2. **Types the skill line** (`assembleInteractivePromptLine`, `internal/pipeline/interactive.go`):
   - without `--agent`: `/<skill> --autonomous`, then the `--args` words;
   - with `--agent`: `/<agent> --autonomous -- <skill> --autonomous`, then the `--args` words;
   - with `--prompt-flag` and a prompt (`--prompt`, `--prompt-file` or `--handoff`): the flag and the text at the end.

   `--no-commit` is typed only when you pass `--no-commit`, in front of the args. APEX v1 skills commit their own work, so nothing tells them not to by default.

3. **Moves the arguments to a file when typing would change them.** When the line is longer than 700 characters, or the `--args` or prompt text holds two or more spaces in a row, a tab, a line break or any other control character, ape writes that text verbatim to `stages/01-<skill>/step-01-<skill>.args.md` in the run directory. It then types the skill part of the line (with `--no-commit`, when given) followed by "The arguments for this run are in `<absolute path>`: read that file and treat its contents as if they were written here." The step's `step-start` event records the file as `args_file`. The reason: Claude Code turns a typed burst past 800 characters into a `[Pasted text #N]` placeholder that never runs as a skill, and the typed line splits `--args` on whitespace, so a line break would submit early.

4. **Submits the line.** ape presses Enter once claude has drawn the line, then waits for the `UserPromptSubmit` hook, pressing Enter up to twice more. A line still not submitted ends the run with termination `prompt_not_submitted` and exit 1.

## What the verifier checks

- ape calls `BeginStep` right before typing the line.
- The first `UserPromptSubmit` after it whose prompt starts with `/` must have the shape above:
  - without `--agent` and without `--no-commit`: exactly `/<skill> --autonomous`, or that followed by a space, so `/apex-plan` never matches `/apex-planner`;
  - with `--agent`: it starts with `/<agent> --autonomous -- <skill> --autonomous`.
- A prompt that does not start with `/` is ignored: a key press that dismissed a modal before the line, for example the trust dialog's accept, can reach the hook inside the window.
- Once the line matched, later `UserPromptSubmit` events of the same step are accepted (a skill may prompt itself).
- `EndStep` clears the contract after the step's wait returns, so a late hook cannot match a new one.

## Failure mode

On the first mismatch the verifier:

1. prints `❌ assertion:step-contract — stage="<skill>" step=<n>: expected skill prompt for "<skill>" (agent="<agent>"), got "<prompt>"` to stderr (the prompt cut at 80 characters);
2. cancels the run;
3. checks nothing further for that step.

The command exits non-zero, and the run manifest records the run as not completed.

## When the step is done

The step ends on the session's `Stop` hook, but a `Stop` is not taken on trust:

- the background-task gates decide whether that `Stop` completes the step, defers it, or fails it;
- in an APEX v1 project, `ape hook stop` may block the same `Stop`, and then the session continues instead of the run ending.

Both are in [step-completion-gates.md](../explanation/step-completion-gates.md). The idle timeout (`--idle-timeout`, default 60m) and `--max-duration` are the backstops; see [tune-long-running-steps.md](../how-to/tune-long-running-steps.md).

APEX v0 projects run the v0 ape their `.bingo` pins, whose pipelines chained several steps in one session with `/clear` between them; its docs are at https://github.com/exoport/apex_process_ape/tree/v0.10.0/docs.

## Related

- [claude-spawn-modes.md](claude-spawn-modes.md): every command that starts a session.
- [pipeline-run-manifest.md](pipeline-run-manifest.md): the run manifest, including `termination`.
- [bridge-ipc.md](bridge-ipc.md): wire schema for the MCP bridge.
- [../explanation/why-pty-only.md](../explanation/why-pty-only.md): why the REPL is typed into rather than called with `-p`.

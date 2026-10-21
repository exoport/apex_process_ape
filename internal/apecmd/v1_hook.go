package apecmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

// The hooks `ape project init` registers. Each reads Claude Code's hook
// payload on stdin and answers on stdout. A hook never fails the session
// it serves: a payload it cannot read, or a project that is not v1, is an
// allow with a note on stderr — but a person act is granted only on a
// recorded prompt, so a failing hook leaves it refused (E2 fails closed). pre-commit is git's (Q89): no payload, and
// its exit code is the verdict.

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Claude Code and git hooks for APEX v1 projects (installed by `ape project init`)",
		Long: `Claude Code hook handlers, and git's pre-commit hook. Each Claude Code hook reads
the hook payload (JSON) on stdin.

  session-start   SessionStart: adds ` + "`ape status --brief`" + ` as context
  stop            Stop and SubagentStop: in a session ape launched, blocks the main
                  session's stop while background work runs, and a stop while the
                  in-progress change has a red command check (at most 3 times in a row);
                  in a person's own session, only shows what was red or still running
  pre-tool        PreToolUse (Edit|Write|MultiEdit|NotebookEdit|Bash): denies edits to
                  development/specs/**, changes/*/state.json, archive/**, _apex/state/**,
                  an existing development/briefs/** file, and Bash commands that create,
                  delete or push a git tag, push at all, stop processes by name (pkill,
                  killall), or rebase, merge or cherry-pick on a change branch; while ape runs a
                  check in the checkout, denies an edit outside development/ that its
                  cleanup would undo; grants a person act on the person's own words
                  (E2), and denies one without them
  user-prompt     UserPromptSubmit: records the prompt a person typed, which is what
                  grants a person act (E2); never blocks the prompt

  pre-commit      git's pre-commit hook (installed by ` + "`ape project init`" + ` and the lift):
                  the commit secret scan on the staged added lines; exit 1 refuses
                  the commit (commit.secret)

Every Claude Code hook exits 0; a block or deny is expressed in its JSON output.
When the project's ape pin cannot run (.bingo/ape.mod; exit 13 for other verbs),
pre-tool exits 2 with the same message, so Claude Code blocks the call: the guard
fails closed. stop and session-start keep 13, which blocks nothing.
pre-commit is git's, not Claude Code's: its exit code is the verdict.

In a session ape task launches in a v1 project, session-start, stop and pre-tool also
come inline in ape's --settings (with --inline), so a hand-edited settings.json cannot
drop them; the settings.json copy then does nothing, so each event is decided once.
user-prompt is never inline: in a session ape launched it records nothing.`,
	}
	cmd.AddCommand(newHookSessionStartCmd(), newHookStopCmd(), newHookPreToolCmd(), newHookUserPromptCmd(), newHookPreCommitCmd())
	return cmd
}

// shadowedHook reports whether this hook run is the .claude/settings.json
// copy of a hook `ape task` already runs inline (apexv1.InlineHooks): the
// session carries apexv1.EnvInlineHooks and this run lacks --inline. It
// does nothing then, so exactly one copy acts on each event — whether or
// not Claude Code deduplicates them.
func shadowedHook(inline bool) bool {
	return !inline && os.Getenv(apexv1.EnvInlineHooks) != ""
}

// addInlineFlag registers the hidden --inline flag ape task's injected
// hooks carry.
func addInlineFlag(cmd *cobra.Command, inline *bool) {
	cmd.Flags().BoolVar(inline, "inline", false, "this run is the copy ape task injected (the settings.json copy then does nothing)")
	_ = cmd.Flags().MarkHidden("inline")
}

// hookPayloadMax bounds what a hook reads from stdin.
const hookPayloadMax = 4 << 20

// emitHook writes a hook's JSON answer; nil is an allow and writes
// nothing. An answer that could not be built is an allow too: a hook
// never fails the session it serves.
//
// Curried so a call reads emitHook(cmd)(decision.Output()).
func emitHook(cmd *cobra.Command) func(out []byte, err error) {
	return func(out []byte, err error) {
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "ape hook: %v; allowing\n", err)
			return
		}
		if out != nil {
			_, _ = cmd.OutOrStdout().Write(append(out, '\n'))
		}
	}
}

func readHookPayload(cmd *cobra.Command) apexv1.HookPayload {
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), hookPayloadMax))
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "ape hook: reading stdin: %v\n", err)
	}
	p, err := apexv1.ParseHookPayload(data)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "ape hook: payload is not JSON (%v); allowing\n", err)
	}
	return p
}

func hookDir(cwd string, payload apexv1.HookPayload) string {
	if cwd != "" {
		return cwd
	}
	if payload.Cwd != "" {
		return payload.Cwd
	}
	wd, _ := os.Getwd()
	return wd
}

func newHookSessionStartCmd() *cobra.Command {
	var cwd string
	var inline bool
	cmd := &cobra.Command{
		Use:   "session-start",
		Short: "SessionStart hook: inject `ape status --brief` as additional context",
		Long: `SessionStart hook. Prints {"hookSpecificOutput":{"hookEventName":"SessionStart",
"additionalContext":"<ape status --brief>"}}. Prints nothing outside a v1 project.

When the session's shell is zsh ($CLAUDE_CODE_SHELL when set, else $SHELL; Claude Code
runs the model's Bash commands in it), a paragraph follows the brief after a blank line:
quote globs and variable expansions, never an unquoted word starting with =, file names
from find, never from ls (apex-v0's operating rules said it; the lift drops them). ape's own
checks run under sh -c and need none of it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readHookPayload(cmd)
			if shadowedHook(inline) {
				return nil
			}
			if p, err := apexv1.Open(apexv1.FindRoot(hookDir(cwd, payload))); err == nil && p.IsV1() {
				emitHook(cmd)(apexv1.SessionStartOutput(apexv1.SessionStartContext(p.Status().Brief(), apexv1.SessionShellIsZsh(os.Getenv))))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "project directory (default: the payload's cwd)")
	addInlineFlag(cmd, &inline)
	return cmd
}

func newHookStopCmd() *cobra.Command {
	var cwd string
	var inline bool
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop/SubagentStop hook: block a stop while background work runs or the in-progress change's checks are red",
		Long: `Stop and SubagentStop hook.

Background work (D88), first: in a session ape launched, the main session's Stop (not a
SubagentStop) is blocked while the payload's background_tasks lists anything — every
type, a backgrounded shell and a monitor included — on any branch, so in review,
delegate and tool sessions too. The reason starts "background work is still running",
names each task, and tells the model to wait for it in this turn, never to end the turn
while it runs, and next time to run a long command in the foreground with a 600000 ms
timeout. It counts toward the same 3 consecutive blocks; once they are spent the stop is
allowed. The tasks are recorded under "background" in the verdict, whatever it is.

Checks: when HEAD is on a change's branch, change/<id>, that
change is in-progress (read from the working tree), and any of the checks it re-runs
exits non-zero now, prints {"decision":"block","reason":"<the failing checks' last lines
and what to do>"}. It re-runs only the checks that were red at the change's last verify
of its current plan (the regression command too, when it was newly red), recorded in
_apex/state/logs/<id>/last-verify.json; before any verify, the task checks (H10).
Re-run budget (D58 2.5): a check whose last recorded run took longer than 60s (or
timeouts.check.max, if lower) is never re-run, nor any once one hook run has spent 600s;
a check with no recorded run is re-run under that budget, and one that outruns it is not
red. Such a check is judged from the last verify at HEAD — red stays red, with its last
lines — or, when the work moved since that verify, the stop is blocked telling the model to
run ` + "`ape change verify <id> --wait 9m`" + `. The hook's timeout stays 1800s: it never runs a
long check, the verify does.
Allows (prints nothing) when HEAD is on no change branch (the base branch: planning,
milestones), when the change is not in-progress (blocked, verified, reviewed), when the
last verify had nothing red, when every re-run check is green, or after 3 consecutive
blocks (the count resets when the payload's stop_hook_active is false).

Only in a session ape launched (H13): the hook blocks when its process carries
APE_SESSION, which every session ape spawns has and Claude Code hands to its hooks. In a
person's own Claude Code session (plain ` + "`claude`" + `, the settings.json copy of the hook) it
never blocks and runs no check: on an in-progress change whose last verify had red
checks it allows the stop with {"systemMessage":"APEX: change <id> is in progress, and at
its last verify … check(s) were red: …"}, shown to the person, and with background work
running it says so the same way ("APEX: the turn ended with background work still
running: …"); otherwise it prints nothing.

The verdict is also written to _apex/state/hooks/<session>[-<agent>].json, and the
main session's to the log <session>.jsonl, which is how ` + "`ape task`" + ` learns that a
Stop it saw did not end the turn (it takes one verdict per Stop, in order), and how
` + "`ape change review --run`" + ` and ` + "`accept --delegate`" + ` learn that a session ended with
live background work.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readHookPayload(cmd)
			if shadowedHook(inline) {
				return nil
			}
			if cwd != "" {
				payload.Cwd = cwd
			} else if payload.Cwd == "" {
				payload.Cwd = hookDir("", payload)
			}
			emitHook(cmd)(apexv1.StopHook(cmd.Context(), payload, f.runOptions()).Output())
			return nil
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "project directory (default: the payload's cwd)")
	addV1TimeoutFlag(cmd, &f)
	addInlineFlag(cmd, &inline)
	return cmd
}

func newHookPreToolCmd() *cobra.Command {
	var cwd string
	var inline bool
	cmd := &cobra.Command{
		Use:   "pre-tool",
		Short: "PreToolUse hook: deny edits to specs, state.json, the archive, _apex/state and briefs, git tag writes, git push, pkill/killall, and rebase/merge/cherry-pick on a change branch",
		Long: `PreToolUse hook for Edit|Write|MultiEdit|NotebookEdit|Bash. Denies, with
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
"permissionDecisionReason":"… specs change only through ape change archive"}}, an edit
whose target is under development/specs/, is development/changes/*/state.json, is under
development/archive/, or is under _apex/state/, and an edit to .claude/settings.json or
.claude/settings.local.json (ape's hooks and the session's environment are a person's;
ape project init restores the hooks).

A running check (RK-4b, Q144): while ape runs a check in this checkout (a verify, a gate,
a baseline, a Stop hook's re-run), denies an edit outside development/ and _apex/state/,
naming the check, its run and pid: when the check ends ape undoes what changed in the
tree while it ran. The check's marker (_apex/state/runs/checking-*.json) counts only while
the process that wrote it lives — its pid and that process's start time — so a check that
crashed never blocks an edit.

Briefs: denies an edit to a file that already exists under development/briefs/ (any
depth) — a brief is a plan's input, dated and verbatim, and is history: a new or
changed request is a new dated brief (development/briefs/YYYY-MM-DD-<slug>.md), linked
from the roadmap's Delivery: line. Writing a new file there is allowed. Symlinked
folders and links to a brief are resolved. A Bash write is not seen here;
` + "`ape project check`" + ` reports it (brief.edited).

Bash: denies a command that writes a git tag — only a person tags or releases:
  git tag <name> (-a, -s, -f, -m …), git tag -d|--delete
  git push --tags | --follow-tags | --mirror
  git push <remote> <ref> where <ref> is refs/tags/… (either side, :refs/tags/x),
    "tag <name>", or a name the repository has as a tag and not as a branch;
    git push --delete|-d <remote> <name> likewise
Listing stays allowed: git tag, -l/--list [<pattern>], -n, --contains, --merged,
--points-at, --sort, --format, -v. The command is parsed: quotes, && || ; | chains,
subshells, NAME=value prefixes, env/sudo/xargs wrappers, git -C/-c, and sh -c / eval
scripts are followed, so ` + "`echo \"git tag v1\"`" + ` is not flagged. Not seen: "$(…)"
inside double quotes, git aliases, script files. A command without "git" is allowed
at once.

Bash, too (D45): denies every git push, in any form — a remote, a refspec, --dry-run,
git -C, sh -c, a chain: only a person pushes (with push.followTags set, even a branch
push publishes every reachable unpushed tag). A tag push needs both overrides below.

Bash, too (D46): denies pkill, killall, killall5 and skill in any form (sudo, env,
timeout, xargs, sh -c, a chain, an absolute path): they stop processes the session did
not start. The deny says: stop the process you started by its PID (kill <pid>), which
stays allowed.

Overrides: APE_ALLOW_TAG=1 allows tag writes, APE_ALLOW_PUSH=1 git push,
APE_ALLOW_KILL=1 pkill/killall. Each counts ONLY in the environment the person launches
the session with (` + "`APE_ALLOW_PUSH=1 claude`" + `, or ` + "`APE_ALLOW_PUSH=1 ape task …`" + `),
which the hook inherits. Set inside a command the model runs — APE_ALLOW_PUSH=1 git
push, env APE_ALLOW_PUSH=1 …, export APE_ALLOW_PUSH=1; … — it does not count: the
command is denied as if it were not there (D47 Q6).

Bash, too (trial decision D40, the model never rebases): denies git rebase, git merge and
git cherry-pick while the repository the command runs in has a change branch, change/<id>, checked out —
ape catches a change up with its moved base itself (` + "`ape change start|verify|archive <id>`" + `
rebase it and re-take the baseline at the base tip). Allowed: rebase/merge/cherry-pick
--abort and --quit, git merge-base, merge-file and merge-tree, and any of the three on
another branch. Parsed like the tag rule, with the same limits.

Bash, too (E2, T6): denies a command that names _apex/state/prompts or
_apex/state/grants, or runs ` + "`ape hook user-prompt`" + `, ` + "`ape hook pre-tool`" + ` or ` + "`ape __run`" + `:
they write or act on the records a person act rests on.

Person acts (E2; D178, D179): eight commands are a person's — from a terminal outside
Claude Code as always, or in the person's own Claude Code session on the person's own
words. Each line is the command, then what the person types (the phrase grammar):
` + apexv1.PersonActGrammar() + `
A Bash command that runs one is allowed only when the prompt that opened this turn — the
tool call's prompt_id is the prompt's — is recorded by ` + "`ape hook user-prompt`" + `, the
session's transcript holds the person's user message with that promptId and the same
words (its origin.kind "human": a turn a notification opened is judged on the person's
latest typed prompt, D238), and the phrase is there: its words in order, the target exact (M2, T3, the change
id, ADR-0012; case aside), no negation word (not, no, never, don't and every n't, wait,
hold, stop, cannot, nope) before them in their sentence nor among the three words after the
target, and the sentence not a question; for an answer, the --text itself after the target.
"yes", "ok", "do it" and "approve it" grant nothing. Then pre-tool writes a grant,
_apex/state/grants/<sha256 of the command line>.json, used once, for 120s; a prompt grants
each act and target once. The command line is normalised — the verb, its arguments and
every flag set but --cwd, --json, --wait and --no-detach, as --name=value, sorted — and
must be literal (no $ or backquote anywhere), one person act per command. The command
consumes the grant and records by: <git config user.name> (prompt <ts>: "<the sentence,
at most 160 characters, secrets redacted>"); a --by there must be git config user.name
(else session.by-mismatch). Without a grant it is refused, session.person-only, naming the
phrase; pre-tool denies it first, saying why. A ` + "`!`" + ` line fires no hook, so it can never be
granted. In a session ape launched (APE_SESSION) nothing is recorded or granted.
A grant counts only in the session it was issued for (its session_id is the command's
CLAUDE_CODE_SESSION_ID; with none set, nothing is), and only while its prompt record and the
transcript's message with that promptId still pass the same match (D219). The residual: a
script the model writes and runs as the same OS user can forge all three; E2 attests the
person's words, it is not a sandbox, and pre-tool sees direct calls only.
A long act (approve, --accept-red, --allow-pending-human; D179) that meets a run of the
same command line on its milestone or change — still running (exit 75: run it again), or
ended with its result not yet printed — attaches with no grant; another run there is
run.busy; once its result is printed, the same command is a new act and needs the
person's words again.

Anything else: no output.

When the project's ape pin cannot run here — it names an ape below the floor (the
installed framework's min_ape_version, or 1.0.0 in a v1 project without
_apex/ape-commands.yaml) or one that is not installed — this hook exits 2 with the
pin's message, which Claude Code reads as a deny: the guard fails closed rather than
letting every tool call through. APE_TOOLCHAIN=local in the session's environment
runs this ape regardless.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readHookPayload(cmd)
			if shadowedHook(inline) {
				return nil
			}
			if cwd != "" {
				payload.Cwd = cwd
			}
			d := apexv1.PreTool(payload)
			if !d.Deny && payload.ToolName == "Bash" {
				d = apexv1.PersonActGate(cmd.Context(), payload, personCallsIn(apexv1.BashCommand(payload.ToolInput)))
			}
			emitHook(cmd)(d.Output())
			return nil
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "project directory (default: the payload's cwd)")
	addInlineFlag(cmd, &inline)
	return cmd
}

func newHookUserPromptCmd() *cobra.Command {
	var cwd string
	cmd := &cobra.Command{
		Use:   "user-prompt",
		Short: "UserPromptSubmit hook: record the prompt a person typed (E2)",
		Long: `UserPromptSubmit hook (E2, D178). Appends {ts, session_id, transcript_path, prompt,
prompt_id, kind} — the prompt at most 64 KiB — to _apex/state/prompts/<session_id>.jsonl
(ignored, never committed): what ` + "`ape hook pre-tool`" + ` grants a person act on (its --help
has the phrase grammar). A prompt Claude Code generated (a <task-notification>, a
<system-reminder>, tags only) is recorded as kind notification and never grants (D238).
Records nothing in a session ape launched (APE_SESSION: ape types those prompts itself)
or outside a v1 project. Never blocks the prompt: it prints nothing and exits 0, an error
noted on stderr. ` + "`ape project init`" + ` registers it; missing, ` + "`ape project check`" + ` lists the notice
hooks.prompt-absent and ` + "`ape doctor`" + ` the row hooks.prompt — and a person act from a Claude
Code session is refused (session.person-only).

The phrase grammar (A12; the same lines as ` + "`ape hook pre-tool --help`" + `, which says how a
grant is decided): each line is the command, then what the person types for it.
` + apexv1.PersonActGrammar() + `
Run the command as a literal line (no $ or backquote, one act per command) and without
--by: ape records by: <git config user.name> with the person's quoted words.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readHookPayload(cmd)
			if cwd != "" {
				payload.Cwd = cwd
			}
			if err := apexv1.RecordPrompt(payload); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "ape hook user-prompt: %v; not recorded\n", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "project directory (default: the payload's cwd)")
	return cmd
}

func newHookPreCommitCmd() *cobra.Command {
	var cwd string
	cmd := &cobra.Command{
		Use:   "pre-commit",
		Short: "git pre-commit hook: refuse a commit whose staged added lines hold a secret",
		Long: `git's pre-commit hook (Q89). ` + "`ape project init`" + ` and ` + "`ape project lift`" + ` install it
as <hooks dir>/pre-commit — the folder ` + "`git rev-parse --git-path hooks`" + ` names: core.hooksPath
when set, else .git/hooks (a linked worktree's is the main checkout's) — when no
pre-commit hook is there: a POSIX sh script, marked as ape's by its
"# ape: pre-commit secret scan" line, that runs ` + "`ape hook pre-commit`" + ` (ape resolved on PATH
when git runs it; not on PATH, the commit is refused saying so). A hook that is not ape's
is never touched: ` + "`ape project check`" + ` then lists the notice hooks.pre-commit-absent, naming
the line to add to it (` + "`ape hook pre-commit || exit 1`" + `), and so does ` + "`ape doctor`" + `. git never
versions .git/hooks, so a fresh clone has no hook until ` + "`ape project init`" + ` runs again
(idempotent: it installs only what is missing). A hook in a core.hooksPath folder inside
the project, not ignored, is committed with the init or lift commit.

The scan is the one every commit ape makes goes through: the added lines of what git is
about to commit (the index git hands its hooks, so ` + "`git commit -a`" + ` and ` + "`git commit <paths>`" + `
are scanned as they commit), betterleaks' default rules plus ape's aws-access-key-id,
the secrets.allow list at the guard's baseline (committed AND accepted), and the same
commit.secret refusal on stderr: each secret as <file>:<line> rule <id>: <redacted>,
the placeholder fix first, the allow entry a person may paste. Its files stay staged.

Exit codes: 0 clean (prints nothing), not a v1 project, or APE_SCANNED=1 (set by ape on
its own commits that run hooks — the archive squash, scanned already; the script checks
it before starting ape) · 1 a secret (commit.secret), or the scan could not run
(commit.failed): git commits nothing. ` + "`git commit --no-verify`" + ` skips the hook — a person's
escape; the archive's scan of the squash stays the backstop.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := cwd
			if dir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				dir = wd
			}
			// git may hand its hooks a relative GIT_INDEX_FILE: it is
			// relative to the hook's directory, and ape runs git elsewhere.
			if idx := os.Getenv("GIT_INDEX_FILE"); idx != "" && !filepath.IsAbs(idx) {
				if wd, err := os.Getwd(); err == nil {
					_ = os.Setenv("GIT_INDEX_FILE", filepath.Join(wd, idx))
				}
			}
			return v1Fail(cmd, &v1Flags{}, apexv1.PreCommitScan(cmd.Context(), dir), nil)
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "project directory (default: the working directory, where git runs its hooks)")
	return cmd
}

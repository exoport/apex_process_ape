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
// allow with a note on stderr. pre-commit is git's (Q89): no payload, and
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
                  killall), or rebase or merge on a change branch

  pre-commit      git's pre-commit hook (installed by ` + "`ape project init`" + ` and the lift):
                  the commit secret scan on the staged added lines; exit 1 refuses
                  the commit (commit.secret)

Every Claude Code hook exits 0; a block or deny is expressed in its JSON output.
pre-commit is git's, not Claude Code's: its exit code is the verdict.

In a session ape task launches in a v1 project, the three hooks also come inline
in ape's --settings (with --inline), so a hand-edited settings.json cannot drop
them; the settings.json copy then does nothing, so each event is decided once.`,
	}
	cmd.AddCommand(newHookSessionStartCmd(), newHookStopCmd(), newHookPreToolCmd(), newHookPreCommitCmd())
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
"additionalContext":"<ape status --brief>"}}. Prints nothing outside a v1 project.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readHookPayload(cmd)
			if shadowedHook(inline) {
				return nil
			}
			if p, err := apexv1.Open(apexv1.FindRoot(hookDir(cwd, payload))); err == nil && p.IsV1() {
				emitHook(cmd)(apexv1.SessionStartOutput(p.Status().Brief()))
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
		Short: "PreToolUse hook: deny edits to specs, state.json, the archive, _apex/state and briefs, git tag writes, git push, pkill/killall, and rebases on a change branch",
		Long: `PreToolUse hook for Edit|Write|MultiEdit|NotebookEdit|Bash. Denies, with
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
"permissionDecisionReason":"… specs change only through ape change archive"}}, an edit
whose target is under development/specs/, is development/changes/*/state.json, is under
development/archive/, or is under _apex/state/.

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

Bash, too (trial decision D40, the model never rebases): denies git rebase and git merge
while the repository the command runs in has a change branch, change/<id>, checked out —
ape catches a change up with its moved base itself (` + "`ape change start|verify|archive <id>`" + `
rebase it and re-take the baseline at the base tip). Allowed: rebase/merge --abort and
--quit, git merge-base, merge-file and merge-tree, and rebase or merge on any other
branch. Parsed like the tag rule, with the same limits.

Anything else: no output.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readHookPayload(cmd)
			if shadowedHook(inline) {
				return nil
			}
			if cwd != "" {
				payload.Cwd = cwd
			}
			emitHook(cmd)(apexv1.PreTool(payload).Output())
			return nil
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "project directory (default: the payload's cwd)")
	addInlineFlag(cmd, &inline)
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

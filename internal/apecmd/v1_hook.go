package apecmd

import (
	"fmt"
	"io"
	"os"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

// The hooks `ape project init` registers. Each reads Claude Code's hook
// payload on stdin and answers on stdout. A hook never fails the session
// it serves: a payload it cannot read, or a project that is not v1, is an
// allow with a note on stderr.

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Claude Code hooks for APEX v1 projects (registered by `ape project init`)",
		Long: `Claude Code hook handlers. Each reads the hook payload (JSON) on stdin.

  session-start   SessionStart: adds ` + "`ape status --brief`" + ` as context
  stop            Stop and SubagentStop: in a session ape launched, blocks a stop while the
                  in-progress change has a red command check (at most 3 times in a row);
                  in a person's own session, only shows what was red
  pre-tool        PreToolUse (Edit|Write|MultiEdit|NotebookEdit|Bash): denies edits to
                  development/specs/**, changes/*/state.json, archive/**, _apex/state/**,
                  an existing development/briefs/** file, and Bash commands that create,
                  delete or push a git tag, push at all, stop processes by name (pkill,
                  killall), or rebase or merge on a change branch

Every hook exits 0; a block or deny is expressed in its JSON output.

In a session ape task launches in a v1 project, the three hooks also come inline
in ape's --settings (with --inline), so a hand-edited settings.json cannot drop
them; the settings.json copy then does nothing, so each event is decided once.`,
	}
	cmd.AddCommand(newHookSessionStartCmd(), newHookStopCmd(), newHookPreToolCmd())
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
		Short: "Stop/SubagentStop hook: block a stop while the in-progress change's checks are red",
		Long: `Stop and SubagentStop hook. When HEAD is on a change's branch, change/<id>, that
change is in-progress (read from the working tree), and any of the checks it re-runs
exits non-zero now, prints {"decision":"block","reason":"<the failing checks' last lines
and what to do>"}. It re-runs only the checks that were red at the change's last verify
of its current plan (the regression command too, when it was newly red), recorded in
_apex/state/logs/<id>/last-verify.json; before any verify, the task checks (H10).
Allows (prints nothing) when HEAD is on no change branch (the base branch: planning,
milestones), when the change is not in-progress (blocked, verified, reviewed), when the
last verify had nothing red, when every re-run check is green, or after 3 consecutive
blocks (the count resets when the payload's stop_hook_active is false).

Only in a session ape launched (H13): the hook blocks when its process carries
APE_SESSION, which every session ape spawns has and Claude Code hands to its hooks. In a
person's own Claude Code session (plain ` + "`claude`" + `, the settings.json copy of the hook) it
never blocks and runs no check: on an in-progress change whose last verify had red
checks it allows the stop with {"systemMessage":"APEX: change <id> is in progress, and at
its last verify … check(s) were red: …"}, shown to the person; otherwise it prints nothing.

The verdict is also written to _apex/state/hooks/<session>[-<agent>].json, which
is how ` + "`ape task`" + ` learns that a Stop it saw did not end the turn.`,
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

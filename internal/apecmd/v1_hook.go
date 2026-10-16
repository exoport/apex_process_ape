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
  stop            Stop and SubagentStop: blocks a stop while the one in-progress change
                  has a red command check (at most 3 times in a row)
  pre-tool        PreToolUse (Edit|Write|MultiEdit|NotebookEdit): denies edits to
                  development/specs/**, changes/*/state.json, changes/archive/**, .apex/**

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
		Long: `Stop and SubagentStop hook. When exactly one change is in-progress and any of its
command checks exits non-zero now, prints {"decision":"block","reason":"<the failing
checks' last lines and what to do>"}. Allows (prints nothing) when no or several changes
are in progress, when the change is blocked, when every check is green, or after 3
consecutive blocks (the count resets when the payload's stop_hook_active is false).

The verdict is also written to development/.apex/stop-gate/<session>[-<agent>].json, which
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
		Short: "PreToolUse hook: deny edits to specs, state.json, the archive and .apex",
		Long: `PreToolUse hook for Edit|Write|MultiEdit|NotebookEdit. Denies, with
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
"permissionDecisionReason":"… specs change only through ape change archive"}}, an edit
whose target is under development/specs/, is development/changes/*/state.json, is under
development/changes/archive/, or is under development/.apex/. Anything else: no output.`,
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

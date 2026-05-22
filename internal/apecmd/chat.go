package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/diegosz/apex_process_ape/internal/bridge/config"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/runlog"
)

// newChatCmd registers `ape chat`. A thin wrapper around `claude`
// that spawns the REPL as a direct child of ape with the terminal
// (stdin/stdout/stderr) inherited — the user interacts with claude
// exactly as if they had typed `claude` themselves. Bridge hooks
// (PreToolUse / PostToolUse / UserPromptSubmit / Stop / etc.) are
// captured to a runlog directory for later inspection. Project-bound
// — must run from a directory with `_apex/config.yaml`.
//
// PLAN-8 (2026-05-22) replaced the tmux-based chat surface (which
// spawned claude inside a named tmux session and exec'd `tmux attach`)
// with this direct-exec shape. The PLAN-6 tmux design supported
// detach/reattach but required tmux on PATH. The current shape drops
// that dependency by letting the user's existing terminal serve as
// the PTY for claude directly — claude inherits ape's stdio (which
// the user's shell already wired up). Detach/reattach is not
// available; the session ends when claude exits.
//
// The bridge still listens on its own TCP port for MCP hook traffic,
// independent of the terminal handoff.
func newChatCmd() *cobra.Command {
	var (
		modelFlag             string
		cwdFlag               string
		ignoreProjectSettings bool
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Bridged claude REPL with hooks captured to a runlog",
		Long: `Spawn claude as a child of ape with the ape bridge attached.
Bridge hooks (PreToolUse, PostToolUse, UserPromptSubmit, Stop, and
friends) are captured to <project>/_output/ape/chats/<id>/ alongside
pipeline runs.

ape chat must be run from a project root (a directory containing
_apex/config.yaml).

While attached:
  /exit, /quit       exit claude (default slash commands)
  Ctrl+D in claude   exits the REPL

ape exits when claude exits. The chat session is bound to this
terminal for its lifetime — there is no detach/reattach. To run
claude in the background, use a real terminal multiplexer
separately (e.g. wrap ape chat in tmux or screen).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("cannot determine working directory: %w", err)
				}
				projectRoot = wd
			}
			cfgPath := filepath.Join(projectRoot, "_apex", "config.yaml")
			if _, err := os.Stat(cfgPath); err != nil {
				return fmt.Errorf("ape chat requires a project root with _apex/config.yaml; not found at %s", cfgPath)
			}
			return runChat(cmd.Context(), projectRoot, modelFlag, ignoreProjectSettings)
		},
	}
	cmd.Flags().StringVar(&modelFlag, "model", "", "Initial claude model (e.g. \"opus[1m]\"); falls back to claude's default when empty.")
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root (default: current working directory).")
	cmd.Flags().BoolVar(&ignoreProjectSettings, "ignore-project-settings", false, "Tell claude to skip project + local .claude/settings*.json.")
	return cmd
}

// runChat wires the bridge runtime, then exec's claude as a foreground
// child with stdio inherited so the user can drive the REPL directly.
// Returns when claude exits.
func runChat(ctx context.Context, projectRoot, modelArg string, ignoreProjectSettings bool) error {
	apeBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("ape chat: locate self: %w", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	chatID := time.Now().UTC().Format("20060102T150405Z")
	runDir := filepath.Join(projectRoot, "_output", "ape", "chats", chatID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("ape chat: create runlog dir: %w", err)
	}
	var (
		runLogMu sync.Mutex
		rl       *runlog.Writer
	)
	if w, openErr := runlog.New(runDir); openErr == nil {
		rl = w
	}
	getRunLog := func() *runlog.Writer {
		runLogMu.Lock()
		defer runLogMu.Unlock()
		return rl
	}

	rt := orchestrator.NewBridgeRuntime(orchestrator.BridgeRuntimeOptions{
		OnHook: func(h orchestrator.HookEvent) {
			if writer := getRunLog(); writer != nil {
				_ = writer.Hook(runlog.HookEntry{
					Timestamp: h.At,
					Event:     h.Event,
					Step:      h.Step,
					SessionID: h.SessionID,
					AgentID:   h.AgentID,
					Payload:   h.Payload,
				})
			}
		},
		OnCall: func(c orchestrator.ToolCall) {
			if writer := getRunLog(); writer != nil {
				_ = writer.Call(runlog.CallEntry{
					Timestamp: c.At,
					Method:    "tools/call",
					Tool:      c.Tool,
					Params:    c.Params,
					Result:    c.Result,
					SessionID: c.SessionID,
					ID:        c.ID,
				})
			}
		},
	})
	if err := rt.Listen(runCtx); err != nil {
		return fmt.Errorf("ape chat: runtime listen: %w", err)
	}

	rtErrCh := make(chan error, 1)
	go func() { rtErrCh <- rt.Serve(runCtx) }()
	defer func() { <-rtErrCh }()

	prepend, err := buildInteractivePrepend(apeBin, rt.IPCPort(), config.ModeTUI, ignoreProjectSettings)
	if err != nil {
		return err
	}

	args := append([]string{}, prepend...)
	args = append(args, "--dangerously-skip-permissions")
	if modelArg != "" {
		args = append(args, "--model", modelArg)
	}

	fmt.Fprintf(os.Stderr, "ape chat: bridged claude (id %s)\n", chatID)
	fmt.Fprintf(os.Stderr, "  runlog:  %s\n", runDir)
	fmt.Fprintf(os.Stderr, "  exit:    /exit in claude, or Ctrl+D\n\n")

	// Run claude directly with inherited stdio. ape already holds
	// the user's TTY; claude inherits it as its controlling terminal,
	// so it sees a real PTY without any in-process multiplexing.
	// When claude exits, Run returns and ape tears the bridge down.
	claude := exec.CommandContext(runCtx, "claude", args...)
	claude.Dir = projectRoot
	claude.Stdin = os.Stdin
	claude.Stdout = os.Stdout
	claude.Stderr = os.Stderr
	if err := claude.Run(); err != nil {
		// Non-zero exit from claude itself is treated as a clean exit
		// — same behaviour the PLAN-6 tmux-era attach path provided.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("ape chat: claude: %w", err)
		}
	}

	runCancel()
	if rl != nil {
		_ = rl.Close()
	}
	fmt.Fprintf(os.Stderr, "ape chat: session ended; runlog at %s\n", runDir)
	return nil
}

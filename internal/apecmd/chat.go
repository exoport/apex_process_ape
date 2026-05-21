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
	"github.com/diegosz/apex_process_ape/internal/tmux"
)

// newChatCmd registers `ape chat`. A thin wrapper around `claude` that
// spawns the REPL inside a named tmux session, attaches the user to
// it, and captures bridge hooks (PreToolUse / PostToolUse /
// UserPromptSubmit / Stop / etc.) to a runlog directory for later
// inspection. Project-bound — must run from a directory with
// `_apex/config.yaml`.
//
// The PLAN-5 / early-PLAN-6 design tried to wrap claude in a Bubble
// Tea TUI driven by a per-message `await_message` / `reply` MCP loop.
// That shape was structurally fragile (the model received slash-
// command-shaped strings via tool-result and couldn't actually invoke
// them) and was replaced with this simpler shape in PLAN-6 / tmux
// pivot. The bridge stays useful for hook observability; the chat
// surface is now claude's own REPL, which the user interacts with
// directly via tmux.
func newChatCmd() *cobra.Command {
	var (
		modelFlag             string
		cwdFlag               string
		ignoreProjectSettings bool
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Bridged claude REPL inside a tmux session, with hooks captured to a runlog",
		Long: `Spawn claude inside a tmux session with the ape bridge attached
and attach the user to it. Bridge hooks (PreToolUse, PostToolUse,
UserPromptSubmit, Stop, and friends) are captured to
<project>/_output/ape/chats/<id>/ alongside pipeline runs.

ape chat must be run from a project root (a directory containing
_apex/config.yaml).

While attached:
  Ctrl+B D       detach (claude keeps running; runlog keeps writing)
  /exit, /quit   exit claude (default slash commands)
  Ctrl+D in claude exits the REPL

ape exits when the tmux session ends. Detaching is fine — ape will
keep capturing hooks until you re-attach and exit cleanly, or until
the underlying claude process dies.`,
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

// runChat spawns claude inside a tmux session named ape-chat-<id>,
// wires the bridge for hook observability, exec's `tmux attach` for
// the user, and tears the session down when the user detaches/exits.
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

	// Spawn claude inside a tmux session. tmux owns the PTY; the
	// user attaches to the session below to interact with the REPL.
	argv := append([]string{"claude"}, prepend...)
	argv = append(argv, "--dangerously-skip-permissions")
	if modelArg != "" {
		argv = append(argv, "--model", modelArg)
	}

	sessionName := "ape-chat-" + chatID
	_ = tmux.KillSession(runCtx, sessionName)
	if err := tmux.NewSession(runCtx, sessionName, projectRoot, argv); err != nil {
		return fmt.Errorf("ape chat: %w", err)
	}
	// Cleanup must run even after runCtx is cancelled — Background is intentional.
	defer func() { _ = tmux.KillSession(context.Background(), sessionName) }() //nolint:contextcheck // cleanup-on-exit; runCtx is already done here

	readyCtx, cancelReady := context.WithTimeout(runCtx, 30*time.Second)
	if err := tmux.WaitForReady(readyCtx, sessionName); err != nil {
		cancelReady()
		return fmt.Errorf("ape chat: claude REPL not ready in tmux session: %w", err)
	}
	cancelReady()

	fmt.Fprintf(os.Stderr, "ape chat: bridged claude in tmux session %s\n", sessionName)
	fmt.Fprintf(os.Stderr, "  runlog:  %s\n", runDir)
	fmt.Fprintf(os.Stderr, "  detach:  Ctrl+B D (session keeps running)\n")
	fmt.Fprintf(os.Stderr, "  exit:    /exit in claude, or Ctrl+D\n\n")

	// Attach the user to the tmux session. exec'ing replaces ape's
	// process with tmux's so the terminal handoff is clean; the
	// bridge goroutine is unaffected because tmux is the parent of
	// the claude child, not us. When the user detaches or claude
	// exits, control returns here.
	attach := exec.CommandContext(runCtx, "tmux", "attach", "-t", sessionName)
	attach.Stdin = os.Stdin
	attach.Stdout = os.Stdout
	attach.Stderr = os.Stderr
	if err := attach.Run(); err != nil {
		// tmux attach exits non-zero when claude exits or the
		// session goes away mid-attach — treat as a clean exit.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("ape chat: tmux attach: %w", err)
		}
	}

	runCancel()
	if rl != nil {
		_ = rl.Close()
	}
	fmt.Fprintf(os.Stderr, "ape chat: session ended; runlog at %s\n", runDir)
	return nil
}

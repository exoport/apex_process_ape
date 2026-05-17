package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/cost"
	"github.com/diegosz/apex_process_ape/internal/runlog"
	"github.com/diegosz/apex_process_ape/internal/sessions"
	"github.com/diegosz/apex_process_ape/internal/web"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// chatSystemPrompt is the system prompt used by `ape chat`. Quoted
// verbatim from PLAN-5 / C3 ("Bootstrap content — PoC verbatim").
const chatSystemPrompt = `You are connected to a Web UI. Call await_message() to receive a message from the user. When it returns a non-empty string, process it and call reply() with your response. If await_message() returns an empty string, call it again. Begin by calling await_message() now.`

// chatBootstrapInput is the synthetic user-turn the parent writes to
// claude's stdin via io.Pipe after the bridge signals ready. Without
// this first turn, claude idles at the interactive prompt indefinitely
// and never calls await_message. PLAN-5 / C3.
const chatBootstrapInput = "Start the await_message loop. Call await_message() now."

func newChatCmd() *cobra.Command {
	var (
		openFlag                  bool
		ignoreProjectSettingsFlag bool
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Open a bridged interactive Claude session in a web UI",
		Long: `Start one bridged interactive Claude session, surfaced via a local
web UI. The Web UI is the only surface — there is no TUI mode and no
--print mode for chat. The bound URL is printed on startup; pass
--open to also fire xdg-open.

Closing the browser does not kill the session; reopening it
reconnects (no backlog replay — the JSONL streams under
<project>/_output/ape/chats/ are the durable record). The Stop button
in the page header SIGTERMs the active claude subprocess and exits
with code 137.

Authentication: the broker binds to 127.0.0.1 only. Any local user on
this machine can hit /api/send and inject text into the session; if
that is a concern, do not run ape chat on a shared-account host. See
docs/reference/bridge-security.md for the full threat model.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apeBin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("ape chat: locate self: %w", err)
			}
			if _, err := exec.LookPath("claude"); err != nil {
				return errors.New("ape chat: `claude` not found on PATH; install Claude Code first")
			}

			// PLAN-5 / C6 — open the chat run-dir before spawning
			// claude so checkpoints capture the chat-start moment.
			cwd, _ := os.Getwd()
			startedAt := time.Now().UTC()
			chatID := runlog.NewChatID(startedAt, cwd, os.Getpid())
			chatDir := runlog.ChatDir(cwd, chatID)
			if err := runlog.EnsureNoCollision(chatDir); err != nil {
				return fmt.Errorf("ape chat: %w", err)
			}
			// First-run .gitignore policy.
			isTTY := term.IsTerminal(int(os.Stdin.Fd()))
			var askPrompt func(string) bool
			if isTTY {
				askPrompt = ttyConfirm
			}
			_, _ = runlog.EnsureGitignore(cwd, askPrompt, os.Stderr)

			rl, err := runlog.New(chatDir)
			if err != nil {
				return fmt.Errorf("ape chat: open run dir: %w", err)
			}
			defer rl.Close()
			_ = rl.Checkpoint(runlog.CheckpointEntry{Kind: "chat-start", Payload: map[string]any{"chat_id": chatID, "cwd": cwd}})

			// C8 — render the HTMX page once at startup and mount
			// the embedded assets/ subtree onto the broker mux.
			tpl := web.MustTemplates()
			pageHTML := web.RenderPage(tpl, web.PageData{
				Title:    "ape chat",
				Subtitle: chatID,
			})
			mountExtras := func(mux *http.ServeMux) {
				if err := web.MountAssets(mux); err != nil {
					fmt.Fprintf(os.Stderr, "ape chat: mount assets: %v\n", err)
				}
				mux.HandleFunc("/dashboard", func(w http.ResponseWriter, _ *http.Request) {
					r, err := cost.LoadRollup(cwd)
					if err != nil {
						http.Error(w, "load rollup: "+err.Error(), 500)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(r)
				})
			}

			session := orchestrator.New(orchestrator.Options{
				APEBin:                apeBin,
				SystemPrompt:          chatSystemPrompt,
				BootstrapInput:        chatBootstrapInput,
				Stdout:                os.Stdout,
				Stderr:                os.Stderr,
				IgnoreProjectSettings: ignoreProjectSettingsFlag,
				PageHTML:              pageHTML,
				MountExtras:           mountExtras,
				FragmentRenderer:      newWebRenderer(tpl),
				OnReply: func(content string) {
					_ = rl.Checkpoint(runlog.CheckpointEntry{Kind: "reply", Payload: map[string]any{"content": content}})
				},
				OnCall: func(c orchestrator.ToolCall) {
					_ = rl.Call(runlog.CallEntry{
						Timestamp: c.At,
						Method:    "tools/call",
						Tool:      c.Tool,
						Params:    c.Params,
						Result:    c.Result,
						SessionID: c.SessionID,
						ID:        c.ID,
					})
				},
				OnHook: func(h orchestrator.HookEvent) {
					_ = rl.Hook(runlog.HookEntry{
						Timestamp: h.At,
						Event:     h.Event,
						Step:      h.Step,
						SessionID: h.SessionID,
						AgentID:   h.AgentID,
						Payload:   h.Payload,
					})
				},
			})
			url, err := session.Listen()
			if err != nil {
				return fmt.Errorf("ape chat: listen: %w", err)
			}
			fmt.Fprintf(os.Stderr, "web ui: %s\n", url)
			if openFlag {
				_ = openBrowser(url)
			}

			// PLAN-5 / C5 — track this session in ~/.ape/registry.json
			// so `ape sessions` can list / open / prune. Best-effort
			// on register/deregister; failure is non-fatal.
			row := sessions.Session{
				PID:       os.Getpid(),
				CWD:       cwd,
				Command:   "ape " + strings.Join(os.Args[1:], " "),
				Port:      session.BrokerPort(),
				URL:       url,
				StartedAt: time.Now().UTC(),
			}
			regPath := sessions.DefaultPath()
			_ = sessions.Register(regPath, row)

			runErr := session.Run(cmd.Context())
			_ = rl.Checkpoint(runlog.CheckpointEntry{Kind: "chat-end", Payload: map[string]any{"exit_code": session.ExitCode}})

			// PLAN-5 / C7 — scan the session JSONL for cost totals.
			// Claude Code writes ~/.claude/projects/<encoded-cwd>/<sid>.jsonl
			// during a session; we find the newest one modified
			// after startedAt and aggregate its usage blocks. Falls
			// back to zero totals when no file matches (typical of
			// runs where claude bailed before producing one).
			endedAt := time.Now().UTC()
			totals, model, jsonlPath, scanErr := cost.ScanLatestSession("", startedAt)
			if scanErr != nil {
				fmt.Fprintf(os.Stderr, "ape chat: cost scan: %v\n", scanErr)
			}
			if jsonlPath != "" {
				// Link the transcript into the run dir so the
				// runlog references the canonical path. Best-effort.
				_ = rl.LinkTranscript("transcript.jsonl", jsonlPath)
			}
			_ = runlog.WriteSessionYAML(chatDir, runlog.SessionMeta{
				ChatID:    chatID,
				StartedAt: startedAt,
				EndedAt:   endedAt,
				Model:     model,
				CostUSD:   totals.CostUSD,
				TokensIn:  int64(totals.InputTokens),
				TokensOut: int64(totals.OutputTokens),
			})
			if r, err := cost.LoadRollup(cwd); err == nil {
				r.FoldChat(chatID, endedAt, totals)
				_ = cost.SaveRollup(cwd, r)
			}
			// os.Exit skips defers; deregister and close explicitly.
			_ = rl.Close()
			_ = sessions.Deregister(regPath, row.PID)
			if runErr != nil {
				return runErr
			}
			if session.ExitCode != 0 {
				os.Exit(session.ExitCode) //nolint:gocritic // explicit code path; cleanup above ran already
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&openFlag, "open", false, "Run xdg-open on the web UI URL after startup")
	cmd.Flags().BoolVar(&ignoreProjectSettingsFlag, "ignore-project-settings", false, "Tell the spawned claude to skip project + local .claude/settings*.json")
	return cmd
}

// openBrowser tries xdg-open (Linux) / open (macOS) / start (Windows).
// Best-effort; failure is silent because the user can copy the URL.
func openBrowser(url string) error {
	bin := "xdg-open"
	args := []string{url}
	switch runtimeGOOS() {
	case "darwin":
		bin = "open"
	case "windows":
		bin = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	}
	return exec.Command(bin, args...).Start()
}

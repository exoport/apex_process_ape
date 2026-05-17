package apecmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/spf13/cobra"
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
			session := orchestrator.New(orchestrator.Options{
				APEBin:                apeBin,
				SystemPrompt:          chatSystemPrompt,
				BootstrapInput:        chatBootstrapInput,
				Stdout:                os.Stdout,
				Stderr:                os.Stderr,
				IgnoreProjectSettings: ignoreProjectSettingsFlag,
			})
			url, err := session.Listen()
			if err != nil {
				return fmt.Errorf("ape chat: listen: %w", err)
			}
			fmt.Fprintf(os.Stderr, "web ui: %s\n", url)
			if openFlag {
				_ = openBrowser(url)
			}
			if err := session.Run(cmd.Context()); err != nil {
				return err
			}
			if session.ExitCode != 0 {
				os.Exit(session.ExitCode) //nolint:gocritic // explicit code path; defers (none) need not run
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

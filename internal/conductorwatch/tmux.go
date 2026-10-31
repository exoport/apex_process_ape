package conductorwatch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tmux is the conductor's tmux pane.
type Tmux struct {
	// Target is the pane (-t): %3, session:window.pane, ….
	Target string
	// ConfigDir is Claude Code's config dir, whose sessions/<pid>.json
	// registers a running session ($CLAUDE_CONFIG_DIR, else ~/.claude).
	ConfigDir string
	// socket is a tmux server socket (-S), for tests; "" the default.
	socket string
}

func (t *Tmux) tmux(ctx context.Context, args ...string) (string, error) {
	if t.socket != "" {
		args = append([]string{"-S", t.socket}, args...)
	}
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	return string(out), err
}

// Capture is the pane's visible text.
func (t *Tmux) Capture(ctx context.Context) (string, error) {
	return t.tmux(ctx, "capture-pane", "-p", "-t", t.Target)
}

// Type types text literally (-l: no key names) and presses Enter.
func (t *Tmux) Type(ctx context.Context, text string) error {
	if _, err := t.tmux(ctx, "send-keys", "-t", t.Target, "-l", text); err != nil {
		return err
	}
	_, err := t.tmux(ctx, "send-keys", "-t", t.Target, "Enter")
	return err
}

// Session is the Claude Code session id of the pane's foreground process,
// when that is claude and registered on this machine (its pid, the pane's
// or a child's, has <config>/sessions/<pid>.json naming its sessionId): a
// stale or wrong pane — a shell, where a typed line runs as a command —
// gets nothing (D664+), and the id tells one session from the next in the
// same pane (D665+).
func (t *Tmux) Session(ctx context.Context) (id, why string) {
	out, err := t.tmux(ctx, "display-message", "-p", "-t", t.Target, "#{pane_pid} #{pane_current_command}")
	if err != nil {
		return "", "tmux has no pane " + t.Target
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		return "", "tmux reported nothing for " + t.Target
	}
	if f[1] != "claude" {
		return "", "the pane runs " + f[1] + ", not Claude Code"
	}
	pids := []string{f[0]}
	if kids, err := exec.CommandContext(ctx, "pgrep", "-P", f[0]).Output(); err == nil { //nolint:gosec // a pid tmux reported
		pids = append(pids, strings.Fields(string(kids))...)
	}
	for _, pid := range pids {
		data, err := os.ReadFile(filepath.Join(t.configDir(), "sessions", pid+".json"))
		if err != nil {
			continue
		}
		var reg struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &reg) != nil || reg.SessionID == "" {
			return "", "the pane's Claude Code registration (" + pid + ".json) names no sessionId"
		}
		return reg.SessionID, ""
	}
	return "", "the pane's Claude Code is no session registered under " + filepath.Join(t.configDir(), "sessions")
}

func (t *Tmux) configDir() string {
	if t.ConfigDir != "" {
		return t.ConfigDir
	}
	return ClaudeConfigDir()
}

// ClaudeConfigDir is Claude Code's config dir: $CLAUDE_CONFIG_DIR, else
// ~/.claude.
func ClaudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// DiscoverPane is the one tmux pane whose foreground process is claude,
// "" when there is none or more than one (then the person names it).
func DiscoverPane(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-a", "-F", "#{pane_id} #{pane_current_command}").Output()
	if err != nil {
		return ""
	}
	found := ""
	for l := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[1] == "claude" {
			if found != "" {
				return ""
			}
			found = f[0]
		}
	}
	return found
}

// StateDir is the watcher's state and acknowledgements: under
// $XDG_STATE_HOME, else ~/.local/state, in ape/conductor.
func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "ape", "conductor")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "ape", "conductor")
}

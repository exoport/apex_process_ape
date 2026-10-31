package conductorwatch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/proctree"
)

// Re-pinning (D666+ F5): a paused watcher resumes only for the session
// that asked, from inside it: `ape conductor watcher --repin` finds the
// Claude Code session it runs under (its process ancestry), and the
// watcher pins that session only when the pane runs it.

// Repin is a --repin request.
type Repin struct {
	Session string `json:"session"`
	At      string `json:"at"`
}

func repinPath(dir string) string { return filepath.Join(dir, "repin.json") }

// RequestRepin records a --repin from session.
func RequestRepin(dir, session string, now time.Time) error {
	data, err := json.Marshal(Repin{Session: session, At: now.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return atomicfile.Write(repinPath(dir), append(data, '\n'))
}

// CallerSession is the Claude Code session this process runs under: the
// sessionId registered for the nearest ancestor with
// <config>/sessions/<pid>.json. "" outside one.
func CallerSession(configDir string) string {
	for _, pid := range proctree.Ancestors(os.Getpid(), proctree.Parent) {
		if id := registeredSession(configDir, strconv.Itoa(pid)); id != "" {
			return id
		}
	}
	return ""
}

// registeredSession is the sessionId of pid's registration, "" when none.
func registeredSession(configDir, pid string) string {
	data, err := os.ReadFile(filepath.Join(configDir, "sessions", pid+".json"))
	if err != nil {
		return ""
	}
	var reg struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(data, &reg) != nil {
		return ""
	}
	return reg.SessionID
}

// checkRepin consumes a waiting --repin request: it pins the session that
// asked when the pane runs it (id), and drops any other — a request from a
// session not in the pane pins nothing, now or later.
func (g *Gate) checkRepin(id string) {
	path := repinPath(g.StateDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || err != nil {
		return
	}
	var r Repin
	_ = json.Unmarshal(data, &r)
	_ = os.Remove(path)
	switch {
	case r.Session == "" || r.Session != id:
		g.logf("watcher: a --repin from session %q ignored: the pane runs %q\n", r.Session, id)
	case r.Session != g.session:
		g.logf("watcher: re-pinned to Claude Code session %s (was %s): typing again\n", id, g.session)
		g.session, g.paused = id, ""
	}
}

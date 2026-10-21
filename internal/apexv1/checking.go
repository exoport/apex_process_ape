package apexv1

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// RK-4b (Q144, D300): after each check ape undoes what changed in the tree
// outside the development folder while it ran (undoStrays) — its litter,
// but also whatever another writer changed meanwhile: the person's own
// session, another session, or the model once `ape change verify --wait`
// returned 75 and the check went on. So while a check runs, ape keeps a
// marker beside the run locks, and the pre-tool hook refuses an edit that
// the cleanup would undo. A marker is honoured only while the process
// that wrote it lives (pid and start time): a crashed check never blocks
// an edit.

// CheckMarker is one check ape is running in the checkout right now.
type CheckMarker struct {
	PID      int    `json:"pid"`
	PIDStart string `json:"pid_start,omitempty"`
	Cmd      string `json:"cmd"`
	Log      string `json:"log,omitempty"`
	Started  string `json:"started"`
	// Run is the long run the check belongs to (`change verify c-1`, its
	// run id), "" when it runs outside one (a hook's re-run).
	Verb  string `json:"verb,omitempty"`
	Scope string `json:"scope,omitempty"`
	RunID string `json:"run_id,omitempty"`
}

const checkMarkerPrefix = "checking-"

// markChecking writes the marker of the check cmd (log at logPath) and
// returns its removal.
func (p *Project) markChecking(cmd, logPath string) func() {
	if err := os.MkdirAll(p.RunsDir(), 0o755); err != nil {
		return func() {}
	}
	m := CheckMarker{PID: os.Getpid(), PIDStart: selfStart(), Cmd: cmd, Started: Stamp()}
	if logPath != "" {
		m.Log = p.Rel(logPath)
	}
	if l := p.ownRunLock(); l != nil {
		m.Verb, m.Scope, m.RunID = l.Verb, l.Scope, l.ID
	}
	data, err := json.Marshal(m)
	if err != nil {
		return func() {}
	}
	const markerRand = 4 // random bytes in the name: one marker per check, never two alike
	path := filepath.Join(p.RunsDir(), checkMarkerPrefix+strconv.Itoa(m.PID)+"-"+randomHex(markerRand)+".json")
	if writeFileAtomic(path, string(data)+"\n") != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

// ownRunLock is the run lock this process holds, nil when none.
func (p *Project) ownRunLock() *RunLock {
	entries, _ := os.ReadDir(p.RunsDir())
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".result.json") || strings.HasPrefix(name, checkMarkerPrefix) {
			continue
		}
		if l := p.LoadRunLock(strings.TrimSuffix(name, ".json")); l != nil && l.PID == os.Getpid() {
			return l
		}
	}
	return nil
}

// LiveCheck is a check running in the checkout now, nil when none. A
// marker whose process is gone, or whose pid names a later process, is
// stale: removed, never honoured.
func (p *Project) LiveCheck() *CheckMarker {
	matches, _ := filepath.Glob(filepath.Join(p.RunsDir(), checkMarkerPrefix+"*.json"))
	sort.Strings(matches)
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m CheckMarker
		if json.Unmarshal(data, &m) != nil || m.PID <= 0 {
			_ = os.Remove(path)
			continue
		}
		if !ownerAlive(m.PID, m.PIDStart) {
			_ = os.Remove(path)
			continue
		}
		return &m
	}
	return nil
}

// checkRunningReason is the pre-tool hook's refusal of an edit to rel
// while m runs: a path the check's cleanup would undo.
func checkRunningReason(rel string, m *CheckMarker) string {
	run, wait := "", "wait for the check to end"
	if m.Verb != "" {
		run = fmt.Sprintf(", for `ape %s %s` (run %s)", m.Verb, m.Scope, m.RunID)
		wait = fmt.Sprintf("wait for that run to end (`ape %s %s --wait 9m` attaches to it)", m.Verb, m.Scope)
	}
	return "ape is running a check in this checkout — `" + m.Cmd + "`" + run + fmt.Sprintf(
		", pid %d, since %s. When it ends, ape undoes what changed outside the development folder while it ran, so an edit to %s now would be lost (RK-4b). %s, then make the edit.",
		m.PID, m.Started, rel, upperFirst(wait))
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

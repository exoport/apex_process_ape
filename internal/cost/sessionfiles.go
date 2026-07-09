package cost

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionKind discriminates a main claude session transcript from a
// sub-agent (Agent tool) transcript.
type SessionKind int

const (
	// SessionMain is the top-level claude session transcript
	// (<proj>/<sid>.jsonl).
	SessionMain SessionKind = iota
	// SessionSubagent is an Agent-tool sub-session transcript
	// (<proj>/<sid>/subagents/agent-<id>.jsonl).
	SessionSubagent
)

func (k SessionKind) String() string {
	if k == SessionSubagent {
		return "subagent"
	}
	return "main"
}

// SessionFile is one transcript file in a run's session set, with the id
// ape uses to attribute its usage and the kind that distinguishes a main
// session from a sub-agent one.
type SessionFile struct {
	// Path is the transcript's absolute path on disk.
	Path string
	// SessionID is the claude session id (main: the <sid> filename) or,
	// for a sub-agent, the agent id parsed from agent-<id>.jsonl (the only
	// stable per-sub identifier — a sub's internal sessionId equals its
	// parent's, so session_id alone collapses every sub into one phantom).
	SessionID string
	// Kind is SessionMain or SessionSubagent.
	Kind SessionKind
}

// SessionFiles enumerates the full transcript set for one claude session:
// the main transcript first, then its sub-agent transcripts
// (<main-without-ext>/subagents/agent-*.jsonl) modified at/after `since`,
// path-sorted and deduped by cleaned path. A zero `since` disables the
// mtime floor.
//
// This is the shared enumeration behind PLAN-13's transcript blob upload
// and PLAN-17's `ape metrics` / `ape transcript` (main + sub-agent set),
// extracted from the interactive runner's per-step telemetry sweep
// (PLAN-10 D2 remainder). The main entry is always included (its existence
// is the caller's concern); sub-agent entries are filtered to existing
// regular files so a dropped SubagentStop hook can't fabricate a phantom.
func SessionFiles(mainTranscript string, since time.Time) []SessionFile {
	if mainTranscript == "" {
		return nil
	}
	out := []SessionFile{{
		Path:      mainTranscript,
		SessionID: sessionIDFromTranscript(mainTranscript),
		Kind:      SessionMain,
	}}
	cleanMain := filepath.Clean(mainTranscript)
	seen := map[string]bool{cleanMain: true}
	for _, p := range subagentTranscripts(mainTranscript, since) {
		cp := filepath.Clean(p)
		if seen[cp] {
			continue
		}
		seen[cp] = true
		out = append(out, SessionFile{
			Path:      p,
			SessionID: AgentIDFromTranscript(p),
			Kind:      SessionSubagent,
		})
	}
	return out
}

// subagentTranscripts globs the sub-agent transcripts of a main session.
// Claude writes them to <proj>/<sid>/subagents/agent-<id>.jsonl alongside
// the main transcript <proj>/<sid>.jsonl. Only files modified at/after
// `since` are returned (a 1s grace absorbs coarse filesystem mtime
// granularity), so a prior NoClear step's subs in the same dir aren't
// re-folded. Result is path-sorted.
func subagentTranscripts(mainTranscript string, since time.Time) []string {
	dir := filepath.Join(strings.TrimSuffix(mainTranscript, ".jsonl"), "subagents")
	matches, err := filepath.Glob(filepath.Join(dir, "agent-*.jsonl"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, p := range matches {
		info, statErr := os.Stat(p)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since.Add(-time.Second)) {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// AgentIDFromTranscript derives a sub-agent's id from its transcript
// filename (agent-<id>.jsonl).
func AgentIDFromTranscript(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return strings.TrimPrefix(name, "agent-")
}

// sessionIDFromTranscript derives a main session's id from its transcript
// filename (<sid>.jsonl).
func sessionIDFromTranscript(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

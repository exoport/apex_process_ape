// Package hookdrift detects the completion gates going silent.
//
// ape's step-completion gates read fields off Claude Code's hook
// payloads: `background_tasks` on Stop decides whether a turn boundary
// really means the step is done, and an Agent-tool `tool_response.status`
// catches a spawn that detached. Claude Code ships on a schedule ape does
// not control and makes no compatibility promise about hook payload
// shape.
//
// If one of those fields is renamed or dropped, nothing errors. The gate
// simply stops firing, and ape silently returns to reporting success on
// runs that did nothing — the exact defect the gates exist to prevent,
// reintroduced invisibly. A gate that can stop firing unnoticed is worse
// than no gate, because it converts an absent protection into a believed-
// present one.
//
// No release-time check can catch that: the change happens on the
// harness's schedule, under an already-released ape binary. This package
// can, the same way `ape costs coverage` catches a model id changing
// under the price table — by reading what the locally-installed Claude
// Code is actually emitting. The corpus is ape's own hook-events.jsonl,
// which every interactive run already writes.
//
// Present-but-empty is healthy. Absent is drift.
package hookdrift

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultWindow is how far back a sweep looks. Runs older than this say
// nothing about the Claude Code installed today.
const DefaultWindow = 30 * 24 * time.Hour

// Field names the gates depend on, as they appear on the wire.
const (
	FieldBackgroundTasks = "background_tasks"
	FieldToolResponse    = "tool_response"
	FieldAgentID         = "agent_id"
)

// Observation counts how often one gate-critical field was present on the
// events that should carry it.
type Observation struct {
	Field   string
	Event   string // the hook event expected to carry it
	Seen    int    // events of this kind observed
	Present int    // ... of which carried the field
}

// OK reports whether the field held up. A field observed zero times is
// not a failure — no evidence is not evidence of absence.
func (o Observation) OK() bool { return o.Seen == 0 || o.Present > 0 }

// Report is the outcome of a sweep.
type Report struct {
	Runs         int           // runlogs read
	Window       time.Duration // how far back the sweep looked
	Observations []Observation
	Versions     []string // claude_version values seen, sorted
}

// Observed reports whether the sweep found anything to judge.
func (r *Report) Observed() bool {
	for _, o := range r.Observations {
		if o.Seen > 0 {
			return true
		}
	}
	return false
}

// Drifted lists the fields that were expected but never present.
func (r *Report) Drifted() []Observation {
	var out []Observation
	for _, o := range r.Observations {
		if !o.OK() {
			out = append(out, o)
		}
	}
	return out
}

// OK reports whether every observed field held up.
func (r *Report) OK() bool { return len(r.Drifted()) == 0 }

// Summary renders a one-line verdict.
func (r *Report) Summary() string {
	if !r.Observed() {
		return "no recent hook events — contract not verified"
	}
	parts := make([]string, 0, len(r.Observations))
	for _, o := range r.Observations {
		if o.Seen == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d/%d", o.Field, o.Present, o.Seen))
	}
	v := ""
	if len(r.Versions) > 0 {
		v = " (Claude Code " + strings.Join(r.Versions, ", ") + ")"
	}
	return fmt.Sprintf("%d run(s): %s%s", r.Runs, strings.Join(parts, ", "), v)
}

// hookRow is one runlog hook-events.jsonl line. Payload stays raw so
// presence can be judged without imposing a shape on it — the whole point
// is to detect the shape changing.
type hookRow struct {
	Event   string                     `json:"event"`
	Payload map[string]json.RawMessage `json:"payload"`
}

// Observe sweeps a project's runlogs for hook payloads written since
// `since`, and reports whether the fields the gates depend on are still
// present.
//
// A project with no runlogs yields an unobserved report and no error:
// absence of evidence is not coverage, so a fresh checkout (or CI) skips
// rather than passes.
func Observe(projectRoot string, since time.Time) (*Report, error) {
	rep := &Report{Window: time.Since(since)}
	root := filepath.Join(projectRoot, "_output", "tasks")

	bg := Observation{Field: FieldBackgroundTasks, Event: "Stop"}
	tr := Observation{Field: FieldToolResponse, Event: "PostToolUse"}
	ag := Observation{Field: FieldAgentID, Event: "SubagentStop"}
	versions := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is skipped, never fatal
		}
		if d.IsDir() || d.Name() != "hook-events.jsonl" {
			return nil
		}
		// `latest/` is a symlink to a real run dir; WalkDir does not follow
		// it, so each run is counted once.
		info, statErr := d.Info()
		if statErr != nil {
			// Raced with a reaper, or permissions changed mid-walk. One
			// unreadable runlog is not a verdict about the hook contract.
			return nil //nolint:nilerr // a runlog we cannot stat is skipped, never fatal to the sweep
		}
		if info.ModTime().Before(since) {
			return nil
		}
		rep.Runs++
		if v := claudeVersionFor(path); v != "" {
			versions[v] = true
		}
		scanRunlog(path, &bg, &tr, &ag)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("sweep %s: %w", root, err)
	}

	rep.Observations = []Observation{bg, tr, ag}
	for v := range versions {
		rep.Versions = append(rep.Versions, v)
	}
	sort.Strings(rep.Versions)
	return rep, nil
}

// scanRunlog folds one runlog's hook events into the observations.
func scanRunlog(path string, bg, tr, ag *Observation) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var row hookRow
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue
		}
		switch row.Event {
		case "Stop":
			count(bg, row)
		case "PostToolUse":
			// Only Agent-tool results carry a spawn status worth gating on.
			if toolName(row) == "Agent" {
				count(tr, row)
			}
		case "SubagentStop":
			count(ag, row)
		}
	}
}

func count(o *Observation, row hookRow) {
	o.Seen++
	if _, ok := row.Payload[o.Field]; ok {
		o.Present++
	}
}

func toolName(row hookRow) string {
	raw, ok := row.Payload["tool_name"]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// claudeVersionFor reads the claude_version recorded in the run's
// manifest, so a drift report names the harness that produced it.
// Best-effort: a missing or unparsable manifest simply contributes
// nothing.
func claudeVersionFor(hookPath string) string {
	f, err := os.Open(filepath.Join(filepath.Dir(hookPath), "manifest.yaml"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if v, found := strings.CutPrefix(line, "claude_version:"); found {
			// `claude --version` prints "2.1.232 (Claude Code)"; the suffix
			// is noise once the report has already said "Claude Code".
			v = strings.TrimSpace(v)
			v = strings.TrimSpace(strings.TrimSuffix(v, "(Claude Code)"))
			return v
		}
	}
	return ""
}

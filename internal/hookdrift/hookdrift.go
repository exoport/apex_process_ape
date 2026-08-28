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
//
// # Which runlogs count, and whose verdict it is
//
// Two things about the corpus were wrong until they were measured against
// a real project, and both made the detector quieter than it looked.
//
// The sweep used to read only <project>/_output/tasks, one of the four
// roots ape writes runs into, so `ape pipeline` — the flagship command —
// was invisible to it. A project that ran pipelines and nothing else
// reported "no interactive runs" for ever and the check had never once
// fired. It now walks runlog.ApeRoot and takes every hook-events.jsonl
// beneath it, so a new run kind is covered the moment it exists rather
// than when someone remembers to add it here.
//
// The verdict is scoped to ONE Claude Code version: the one that wrote the
// most recent run. A field absent from every payload of that version is
// drift; older versions in the window are counted and reported, never
// judged. Without that scoping a 30-day window straddling an upgrade masks
// fresh drift for a month — 25 healthy pre-upgrade runs keep Present > 0
// while every post-upgrade run lacks the field.
//
// Alternatives considered, and why this one:
//
//   - a presence RATIO threshold ("flag below 50%") needs a constant
//     nobody can justify, and would misfire on a field that is
//     legitimately sparse on a healthy harness;
//   - judging the newest N runs approximates recency but still mixes
//     versions when the upgrade lands mid-window, and N is as arbitrary
//     as the ratio;
//   - shrinking the window is cruder still and raises the skip rate,
//     pushing the gate toward "not verified" — the one outcome worth
//     avoiding, since a gate that cannot speak reads as a gate that
//     approved.
//
// Version scoping needs no tuned constant, reuses a value the manifest
// already carries, and asks the question the gate actually means: does the
// Claude Code I have installed right now still send this field?
//
// Runs that carry no version stamp form their own bucket, so an older
// ape's runlogs are judged together rather than silently fused with a
// stamped generation.
//
// That bucket used to be much larger than it looked. The stamp was read
// only from the pipeline manifest, which `ape prompt` and `ape chat` do
// not write — so runs from today's ape landed in it alongside runs from
// an ape three releases old, and since the judged version is the newest
// run's, one recent prompt run dragged the whole verdict in there and
// excluded every stamped pipeline run beside it. Both producers now stamp
// runlog.HarnessFile, which every run kind writes; see claudeVersionFor.
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

	"github.com/exoport/apex_process_ape/internal/runlog"
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
	Runs         int           // runlogs in the window, across every version
	Window       time.Duration // how far back the sweep looked
	Observations []Observation // counted from the JUDGED version only
	Versions     []string      // every claude_version seen in the window, sorted

	// Judged is the claude_version the verdict is about: the version that
	// wrote the most recent run. Empty means those runlogs carried no
	// version stamp.
	Judged string
	// Scanned is how many runs the verdict was computed from, and Ignored
	// how many were in the window but written by a different version. A
	// non-zero Ignored is normal right after a Claude Code upgrade.
	Scanned int
	Ignored int
	// IgnoredVersions is how many distinct versions those ignored runs came
	// from, counting the unstamped bucket as one.
	IgnoredVersions int
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
	if r.Judged != "" {
		v = " (Claude Code " + r.Judged + ")"
	}
	// Name the runs the verdict did NOT come from. Silently dropping them
	// would make a one-run verdict look like it spoke for the whole window.
	ignored := ""
	if r.Ignored > 0 {
		ignored = fmt.Sprintf("; %d run(s) from %d other version(s) not judged",
			r.Ignored, r.IgnoredVersions)
	}
	return fmt.Sprintf("%d run(s): %s%s%s", r.Scanned, strings.Join(parts, ", "), v, ignored)
}

// hookRow is one runlog hook-events.jsonl line. Payload stays raw so
// presence can be judged without imposing a shape on it — the whole point
// is to detect the shape changing.
type hookRow struct {
	Event   string                     `json:"event"`
	Payload map[string]json.RawMessage `json:"payload"`
}

// HookEventsFile is the per-run file every runlog producer writes.
const HookEventsFile = "hook-events.jsonl"

// The sweep walks runlog.ApeRoot — the one subtree ape owns — rather than
// an enumerated list of run roots. An enumeration is what silently
// excluded `ape pipeline` runs from this check for its entire existence,
// and a fifth producer would have repeated the mistake.
//
// It walks ape's subtree and not the whole output folder because
// `_output/` belongs to the framework: handoffs, briefs and
// verify-orchestrator reports live there too, and sweeping them to find
// ape's own runlogs would be reading someone else's tree.

// runRef is one runlog found in the window, with the harness version that
// produced it.
type runRef struct {
	path    string
	mtime   time.Time
	version string // "" when the run's manifest carried no stamp
}

// Observe sweeps a project's runlogs for hook payloads written since
// `since`, and reports whether the fields the gates depend on are still
// present in the output of the Claude Code that wrote the most recent run.
//
// A project with no runlogs yields an unobserved report and no error:
// absence of evidence is not coverage, so a fresh checkout (or CI) skips
// rather than passes.
func Observe(projectRoot string, since time.Time) (*Report, error) {
	runs, err := discover(projectRoot, since)
	if err != nil {
		return nil, err
	}

	rep := &Report{Window: time.Since(since), Runs: len(runs)}
	bg := Observation{Field: FieldBackgroundTasks, Event: "Stop"}
	tr := Observation{Field: FieldToolResponse, Event: "PostToolUse"}
	ag := Observation{Field: FieldAgentID, Event: "SubagentStop"}
	rep.Observations = []Observation{bg, tr, ag}
	if len(runs) == 0 {
		return rep, nil
	}

	seen := map[string]bool{}
	for _, r := range runs {
		if r.version != "" {
			seen[r.version] = true
		}
	}
	for v := range seen {
		rep.Versions = append(rep.Versions, v)
	}
	sort.Strings(rep.Versions)

	// The verdict belongs to whichever Claude Code wrote the newest run —
	// mtime rather than a semver comparison, so a downgrade is judged as
	// what it is (the harness in use) instead of being ranked below the
	// version it replaced.
	newest := runs[0]
	for _, r := range runs[1:] {
		if r.mtime.After(newest.mtime) {
			newest = r
		}
	}
	rep.Judged = newest.version

	ignoredVersions := map[string]bool{}
	for _, r := range runs {
		if r.version != rep.Judged {
			rep.Ignored++
			ignoredVersions[r.version] = true
			continue
		}
		rep.Scanned++
		scanRunlog(r.path, &bg, &tr, &ag)
	}
	// Counted here rather than derived from len(Versions)-1 in Summary:
	// Versions holds only stamped versions, so unstamped runs are ignored
	// without being counted, and the summary reported "N run(s) from 0
	// other version(s)". Unstamped runs are the common case straddling an
	// upgrade, which is exactly when the line is read.
	rep.IgnoredVersions = len(ignoredVersions)
	rep.Observations = []Observation{bg, tr, ag}
	return rep, nil
}

// discover finds every runlog under <projectRoot>/_output modified since
// `since`. A missing tree is not an error — it is a project nobody has run
// ape in.
func discover(projectRoot string, since time.Time) ([]runRef, error) {
	root := runlog.ApeRoot(projectRoot)
	var runs []runRef
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is skipped, never fatal
		}
		if d.IsDir() || d.Name() != HookEventsFile {
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
		runs = append(runs, runRef{
			path:    path,
			mtime:   info.ModTime(),
			version: claudeVersionFor(path),
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("sweep %s: %w", root, err)
	}
	// Path order, so a tie on mtime resolves the same way on every run.
	sort.Slice(runs, func(i, j int) bool { return runs[i].path < runs[j].path })
	return runs, nil
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

// claudeVersionFor reads the claude_version recorded beside the run's
// hook events, so a drift report names the harness that produced it.
// Best-effort: a run that records no version simply contributes nothing.
//
// Two sources, in order. runlog.HarnessFile is written by every producer,
// because they all open a runlog.Writer; manifest.yaml is written only by
// the pipeline runner. Reading the harness stamp first is what makes
// `ape prompt` and `ape chat` runs attributable at all.
//
// The manifest fallback is not vestigial: every runlog already on disk
// predates the harness stamp, and those runs are the entire corpus for
// any project with history. Dropping it would blank out the existing
// evidence and push a healthy project to "not verified".
func claudeVersionFor(hookPath string) string {
	dir := filepath.Dir(hookPath)
	for _, name := range []string{runlog.HarnessFile, "manifest.yaml"} {
		if v := readClaudeVersion(filepath.Join(dir, name)); v != "" {
			return v
		}
	}
	return ""
}

// readClaudeVersion pulls the `claude_version:` line out of one YAML file.
// Line-scanned rather than parsed: the two files it reads share nothing
// but this key, and a full unmarshal would couple the detector to the
// pipeline manifest's schema.
func readClaudeVersion(path string) string {
	f, err := os.Open(path)
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

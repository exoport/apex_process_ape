// Package release projects the framework's release record.
//
// A **slice** is an operator-declared set of epics, living in two
// top-level keys of `sprint-status.yaml` — `release_slices:` and
// `active_slice:`. A **record** is one Markdown file per release under
// `{planning_folder}/releases/release-<id>.md`, whose frontmatter carries
// the release's asserted state. `apex-release-record` writes the record;
// `apex-sprint-sync --declare-slice` writes the tracker keys. Nothing
// here writes either.
//
// Two things make this a projection rather than a second opinion.
//
// **The status is read, never computed.** "The release's status is
// asserted in exactly one place, this `status:` field" — so a slice is
// released when its record says so, and by no other route. A tracker
// entry carries no `status:` of its own precisely so a shipped release
// cannot be counted as unshipped by a writer that only ever wrote
// `declared`.
//
// **Only the frontmatter is parsed.** The record's body carries nine
// tables `apex-release-record` assembles, including the gate table with
// its per-row PASS / RED / NOT-RUN / PENDING results. Those are that
// skill's to compute and report; re-deriving them from a Markdown table
// here would put a second source of truth behind the release's own
// verdict. What this package reports about gates is the *declaration* —
// how many, how many required — never a result. The verdict is already in
// the frontmatter: `status:` asserts it, `blocking:` says why it is not
// `prepared`, `acceptance:` says why a run that reached no verdict
// reached none. A results roll-up would add no verdict information and
// one more thing to disagree with the record.
//
// The key set is pinned against the framework's own record template at
// `apex-release-record/resources/release-record-template.md`, confirmed
// final for framework v0.16.0 by the framework session on 2026-09-05.
// Absence is normal rather than malformed for `release_notes` and the
// four conditional tag keys.
package release

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"gopkg.in/yaml.v3"
)

// Tracker keys. Both are optional and absent by default: a project that
// has never declared a slice carries neither.
const (
	SlicesKey      = "release_slices"
	ActiveSliceKey = "active_slice"
)

// RecordsDirName is the folder under `{planning_folder}` holding records.
const RecordsDirName = "releases"

// The statuses a record's frontmatter may assert. Only Released changes
// what this package derives; the rest are reported as read.
const (
	StatusDeclared            = "declared"
	StatusPrepared            = "prepared"
	StatusPreparedWithDefects = "prepared_with_defects"
	StatusReleased            = "released"
	StatusWithdrawn           = "withdrawn"
	StatusSuperseded          = "superseded"
)

// How `active_slice` resolved. The two implicit forms both mean "every
// epic not inside a released slice", and they are kept apart because a
// consumer branches differently on them: `undeclared` is a project that
// has not declared a release yet, `dangling` is a tracker naming a slice
// that is not there — a repair, not a default.
const (
	ActiveDeclared   = "declared"
	ActiveUndeclared = "undeclared"
	ActiveDangling   = "dangling"
)

// Gate is one entry of a record's `gates:` list — the declaration alone.
// The result of running it lives in the record's body table and is not
// read here.
type Gate struct {
	Name     string   `json:"name"              yaml:"name"`
	Command  string   `json:"command,omitempty" yaml:"command,omitempty"`
	Required bool     `json:"required"          yaml:"required"`
	Covers   []string `json:"covers,omitempty"  yaml:"covers,omitempty"`
	Tier     string   `json:"tier,omitempty"    yaml:"tier,omitempty"`
}

// Record is a release record's frontmatter.
//
// Present and Unreadable are the two ways a record can fail to say
// anything, and they are separate fields rather than one absent status:
// the framework's own slice resolution treats an absent record, an absent
// folder and an unreadable record alike as "not released", but only the
// third is a problem someone has to fix.
type Record struct {
	Path    string `json:"path"    yaml:"path"`
	Present bool   `json:"present" yaml:"present"`
	// Unreadable carries the parse failure when a record exists and does
	// not yield frontmatter. Never a status: a record that cannot be read
	// asserts nothing, and inventing `declared` for it would be a verdict
	// with no basis.
	Unreadable string `json:"unreadable,omitempty" yaml:"unreadable,omitempty"`

	ReleaseID        string     `json:"release_id,omitempty"         yaml:"release_id,omitempty"`
	Slice            string     `json:"slice,omitempty"              yaml:"slice,omitempty"`
	Status           string     `json:"status,omitempty"             yaml:"status,omitempty"`
	FrozenAt         string     `json:"frozen_at,omitempty"          yaml:"frozen_at,omitempty"`
	Blocking         []string   `json:"blocking"                     yaml:"blocking"`
	Acceptance       string     `json:"acceptance,omitempty"         yaml:"acceptance,omitempty"`
	Ships            []string   `json:"ships,omitempty"              yaml:"ships,omitempty"`
	ReleaseNotes     []string   `json:"release_notes,omitempty"      yaml:"release_notes,omitempty"`
	Gates            []Gate     `json:"gates,omitempty"              yaml:"gates,omitempty"`
	EvidenceFolder   string     `json:"evidence_folder,omitempty"    yaml:"evidence_folder,omitempty"`
	RepairCommitType string     `json:"repair_commit_type,omitempty" yaml:"repair_commit_type,omitempty"`
	Readiness        Readiness  `json:"readiness,omitzero"           yaml:"readiness,omitempty"`
	Overrides        []Override `json:"overrides,omitempty"          yaml:"overrides,omitempty"`

	// The four conditional tag keys. All four are absent by default, and a
	// record without them is a correct record, so all four are omitempty.
	TagAuthorization string `json:"tag_authorization,omitempty" yaml:"tag_authorization,omitempty"`
	Tagger           string `json:"tagger,omitempty"            yaml:"tagger,omitempty"`
	TagSha           string `json:"tag_sha,omitempty"           yaml:"tag_sha,omitempty"`
	// TaggerFromObject is PROVENANCE, NEVER AUTHORIZATION. It appears only
	// on a `--backfill-legacy` record, where it names whoever the git tag
	// object credits — a fact reconstructed from history, not a statement
	// anybody made about this release. Reading it as authorization would
	// let a reconstructed record assert something no human ever said, so
	// it is labelled at every point it surfaces and is never folded in
	// with TagAuthorization.
	TaggerFromObject string `json:"tagger_from_object,omitempty" yaml:"tagger_from_object,omitempty"`

	// SliceChangeLogEntries is the LENGTH of the record's append-only
	// `slice_change_log:`, emitted in place of the entries. The log is
	// history and the record file is its home: a status projection
	// carrying it would grow without bound, and one carrying part of it
	// would be a truncation nothing announced.
	SliceChangeLogEntries int `json:"slice_change_log_entries" yaml:"slice_change_log_entries"`
}

// recordDoc is the decode shape. It exists so `slice_change_log:` can be
// read without Record having a field that would re-emit it: this struct
// is what the frontmatter decodes into, Record is what callers get.
type recordDoc struct {
	Record         `yaml:",inline"`
	SliceChangeLog []any `yaml:"slice_change_log"`
}

// Readiness is the record's `readiness:` object. Absent on a record that
// has captured none, which is why every field is omitempty.
type Readiness struct {
	Report           string `json:"report,omitempty"            yaml:"report,omitempty"`
	Verdict          string `json:"verdict,omitempty"           yaml:"verdict,omitempty"`
	FindingsCritical int    `json:"findings_critical,omitempty" yaml:"findings_critical,omitempty"`
}

// Override is one `overrides:` entry, written only by `--override-owner`.
type Override struct {
	Path          string `json:"path"                    yaml:"path"`
	Authorization string `json:"authorization,omitempty" yaml:"authorization,omitempty"`
}

// GatesRequired counts the declared gates marked required.
func (r Record) GatesRequired() int {
	n := 0
	for _, g := range r.Gates {
		if g.Required {
			n++
		}
	}
	return n
}

// Leave is one declared leave-behind on a slice's tracker entry.
type Leave struct {
	Story  string `json:"story"            yaml:"story"`
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// Slice is one `release_slices:` entry, with the record that decides its
// status.
type Slice struct {
	ID         string  `json:"id"                    yaml:"id"`
	Epics      []int   `json:"epics"                 yaml:"epics"`
	DeclaredAt string  `json:"declared_at,omitempty" yaml:"declared_at,omitempty"`
	Leave      []Leave `json:"leave,omitempty"       yaml:"leave,omitempty"`
	Active     bool    `json:"active"                yaml:"active"`
	// Released is Record.Status == released, and nothing else. It is the
	// only field here derived from the record rather than read from it.
	Released bool   `json:"released" yaml:"released"`
	Record   Record `json:"record"   yaml:"record"`
	// Malformed carries a tracker entry this package could not read as a
	// slice. The entry is still listed: `apex-sprint-sync` HALTs rather
	// than regenerating a tracker whose release keys it cannot re-emit, so
	// a shape nobody can parse is a finding, not something to drop.
	Malformed string `json:"malformed,omitempty" yaml:"malformed,omitempty"`
}

// Status is the whole projection.
type Status struct {
	TrackerPath    string `json:"tracker_path"    yaml:"tracker_path"`
	TrackerPresent bool   `json:"tracker_present" yaml:"tracker_present"`
	RecordsDir     string `json:"records_dir"     yaml:"records_dir"`

	// ActiveSlice is the tracker's `active_slice:` verbatim — the empty
	// string when the key is absent, and a dangling id when it names a
	// slice `release_slices:` does not carry.
	ActiveSlice string `json:"active_slice" yaml:"active_slice"`
	// ActiveResolution is one of declared / undeclared / dangling.
	ActiveResolution string `json:"active_resolution" yaml:"active_resolution"`
	// ActiveEpics is the scope the active slice resolves to: that slice's
	// epics when one is declared, and otherwise every epic not inside a
	// released slice — the implicit resolution, so the release rules
	// always have a subject.
	ActiveEpics []int `json:"active_epics" yaml:"active_epics"`

	Slices []Slice `json:"slices" yaml:"slices"`
	// UnreleasedEpics is every epic with a tracker row that is not inside
	// a slice whose record reads `released`.
	//
	// NULL, not empty, when there is no tracker. The framework's own
	// resolution discovers epics from the epic shards and falls back to
	// "every discovered epic" on a greenfield project with no tracker —
	// byte-for-byte the old `--epics all` behaviour, so a pipeline can
	// never mint nothing by accident. This projection enumerates epics
	// from tracker rows and has no other source, so with no tracker it
	// cannot name that set. An empty list would read as "no epics", which
	// is the one answer that would be actively wrong.
	UnreleasedEpics []int `json:"unreleased_epics" yaml:"unreleased_epics"`
}

// ActiveRecord returns the record of the declared active slice, and
// whether there is one. An implicit scope has no record.
func (s Status) ActiveRecord() (Record, bool) {
	if s.ActiveResolution != ActiveDeclared {
		return Record{}, false
	}
	for i := range s.Slices {
		if s.Slices[i].ID == s.ActiveSlice {
			return s.Slices[i].Record, true
		}
	}
	return Record{}, false
}

// Project reads the tracker and every declared slice's record.
//
// An absent tracker is not an error — a project before its first sprint
// has none, and the projection is then empty rather than a failure. An
// unreadable record is likewise carried as a field, not returned as an
// error: one bad record must not stop the other slices being reported.
func Project(trackerPath, planningDir string) (*Status, error) {
	st := &Status{TrackerPath: trackerPath, ActiveResolution: ActiveUndeclared}
	if planningDir != "" {
		st.RecordsDir = filepath.Join(planningDir, RecordsDirName)
	}

	tracker, err := sprint.Load(trackerPath)
	if err != nil {
		return nil, err
	}
	st.TrackerPresent = !tracker.Missing

	st.ActiveSlice = stringValue(tracker.Raw[ActiveSliceKey])
	st.Slices = readSlices(tracker.Raw[SlicesKey], st.RecordsDir)

	byID := map[string]int{}
	for i := range st.Slices {
		byID[st.Slices[i].ID] = i
	}
	switch st.ActiveSlice {
	case "":
		st.ActiveResolution = ActiveUndeclared
	default:
		if i, ok := byID[st.ActiveSlice]; ok {
			st.ActiveResolution = ActiveDeclared
			st.Slices[i].Active = true
		} else {
			st.ActiveResolution = ActiveDangling
		}
	}

	if st.TrackerPresent {
		st.UnreleasedEpics = unreleasedEpics(sprint.EpicNumbers(tracker.Rows), st.Slices)
	}
	if st.ActiveResolution == ActiveDeclared {
		// A declared slice names its own epics, so this one answer
		// survives an absent tracker — except that without a tracker there
		// are no slices to declare, so in practice it does not arise.
		st.ActiveEpics = st.Slices[byID[st.ActiveSlice]].Epics
	} else {
		st.ActiveEpics = st.UnreleasedEpics
	}
	return st, nil
}

// readSlices decodes the `release_slices:` mapping. Slices come back in id
// order, so two runs against one tracker report the same thing.
func readSlices(raw any, recordsDir string) []Slice {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]Slice, 0, len(ids))
	for _, id := range ids {
		sl := Slice{ID: id, Epics: []int{}}
		entry, ok := m[id].(map[string]any)
		if !ok {
			sl.Malformed = "entry is not a mapping"
		} else {
			sl.Epics = intList(entry["epics"])
			sl.DeclaredAt = stringValue(entry["declared_at"])
			sl.Leave = readLeave(entry["leave"])
		}
		sl.Record = LoadRecord(recordsDir, id)
		sl.Released = sl.Record.Status == StatusReleased
		out = append(out, sl)
	}
	return out
}

func readLeave(raw any) []Leave {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []Leave
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Leave{
			Story:  stringValue(m["story"]),
			Reason: stringValue(m["reason"]),
		})
	}
	return out
}

// RecordPath is the one place that knows a record's filename.
func RecordPath(recordsDir, sliceID string) string {
	if recordsDir == "" {
		return ""
	}
	return filepath.Join(recordsDir, "release-"+sliceID+".md")
}

// LoadRecord reads one record's frontmatter.
//
// Every failure is reported on the Record rather than returned: an absent
// records folder, an absent record and an unreadable one all mean "this
// slice is not released", which is the framework's own rule, and the
// caller still needs the other slices.
func LoadRecord(recordsDir, sliceID string) Record {
	rec := Record{Path: RecordPath(recordsDir, sliceID), Blocking: []string{}}
	if rec.Path == "" {
		return rec
	}
	data, err := os.ReadFile(rec.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			rec.Present = true
			rec.Unreadable = err.Error()
		}
		return rec
	}
	rec.Present = true
	fm, _, err := frontmatter.Split(data)
	if err != nil {
		rec.Unreadable = err.Error()
		return rec
	}
	var doc recordDoc
	if err := yaml.Unmarshal(fm, &doc); err != nil {
		rec.Unreadable = fmt.Sprintf("frontmatter is not valid YAML: %v", err)
		return rec
	}
	parsed := doc.Record
	// The three fields this package synthesizes are assigned AFTER the
	// decode, so a record carrying keys by those names cannot make itself
	// look present, elsewhere, or readable.
	parsed.Path, parsed.Present, parsed.Unreadable = rec.Path, true, ""
	if parsed.Blocking == nil {
		parsed.Blocking = []string{}
	}
	parsed.SliceChangeLogEntries = len(doc.SliceChangeLog)
	return parsed
}

// unreleasedEpics is the implicit active scope: every epic with a tracker
// row that no released slice holds.
//
// An epic listed by a slice whose record is absent, unreadable, or reads
// anything other than `released` stays in the set. That asymmetry is the
// point — the only way out of the set is a record that says the epic
// shipped.
func unreleasedEpics(all []int, slices []Slice) []int {
	released := map[int]bool{}
	for i := range slices {
		if !slices[i].Released {
			continue
		}
		for _, e := range slices[i].Epics {
			released[e] = true
		}
	}
	out := make([]int, 0, len(all))
	for _, e := range all {
		if !released[e] {
			out = append(out, e)
		}
	}
	return out
}

func stringValue(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

// intList reads an `epics: [1, 2]` list. A non-integer entry is skipped
// rather than guessed at; the entry is still reported through the slice's
// own fields.
func intList(v any) []int {
	items, ok := v.([]any)
	if !ok {
		return []int{}
	}
	out := make([]int, 0, len(items))
	for _, item := range items {
		if n, ok := item.(int); ok {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

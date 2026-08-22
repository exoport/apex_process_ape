// Package sprint reads and maintains `sprint-status.yaml`, the tracker
// whose `development_status` map carries one row per story and one per
// epic.
//
// Three operations, with deliberately different contracts:
//
//   - Check   compares tracker rows against story files on disk and
//     reports divergence. It never picks a winner and always exits 0,
//     because the findings it produces are ones no tool can resolve.
//   - Verify  asserts one row landed as written, including against the
//     last committed value. A gate, with exit codes preserved from
//     verify-sprint-status-row.py.
//   - Reconcile projects an epic's status from its story rows. A
//     mutation, with a targeted line-level write and a real file lock.
package sprint

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// StatusKey is the top-level tracker key holding the row map.
const StatusKey = "development_status"

// Row kinds. Only story rows participate in the epic projection; the
// other two are classified out.
const (
	KindStory         = "story"
	KindEpic          = "epic"
	KindRetrospective = "retrospective"
	KindOther         = "other"
)

// Statuses the projection reasons about. `blocked` is deliberately not
// listed: it falls into the projection's final clause, where it holds an
// epic open.
const (
	StatusDone       = "done"
	StatusBacklog    = "backlog"
	StatusCancelled  = "cancelled"
	StatusInProgress = "in-progress"
	// StatusDrafted is the tracker's spelling of what a story file calls
	// ready-for-dev. Normalised on comparison, never rewritten.
	StatusDrafted     = "drafted"
	StatusReadyForDev = "ready-for-dev"
)

var (
	// epicRowRe matches an epic row key: `epic-12`.
	epicRowRe = regexp.MustCompile(`^epic-(\d+)$`)
	// storyRowRe matches a story row key belonging to epic N: `12-3`,
	// `12-3-slug`, `12-3_slug`. Mirrors reconcile-epic-status.py's
	// `^{N}-\d+[-_]` plus the bare `N-M` form.
	storyRowRe = regexp.MustCompile(`^(\d+)-(\d+)([-_].*)?$`)
	// retroRowRe matches a retrospective row.
	retroRowRe = regexp.MustCompile(`(?i)retrospective`)
)

// Row is one tracker entry.
type Row struct {
	Key    string `json:"key"    yaml:"key"`
	Status string `json:"status" yaml:"status"`
	Kind   string `json:"kind"   yaml:"kind"`
	// Epic is the owning epic number for a story row, or the epic's own
	// number for an epic row. Zero when neither.
	Epic int `json:"epic,omitempty" yaml:"epic,omitempty"`
}

// Tracker is a parsed sprint-status.yaml.
type Tracker struct {
	Path string
	Rows []Row
	// Raw is the decoded document, for callers that need other top-level
	// fields (updated_at, created_at).
	Raw map[string]any
	// Missing records a tracker that does not exist on disk.
	Missing bool
}

// ErrNotMapping reports a tracker whose development_status is not a map.
var ErrNotMapping = errors.New("development_status is not a mapping")

// Load parses the tracker. An absent file is not an error: a project
// before its first sprint has no tracker, and every caller here treats
// that as "nothing to compare" rather than a failure.
func Load(path string) (*Tracker, error) {
	t := &Tracker{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Missing = true
			return t, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	t.Raw = raw
	statuses, ok := raw[StatusKey]
	if !ok {
		return t, nil
	}
	m, ok := statuses.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: %w", path, ErrNotMapping)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		value := ""
		if m[k] != nil {
			value = fmt.Sprintf("%v", m[k])
		}
		t.Rows = append(t.Rows, Row{Key: k, Status: value, Kind: classify(k), Epic: rowEpic(k)})
	}
	return t, nil
}

// classify labels a row key. The classification is what keeps `epic-*`
// and `*-retrospective` rows out of the story comparison — they have no
// story file to diverge from.
func classify(key string) string {
	switch {
	case epicRowRe.MatchString(key):
		return KindEpic
	case retroRowRe.MatchString(key):
		return KindRetrospective
	case storyRowRe.MatchString(key):
		return KindStory
	default:
		return KindOther
	}
}

func rowEpic(key string) int {
	if m := epicRowRe.FindStringSubmatch(key); m != nil {
		return atoi(m[1])
	}
	if m := storyRowRe.FindStringSubmatch(key); m != nil {
		return atoi(m[1])
	}
	return 0
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// NormalizeStatus maps the tracker's vocabulary onto a story file's.
//
// The tracker writes `drafted` where a story file writes
// `ready-for-dev`. They mean the same thing, so comparing them raw would
// report a divergence on every drafted story — which is how a checker
// gets muted. Normalising on comparison is not rewriting: neither file is
// touched.
func NormalizeStatus(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), StatusDrafted) {
		return StatusReadyForDev
	}
	return strings.TrimSpace(s)
}

// Project computes an epic's status from its story rows.
//
// The rule, reproduced verbatim from reconcile-epic-status.py — an epic's
// status is not a fact any skill asserts, it is a projection:
//
//	rows   = development_status keys matching ^{N}-\d+[-_]
//	active = rows whose status is not 'cancelled'
//
//	no rows              -> leave unchanged (never close an epic with no stories)
//	no active rows       -> leave unchanged (all-cancelled is a scope decision)
//	all active 'done'    -> 'done'
//	all active 'backlog' -> 'backlog'
//	otherwise            -> 'in-progress'
//
// `blocked` lands in the final clause, so a blocked story holds its epic
// open. An unrecognised status is treated as neither done nor backlog —
// it can only ever hold an epic open, never close it — and is returned so
// the caller can name it rather than swallow it.
func Project(rows []Row, epic int) (target string, unrecognised []string) {
	active := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Kind != KindStory || r.Epic != epic {
			continue
		}
		if r.Status == StatusCancelled {
			continue
		}
		active = append(active, r.Status)
	}
	if len(active) == 0 {
		return "", nil
	}
	allDone, allBacklog := true, true
	seen := map[string]bool{}
	for _, status := range active {
		switch status {
		case StatusDone:
			allBacklog = false
		case StatusBacklog:
			allDone = false
		case StatusInProgress, "blocked", "review", StatusDrafted, StatusReadyForDev:
			allDone, allBacklog = false, false
		default:
			allDone, allBacklog = false, false
			if !seen[status] {
				seen[status] = true
				unrecognised = append(unrecognised, status)
			}
		}
	}
	sort.Strings(unrecognised)
	switch {
	case allDone:
		return StatusDone, unrecognised
	case allBacklog:
		return StatusBacklog, unrecognised
	default:
		return StatusInProgress, unrecognised
	}
}

// HasStoryRows reports whether the epic has any story rows at all, which
// the projection needs to distinguish "no rows" from "no active rows".
func HasStoryRows(rows []Row, epic int) bool {
	for _, r := range rows {
		if r.Kind == KindStory && r.Epic == epic {
			return true
		}
	}
	return false
}

// EpicNumbers lists every epic that has a row or a story, ascending.
func EpicNumbers(rows []Row) []int {
	seen := map[int]bool{}
	for _, r := range rows {
		if r.Epic > 0 {
			seen[r.Epic] = true
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// RowStatus returns a row's status and whether the key exists.
func (t *Tracker) RowStatus(key string) (string, bool) {
	for _, r := range t.Rows {
		if r.Key == key {
			return r.Status, true
		}
	}
	return "", false
}

// TopLevelString reads a top-level scalar (updated_at, created_at).
func (t *Tracker) TopLevelString(key string) string {
	if t.Raw == nil {
		return ""
	}
	if v, ok := t.Raw[key]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

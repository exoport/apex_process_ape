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
	"path/filepath"
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
	// canonicalStoryRowRe is storyRowRe's stricter half: the shape the
	// framework actually writes, `N-M` followed by a separator and a slug,
	// because a row key IS a story key and a story key IS a file stem.
	//
	// The gap between the two patterns is deliberate and is a live hazard
	// while the Python it replaces still ships — see BareStoryRowKeys.
	canonicalStoryRowRe = regexp.MustCompile(`^\d+-\d+[-_].+$`)
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

// IsBareStoryRowKey reports a story row key written as `N-M` with no
// separator and slug after it — `2-1` rather than `2-1_refuse-a-long-name`.
//
// Such a key is non-conforming twice over, and the second one is invisible:
//
//  1. A row key IS the story file's stem, so `2-1` claims a file named
//     `2-1.md`. On a framework-authored project no such file exists, because
//     `apex-create-story` writes `{story_key}.md` and a story key carries a
//     slug.
//  2. **ape and the script it is replacing disagree about it.**
//     `reconcile-epic-status.py` matches story rows with `^(\d+)-\d+[-_]`,
//     which REQUIRES the separator, so a bare row is invisible to it and
//     contributes nothing to its epic's projection. ape's pattern makes the
//     separator optional, so the same row does contribute. Both are
//     defensible; they are not the same. Until the retirement lands, the
//     same tracker reconciles differently depending on which one ran — and
//     nothing about the output of either would tell you that happened.
//
// So ape says it, rather than quietly being the more useful of the two.
func IsBareStoryRowKey(key string) bool {
	return storyRowRe.MatchString(key) && !canonicalStoryRowRe.MatchString(key)
}

// BareStoryRowKeys lists the story rows whose keys are bare, in row order.
func BareStoryRowKeys(rows []Row) []string {
	var out []string
	for _, r := range rows {
		if r.Kind == KindStory && IsBareStoryRowKey(r.Key) {
			out = append(out, r.Key)
		}
	}
	return out
}

// BareRowKeyRemediation is the fix for a caller that CANNOT LOOK — the one
// that has not read the implementation folder and so does not know whether a
// story file matches the row.
//
// That is exactly one caller: `ape sprint reconcile`, which is tracker-only
// by design (no story-file reads, D12). Its "ask sprint check" tail is honest
// there and only there, which is why RemediationFor never returns this
// constant — a command that has already looked and found nothing must say so,
// not send the reader to re-run the command they are already reading the
// output of.
//
// The example is `N-M` and not a real-looking key on purpose. An earlier
// version carried a worked example (`2-1` -> `2-1_refuse-an-over-long-name`),
// which read as specific and was therefore wrong on every project whose bare
// row was not `2-1`: the reader was handed a rename for a row that appears
// nowhere in the finding beside it.
const BareRowKeyRemediation = "rename the row to its story file's stem " +
	"(`N-M` -> `N-M_slug`), or rename the story file to match the row; " +
	"`ape sprint check` names the specific pair"

// MatchesBareRowKey reports whether a story file's stem is a slugged form of
// a bare row key — `7-3_payment-retry` for `7-3`.
//
// The separator check is what keeps `1-10_x` from matching `1-1`: a prefix
// test alone would claim epic 1 story 10 as a candidate for story 1.
func MatchesBareRowKey(rowKey, stem string) bool {
	if !strings.HasPrefix(stem, rowKey) || len(stem) <= len(rowKey) {
		return false
	}
	sep := stem[len(rowKey)]
	return sep == '-' || sep == '_'
}

// RemediationFor states the fix for one bare row key, for a caller that HAS
// looked — so all three of its branches report a fact this run established,
// including the branch where the fact is "nothing matches".
//
// This is the difference between a payload a caller can act on and one it has
// to re-derive: the same run already knows that `7-3` and
// `7-3_payment-retry.md` are one story, so saying so costs nothing and saying
// something generic instead throws that away.
//
// Two candidates is not a worse version of one — it is a different problem.
// The row cannot be renamed to both, so the answer is a judgment about which
// story it meant (or that both need their own row), and the message says that
// rather than picking.
func RemediationFor(rowKey string, candidates []string) string {
	switch len(candidates) {
	case 0:
		// NOT BareRowKeyRemediation. That constant ends by pointing at
		// `ape sprint check`, which is the command printing this — having
		// already searched and found nothing. Promising a specific pair from
		// a re-run that will say the same thing is worse than saying less.
		return fmt.Sprintf(
			"no story file matches this row — nothing under the implementation folder has a "+
				"stem of `%s` plus a separator and slug. Either the story is filed under an "+
				"unrelated name, in which case rename the row to that file's stem, or the "+
				"story does not exist and the row is stale",
			rowKey,
		)
	case 1:
		return fmt.Sprintf(
			"rename the row `%s` to `%s` to match the story file, or rename `%s.md` to `%s.md`",
			rowKey, candidates[0], candidates[0], rowKey,
		)
	default:
		return fmt.Sprintf(
			"%d story files could be this row (%s) — rename the row to whichever it means, "+
				"and give the others a row of their own",
			len(candidates), strings.Join(candidates, ", "),
		)
	}
}

// LockSuffix is appended to the tracker path to name the advisory-lock
// sidecar. Exported because the file OUTLIVES the run that made it: nothing
// unlinks it — releasing the lock and deleting the file are different acts,
// and deleting one another process may be waiting on is how you lose the
// mutual exclusion the lock exists for — so it lands in the project as a
// permanent, untracked artifact that wants a `.gitignore` entry.
//
// `ape doctor`'s sprint.lock_ignored row is what makes that visible. One
// definition here so the locker, the doctor check and the remediation text
// cannot drift apart.
const LockSuffix = ".lock"

// LockPath is the advisory-lock sidecar for a tracker.
func LockPath(trackerPath string) string { return trackerPath + LockSuffix }

// StoryKeyFromPath derives a story's tracker key from its file path.
//
// This is the join between the two halves of a project's story state, and
// getting it wrong is silent: the tracker keys its rows on the STORY KEY —
// the file's stem, because the framework writes stories to
// `{implementation_folder}/{{story_key}}.md` and rows the tracker on the
// same `{{story_key}}` — while a story file's frontmatter carries a
// separate, DOTTED `story_id`:
//
//	development_status:
//	  1-1_greet-a-name-from-the-domain: done     # the story key
//
//	---
//	story_id: "1.1"                              # NOT the story key
//
// Joining on `story_id` matches nothing on a real project, so every row
// reports as having no story file and every story as having no row: a
// checker that is 100% false positives and can never find the status
// divergence it exists to find.
func StoryKeyFromPath(path string) string {
	base := filepath.Base(filepath.FromSlash(path))
	return strings.TrimSuffix(base, filepath.Ext(base))
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

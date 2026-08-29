package sprint

import "sort"

// Summary is the whole sprint reduced to what a status view shows: counts by
// status, what is in flight, and what is waiting on a person.
//
// A LIBRARY FUNCTION, deliberately, and not part of whatever draws it. The
// derivation is arithmetic over the tracker, and the moment a second consumer
// wants it — a TUI, a status line, a hook — the alternative is a second
// implementation that can disagree with this one. A status view that is
// quietly wrong is worse than none, because the reader stops checking the
// thing it replaced.
//
// Nothing here reads a file, a story body or the deferred store. The tracker
// is the only input, which is what makes it cheap enough to run on every
// `sprint reconcile` and impossible to disagree with the tracker.
type Summary struct {
	// StoryCounts is keyed by NORMALISED status, and carries a zero for every
	// status in SummaryStatuses. Zeros are present on purpose: a board that
	// hides "blocked: 0" and "blocked: 4" the same way when the key is missing
	// cannot be read at a glance.
	StoryCounts map[string]int `json:"storyCounts" yaml:"storyCounts"`
	// StoryUnknown counts rows whose status is outside the vocabulary, keyed by
	// the status as written. Surfaced rather than folded into a total: an
	// unrecognised status is a tracker problem, and averaging it away is how it
	// survives.
	StoryUnknown map[string]int `json:"storyUnknown,omitempty" yaml:"storyUnknown,omitempty"`
	Stories      int            `json:"stories"                yaml:"stories"`

	// EpicCounts is keyed by DERIVED status — Project's answer, not the row's
	// recorded one — falling back to the recorded status only where the
	// projection declines to answer (an epic with no stories, or none active).
	EpicCounts map[string]int `json:"epicCounts" yaml:"epicCounts"`
	Epics      int            `json:"epics"      yaml:"epics"`

	// InFlight is every story not in a terminal state, the panel someone
	// actually watches during a batch run.
	InFlight []StoryRef `json:"inFlight,omitempty" yaml:"inFlight,omitempty"`
	// Attention is what needs a person. See the doc on Summarize for what this
	// can and cannot see.
	Attention []StoryRef `json:"attention,omitempty" yaml:"attention,omitempty"`
}

// StoryRef is one story row, flattened for display.
type StoryRef struct {
	Key    string `json:"key"    yaml:"key"`
	Status string `json:"status" yaml:"status"`
	Epic   int    `json:"epic"   yaml:"epic"`
}

// StatusBlocked and StatusReview complete the vocabulary the tracker uses.
// The projection in Project() reasons about the others by name and lets these
// two fall through its final clause, so they have no constants there.
const (
	StatusBlocked = "blocked"
	StatusReview  = "review"
)

// SummaryStatuses is the display order, which is workflow order rather than
// alphabetical — and blocked and cancelled sit at the end as their own cells
// rather than folded into a total, because a sprint of nothing but blocked
// work must not read as healthy.
var SummaryStatuses = []string{
	StatusBacklog,
	StatusReadyForDev,
	StatusInProgress,
	StatusReview,
	StatusDone,
	StatusBlocked,
	StatusCancelled,
}

// terminal statuses: work that has stopped moving, one way or the other.
func terminal(status string) bool {
	return status == StatusDone || status == StatusCancelled
}

// Summarize reduces tracker rows to a Summary.
//
// What it can see is exactly what `sprint-status.yaml` holds: a row's key,
// status, kind and epic. What it CANNOT see is worth naming, because the
// framework's dashboard request asks for two of them:
//
//   - `reopened_by` lives in a story's frontmatter, not the tracker.
//   - parked patches live in the deferred store.
//
// Both would need a corpus read, and this runs on a command called many times
// per run whose cost must stay near zero. So Attention reports what the
// tracker knows — blocked stories — and the seam for the rest is a cheap
// index, not a scan bolted onto this.
func Summarize(rows []Row) Summary {
	s := Summary{
		StoryCounts: make(map[string]int, len(SummaryStatuses)),
		EpicCounts:  map[string]int{},
	}
	for _, name := range SummaryStatuses {
		s.StoryCounts[name] = 0
	}

	known := make(map[string]bool, len(SummaryStatuses))
	for _, name := range SummaryStatuses {
		known[name] = true
	}

	for _, r := range rows {
		if r.Kind != KindStory {
			continue
		}
		s.Stories++
		status := NormalizeStatus(r.Status)
		ref := StoryRef{Key: r.Key, Status: status, Epic: r.Epic}
		switch {
		case known[status]:
			s.StoryCounts[status]++
		default:
			if s.StoryUnknown == nil {
				s.StoryUnknown = map[string]int{}
			}
			s.StoryUnknown[status]++
		}
		if !terminal(status) {
			s.InFlight = append(s.InFlight, ref)
		}
		if status == StatusBlocked {
			s.Attention = append(s.Attention, ref)
		}
	}

	for _, epic := range EpicNumbers(rows) {
		s.Epics++
		derived, _ := Project(rows, epic)
		if derived == "" {
			// The projection declines on an epic with no stories, or none
			// active — both are "leave it alone" rather than a status. Fall
			// back to what the row records, so the count still adds up.
			derived = recordedEpicStatus(rows, epic)
		}
		if derived == "" {
			derived = "unset"
		}
		s.EpicCounts[NormalizeStatus(derived)]++
	}

	sortRefs(s.InFlight)
	sortRefs(s.Attention)
	return s
}

// recordedEpicStatus is the epic row's own status, for the cases where the
// projection declines to derive one.
func recordedEpicStatus(rows []Row, epic int) string {
	for _, r := range rows {
		if r.Kind == KindEpic && r.Epic == epic {
			return r.Status
		}
	}
	return ""
}

// sortRefs orders by epic then key, so a refresh that changes nothing
// produces a byte-identical tab and the board records no write.
func sortRefs(refs []StoryRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Epic != refs[j].Epic {
			return refs[i].Epic < refs[j].Epic
		}
		return refs[i].Key < refs[j].Key
	})
}

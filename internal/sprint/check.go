package sprint

import (
	"fmt"
	"sort"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/story"
)

// Check names for the divergence report.
const (
	CheckRowWithoutStory   = "sprint.row_without_story"
	CheckStoryWithoutRow   = "sprint.story_without_row"
	CheckStatusDivergence  = "sprint.status_divergence"
	CheckTrackerUnreadable = "sprint.tracker_unreadable"
)

// Finding is one divergence. It names both sides and picks neither.
type Finding struct {
	Check   string `json:"check"             yaml:"check"`
	Key     string `json:"key,omitempty"     yaml:"key,omitempty"`
	Path    string `json:"path,omitempty"    yaml:"path,omitempty"`
	Tracker string `json:"tracker,omitempty" yaml:"tracker,omitempty"`
	Story   string `json:"story,omitempty"   yaml:"story,omitempty"`
	Message string `json:"message"           yaml:"message"`
}

// CheckReport is the payload of `ape sprint check`.
type CheckReport struct {
	Tracker  string       `json:"tracker"  yaml:"tracker"`
	Findings []Finding    `json:"findings" yaml:"findings"`
	Summary  CheckSummary `json:"summary"  yaml:"summary"`
}

// CheckSummary aggregates the comparison.
type CheckSummary struct {
	StoryRows     int            `json:"story_rows"         yaml:"story_rows"`
	EpicRows      int            `json:"epic_rows"          yaml:"epic_rows"`
	RetroRows     int            `json:"retrospective_rows" yaml:"retrospective_rows"`
	OtherRows     int            `json:"other_rows"         yaml:"other_rows"`
	StoriesOnDisk int            `json:"stories_on_disk"    yaml:"stories_on_disk"`
	Findings      int            `json:"findings"           yaml:"findings"`
	ByCheck       map[string]int `json:"by_check,omitempty" yaml:"by_check,omitempty"`
}

// OK reports whether tracker and disk agree.
func (r *CheckReport) OK() bool { return len(r.Findings) == 0 }

// RunCheck set-compares the tracker against story files on disk and
// compares each row's status against that story's own frontmatter.
//
// It reports divergence and NEVER picks a winner: which side is right is
// judgment, and a tool that guessed would be wrong often enough to be
// worse than silence. That is also why the caller always exits 0 — these
// findings are for a person, and wiring them into a build loop would stop
// runs over something no tool can fix.
func RunCheck(cfg *apexcfg.Resolved) (*CheckReport, error) {
	report := &CheckReport{Tracker: cfg.Paths.SprintStatus}
	tracker, loadErr := Load(cfg.Paths.SprintStatus)
	if loadErr != nil {
		// A tracker that exists but does not parse is one FINDING, not a
		// failed command. Returning the error would make `ape sprint check`
		// exit non-zero, and this command's whole contract is that it
		// always exits 0 — the divergence report is for a person.
		report.Findings = append(report.Findings, Finding{
			Check:   CheckTrackerUnreadable,
			Path:    cfg.Paths.SprintStatus,
			Message: loadErr.Error(),
		})
		report.tally()
		return report, nil //nolint:nilerr // deliberate: the parse failure IS the finding, and this command always exits 0
	}
	if tracker.Missing {
		report.tally()
		return report, nil
	}

	stories := map[string]string{} // story key -> status from its own file
	paths := map[string]string{}
	if cfg.Paths.Implementation != "" {
		heads, err := story.ScanHeads(cfg.Paths.Implementation)
		if err != nil {
			return nil, err
		}
		for _, h := range heads {
			if !h.IsStory() {
				continue
			}
			key := h.StoryID()
			status := ""
			if v, ok := h.Raw["status"]; ok && v != nil {
				status = fmt.Sprintf("%v", v)
			}
			stories[key] = status
			paths[key] = h.Path
		}
	}
	report.Summary.StoriesOnDisk = len(stories)

	for _, row := range tracker.Rows {
		switch row.Kind {
		case KindEpic:
			report.Summary.EpicRows++
			continue
		case KindRetrospective:
			report.Summary.RetroRows++
			continue
		case KindOther:
			report.Summary.OtherRows++
			continue
		case KindStory:
			report.Summary.StoryRows++
		}
		fileStatus, onDisk := stories[row.Key]
		if !onDisk {
			report.Findings = append(report.Findings, Finding{
				Check:   CheckRowWithoutStory,
				Key:     row.Key,
				Tracker: row.Status,
				Message: "tracker row has no story file claiming that story_id",
			})
			continue
		}
		if NormalizeStatus(row.Status) != NormalizeStatus(fileStatus) {
			report.Findings = append(report.Findings, Finding{
				Check:   CheckStatusDivergence,
				Key:     row.Key,
				Path:    paths[row.Key],
				Tracker: row.Status,
				Story:   fileStatus,
				Message: "tracker and story file disagree — neither is assumed correct",
			})
		}
	}

	rowKeys := map[string]bool{}
	for _, row := range tracker.Rows {
		rowKeys[row.Key] = true
	}
	missing := make([]string, 0)
	for key := range stories {
		if !rowKeys[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		report.Findings = append(report.Findings, Finding{
			Check:   CheckStoryWithoutRow,
			Key:     key,
			Path:    paths[key],
			Story:   stories[key],
			Message: "story file has no tracker row",
		})
	}

	sortCheckFindings(report.Findings)
	report.tally()
	return report, nil
}

func (r *CheckReport) tally() {
	r.Summary.Findings = len(r.Findings)
	if len(r.Findings) == 0 {
		return
	}
	r.Summary.ByCheck = map[string]int{}
	for _, f := range r.Findings {
		r.Summary.ByCheck[f.Check]++
	}
}

func sortCheckFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Check != findings[j].Check {
			return findings[i].Check < findings[j].Check
		}
		return findings[i].Key < findings[j].Key
	})
}

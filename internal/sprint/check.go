package sprint

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"github.com/exoport/apex_process_ape/internal/story"
)

// Check names for the divergence report.
const (
	CheckRowWithoutStory   = "sprint.row_without_story"
	CheckStoryWithoutRow   = "sprint.story_without_row"
	CheckStatusDivergence  = "sprint.status_divergence"
	CheckTrackerUnreadable = "sprint.tracker_unreadable"
	// CheckNonStandardRowKey is a story row key written as `N-M` with no
	// separator and slug. It is reported rather than resolved because the
	// two possible fixes — rename the row, or rename the file — are a
	// judgment about which name is the right one.
	CheckNonStandardRowKey = "sprint.nonstandard_row_key"
	// CheckEpicWithoutRetro is an epic with no retrospective row.
	//
	// It exists because counting rows cannot answer the question. A
	// framework migration's check asked "is there a retrospective row per
	// epic" and the only datum available was
	// `retrospective_rows >= epic_rows`, which two retros on one epic and
	// none on another satisfies. A migration whose check can report
	// applied while the thing it checks is false is worse than one with no
	// check at all: the runner writes the id to the ledger and never looks
	// again, so a false "applied" is permanent.
	CheckEpicWithoutRetro = "sprint.epic_without_retro"
	// CheckEpicProjectionDivergence is an `epic-N` row whose asserted
	// status disagrees with the projection of that epic's story rows.
	//
	// Report-only, like every class here, and for a sharper reason than
	// the others: an epic row is DERIVED. The framework's operating rules
	// say "never set, close, or reopen an epic row by hand", so unlike a
	// status divergence — where which side is right is genuine judgment —
	// this one has exactly one correct resolution, and it is mechanical.
	// Which is also why there is no --fix here: `ape sprint reconcile`
	// already owns the write, and a second writer of the same rows is how
	// two tools start disagreeing about a value neither of them decides.
	CheckEpicProjectionDivergence = "sprint.epic_projection_divergence"
)

// EpicProjectionRemediation is the fix for an epic-projection
// divergence, stated once so the finding, the framework's doctor routing
// prose and any future caller cannot drift apart.
//
// It names a command and a skill and NOT a hand edit, deliberately: the
// tracker's own header says "DERIVED — never edit an epic row by hand",
// and a remediation that suggested otherwise would contradict the file it
// is printed about.
const EpicProjectionRemediation = "epic rows are derived, not asserted: re-derive with " +
	"`ape sprint reconcile` (the apex-sprint-sync skill runs it). Never hand-edit an epic row."

// Finding is one divergence. It names both sides and picks neither.
type Finding struct {
	Check string `json:"check"         yaml:"check"`
	Key   string `json:"key,omitempty" yaml:"key,omitempty"`
	// StoryID is the story file's own `story_id`, carried alongside Key
	// because the two are DIFFERENT strings: Key is the story key (the file
	// stem, which is what the tracker rows on), while story_id is the dotted
	// form ("1.1"). A report that showed only one of them would send a
	// reader looking for a file that does not exist under that name.
	StoryID string `json:"story_id,omitempty" yaml:"story_id,omitempty"`
	Path    string `json:"path,omitempty"     yaml:"path,omitempty"`
	Tracker string `json:"tracker,omitempty"  yaml:"tracker,omitempty"`
	Story   string `json:"story,omitempty"    yaml:"story,omitempty"`
	// Candidates are the story keys a nonstandard_row_key finding could be
	// naming, structured rather than only spelled out in Message.
	//
	// It carries the two-candidate case in particular. Deciding WHICH of two
	// files a row meant is judgment and this command does not make it — but
	// "we do not decide" is not a reason to make the decider parse prose to
	// learn what the options were. One candidate populates this too, so a
	// consumer reads one field regardless of which branch it landed in.
	Candidates []string `json:"candidates,omitempty" yaml:"candidates,omitempty"`
	// Projected is the value an epic-projection divergence COMPUTED, to
	// sit beside Tracker's asserted one. A separate field rather than
	// reusing Story, which means "the story file's own value" everywhere
	// else — a consumer reading a projection out of it would be reading a
	// field that names the wrong source.
	//
	// omitempty, so no other class's payload gains a key.
	Projected string `json:"projected,omitempty" yaml:"projected,omitempty"`
	Message   string `json:"message"             yaml:"message"`
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
	// findings is non-nil from the start so an empty report marshals as
	// `"findings": []` and never `null`. A caller that iterates the array
	// meets a list on a clean project instead of a null it has to
	// special-case — the deferred-repair skill is explicitly told to take
	// its work list from one of these payloads rather than globbing.
	report := &CheckReport{Tracker: cfg.Paths.SprintStatus, Findings: []Finding{}}
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

	disk, err := scanStoryFiles(cfg.Paths.Implementation, tracker.Rows)
	if err != nil {
		return nil, err
	}
	stories, ids, paths, statusless := disk.status, disk.ids, disk.paths, disk.statusless
	report.Summary.StoriesOnDisk = len(stories)

	// claimed holds story keys already accounted for by a nonstandard_row_key
	// finding, so the story_without_row pass below does not report them a
	// second time under a different diagnosis.
	claimed := map[string]bool{}

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
		if IsBareStoryRowKey(row.Key) {
			// One finding, carrying every half: why the row is wrong, why it
			// matters before the retirement, and the actual rename.
			//
			// Reported INSTEAD of row_without_story, and — when exactly one
			// story file is this row's — instead of that file's
			// story_without_row too. The bare key is the single cause of both
			// lookups failing, so emitting either of the others would tell the
			// reader to create a file that should not exist or add a row that
			// already does. Suppressing them is the whole point; the first cut
			// of this check only suppressed the row side, which is the same
			// misdirection arriving from the other direction.
			candidates := bareRowCandidates(row.Key, stories)
			f := Finding{
				Check:      CheckNonStandardRowKey,
				Key:        row.Key,
				Tracker:    row.Status,
				Candidates: candidates,
				Message: "row key has no separator and slug, so it names no story file — and " +
					"`ape sprint reconcile` counts it toward epic " + strconv.Itoa(row.Epic) +
					" while the reconcile-epic-status.py it replaces does not. " +
					RemediationFor(row.Key, candidates),
			}
			if len(candidates) == 1 {
				// One unambiguous owner: name it, claim it, and show its own
				// status beside the row's so both sides are visible — this
				// finding is standing in for the two it suppressed.
				claimed[candidates[0]] = true
				f.StoryID = ids[candidates[0]]
				f.Path = paths[candidates[0]]
				f.Story = stories[candidates[0]]
			}
			report.Findings = append(report.Findings, f)
			continue
		}
		fileStatus, onDisk := stories[row.Key]
		if path, present := statusless[row.Key]; present {
			report.Findings = append(report.Findings, Finding{
				Check:   CheckRowWithoutStory,
				Key:     row.Key,
				Path:    path,
				Tracker: row.Status,
				Message: row.Key + ".md is on disk but states no status — " +
					"neither a frontmatter status nor a body Status: line",
			})
			continue
		}
		if !onDisk {
			report.Findings = append(report.Findings, Finding{
				Check:   CheckRowWithoutStory,
				Key:     row.Key,
				Tracker: row.Status,
				Message: "tracker row has no " + row.Key + ".md under the implementation folder",
			})
			continue
		}
		if NormalizeStatus(row.Status) != NormalizeStatus(fileStatus) {
			report.Findings = append(report.Findings, Finding{
				Check:   CheckStatusDivergence,
				Key:     row.Key,
				StoryID: ids[row.Key],
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
		if !rowKeys[key] && !claimed[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		report.Findings = append(report.Findings, Finding{
			Check:   CheckStoryWithoutRow,
			Key:     key,
			StoryID: ids[key],
			Path:    paths[key],
			Story:   stories[key],
			Message: "story file has no tracker row",
		})
	}

	report.Findings = append(report.Findings, epicsWithoutRetro(tracker.Rows)...)
	report.Findings = append(report.Findings, epicProjectionDivergences(tracker.Rows)...)

	sortCheckFindings(report.Findings)
	report.tally()
	return report, nil
}

// storyFiles is what the implementation folder says about each story key.
type storyFiles struct {
	status map[string]string // story key -> status from its own file
	ids    map[string]string // story key -> its frontmatter story_id
	paths  map[string]string
	// statusless holds tracker-claimed files that state no status anywhere —
	// no frontmatter `status`, no body `Status:` line. Still reported with
	// the missing rows, as the framework counts them, but not as a file
	// that is not there.
	statusless map[string]string
}

// scanStoryFiles reads the story files under the implementation folder.
//
// A story is a file that declares story_id, OR one whose name a tracker
// story row claims. The second is the brownfield story (what
// apex-lift-project leaves behind): no frontmatter at all, its status on a
// body line. Seeing only the first branch reported such a story as
// "tracker row has no <key>.md" with the file right there. The claim is
// bounded by the tracker, so an unclaimed frontmatter-less file — a README —
// is still not a story.
func scanStoryFiles(implDir string, rows []Row) (storyFiles, error) {
	out := storyFiles{
		status: map[string]string{}, ids: map[string]string{},
		paths: map[string]string{}, statusless: map[string]string{},
	}
	if implDir == "" {
		return out, nil
	}
	scan, err := story.ScanHeads(implDir)
	if err != nil {
		return out, err
	}
	heads := scan.Heads
	rowKeys := map[string]bool{}
	for _, row := range rows {
		if row.Kind == KindStory {
			rowKeys[row.Key] = true
		}
	}
	for _, h := range heads {
		key := StoryKeyFromPath(h.Path)
		noFrontmatter := errors.Is(h.Err, frontmatter.ErrNoFrontmatter)
		if !h.IsStory() && (!rowKeys[key] || (h.Err != nil && !noFrontmatter)) {
			continue
		}
		status := ""
		if v, ok := h.Raw["status"]; ok && v != nil {
			status = fmt.Sprintf("%v", v)
		}
		if noFrontmatter {
			if st, readErr := story.ReadBodyStatus(h.Abs); readErr == nil {
				status = st
			}
		}
		if status == "" && !h.IsStory() {
			out.statusless[key] = h.Path
			continue
		}
		out.status[key] = status
		out.ids[key] = h.StoryID()
		out.paths[key] = h.Path
	}
	return out, nil
}

// epicProjectionDivergences reports every `epic-N` row whose asserted
// status disagrees with the projection of that epic's story rows.
//
// It calls the SAME Project that `ape sprint reconcile` writes from,
// rather than reimplementing the rule. That is the whole design: the
// report and the write cannot disagree about which epics are drifting,
// because a key one side counts and the other does not is exactly how
// they drift apart. It also means this class inherits Project's decisions
// for free — cancelled rows dropped from the active set, `blocked`
// holding an epic open, and a bare `N-M` row counted toward its epic
// (which reconcile discloses in BareRowKeys and the script it replaced
// did not count at all).
//
// Two silences, both deliberate:
//
//   - An epic with no story rows, or none that are active, has NO
//     projection — Project returns "" — so there is nothing to disagree
//     with. Reporting those would fire on every epic mid-mint and on
//     every all-cancelled epic, which is a scope decision rather than
//     drift.
//   - An epic with story rows but no `epic-N` row asserts nothing, so
//     nothing can diverge. The absent header is a different finding with
//     a different fix, and inventing a divergence against a value nobody
//     wrote would send a reader to correct a row that does not exist.
func epicProjectionDivergences(rows []Row) []Finding {
	asserted := map[int]string{}
	for _, r := range rows {
		if r.Kind == KindEpic && r.Epic > 0 {
			asserted[r.Epic] = r.Status
		}
	}

	var findings []Finding
	for _, epic := range EpicNumbers(rows) {
		current, declared := asserted[epic]
		if !declared {
			continue // nothing asserted, so nothing to diverge from
		}
		target, unrecognised := Project(rows, epic)
		if target == "" {
			continue // no rows, or no active rows: no projection exists
		}
		if strings.EqualFold(NormalizeEpicStatus(current), target) {
			continue
		}

		msg := fmt.Sprintf(
			"epic row asserts %q but its story rows project %q — %s",
			current, target, EpicProjectionRemediation,
		)
		if len(unrecognised) > 0 {
			// Named rather than swallowed, for the same reason reconcile
			// names them: an unrecognised status can only ever hold an epic
			// open, so it may be the whole cause of the divergence, and a
			// reader who cannot see it would read the projection as wrong.
			msg += " Unrecognised story status(es) held this epic open: " +
				strings.Join(unrecognised, ", ") + "."
		}

		findings = append(findings, Finding{
			Check:     CheckEpicProjectionDivergence,
			Key:       "epic-" + strconv.Itoa(epic),
			Tracker:   current,
			Projected: target,
			Message:   msg,
		})
	}
	return findings
}

// epicsWithoutRetro reports every epic carrying no retrospective row.
//
// The subject is the EPIC SET, not a row count. An epic is in it when the
// tracker has an `epic-N` row or any story row belonging to N, so an epic
// mid-mint with rows but no header is still asked the question.
//
// Like every other class here it reports and never resolves: whether a
// missing retrospective should be minted, waived, or is simply not due yet
// is the retrospective ceremony's call and not a checker's.
func epicsWithoutRetro(rows []Row) []Finding {
	epics := map[int]bool{}
	withRetro := map[int]bool{}
	for i := range rows {
		r := &rows[i]
		switch r.Kind {
		case KindEpic, KindStory:
			if r.Epic > 0 {
				epics[r.Epic] = true
			}
		case KindRetrospective:
			if n := retroRowEpic(r.Key); n > 0 {
				withRetro[n] = true
			}
		}
	}
	numbers := make([]int, 0, len(epics))
	for n := range epics {
		if !withRetro[n] {
			numbers = append(numbers, n)
		}
	}
	sort.Ints(numbers)

	out := make([]Finding, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, Finding{
			Check: CheckEpicWithoutRetro,
			Key:   "epic-" + strconv.Itoa(n),
			Message: "epic " + strconv.Itoa(n) + " has no retrospective row — expected " +
				"`epic-" + strconv.Itoa(n) + "-retrospective`",
		})
	}
	return out
}

// retroRowEpic reads the epic number out of a retrospective row key.
//
// Separate from rowEpic because the two patterns it tries do not match a
// retro key at all: `^epic-(\d+)$` is anchored at the end, and the story
// pattern needs a leading digit. A retro row therefore carries Epic 0
// today, which is harmless for the projection (it never reads them) and
// is exactly what this check needs.
//
// Zero when the key names no epic, and that is reported as "this epic has
// no retro" rather than silently crediting an unattributable row to
// whichever epic is missing one.
func retroRowEpic(key string) int {
	m := retroEpicRe.FindStringSubmatch(key)
	if m == nil {
		return 0
	}
	return atoi(m[1])
}

// bareRowCandidates lists the story keys on disk that a bare row key could be
// naming, sorted so a two-candidate message reads the same way twice.
func bareRowCandidates(rowKey string, stories map[string]string) []string {
	var out []string
	for key := range stories {
		if MatchesBareRowKey(rowKey, key) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (r *CheckReport) tally() {
	r.Summary.Findings = len(r.Findings)
	if len(r.Findings) == 0 {
		return
	}
	r.Summary.ByCheck = map[string]int{}
	for i := range r.Findings {
		r.Summary.ByCheck[r.Findings[i].Check]++
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

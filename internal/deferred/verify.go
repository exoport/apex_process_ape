package deferred

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Check names. The store's `verify` carries BOTH halves of what upstream
// called `lint`: hard invariants and heuristics. The heuristics are tagged
// `confidence: candidate` and documented as never auto-actionable, which
// is the distinction a second verb name would have carried.
const (
	CheckSchema       = "deferred.schema"
	CheckDanglingRef  = "deferred.dangling_ref"
	CheckUnparsable   = "deferred.unparseable"
	CheckFreeForm     = "deferred.free_form"
	CheckDeadAnchor   = "deferred.dead_anchor"
	CheckDuplicate    = "deferred.duplicate_candidate"
	CheckTriggerFired = "deferred.trigger_may_have_fired"
	CheckClosureMark  = "deferred.closure_marker_in_open_record"
	// CheckSupersededMark is the OPPOSITE verdict to CheckClosureMark, and it
	// is a separate check name rather than a nuance inside that one because
	// the two demand opposite remediations: a closure marker means the work
	// was done and only an operator may `close` it, while a supersession
	// means it was overtaken and never done, which is a `discard`.
	//
	// While one code covered both, a consumer had to re-open the record body
	// and re-derive the distinction from prose — and the framework's repair
	// skill, the only consumer, got it backwards on its first attempt and
	// routed superseded records at `close`. That stamps a delivery that never
	// happened, which is the exact defect `discarded` exists to prevent.
	// Verify reads structure everywhere else; handing back prose to be
	// re-parsed was the one place it did not.
	CheckSupersededMark = "deferred.superseded_marker_in_open_record"
)

// Confidence levels. `candidate` findings are heuristics: a person or a
// skill decides, and nothing in ape ever acts on them. Closing a record
// requires re-verifying its premises against HEAD, which is judgment.
const (
	ConfidenceCertain   = "certain"
	ConfidenceCandidate = "candidate"
)

// Finding is one thing to look at.
type Finding struct {
	Check      string `json:"check"        yaml:"check"`
	ID         string `json:"id,omitempty" yaml:"id,omitempty"`
	Confidence string `json:"confidence"   yaml:"confidence"`
	Message    string `json:"message"      yaml:"message"`
}

// VerifyReport is the payload of `ape deferred verify`.
type VerifyReport struct {
	Findings []Finding     `json:"findings" yaml:"findings"`
	Summary  VerifySummary `json:"summary"  yaml:"summary"`
}

// VerifySummary aggregates.
type VerifySummary struct {
	Records    int            `json:"records"            yaml:"records"`
	Open       int            `json:"open"               yaml:"open"`
	Closed     int            `json:"closed"             yaml:"closed"`
	Findings   int            `json:"findings"           yaml:"findings"`
	Candidates int            `json:"candidates"         yaml:"candidates"`
	ByCheck    map[string]int `json:"by_check,omitempty" yaml:"by_check,omitempty"`
}

// OK reports whether anything needs attention.
func (r *VerifyReport) OK() bool { return len(r.Findings) == 0 }

// VerifyOptions carries the context the heuristics need.
type VerifyOptions struct {
	// ProjectRoot resolves anchor paths.
	ProjectRoot string
	// DoneStories is the set of story keys now done, for the fired-trigger
	// heuristic. Optional.
	DoneStories map[string]bool
}

// Verify checks the store.
func (s *Store) Verify(opts VerifyOptions) (*VerifyReport, error) {
	res, err := s.Load(LoadOptions{IncludeClosed: true})
	if err != nil {
		return nil, err
	}
	// Non-nil so a clean store marshals as `"findings": []`, not `null`.
	// The deferred-repair skill is explicitly told to take its work list from this
	// payload rather than globbing the directory.
	report := &VerifyReport{Findings: []Finding{}}
	for _, w := range res.Warnings {
		report.Findings = append(report.Findings, Finding{
			Check: CheckUnparsable, Confidence: ConfidenceCertain, Message: w,
		})
	}

	ids := make(map[string]bool, len(res.Records))
	for i := range res.Records {
		ids[res.Records[i].ID] = true
	}

	byTitle := map[string][]string{}
	for i := range res.Records {
		rec := &res.Records[i]
		report.Summary.Records++
		if rec.IsOpen() {
			report.Summary.Open++
		} else {
			report.Summary.Closed++
		}

		report.Findings = append(report.Findings, checkSchema(rec)...)
		report.Findings = append(report.Findings, checkRefs(rec, ids)...)
		report.Findings = append(report.Findings, checkDischargeMarker(rec)...)

		// Heuristics — candidates only, never conclusions.
		if rec.IsOpen() {
			report.Findings = append(report.Findings, checkAnchors(rec, opts.ProjectRoot)...)
			report.Findings = append(report.Findings, checkTrigger(rec, opts.DoneStories)...)
			byTitle[normaliseTitle(rec.Title)] = append(byTitle[normaliseTitle(rec.Title)], rec.ID)
		}
	}

	for _, key := range sortedKeys(byTitle) {
		if group := byTitle[key]; len(group) > 1 {
			report.Findings = append(report.Findings, Finding{
				Check:      CheckDuplicate,
				Confidence: ConfidenceCandidate,
				Message: fmt.Sprintf("%d open records share a near-identical title (%s) — the re-filing failure, if it is one",
					len(group), strings.Join(group, ", ")),
			})
		}
	}

	sortFindings(report.Findings)
	report.Summary.Findings = len(report.Findings)
	report.Summary.ByCheck = map[string]int{}
	for _, f := range report.Findings {
		report.Summary.ByCheck[f.Check]++
		if f.Confidence == ConfidenceCandidate {
			report.Summary.Candidates++
		}
	}
	if len(report.Summary.ByCheck) == 0 {
		report.Summary.ByCheck = nil
	}
	return report, nil
}

// checkSchema is a hard invariant: a record has to have the fields that
// make it addressable.
func checkSchema(rec *Record) []Finding {
	var out []Finding
	if strings.TrimSpace(rec.Title) == "" {
		out = append(out, Finding{
			Check: CheckSchema, ID: rec.ID, Confidence: ConfidenceCertain,
			Message: "record has no title",
		})
	}
	switch rec.Status {
	case StatusOpen, StatusClosed, StatusDiscarded:
	default:
		out = append(out, Finding{
			Check: CheckSchema, ID: rec.ID, Confidence: ConfidenceCertain,
			Message: fmt.Sprintf("status %q is not open, closed or discarded", rec.Status),
		})
	}
	if (rec.Status == StatusClosed || rec.Status == StatusDiscarded) && rec.ResolvedBy == "" && rec.DiscardReason == "" {
		out = append(out, Finding{
			Check: CheckSchema, ID: rec.ID, Confidence: ConfidenceCertain,
			Message: "closed without a resolved_by or discard_reason — nothing records why",
		})
	}
	if rec.FreeForm && rec.IsOpen() {
		out = append(out, Finding{
			Check: CheckFreeForm, ID: rec.ID, Confidence: ConfidenceCandidate,
			Message: "free-form record: fields could not be parsed, body kept verbatim — `ape deferred repair` completes or retires these",
		})
	}
	return out
}

// checkDischargeMarker flags an open record whose own body says it is closed.
//
// CERTAIN, not a candidate, and the reason is that it reads STRUCTURE
// rather than prose: the same two anchored recognisers the write paths act
// on, so the surfaces cannot disagree about what a closure marker is.
//
// NO WRITE DOOR CAN PRODUCE THIS STATE. Both Migrate and Ingest run the
// three discharge readings before they write, and Close/Discard set the
// status themselves — so every occurrence is drift introduced afterwards, by
// a skill appending to a body or by a hand edit, and mechanically checkable.
// That is what makes it a fact rather than a heuristic; while ingest skipped
// the readings, this check could fire on a record ape had just written.
//
// It matters because of who reads this list: `ape deferred repair` is
// forbidden to touch a record `verify` did not flag, so a record that
// announces its own discharge and is not reported here is unreachable by
// the only sanctioned discharge path. Nothing here closes it — verify never
// writes — but naming it puts it back in front of the one thing that can.
func checkDischargeMarker(rec *Record) []Finding {
	if !rec.IsOpen() {
		return nil
	}
	// An in-body closure marker is read FIRST, matching the order the write
	// paths apply their three discharge readings — so a record carrying both
	// is classified the same way here as it would be stored.
	//
	// The DONE messages state the fact and stop there. They must not name
	// `ape deferred close`, which `ape deferred repair` — the main consumer
	// of this list — is explicitly forbidden to run: discharging an open
	// record as delivered is the operator's judgment, and a finding that
	// instructs the one reader who must not act on it is worse than no
	// finding at all. The SUPERSEDED message may name the verdict, because
	// `discard` is exactly what that skill was dispatched to reach.
	if inBodyClosureRe.MatchString(rec.Body) {
		return []Finding{{
			Check: CheckClosureMark, ID: rec.ID, Confidence: ConfidenceCertain,
			Message: "open record carries an appended closure marker in its body — " +
				"the record's own text says the work was DONE; report it for the operator",
		}}
	}
	switch lastDischargingAnnotation(rec.Body) {
	case annotationClosed:
		return []Finding{{
			Check: CheckClosureMark, ID: rec.ID, Confidence: ConfidenceCertain,
			Message: "open record's last status annotation is [Closed] — " +
				"the record's own text says the work was DONE; report it for the operator",
		}}
	case annotationSuperseded:
		return []Finding{{
			Check: CheckSupersededMark, ID: rec.ID, Confidence: ConfidenceCertain,
			Message: "open record's last status annotation is [Superseded] — the record's own " +
				"text says the work was OVERTAKEN and never done, so it is a discard, not a close",
		}}
	}
	return nil
}

// lastDischargingAnnotation returns a body's LAST status tag when that tag
// discharges the record, and "" otherwise — so the caller can tell WHICH
// discharge it is rather than only that there was one.
//
// It must read the tags exactly as applyStatusAnnotation does, or the two
// surfaces disagree: the annotations on one entry are an append-only
// chronology, so a record whose final tag is `[Open]` is open however many
// `[Closed]` lines precede it, and flagging it here would report a record
// the migration correctly left alone. `[Open]` is a status annotation too,
// so matching the tag family alone would flag every open record in a
// register that annotates all of them.
func lastDischargingAnnotation(body string) string {
	matches := statusAnnotationRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return ""
	}
	switch last := matches[len(matches)-1][1]; last {
	case annotationClosed, annotationSuperseded:
		return last
	}
	return ""
}

// checkRefs is the other hard invariant: a pointer that points nowhere.
// The real ledger has two.
func checkRefs(rec *Record, ids map[string]bool) []Finding {
	var out []Finding
	for _, group := range []struct {
		field string
		refs  []string
	}{
		{"related", rec.Related},
		{"supersedes", rec.Supersedes},
	} {
		for _, ref := range group.refs {
			if !ids[ref] {
				out = append(out, Finding{
					Check: CheckDanglingRef, ID: rec.ID, Confidence: ConfidenceCertain,
					Message: fmt.Sprintf("%s[] names %s, which is not a record in this store", group.field, ref),
				})
			}
		}
	}
	return out
}

// checkAnchors flags a record whose cited file:line no longer resolves.
// A CANDIDATE: the record may be moot, or the code may simply have moved.
func checkAnchors(rec *Record, root string) []Finding {
	if root == "" {
		return nil
	}
	var out []Finding
	for _, anchor := range rec.Anchors {
		path, _, found := strings.Cut(anchor, ":")
		if !found || path == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			out = append(out, Finding{
				Check: CheckDeadAnchor, ID: rec.ID, Confidence: ConfidenceCandidate,
				Message: fmt.Sprintf("anchor %s no longer resolves — the record may be moot", anchor),
			})
		}
	}
	return out
}

// checkTrigger flags a record whose closing condition names a story that
// is now done. A CANDIDATE, emphatically: whether the condition actually
// fired requires re-verifying the premise against HEAD, which is judgment
// and is why `close` is never automatic.
func checkTrigger(rec *Record, done map[string]bool) []Finding {
	if rec.Trigger == "" || len(done) == 0 {
		return nil
	}
	for key := range done {
		if key != "" && strings.Contains(rec.Trigger, key) {
			return []Finding{{
				Check: CheckTriggerFired, ID: rec.ID, Confidence: ConfidenceCandidate,
				Message: fmt.Sprintf("trigger names story %s, which is now done — the closing condition MAY have fired; verify against HEAD before closing", key),
			}}
		}
	}
	return nil
}

// normaliseTitle collapses a title for duplicate detection: lowercase,
// alphanumerics only. Crude on purpose — the finding is a candidate, and a
// cleverer similarity metric would only make it look more authoritative
// than it is.
func normaliseTitle(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Message < b.Message
	})
}

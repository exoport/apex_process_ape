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
	// apex-defer-repair is explicitly told to take its work list from this
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

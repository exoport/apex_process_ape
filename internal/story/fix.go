package story

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
)

// FixChange is one repair applied, or one that would be.
type FixChange struct {
	Story  string `json:"story,omitempty" yaml:"story,omitempty"`
	Path   string `json:"path"            yaml:"path"`
	Field  string `json:"field"           yaml:"field"`
	Before string `json:"before"          yaml:"before"`
	After  string `json:"after"           yaml:"after"`
}

// FixResult is what --fix did, plus what it deliberately did not do.
type FixResult struct {
	Changes []FixChange `json:"changes" yaml:"changes"`
	// Remaining are the findings --fix does not own, carried through so the
	// caller never has to re-run verify to learn the repair is partial.
	Remaining []Finding `json:"remaining" yaml:"remaining"`
	Check     bool      `json:"check"     yaml:"check"`
}

// Changed reports whether anything was repairable.
func (r *FixResult) Changed() bool { return len(r.Changes) > 0 }

// fixableChecks is the whole scope of --fix, and it is deliberately one
// class: a `depends_on` item that YAML decoded as a number.
//
// WHY ONLY THIS ONE. A repair belongs here when the correct output is
// derivable from the finding alone, with no second source and no judgment.
// `- 53.1` decoding as a float has exactly one right answer — the same
// characters, quoted — because a story key is text and the digits are
// already sitting there.
//
// The other two mechanical-looking classes fail that test, and putting
// them here would make this command fabricate:
//
//   - `features:` items must be `{id, contribution}`, and the contribution
//     is not in the finding. Its only source is the prose `## Stories`
//     table inside the feature record, and where that table has no row for
//     the story, the disagreement between the two documents is the real
//     defect — resolving it is judgment. This command already refuses to
//     coerce a contribution; see the note on the verify command.
//   - a missing `features:`/`capabilities:` key needs a VALUE, and `[]` is
//     a claim that the story contributes to nothing. That is true only if
//     no record lists it, which is a registry question, not a frontmatter
//     one.
//
// Both go to apex-frontmatter-repair, which may read documents and judge.
var fixableChecks = map[string]bool{CheckTypeMismatch: true}

// dependsOnFieldRe matches the Field a depends_on type finding reports.
var dependsOnFieldRe = regexp.MustCompile(`^depends_on\[\d+\]$`)

// FixCorpus repairs every derivable finding under the implementation
// folder, re-verifying each file it touches before keeping the write.
//
// The apply/re-verify/revert loop is the point. A lexical rewrite of YAML
// is fast and preserves the formatting a node round-trip would destroy —
// this corpus has hand-wrapped flow sequences a re-render would reflow —
// but lexical rewrites are also how a fixer corrupts a file while
// reporting success. So every touched file is re-read and re-verified, and
// a write that does not strictly reduce that file's findings is rolled
// back and reported.
func FixCorpus(cfg *apexcfg.Resolved, check bool) (*FixResult, error) {
	report, err := VerifyCorpus(cfg)
	if err != nil {
		return nil, err
	}
	// A Finding's Path is relative to the scan root — a display name, not
	// something to open. Resolve it through the same scan the report came
	// from rather than rebuilding the path by hand.
	heads, err := ScanHeads(cfg.Paths.Implementation)
	if err != nil {
		return nil, err
	}
	abs := make(map[string]string, len(heads))
	for _, h := range heads {
		abs[h.Path] = h.Abs
	}

	res := &FixResult{Check: check, Changes: []FixChange{}, Remaining: []Finding{}}

	byPath := map[string][]Finding{}
	var order []string
	for _, f := range report.Findings {
		if !fixableChecks[f.Check] || !dependsOnFieldRe.MatchString(f.Field) {
			res.Remaining = append(res.Remaining, f)
			continue
		}
		if _, seen := byPath[f.Path]; !seen {
			order = append(order, f.Path)
		}
		byPath[f.Path] = append(byPath[f.Path], f)
	}

	for _, path := range order {
		target, ok := abs[path]
		if !ok {
			res.Remaining = append(res.Remaining, byPath[path]...)
			continue
		}
		changes, err := fixFile(target, path, byPath[path], cfg.Ext, check)
		if err != nil {
			return nil, err
		}
		if len(changes) == 0 {
			// Not repairable after all — hand the findings back rather than
			// silently dropping them.
			res.Remaining = append(res.Remaining, byPath[path]...)
			continue
		}
		res.Changes = append(res.Changes, changes...)
	}
	return res, nil
}

func fixFile(target, rel string, findings []Finding, ext apexcfg.Ext, check bool) ([]FixChange, error) {
	original, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	before := string(original)
	after, line, ok := quoteDependsOn(before)
	if !ok {
		return nil, nil
	}

	story := ""
	if len(findings) > 0 {
		story = findings[0].Story
	}
	change := []FixChange{{
		Story: story, Path: rel, Field: "depends_on",
		Before: strings.TrimSpace(line.before), After: strings.TrimSpace(line.after),
	}}
	if check {
		return change, nil
	}

	if err := atomicfile.Write(target, []byte(after)); err != nil {
		return nil, err
	}
	// Re-verify, and roll back anything that did not strictly improve. A
	// lexical YAML rewrite preserves the formatting a node round-trip would
	// reflow, and is also exactly how a fixer corrupts a file while
	// reporting success — so the write is provisional until this passes.
	//
	// VerifyFile cannot see the corpus, so it never reports the referential
	// class. The count must also be narrowed to the findings --fix OWNS, not
	// every type_mismatch: that check fires on `features[i]` too, which this
	// command deliberately refuses. Counting those made a file carrying both
	// classes compare 1 surviving features finding against 1 repaired
	// depends_on finding, trip `>=`, and roll back a correct repair — on
	// exactly the documents most likely to have both.
	was := len(findings)
	now := VerifyFile(target, ext)
	typeFindings := 0
	for _, f := range now.Findings {
		if f.Check == CheckTypeMismatch && dependsOnFieldRe.MatchString(f.Field) {
			typeFindings++
		}
	}
	if now.Code == FileParseFailure || typeFindings >= was {
		if restoreErr := atomicfile.Write(target, original); restoreErr != nil {
			return nil, fmt.Errorf("rolling back %s: %w", rel, restoreErr)
		}
		return nil, nil
	}
	return change, nil
}

// lineEdit records the one frontmatter line a fix rewrote, for reporting.
type lineEdit struct{ before, after string }

// bareNumberRe matches a whole item that is nothing but digits and dots —
// the shape a story key takes once YAML has decoded it as a number.
// Anchored end to end so `v1.2`, `112.1a` and an already-quoted `"112.1"`
// are all left alone.
var bareNumberRe = regexp.MustCompile(`^\d+(?:\.\d+)*$`)

// blockItemRe matches a block-sequence item holding a bare number.
var blockItemRe = regexp.MustCompile(`^(\s*-\s+)(\d+(?:\.\d+)*)\s*$`)

// quoteDependsOn quotes every bare-number item in the frontmatter's
// depends_on, in either the flow or the block shape, and touches nothing
// else in the file. Returns false when there was nothing to do.
func quoteDependsOn(doc string) (string, lineEdit, bool) {
	lines := strings.Split(doc, "\n")
	end := frontmatterEnd(lines)
	if end < 0 {
		return doc, lineEdit{}, false
	}

	for i := 1; i < end; i++ {
		line := lines[i]
		if !strings.HasPrefix(line, "depends_on:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "depends_on:"))

		// Flow shape: depends_on: [112.1, 112.2]
		if strings.HasPrefix(value, "[") {
			fixed := quoteFlowItems(value)
			if fixed == value {
				return doc, lineEdit{}, false
			}
			edit := lineEdit{before: line, after: "depends_on: " + fixed}
			lines[i] = edit.after
			return strings.Join(lines, "\n"), edit, true
		}

		// Block shape: depends_on:\n  - 112.1
		if value != "" {
			return doc, lineEdit{}, false
		}
		var was, now []string
		for j := i + 1; j < end; j++ {
			// The first line that is not a sequence item ends the sequence.
			if !strings.HasPrefix(strings.TrimSpace(lines[j]), "-") {
				break
			}
			m := blockItemRe.FindStringSubmatch(lines[j])
			if m == nil {
				continue // an item that is already a string
			}
			was = append(was, strings.TrimSpace(lines[j]))
			lines[j] = m[1] + `"` + m[2] + `"`
			now = append(now, strings.TrimSpace(lines[j]))
		}
		if len(was) == 0 {
			return doc, lineEdit{}, false
		}
		return strings.Join(lines, "\n"),
			lineEdit{before: strings.Join(was, ", "), after: strings.Join(now, ", ")},
			true
	}
	return doc, lineEdit{}, false
}

// quoteFlowItems quotes every bare-number item inside a `[...]` flow
// sequence.
//
// Split on commas rather than running one regex over the whole value: a
// pattern that anchors on the delimiters has to consume the trailing comma
// to know an item ended, which then eats the NEXT item's leading comma and
// silently leaves it unquoted. `[112.1, 112.2]` becoming `["112.1", 112.2]`
// is that bug, and it is worse than not fixing at all — it looks repaired.
func quoteFlowItems(value string) string {
	open := strings.Index(value, "[")
	shut := strings.LastIndex(value, "]")
	if open < 0 || shut <= open {
		return value
	}
	parts := strings.Split(value[open+1:shut], ",")
	changed := false
	for i, part := range parts {
		item := strings.TrimSpace(part)
		if !bareNumberRe.MatchString(item) {
			continue
		}
		lead := part[:len(part)-len(strings.TrimLeft(part, " \t"))]
		parts[i] = lead + `"` + item + `"`
		changed = true
	}
	if !changed {
		return value
	}
	return value[:open+1] + strings.Join(parts, ",") + value[shut:]
}

// frontmatterEnd returns the index of the closing `---`, or -1.
func frontmatterEnd(lines []string) int {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return -1
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return i
		}
	}
	return -1
}

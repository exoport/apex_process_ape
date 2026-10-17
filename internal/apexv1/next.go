package apexv1

import (
	"context"
	"regexp"
	"strings"
)

// The roadmap's Next (H26, D45 Q7): findings noticed but out of scope,
// one dated line each, for apex-plan to triage at the next delivery. ape
// writes them on the base branch from any branch (`ape next add`); a
// model and a person alike may add one. Next is not a milestone: a `##
// Next` section of roadmap.md is not read as one.

// nextHeadingRe is a roadmap section that holds Next, not a milestone.
var nextHeadingRe = regexp.MustCompile(`(?i)^next\b`)

// nextLabelRe is the label line apex-plan writes Next under, above the
// first milestone: `Next — not milestones:`.
var nextLabelRe = regexp.MustCompile(`(?i)^next\b.*:\s*$`)

// NextLine is one Next item: `- YYYY-MM-DD <text>`, and ` (from <id>)`
// when a change found it.
func NextLine(date, text, from string) string {
	line := "- " + date + " " + strings.TrimSpace(text)
	if from != "" {
		line += " (from " + from + ")"
	}
	return line
}

// AppendNextLines inserts lines (each a NextLine) into a roadmap.md: at
// the end of its `## Next` section; else, under a `Next …:` label line
// above the first milestone (apex-plan's form), after the list that
// follows it; else in a new `## Next` section at the end. Code blocks are
// skipped. The rest of the document is left as it is.
func AppendNextLines(roadmap []byte, lines []string) []byte {
	if len(lines) == 0 {
		return roadmap
	}
	doc := strings.ReplaceAll(string(roadmap), "\r\n", "\n")
	trailing := strings.HasSuffix(doc, "\n")
	ls := strings.Split(strings.TrimSuffix(doc, "\n"), "\n")
	inCode := false
	section, label, firstH2 := -1, -1, -1
	end := len(ls)
	for i, l := range ls {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		level := len(t) - len(strings.TrimLeft(t, "#"))
		heading := level > 0 && level <= 2 && strings.HasPrefix(t[level:], " ")
		switch {
		case heading && section >= 0 && end == len(ls):
			end = i
		case heading && level == 2:
			if firstH2 < 0 {
				firstH2 = i
			}
			if section < 0 && nextHeadingRe.MatchString(strings.TrimSpace(t[level:])) {
				section = i
			}
		case !heading && firstH2 < 0 && label < 0 && nextLabelRe.MatchString(t):
			label = i
		}
	}
	at := -1
	switch {
	case section >= 0:
		at = end
		for at > section+1 && strings.TrimSpace(ls[at-1]) == "" {
			at--
		}
	case label >= 0:
		at = label + 1
		for at < len(ls) && (firstH2 < 0 || at < firstH2) && isListLine(ls[at]) {
			at++
		}
	}
	var out []string
	if at < 0 {
		out = append(out, ls...)
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		out = append(out, "", "## Next")
		out = append(out, lines...)
		trailing = true
	} else {
		out = append(out, ls[:at]...)
		out = append(out, lines...)
		out = append(out, ls[at:]...)
	}
	res := strings.Join(out, "\n")
	if trailing {
		res += "\n"
	}
	return []byte(res)
}

// isListLine is a list item, or a list item's indented continuation.
func isListLine(l string) bool {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || t != "" && (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t"))
}

// NextResult is what `ape next add` reports.
type NextResult struct {
	Line   string `json:"line"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// AddNext appends one dated Next line to roadmap.md and commits it alone
// on the base branch as "apex: next — <text>", from whichever branch HEAD
// is on (editOnBranch): on a change's branch, its base branch.
func (p *Project) AddNext(ctx context.Context, text, from string) (*NextResult, error) {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, "\r\n") {
		return nil, &UsageError{Code: "usage.text", Msg: "the finding is one line of text — e.g. `ape next add \"the CSV export ignores --tz\" --from <id>`"}
	}
	from = strings.TrimSpace(from)
	if from != "" && !ChangeIDRe.MatchString(from) {
		return nil, &UsageError{Code: "usage.change-id", Msg: "--from " + from + " is not a change id (^[a-z][a-z0-9-]{2,39}$)"}
	}
	line := NextLine(Today(), text, from)
	rel := p.Rel(p.RoadmapMD())
	res := &NextResult{Line: line, Path: rel}
	if !p.isWorkTree(ctx) {
		doc, err := readFileString(p.RoadmapMD())
		if err != nil {
			return nil, &FailError{Code: "roadmap.missing", Msg: "no " + rel + " — run `ape project init`"}
		}
		return res, writeFileAtomic(p.RoadmapMD(), string(AppendNextLines([]byte(doc), []string{line})))
	}
	base, err := p.baseBranch(ctx)
	if err != nil {
		return nil, err
	}
	res.Branch = base
	res.Commit, err = p.editOnBranch(ctx, base, "apex: next — "+truncate(text, 60), []string{rel},
		func(cur map[string][]byte) (map[string][]byte, error) {
			if cur[rel] == nil {
				return nil, &FailError{Code: "roadmap.missing", Msg: "no " + rel + " on " + base + " — run `ape project init`"}
			}
			return map[string][]byte{rel: AppendNextLines(cur[rel], []string{line})}, nil
		})
	if err != nil {
		return nil, err
	}
	return res, nil
}

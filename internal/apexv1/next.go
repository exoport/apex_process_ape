package apexv1

import (
	"context"
	"os"
	"path/filepath"
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
//
//nolint:gocyclo // three placements, tried in order, over one line scan that skips code blocks
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

// NextItems are the roadmap's Next items, each without its list marker:
// those of the `## Next` section, and those under apex-plan's `Next …:`
// label above the first milestone. An item's indented continuation lines
// are joined onto it; code blocks are skipped. [] when there are none.
func NextItems(roadmap string) []string {
	lines := splitLines(roadmap)
	out := []string{}
	// collect reads one list. Under the label it ends at the first line
	// that is not a list line, as AppendNextLines places them; in the
	// section, blank lines and prose between items are passed over.
	collect := func(body []mdLine, label bool) {
		in := false
		for _, l := range body {
			if l.InCode {
				continue
			}
			if lv, _ := headingLevel(l); lv > 0 {
				return
			}
			t := strings.TrimSpace(l.Text)
			switch {
			case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
				if item := strings.TrimSpace(t[2:]); item != "" {
					out = append(out, item)
					in = true
				}
			case in && t != "" && isListLine(l.Text):
				out[len(out)-1] += " " + t
			case label:
				return
			default:
				in = false
			}
		}
	}
	for i, l := range lines {
		if lv, _ := headingLevel(l); lv == 2 {
			break
		}
		if !l.InCode && nextLabelRe.MatchString(strings.TrimSpace(l.Text)) {
			collect(lines[i+1:], true)
			break
		}
	}
	for _, s := range h2Sections(lines) {
		if nextHeadingRe.MatchString(s.Title) {
			collect(s.Body, false)
		}
	}
	return out
}

// roadmapNext is the base branch's Next (D60): `ape next add` writes it
// there from any branch, so on a change's branch the working tree's
// roadmap may not have the newest lines yet.
func (p *Project) roadmapNext(ctx context.Context, base string) []string {
	if base != "" && p.isWorkTree(ctx) {
		if data, ok := p.readOnBranch(ctx, base, p.Rel(p.RoadmapMD())); ok {
			return NextItems(string(data))
		}
	}
	doc, err := readFileString(p.RoadmapMD())
	if err != nil {
		return []string{}
	}
	return NextItems(doc)
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

// noticedTitleRe is the title of review.md's section of findings outside
// the change (H26, D48 Q6): apex-review writes it, archive carries it into
// Next.
var noticedTitleRe = regexp.MustCompile(`(?i)^noticed,?\s+out of scope$`)

// noticedItems are the top-level list items of every `Noticed, out of
// scope` section of review.md, one line each, first-seen order, each item
// once ("none", empty items and answers left out). The framework writes one
// `## Review <n>` section per round (D89 Q41), so the heading may come once
// per round and at level 2 to 4; a section ends at the next heading of its
// level or higher, outside code. Only a line that starts with `- ` or `* `
// is an item: an answer written under a finding, indented or blockquoted —
// or a top-level `- Answer:` line — never reaches the roadmap's Next.
func noticedItems(review string) []string {
	var out []string
	seen := map[string]bool{}
	in := 0 // the level of the Noticed section the line is in; 0 outside one
	for _, l := range splitLines(review) {
		if lv, title := headingLevel(l); lv > 0 {
			switch {
			case lv >= 2 && lv <= 4 && noticedTitleRe.MatchString(title):
				in = lv
			case lv <= in:
				in = 0
			}
			continue
		}
		if in == 0 || l.InCode || !strings.HasPrefix(l.Text, "- ") && !strings.HasPrefix(l.Text, "* ") {
			continue
		}
		item := strings.TrimSpace(l.Text[2:])
		if item == "" || strings.EqualFold(strings.TrimRight(item, "."), "none") ||
			len(item) >= 7 && strings.EqualFold(item[:7], "answer:") || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// appendNoticed carries the archived change's noticed findings into the
// roadmap's Next, staged with the squash (H26): dest is the archived
// folder, which holds review.md.
func (p *Project) appendNoticed(ctx context.Context, id, dest string) error {
	data, err := os.ReadFile(filepath.Join(dest, "review.md"))
	if err != nil {
		return nil //nolint:nilerr // no review.md: nothing noticed
	}
	items := noticedItems(string(data))
	if len(items) == 0 {
		return nil
	}
	roadmap, err := os.ReadFile(p.RoadmapMD())
	if err != nil {
		return err
	}
	lines := make([]string, len(items))
	for i, it := range items {
		lines[i] = NextLine(Today(), it, id)
	}
	if err := writeFileAtomic(p.RoadmapMD(), string(AppendNextLines(roadmap, lines))); err != nil {
		return err
	}
	_, err = git(ctx, p.Root, "add", "--", p.Rel(p.RoadmapMD()))
	return err
}

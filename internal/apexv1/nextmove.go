package apexv1

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// D217: Next moves out of the roadmap into development/next.md. This file
// is where a roadmap's Next sits (scanRoadmapNext), how next.md grows
// (appendNextMD), `ape next move` (the framework's v1.0.0_seq-09), and
// what project check says about either file.

// rc.7b (rc7b-next).
const (
	// CodeNextMovePending: `ape next move --check` while the roadmap still
	// holds Next, or next.md or the pointer line is missing (Q126).
	CodeNextMovePending = "next.move-pending"
	// CodeRoadmapNextInline: project check's notice for a roadmap that
	// still holds a Next block (Q127).
	CodeRoadmapNextInline = "roadmap.next-inline"
)

// roadmapNextLayout is where Next sits in a roadmap.md, as indexes into
// its splitLines.
type roadmapNextLayout struct {
	// label is apex-plan's `Next …:` label line above the first
	// milestone, and labelEnd the end of the list under it ([label,
	// labelEnd)); both -1 when there is none.
	label, labelEnd int
	// sections are the `## Next` sections, each from its heading to the
	// next heading of level 1 or 2 ([from, to)).
	sections [][2]int
	// firstH2 is the first level-2 heading; len(lines) when none.
	firstH2 int
	// pointer is the line pointing at next.md (outside code and outside a
	// Next section); -1 when none. residue is what follows it up to the
	// first milestone when that holds items ([pointer+1, residueEnd)): an
	// earlier move that stopped at a blank line left them (R7b-1); -1 when
	// none.
	pointer, residueEnd int
}

// scanRoadmapNext finds a roadmap's Next block and pointer line, as
// NextItems and AppendNextLines read them.
func scanRoadmapNext(lines []mdLine) roadmapNextLayout {
	l := roadmapNextLayout{label: -1, labelEnd: -1, firstH2: len(lines), pointer: -1, residueEnd: -1}
	open := -1
	for i, ln := range lines {
		if lv, title := headingLevel(ln); lv > 0 && lv <= 2 {
			if open >= 0 {
				l.sections = append(l.sections, [2]int{open, i})
				open = -1
			}
			if lv == 2 {
				l.firstH2 = min(l.firstH2, i)
				if nextHeadingRe.MatchString(title) {
					open = i
				}
			}
			continue
		}
		if ln.InCode || open >= 0 {
			continue
		}
		t := strings.TrimSpace(ln.Text)
		switch {
		case l.pointer < 0 && nextPointerRe.MatchString(t):
			l.pointer = i
		case l.label < 0 && l.firstH2 == len(lines) && nextLabelRe.MatchString(t):
			l.label = i
		}
	}
	if open >= 0 {
		l.sections = append(l.sections, [2]int{open, len(lines)})
	}
	// The label's list, and a pointer's residue, run over blank lines
	// (R7b-1) to the first milestone or other line; trailing blank lines
	// stay with the roadmap.
	if l.label >= 0 {
		l.labelEnd = blockEnd(lines, l.label, l.firstH2)
	}
	if l.pointer >= 0 && l.pointer < l.firstH2 && l.pointer != l.label {
		if end := blockEnd(lines, l.pointer, l.firstH2); end > l.pointer+1 {
			l.residueEnd = end
		}
	}
	return l
}

// blockEnd is the end of the list under line from: the line after its
// last list line, blank lines inside it allowed (R7b-1), up to limit or
// the first other line (from+1 when there is no list).
func blockEnd(lines []mdLine, from, limit int) int {
	end := from + 1
	for j := from + 1; j < limit && !lines[j].InCode; j++ {
		if isListLine(lines[j].Text) {
			end = j + 1
		} else if strings.TrimSpace(lines[j].Text) != "" {
			break
		}
	}
	return end
}

// hasNextBlock reports a roadmap that still holds Next: a `## Next`
// section, or apex-plan's label above the first milestone.
func hasNextBlock(roadmap string) bool {
	l := scanRoadmapNext(splitLines(roadmap))
	return l.label >= 0 || len(l.sections) > 0 || l.residueEnd >= 0
}

// withNextPointer is roadmap with NextPointer above its first milestone
// (at the end when it has none), a blank line on each side.
func withNextPointer(roadmap string) string {
	lines := splitLines(roadmap)
	at := scanRoadmapNext(lines).firstH2
	out := make([]string, 0, len(lines)+3)
	for _, l := range lines[:at] {
		out = append(out, l.Text)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	out = append(out, NextPointer)
	if at < len(lines) {
		out = append(out, "")
	}
	for _, l := range lines[at:] {
		out = append(out, l.Text)
	}
	return strings.Join(out, "\n") + "\n"
}

// appendNextMD is next.md (nil: absent, so its skeleton) with lines added
// at its end: right under a list that ends it, else after a blank line.
// With no lines an existing next.md is returned as it is.
func appendNextMD(next []byte, lines []string) []byte {
	if next != nil && len(lines) == 0 {
		return next
	}
	doc := nextMDSkeleton
	if next != nil {
		doc = strings.ReplaceAll(string(next), "\r\n", "\n")
	}
	ls := strings.Split(strings.TrimRight(doc, "\n"), "\n")
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	if len(lines) > 0 {
		if len(ls) > 0 && (!isListLine(ls[len(ls)-1]) || !isListLine(lines[0])) {
			ls = append(ls, "")
		}
		ls = append(ls, lines...)
	}
	return []byte(strings.Join(ls, "\n") + "\n")
}

// nextChunk is one piece of a roadmap's Next block as the move carries it,
// byte for byte: an item — its list line and its continuation lines — or
// one other line of a `## Next` section (prose, a sub-heading, a blank
// line, code).
type nextChunk struct {
	// item is the item as NextItems reads it (marker dropped,
	// continuations joined); "" for any other line.
	item string
	raw  []string
	// blank is a blank line outside code.
	blank bool
}

// chunks are the Next block's pieces in document order: the label's list,
// then each section's body.
func (l roadmapNextLayout) chunks(lines []mdLine) []nextChunk {
	var out []nextChunk
	read := func(body []mdLine) {
		in := false
		for _, ln := range body {
			t := strings.TrimSpace(ln.Text)
			switch {
			case ln.InCode:
				in = false
				out = append(out, nextChunk{raw: []string{ln.Text}})
			case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
				item := strings.TrimSpace(t[2:])
				in = item != ""
				out = append(out, nextChunk{item: item, raw: []string{ln.Text}})
			case in && t != "" && isListLine(ln.Text):
				c := &out[len(out)-1]
				c.item += " " + t
				c.raw = append(c.raw, ln.Text)
			default:
				in = false
				out = append(out, nextChunk{raw: []string{ln.Text}, blank: t == ""})
			}
		}
	}
	if l.label >= 0 {
		read(lines[l.label+1 : l.labelEnd])
	}
	if l.residueEnd >= 0 {
		read(lines[l.pointer+1 : l.residueEnd])
	}
	for _, s := range l.sections {
		read(lines[s[0]+1 : s[1]])
	}
	return out
}

// moved is the roadmap without its Next block: apex-plan's label and its
// list replaced by NextPointer in place, each `## Next` section removed,
// and — with no label to replace — NextPointer put above the first
// milestone. A roadmap with the pointer already gets no second one. The
// rest is kept as it is.
func (l roadmapNextLayout) moved(lines []mdLine) string {
	type outLine struct {
		text      string
		h2, blank bool
	}
	var out []outLine
	placed := l.pointer >= 0
	section := map[int]int{}
	for _, s := range l.sections {
		section[s[0]] = s[1]
	}
	// joined: a removal just happened, so a blank line that would double
	// the one before it is dropped.
	joined := false
	for i := 0; i < len(lines); i++ {
		if i == l.label {
			if !placed {
				out = append(out, outLine{text: NextPointer})
				placed = true
			} else {
				joined = true
			}
			i = l.labelEnd - 1
			continue
		}
		if to, ok := section[i]; ok {
			joined = true
			i = to - 1
			continue
		}
		if i == l.pointer && l.residueEnd >= 0 {
			// The pointer stays; the items an earlier move left under it go.
			out = append(out, outLine{text: lines[i].Text})
			joined = true
			i = l.residueEnd - 1
			continue
		}
		ln := lines[i]
		blank := !ln.InCode && strings.TrimSpace(ln.Text) == ""
		if joined && blank && (len(out) == 0 || out[len(out)-1].blank) {
			continue
		}
		joined = false
		lv, _ := headingLevel(ln)
		out = append(out, outLine{text: ln.Text, h2: lv == 2, blank: blank})
	}
	if joined {
		for len(out) > 0 && out[len(out)-1].blank {
			out = out[:len(out)-1]
		}
	}
	if !placed {
		at := len(out)
		for i, o := range out {
			if o.h2 {
				at = i
				break
			}
		}
		ins := []outLine{{text: NextPointer}}
		if at > 0 && !out[at-1].blank {
			ins = append([]outLine{{blank: true}}, ins...)
		}
		if at < len(out) && !out[at].blank {
			ins = append(ins, outLine{blank: true})
		}
		out = append(out[:at], append(ins, out[at:]...)...)
	}
	texts := make([]string, len(out))
	for i, o := range out {
		texts[i] = o.text
	}
	return strings.Join(texts, "\n") + "\n"
}

// NextMoveResult is what `ape next move` reports.
type NextMoveResult struct {
	// Moved are the roadmap's Next items carried into next.md (or, under
	// --check, to carry), one line each without the list marker; [] when
	// none.
	Moved []string `json:"moved"`
	// Duplicates are the roadmap's items left out because next.md holds
	// them already, or the block itself did earlier; [] when none.
	Duplicates []string `json:"duplicates"`
	// Path is next.md; Roadmap the roadmap.
	Path    string `json:"path"`
	Roadmap string `json:"roadmap"`
	// Created: next.md did not exist and is (or would be) written.
	Created bool `json:"created"`
	// PointerAdded: the roadmap gets the line pointing at next.md.
	PointerAdded bool `json:"pointer_added"`
	// Pending: something is (or was) to do; false when the project's Next
	// is in next.md already, so nothing was written.
	Pending bool   `json:"pending"`
	Commit  string `json:"commit,omitempty"`
}

// nextMoveSubject is the commit `ape next move` makes (D218).
func nextMoveSubject(nextRel string) string {
	return "apex: Next moved to " + nextRel + " (seq-09)"
}

// planNextMove is the move of roadmap's Next into next (nil: absent): the
// result, and the two files as they are after it.
func planNextMove(roadmap string, next []byte, res *NextMoveResult) (newRoadmap, newNext string) {
	lines := splitLines(roadmap)
	l := scanRoadmapNext(lines)
	seen := map[string]bool{}
	if next != nil {
		for _, it := range NextFileItems(string(next)) {
			seen[it] = true
		}
	}
	var block []string
	lastBlank := true // no blank line leads the block
	for _, c := range l.chunks(lines) {
		switch {
		case c.item != "" && seen[c.item]:
			res.Duplicates = append(res.Duplicates, c.item)
			continue
		case c.item != "":
			seen[c.item] = true
			res.Moved = append(res.Moved, c.item)
		case c.blank && lastBlank:
			continue
		}
		block = append(block, c.raw...)
		lastBlank = c.blank
	}
	for len(block) > 0 && strings.TrimSpace(block[len(block)-1]) == "" {
		block = block[:len(block)-1]
	}
	res.Created = next == nil
	res.PointerAdded = l.pointer < 0
	res.Pending = res.Created || res.PointerAdded || l.label >= 0 || len(l.sections) > 0 || l.residueEnd >= 0
	return l.moved(lines), string(appendNextMD(next, block))
}

// nextMovePlan reads the two files and plans the move.
func (p *Project) nextMovePlan() (res *NextMoveResult, roadmap, next string, err error) {
	res = &NextMoveResult{Moved: []string{}, Duplicates: []string{}, Path: p.Rel(p.NextMD()), Roadmap: p.Rel(p.RoadmapMD())}
	doc, err := readFileString(p.RoadmapMD())
	if err != nil {
		return res, "", "", &FailError{Code: "roadmap.missing", Msg: "no " + res.Roadmap + " — run `ape project init`"}
	}
	cur, err := os.ReadFile(p.NextMD())
	if err != nil {
		cur = nil
	}
	roadmap, next = planNextMove(doc, cur, res)
	return res, roadmap, next, nil
}

// NextMoveCheck is `ape next move --check`, the check of the framework's
// v1.0.0_seq-09 (D217 Q126): nil when Next is in next.md already — the
// roadmap holds no Next block and has the pointer line, and next.md
// exists — else next.move-pending (exit 1) saying what is left. It writes
// nothing.
func (p *Project) NextMoveCheck() (*NextMoveResult, error) {
	res, _, _, err := p.nextMovePlan()
	if err != nil || !res.Pending {
		return res, err
	}
	var what []string
	if n := len(res.Moved) + len(res.Duplicates); n > 0 {
		what = append(what, res.Roadmap+" holds "+strconv.Itoa(n)+" Next item(s)")
	}
	if res.Created {
		what = append(what, res.Path+" does not exist")
	}
	if res.PointerAdded {
		what = append(what, res.Roadmap+" has no line pointing at it ("+NextPointer+")")
	}
	if len(what) == 0 {
		what = append(what, res.Roadmap+" holds an empty Next block")
	}
	return res, &FailError{Code: CodeNextMovePending, Msg: "Next is not in " + res.Path + " yet: " + strings.Join(what, ", ") +
		" — `ape next move` moves it in one commit"}
}

// NextMove is `ape next move` (D217 Q126, the framework's v1.0.0_seq-09):
// the roadmap's Next — the `## Next` sections and the items under
// apex-plan's `Next …:` label — moved into next.md, byte for byte, in
// order, each item once (one next.md holds already is left out); the label
// and its list replaced by the pointer line, the sections removed and the
// pointer put above the first milestone; next.md created from its
// skeleton when absent. One commit, "apex: Next moved to
// development/next.md (seq-09)". Idempotent: with nothing to do it writes
// nothing. Refused outside a git repository, on a change branch, and while
// either file has uncommitted changes.
func (p *Project) NextMove(ctx context.Context) (*NextMoveResult, error) {
	res, roadmap, next, err := p.nextMovePlan()
	if err != nil || !res.Pending {
		return res, err
	}
	if !p.isWorkTree(ctx) {
		return res, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — moving Next is one commit"}
	}
	if err := p.recoverArchive(ctx); err != nil {
		return res, err
	}
	if err := p.refuseOnChangeBranch(ctx, "moving Next to "+res.Path); err != nil {
		return res, err
	}
	rels := []string{res.Roadmap, res.Path}
	if err := p.refuseDirty(ctx, "moving Next", rels); err != nil {
		return res, err
	}
	own := p.beforeOwnEdit(ctx, rels...)
	if err := writeFileAtomic(p.RoadmapMD(), roadmap); err != nil {
		return res, err
	}
	if err := writeFileAtomic(p.NextMD(), next); err != nil {
		return res, err
	}
	sha, err := p.commitOwnRecord(ctx, own, nextMoveSubject(res.Path), "", rels...)
	if err != nil {
		return res, commitFailed(err, "Next was moved but its commit failed: "+err.Error())
	}
	res.Commit = sha
	return res, nil
}

// nextInlineNotices is project check's notice for a roadmap that still
// holds Next (D217 Q127): never a finding, so a project not yet moved is
// not stopped mid-loop.
func (p *Project) nextInlineNotices() []Diagnostic {
	doc, err := readFileString(p.RoadmapMD())
	if err != nil {
		return nil
	}
	lines := splitLines(doc)
	l := scanRoadmapNext(lines)
	at := l.label
	if at < 0 && len(l.sections) > 0 {
		at = l.sections[0][0]
	}
	if at < 0 && l.residueEnd >= 0 {
		at = l.pointer // items an earlier move left under the pointer (R7b-1)
	}
	if at < 0 {
		return nil
	}
	return []Diagnostic{notice(Diagnostic{
		Code: CodeRoadmapNextInline, File: p.Rel(p.RoadmapMD()), Line: lines[at].N,
		Message: "the roadmap still holds Next (" + strconv.Itoa(len(NextItems(doc))) + " item(s)), which now lives in " + p.Rel(p.NextMD()) +
			" — `ape next move` moves it there in one commit, merged with what next.md holds (the framework's v1.0.0_seq-09 runs it)",
	})}
}

// nextDateRe is an item that starts with a date, well formed or not.
var nextDateRe = regexp.MustCompile(`^(\d{2,4}-\d{1,2}-\d{1,2})(\s+|$)`)

// nextFromRe is an item's trailing `(from <id>)`.
var nextFromRe = regexp.MustCompile(`\(from ([^()]*)\)$`)

// ValidateNextMD checks next.md's items (D217 Q127), and nothing else of
// it: an item is `- YYYY-MM-DD <text> (from <change-id>)` as ape next add
// writes it, or an undated line as apex-plan wrote them under its label
// (the move carries those byte for byte). A date that is not a real
// YYYY-MM-DD, a date with no text, or a `(from …)` that names no change id
// is a finding.
func ValidateNextMD(file, doc string) []Finding {
	var out []Finding
	for _, l := range splitLines(doc) {
		if l.InCode || !strings.HasPrefix(l.Text, "- ") && !strings.HasPrefix(l.Text, "* ") {
			continue
		}
		item := strings.TrimSpace(l.Text[2:])
		bad := ""
		if m := nextDateRe.FindStringSubmatch(item); m != nil {
			if _, err := time.Parse("2006-01-02", m[1]); err != nil || len(m[1]) != len("2006-01-02") {
				bad = "its date " + m[1] + " is not a real YYYY-MM-DD"
			} else if strings.TrimSpace(item[len(m[0]):]) == "" {
				bad = "it has no text after its date"
			}
		}
		if m := nextFromRe.FindStringSubmatch(item); bad == "" && m != nil && !ChangeIDRe.MatchString(m[1]) {
			bad = "(from " + m[1] + ") names no change id"
		}
		if bad != "" {
			out = append(out, Finding{File: file, Line: l.N, Message: "next item \"" + truncate(item, 60) + "\" is malformed: " + bad +
				" — an item reads `- YYYY-MM-DD <text> (from <change-id>)`, as ape next add writes it"})
		}
	}
	return out
}

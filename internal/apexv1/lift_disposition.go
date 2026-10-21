package apexv1

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// D47 Q4: the lift writes development/lift-disposition.md, committed in
// the lift commit. apex-plan fills its disposition column, a fresh
// read-only session reviews it, then the person does; the person deletes
// it. Its full lists go to _apex/state/lift-disposition.json, which git
// ignores (R3). It holds the binding-rule candidates — the rules v0 kept in
// CLAUDE.md, project-context.md and team-memory, which v1 has no file
// for — and everything the lift left for a reader: ids it did not edit,
// ADR gaps, references to removed paths.
//
// RG-4 (D252, D257): the candidates were CLAUDE.md's list items,
// project-context's "don't miss" section and team-memory's gotcha titles,
// so on the rehearsals 46–78 rows stood for 94–163 project-context list
// items and 37–249 team-memory ones; every other rule section was deleted
// with its file, recorded nowhere. Now every section of both files counts:
// its list items and its rule-shaped paragraphs, and a team-memory entry
// whose body has neither is listed by its paragraphs, else by its title.
//
// RG-4b (D262): rc7f still skipped a "pure overview" (a heading titled
// overview, introduction, about, …), and the rc7f rehearsal reviews found
// binding rules there — radix's "never fold radix-release-gen into
// cmd/radix", mdpdf's import-cycle rule for pkg/mdpdf/signal. No section
// is skipped now: an overview's facts are candidates apex-plan drops with
// a reason. A list item that is only links (a table of contents) gives
// none.

var (
	entryDateTail  = regexp.MustCompile(`\s+[—–-]+\s+\d{4}-\d{2}-\d{2}(?:\s*\([^()]*\))?\s*$`)
	minRuleWordsRe = regexp.MustCompile(`\S+\s+\S+\s+\S+`)
	// linksOnlyRe is a list item that is nothing but links, as in a table
	// of contents: it names sections, it binds nothing (RG-4b).
	linksOnlyRe = regexp.MustCompile(`^(?:\[[^\]]*\]\([^)]*\)[\s,;·|–—-]*)+$`)
	// tableSepRe is a table's header separator row (RG-4c).
	tableSepRe = regexp.MustCompile(`^\s*\|[\s:|-]*-[\s:|-]*\|\s*$`)
	// ruleWordRe makes a paragraph rule-shaped (RG-4).
	ruleWordRe = regexp.MustCompile(`(?i)\b(?:must|never|always|do\s+not|don['’]t|avoid|only|required|prefer|shall|forbidden)\b`)
	thematicRe = regexp.MustCompile(`^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	quoteRe    = regexp.MustCompile(`^\s{0,3}>\s?`)
	// citationLineRe is the line v0's retrospectives cite an entry's
	// sources on: where a rule came from, not a rule.
	citationLineRe = regexp.MustCompile(`(?i)^[*_]*sources?[*_]*:`)
)

// listItems returns the list items of lines[from:to] outside code, each
// with its indented continuation lines folded in.
func listItems(lines []mdLine) []LiftCandidate {
	var out []LiftCandidate
	cur := -1
	for _, l := range lines {
		if l.InCode {
			cur = -1
			continue
		}
		if m := listItemRe.FindStringSubmatch(l.Text); m != nil {
			out = append(out, LiftCandidate{Text: strings.TrimSpace(m[1]), Source: itoaLine(l.N), line: l.N})
			cur = len(out) - 1
			continue
		}
		t := strings.TrimSpace(l.Text)
		if cur >= 0 && t != "" && (strings.HasPrefix(l.Text, " ") || strings.HasPrefix(l.Text, "\t")) {
			out[cur].Text += " " + t
			continue
		}
		cur = -1
	}
	var keep []LiftCandidate
	for _, c := range out {
		if minRuleWordsRe.MatchString(c.Text) {
			keep = append(keep, c)
		}
	}
	return keep
}

func itoaLine(n int) string { return strconv.Itoa(n) }

// frontMatterEnd is the index of the first line after a leading YAML
// front matter block (0 when there is none).
func frontMatterEnd(lines []mdLine) int {
	if len(lines) == 0 || strings.TrimSpace(lines[0].Text) != "---" {
		return 0
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i].Text) == "---" {
			return i + 1
		}
	}
	return 0
}

// mdHeading is one open heading of a heading-path walk.
type mdHeading struct {
	level int
	title string
}

// headingPaths returns, per line number, the heading path a line sits
// under: its titles joined with " > ", a document's lone H1 (its title)
// left out — except for a line under that H1 before any other heading,
// whose path is the title itself (E-R7f-1: those rows had none).
func headingPaths(lines []mdLine) map[int]string {
	out := map[int]string{}
	var stack []mdHeading
	h1 := soleTitle(lines)
	title := ""
	for _, l := range lines {
		if lv, t := headingLevel(l); lv > 0 {
			stack = pushHeading(stack, lv, t, h1)
			if lv == 1 && h1 {
				title = t
			}
		}
		out[l.N] = joinPath(stack)
		if out[l.N] == "" {
			out[l.N] = title
		}
	}
	return out
}

// soleTitle reports whether a document has exactly one H1: its title,
// not a section.
func soleTitle(lines []mdLine) bool {
	n := 0
	for _, l := range lines {
		if lv, _ := headingLevel(l); lv == 1 {
			n++
		}
	}
	return n == 1
}

func pushHeading(stack []mdHeading, lv int, t string, soleH1 bool) []mdHeading {
	for len(stack) > 0 && stack[len(stack)-1].level >= lv {
		stack = stack[:len(stack)-1]
	}
	if lv == 1 && soleH1 {
		return stack
	}
	return append(stack, mdHeading{lv, t})
}

func joinPath(stack []mdHeading) string {
	parts := make([]string, len(stack))
	for i, h := range stack {
		parts[i] = h.title
	}
	return strings.Join(parts, " > ")
}

// ruleCandidates returns the rule candidates of a v0 rule document
// (project-context.md, team-memory): every list item of three words or
// more and every rule-shaped paragraph (outside code, lists, tables and
// headings: the paragraph whole), in every section, each with its
// heading path. With entries (team
// memory), each H3+ heading is an entry, and every entry is a rule: one
// whose body gives no candidate is listed by its paragraphs, or, with
// none, by its title, so no entry goes unrecorded.
func ruleCandidates(doc string, entries bool) []LiftCandidate {
	return ruleWalkDoc(doc, entries, false)
}

// claudeCandidates returns CLAUDE.md's candidates (RG-4c, D264). CLAUDE.md
// is the project's own instruction file, read whole by every session, so
// its prose binds whatever its wording: cortex's review found ten of its
// rules with no row, and the rule-word test would have missed seven —
// plain prose ("Keep this file current"), table cells ("MCP session state
// is not the ledger") and a code block's comment ("_output/ … never commit
// anything here"). So every paragraph of three words or more is a
// candidate, every table body row (its cells joined), and every comment in
// its code blocks (codeNotes), besides its list items.
func claudeCandidates(doc string) []LiftCandidate {
	return ruleWalkDoc(doc, false, true)
}

// teamMemoryCandidates returns team-memory's candidates, read whole like
// CLAUDE.md's (D267, D277): every entry body's list items, paragraphs and
// table body rows of three words or more, and its code-block notes —
// whatever their wording. mdpdf's review on rc7g found entry prose with
// no rule word ("a session may not grant itself this route") with no row;
// every team-memory entry is a lesson the project chose to keep, so its
// prose binds as CLAUDE.md's does. An entry that gives nothing else is
// still listed by its title.
func teamMemoryCandidates(doc string) []LiftCandidate {
	return ruleWalkDoc(doc, true, true)
}

// ruleWalkDoc walks doc for candidates. Read whole, a code block's notes
// (codeNotes) are taken where they stand, so they carry the walk's heading
// path and count as their entry's content (an entry whose only rule is a
// commented command is not also listed by its title).
func ruleWalkDoc(doc string, entries, whole bool) []LiftCandidate {
	lines := splitLines(doc)
	lines = lines[frontMatterEnd(lines):]
	w := &ruleWalk{entries: entries, whole: whole, soleH1: soleTitle(lines), item: -1, listed: map[int]bool{}, lastListed: -1}
	if whole {
		w.notes = map[int]LiftCandidate{}
		for _, c := range codeNotes(lines, 0, -1) {
			w.notes[c.line] = c
		}
	}
	for _, l := range lines {
		w.line(l)
	}
	w.flush()
	w.closeTo(1)
	keep := w.out[:0]
	for i, c := range w.out {
		if w.listed[i] || isRule(c) {
			c.Source = itoaLine(c.line)
			keep = append(keep, c)
		}
	}
	return keep
}

// ruleWalk is one ruleCandidates pass over a document.
type ruleWalk struct {
	entries, soleH1 bool
	// whole reads every paragraph as a candidate, not only a rule-shaped
	// one, and every table body row (CLAUDE.md, RG-4c).
	whole bool
	// inTable is whether the previous line was a table row: a row after
	// one is a body row, the first is the header.
	inTable bool
	// notes are the code blocks' notes by their first line (whole only).
	notes map[int]LiftCandidate
	out   []LiftCandidate
	stack []ruleHeading
	para  []mdLine
	// item is the list item being read (its index in out), -1 when none.
	item int
	// listed are an entry's own paragraphs or title, listed because
	// nothing else in it was: kept whatever their shape; lastListed is
	// the last of them.
	listed     map[int]bool
	lastListed int
	// comment is where an open multi-line HTML comment's lines go (RJ-3).
	comment ruleComment
}

// ruleComment is where a ruleWalk puts an open HTML comment's text.
type ruleComment int

const (
	commentNone ruleComment = iota
	// commentItem continues the open list item: the comment is indented
	// under it (cortex's pin bullet, whose LOAD-BEARING note says which
	// gate parses it).
	commentItem
	// commentPara is a paragraph of its own, from the comment's first line.
	commentPara
)

// commentText is an HTML comment line's text without its markers: a
// <!-- left in a table cell would hide the rest of the row when rendered.
func commentText(t string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(t, "<!--", ""), "-->", ""))
}

// ruleHeading is an open heading of a ruleWalk.
type ruleHeading struct {
	mdHeading
	line, from int
	// prose is an entry's paragraphs that are not rule-shaped.
	prose []LiftCandidate
}

func (w *ruleWalk) line(l mdLine) {
	t := strings.TrimSpace(l.Text)
	if w.comment != commentNone {
		w.commentLine(l.N, t)
		return
	}
	if lv, title := headingLevel(l); lv > 0 {
		w.flush()
		w.closeTo(lv)
		w.inTable = false
		w.stack = append(w.stack, ruleHeading{mdHeading: mdHeading{lv, entryDateTail.ReplaceAllString(title, "")}, line: l.N, from: len(w.out)})
		return
	}
	if c, ok := w.notes[l.N]; ok {
		w.flush()
		c.Section = w.path()
		w.out = append(w.out, c)
		return
	}
	if !l.InCode && strings.HasPrefix(t, "<!--") {
		w.openComment(l, t)
		return
	}
	row := !l.InCode && tableRowRe.MatchString(l.Text)
	defer func() { w.inTable = row }()
	switch {
	case row && w.whole && w.inTable:
		w.flush()
		if !tableSepRe.MatchString(l.Text) {
			cells := strings.Split(strings.Trim(t, "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			w.out = append(w.out, LiftCandidate{Text: strings.Join(cells, " | "), line: l.N, Section: w.path()})
		}
	case l.InCode, t == "", thematicRe.MatchString(l.Text), row:
		w.flush()
	case listItemRe.MatchString(l.Text):
		w.flush()
		w.out = append(w.out, LiftCandidate{Text: strings.TrimSpace(listItemRe.FindStringSubmatch(l.Text)[1]), line: l.N, Section: w.path()})
		w.item = len(w.out) - 1
	case w.item >= 0:
		// A continuation, indented or lazy, belongs to its item.
		w.out[w.item].Text += " " + t
	default:
		w.para = append(w.para, l)
	}
}

// openComment reads a line that opens an HTML comment (RJ-3, D291). The
// comment is text, not a break: cortex's pin bullet went on in an
// indented <!-- LOAD-BEARING … --> naming the gate that parses it, then in
// a continuation line, and ending the item at the comment left the bullet
// alone and made the comment's body, minus its first line, and the
// continuation a row starting mid-sentence. Indented under an open list
// item, the comment continues the item, and so do the indented lines
// after it; outside one, a comment of several lines is a paragraph from
// its first line; a one-line comment outside an item (a marker, a lint
// directive) still only ends the paragraph.
func (w *ruleWalk) openComment(l mdLine, t string) {
	w.inTable = false
	closes := strings.Contains(t[len("<!--"):], "-->")
	text := commentText(t)
	switch {
	case w.item >= 0 && strings.TrimLeft(l.Text, " \t") != l.Text:
		if text != "" {
			w.out[w.item].Text += " " + text
		}
		if !closes {
			w.comment = commentItem
		}
	case closes:
		w.flush()
	default:
		w.flush()
		w.para = append(w.para, mdLine{N: l.N, Text: text})
		w.comment = commentPara
	}
}

// commentLine reads a line inside an open HTML comment, up to its -->.
func (w *ruleWalk) commentLine(n int, t string) {
	closes := strings.Contains(t, "-->")
	text := commentText(t)
	if w.comment == commentItem && text != "" {
		w.out[w.item].Text += " " + text
	} else if w.comment == commentPara && text != "" {
		w.para = append(w.para, mdLine{N: n, Text: text})
	}
	if !closes {
		return
	}
	if w.comment == commentPara {
		w.flush()
	}
	w.comment = commentNone
}

// path is the heading path of the line being read: the open headings, a
// sole H1 (the document's title) left out unless it is the only one open
// (E-R7f-1: a candidate right under the title had no section).
func (w *ruleWalk) path() string {
	hs := make([]mdHeading, 0, len(w.stack))
	for _, h := range w.stack {
		if h.level != 1 || !w.soleH1 {
			hs = append(hs, h.mdHeading)
		}
	}
	if len(hs) == 0 && len(w.stack) > 0 {
		return w.stack[0].title
	}
	return joinPath(hs)
}

// flush ends a paragraph: a candidate when it is rule-shaped, else, in an
// entry, held for the entry.
func (w *ruleWalk) flush() {
	if len(w.para) > 0 {
		b := make([]string, 0, len(w.para))
		for _, l := range w.para {
			b = append(b, strings.TrimSpace(quoteRe.ReplaceAllString(l.Text, "")))
		}
		c := LiftCandidate{Text: strings.TrimSpace(strings.Join(b, " ")), line: w.para[0].N, Section: w.path()}
		switch top := len(w.stack) - 1; {
		case w.whole || ruleWordRe.MatchString(c.Text):
			w.out = append(w.out, c)
		case w.entries && top >= 0 && w.stack[top].level >= 3:
			w.stack[top].prose = append(w.stack[top].prose, c)
		}
	}
	w.para, w.item = nil, -1
}

// closeTo closes the open headings of level lv and deeper; an entry that
// gave no candidate is listed by its paragraphs, else its title.
func (w *ruleWalk) closeTo(lv int) {
	for len(w.stack) > 0 && w.stack[len(w.stack)-1].level >= lv {
		h := w.stack[len(w.stack)-1]
		w.stack = w.stack[:len(w.stack)-1]
		if !w.entries || h.level < 3 || slices.ContainsFunc(w.out[h.from:], isRule) || w.lastListed >= h.from {
			continue
		}
		prose := slices.DeleteFunc(h.prose, func(c LiftCandidate) bool { return !isRule(c) })
		if len(prose) == 0 {
			prose = []LiftCandidate{{Text: h.title, line: h.line, Section: w.path()}}
		}
		for _, c := range prose {
			w.listed[len(w.out)], w.lastListed = true, len(w.out)
			w.out = append(w.out, c)
		}
	}
}

// isRule reports whether a list item or paragraph of a rule document is
// a candidate: three words or more, and not the "Source: Epic N
// retrospective" line v0's retrospectives put under each entry, nor a
// list item of links only.
func isRule(c LiftCandidate) bool {
	return minRuleWordsRe.MatchString(c.Text) && !citationLineRe.MatchString(c.Text) && !linksOnlyRe.MatchString(c.Text)
}

func (lf *lifter) candidates() []LiftCandidate {
	var out []LiftCandidate
	add := func(src string, cs []LiftCandidate) {
		for _, c := range cs {
			c.Source = src + ":" + c.Source
			out = append(out, c)
		}
	}
	// withSections fills each list item's heading path from doc.
	withSections := func(doc string, cs []LiftCandidate) []LiftCandidate {
		paths := headingPaths(splitLines(doc))
		for i := range cs {
			cs[i].Section = paths[cs[i].line]
		}
		return cs
	}
	if doc := lf.claudeBefore; doc != "" {
		// The managed block is the v0 framework's, not the project's: its
		// lines are blanked, so every other line keeps its number.
		// RG-4c (D264): CLAUDE.md gave its list items only, and cortex's
		// review found ten rules written as paragraphs with no row; it now
		// goes through the same filter as project-context.md.
		raw := strings.Split(doc, "\n")
		in := false
		for i, l := range raw {
			switch strings.TrimSpace(l) {
			case v0ManagedBegin:
				in = true
				raw[i] = ""
				continue
			case v0ManagedEnd:
				in = false
				raw[i] = ""
				continue
			}
			if in {
				raw[i] = ""
			}
		}
		cs := append(claudeCandidates(strings.Join(raw, "\n")), lf.importCandidates(doc)...)
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].line < cs[j].line })
		add("CLAUDE.md", cs)
	}
	if doc, ok := lf.texts["AGENTS.md"]; ok {
		add("AGENTS.md", withSections(doc, listItems(splitLines(doc))))
	}
	if lf.projectContext != "" {
		doc := lf.readRel(lf.projectContext)
		cs := append(ruleCandidates(doc, false), codeValidationNotes(doc)...)
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].line < cs[j].line })
		add(LiftTag+":"+lf.projectContext, cs)
	}
	if tm := lf.rel(lf.layout.Cfg.Paths.TeamMemory); tm != "" {
		if doc := lf.readRel(tm); doc != "" {
			add(LiftTag+":"+tm, teamMemoryCandidates(doc))
		}
	}
	return dedupCandidates(out)
}

// importCandidates gives one row to each CLAUDE.md import the lift drops
// (RJ-1, D289, D290): the apex:managed block's @_apex/apex-operating-rules.md,
// and any @ import of a file the lift deletes. The blanked block and a
// one-word line gave none, so the v0 operating rules every session loaded
// had no disposition. The row names the file at the tag and how to read it
// there, the lift having deleted it.
func (lf *lifter) importCandidates(doc string) []LiftCandidate {
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	paths := headingPaths(splitLines(doc))
	var out []LiftCandidate
	in := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == v0ManagedBegin:
			in = true
			continue
		case t == v0ManagedEnd:
			in = false
			continue
		case !strings.HasPrefix(t, "@"):
			continue
		}
		rel := strings.TrimPrefix(t, "@")
		if !in && !lf.isDeleted(rel) {
			continue
		}
		from := "CLAUDE.md's apex:managed block"
		if !in {
			from = "CLAUDE.md"
		}
		what := "a file"
		if rel == v0OperatingRules {
			what = "the apex-v0 framework's operating rules"
		}
		text := "`" + t + "`: " + what + ", imported by " + from + "; the lift removes the import and the file."
		if data, err := os.ReadFile(filepath.Join(lf.root, filepath.FromSlash(rel))); err == nil {
			n := strings.Count(string(data), "\n")
			if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
				n++
			}
			unit := "lines"
			if n == 1 {
				unit = "line"
			}
			const kib = 1 << 10
			text = "`" + t + "`: " + fmt.Sprintf("%s (%d %s, %d KB), imported by %s; the lift removes the import and the file. Read it at the tag: `git show %s:%s`.",
				what, n, unit, (len(data)+kib-1)/kib, from, LiftTag, rel)
		} else {
			text += " (not in the tree at " + LiftTag + ")"
		}
		if rel == v0OperatingRules {
			text += " apex-v1 replaces them; keep any rule that still applies to this project."
		}
		out = append(out, LiftCandidate{Text: text, Source: itoaLine(i + 1), Section: paths[i+1], line: i + 1})
	}
	return out
}

// dedupCandidates lists identical text once — compared without case,
// spacing or a closing period — with the other places in AlsoIn.
func dedupCandidates(cs []LiftCandidate) []LiftCandidate {
	seen := map[string]int{}
	var out []LiftCandidate
	for _, c := range cs {
		k := strings.ToLower(strings.TrimRight(strings.Join(strings.Fields(c.Text), " "), ".;:"))
		if i, ok := seen[k]; ok {
			out[i].AlsoIn = append(out[i].AlsoIn, c.Source)
			continue
		}
		seen[k] = len(out)
		out = append(out, c)
	}
	return out
}

// candidateGroup is the disposition's heading for a candidate: its
// source file, then its section.
func candidateGroup(c LiftCandidate) string {
	file := c.Source
	if i := strings.LastIndex(file, ":"); i > 0 {
		file = file[:i]
	}
	if c.Section == "" {
		return "`" + file + "`"
	}
	return "`" + file + "` — " + strings.ReplaceAll(c.Section, "\n", " ")
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}

// The disposition file is what apex-plan, a fresh reviewer and the person
// read whole, so it holds summaries (L11): the dry runs wrote 12–379 KB,
// most of it references to removed paths inside evidence logs, past any
// reader's cap. On rc.3 it was still 109 KB in radix, 78.6 KB of it the
// code and CI references listed line by line, and the JSON beside it was
// a 5.8 MB commit in tenax (R3). So the file has a ceiling, its code and
// CI section is one line per file, and the full lists go to the ignored
// _apex/state/lift-disposition.json: in the checkout that lifted, never in
// history.
//
// R14 (the rc.4 dry runs): cut to its first 10 rows, the binding-rule
// table left 36–68 of each project's candidates in that checkout-local
// JSON — out of reach of the fresh reviewer, and of an apex-plan run in
// any other checkout. The candidates are the list the file exists for, so
// they are never cut to fit the cap: the summaries go down to counts
// first, then the ADR table to the ADRs with gaps, and if the candidates
// still overflow the cap, the file grows past it — up to
// liftDispositionCeiling, and only past that are candidates cut, the file
// saying how many and where they are.
const liftDispositionCap = 16 << 10

// liftDispositionCeiling is the most the disposition file grows to hold
// every candidate: the hard ceiling of the caps on project.md and
// AGENTS.md (caps.go). The file is transient — deleted once its
// candidates have a home.
const liftDispositionCeiling = 48 << 10

// dispLimits bound one rendering of the disposition file: files is how
// many files a summary names (the rest are counted; the report has them
// all), adrs how many ADR rows are listed; readerCounts puts the v0 state
// readers as counts, not one line per file. sizeKB is the rendering's own
// size, for the candidate count line (render). The binding-rule
// candidates have no limit: they are never cut (D274).
type dispLimits struct {
	files, adrs  int
	readerCounts bool
	sizeKB       int
}

// dispSteps are tried in order until a rendering fits the cap: the
// summaries down to counts, then the ADR table. The v0 state readers are
// counted only past the ceiling (disposition); the candidates never are.
//
//nolint:mnd // a table of render steps: each number is a row, not a tunable
var dispSteps = []dispLimits{
	{files: 40, adrs: -1},
	{files: 20, adrs: -1},
	{files: 10, adrs: -1},
	{files: 5, adrs: -1},
	{files: 3, adrs: -1},
	{files: 0, adrs: -1},
	{files: 0, adrs: 60},
	{files: 0, adrs: 30},
	{files: 0, adrs: 10},
}

// liftReport is lift-disposition.json: every list the lift made, whole.
type liftReport struct {
	Version             string           `json:"v0_version"`
	Tag                 string           `json:"tag"`
	TagSHA              string           `json:"tag_sha,omitempty"`
	Deleted             []LiftDeletion   `json:"deleted"`
	ADRs                []LiftADR        `json:"adrs"`
	Changed             []LiftFileChange `json:"changed"`
	Skipped             []LiftSkip       `json:"skipped,omitempty"`
	GoUnformatted       []string         `json:"go_not_formatted,omitempty"`
	Hashed              []LiftHashed     `json:"edited_but_hashed,omitempty"`
	V0StateReaders      []LiftV0Reader   `json:"v0_state_readers,omitempty"`
	UnresolvedCitations []LiftIDRef      `json:"unresolved_adr_citations,omitempty"`
	IDsLeft             []LiftIDRef      `json:"ids_left,omitempty"`
	DanglingRefs        []LiftIDRef      `json:"references_to_removed_paths,omitempty"`
	Candidates          []LiftCandidate  `json:"rule_candidates"`
	Gates               LiftGates        `json:"gates"`
	Readings            []string         `json:"readings,omitempty"`
	// Regression is the lift's regression verdict (RG-6).
	Regression *LiftRegressionRecord `json:"regression,omitempty"`
}

func (lf *lifter) report() string {
	r := lf.res
	data, err := json.MarshalIndent(liftReport{
		Version: r.Version, Tag: LiftTag, TagSHA: r.TagSHA, Deleted: r.Deleted, ADRs: r.ADRs, Changed: r.Changed,
		Skipped: r.Skipped, GoUnformatted: r.GoUnformatted, Hashed: lf.hashedAll, V0StateReaders: r.V0StateReaders, UnresolvedCitations: r.UnresolvedCitations,
		IDsLeft: r.IDsLeft, DanglingRefs: r.DanglingRefs, Candidates: r.Candidates, Gates: r.Gates, Readings: r.Readings,
		Regression: lf.regression,
	}, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(data) + "\n"
}

// disposition renders the disposition file: within liftDispositionCap
// when any step of dispSteps fits; else, every v0 state reader kept,
// within liftDispositionCeiling; else with the readers as counts, at
// whatever size the candidates need.
//
// The binding-rule candidates are never cut (D274). The JSON beside the
// file is git-ignored and exists only in the checkout that lifted, so a
// candidate cut to it was lost to apex-plan run in any other clone: on the
// rc7f rehearsals 61–307 of each project's 213–441 candidates. Every row is
// here, and the file says how many there are, so a reader that read it in
// parts can tell it reached the last (writeCandidates). Measured with every
// row kept: cortex 146 KB, tenax 99 KB, mdpdf 84 KB, radix 71 KB.
func (lf *lifter) disposition() string {
	var out string
	for _, l := range dispSteps {
		if out = lf.render(l); len(out) <= liftDispositionCap {
			return out
		}
	}
	if len(out) <= liftDispositionCeiling {
		return out
	}
	last := dispSteps[len(dispSteps)-1]
	last.readerCounts = true
	return lf.render(last)
}

// render is dispositionWith with the file's own size in its candidate
// count line: rendered once to measure it, then with that size.
func (lf *lifter) render(lim dispLimits) string {
	const kib = 1 << 10
	lim.sizeKB = 0
	lim.sizeKB = (len(lf.dispositionWith(lim)) + kib - 1) / kib
	return lf.dispositionWith(lim)
}

func (lf *lifter) dispositionWith(lim dispLimits) string {
	r := lf.res
	report := r.Report
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	p("# Lift disposition\n\n")
	p("Lifted from apex-v0 %s; the apex-v0 state is archived at the tag `%s` (%s), read it as `git show %s:<path>`.\n\n", r.Version, LiftTag, short(r.TagSHA), LiftTag)
	p("This file is the lift's hand-off. apex-plan fills the Disposition column and carries each kept rule to its one home;\n")
	p("a fresh read-only session reviews the result; then the person does, and deletes this file.\n")
	p("Every binding-rule candidate is in this file. The other long lists are summarised here; `%s` holds every one whole. Git ignores it: it is in the checkout that lifted, never committed.\n\n", report)
	// RG-2: named relative to the project root, never a home path (D282).
	for _, w := range r.Warnings {
		p("**Warning (%s):** %s.\n\n", w.Code, w.Message)
	}

	p("## Binding-rule candidates\n\n")
	p("Disposition is one of: `kept in AGENTS.md` · `gated by <cmd>` · `ADR-NNNN` · `docs/<page>` · `dropped: <reason>`.\n\n")
	writeCandidates(&b, r.Candidates, lim.sizeKB)

	p("## Gate commands\n\n")
	switch r.Gates.Source {
	case "":
		p("No `## Code Validation` section was found: AGENTS.md has no commands and `regression:` is not set.\n\n")
	default:
		p("From `%s` at %s, into AGENTS.md `## Commands`:\n\n", r.Gates.Source, LiftTag)
		for _, c := range r.Gates.Commands {
			p("- `%s`\n", strings.ReplaceAll(c, "\n", " ⏎ "))
		}
		if r.Gates.Regression != "" {
			p("\n`regression:` in _apex/config.yaml is `%s` — check it is the suite a change must keep green.\n", r.Gates.Regression)
		}
		for _, c := range r.Gates.NotRunnable {
			p("- not in `regression:` (it has a placeholder): `%s`\n", c)
		}
		p("\n")
	}

	// What can break the build or a guard comes before what only reads.
	writeV0Readers(&b, r.V0StateReaders, lim.readerCounts, report)
	readers := map[string]bool{}
	for _, v := range r.V0StateReaders {
		readers[v.File] = true
	}
	code, records := lf.splitRefs(r.DanglingRefs)
	code = slices.DeleteFunc(code, func(x LiftIDRef) bool { return readers[x.File] })
	p("## Code and CI that still read removed paths\n\n")
	if len(code) == 0 {
		p("None.\n\n")
	} else {
		p("Kept code, scripts and CI files that name a path the lift removed in a comment, or in a file that is not code (.json, .gitignore, …): repoint or drop them (%d reference(s) in %d file(s); per file, the count and the first; the readers above are not repeated):\n\n", len(code), countFiles(code))
		writeFirstRefs(&b, code, lim.files, report)
	}
	if len(r.Hashed) > 0 {
		p("Edited files a test, CI script or lock file seems to pin the bytes of (re-run that guard; if it is red, the file belonged under --exclude):\n\n")
		lf.writeHashed(&b, lim.files, report)
		p("\n")
	}
	if len(r.GoUnformatted) > 0 {
		p("Go files that were not gofmt-clean at %s: edited, not formatted — `gofmt -l` flags them as it did before:\n\n", LiftTag)
		writeList(&b, r.GoUnformatted, lim.files, report)
	}

	p("## ADRs\n\n")
	if sa := r.StandardADR; sa != nil {
		p("[%s](%s) is APEX's standard ADR, Record architecture decisions (%s), written by the lift%s.\n\n",
			StandardADRID, strings.TrimPrefix(sa.Path, lf.devRel+"/"), standardSourceNote(sa), standardReplacesNote(sa))
	}
	if len(r.ADRs) == 0 {
		p("No ADRs.\n\n")
	} else {
		writeADRTable(&b, r.ADRs, lim.adrs, lf.devRel, report)
	}
	if r.Canon != "" {
		p("Canon: %s.\n\n", r.Canon)
	}
	for _, f := range r.ADRsNotConverted {
		p("- not converted (no ADR id in it), left in place: `%s`\n", f)
	}
	if len(r.UnresolvedCitations) > 0 {
		p("ADR citations that name no ADR of the project's, left as written — another log's short id (ADR-08) or a number no record has (%d in %d file(s)):\n\n",
			len(r.UnresolvedCitations), countFiles(r.UnresolvedCitations))
		writeCounts(&b, r.UnresolvedCitations, func(x LiftIDRef) string { return x.Text }, lim.files, report)
	}

	p("## Ids left in code\n\n")
	p("The lift edits comments only (D37). These ids sit in identifiers, strings, directives, data-shaped comments, comments that use one as a possessive (\"Story 3.2's\": no removal keeps that sentence) or files it does not edit:\n")
	p("rename them in a change if they should go. An ADR id here still has its OLD number.\n\n")
	if len(r.IDsLeft) == 0 {
		p("None.\n\n")
	} else {
		p("%d id(s) in %d file(s), by file and kind:\n\n", len(r.IDsLeft), countFiles(r.IDsLeft))
		writeCounts(&b, r.IDsLeft, func(x LiftIDRef) string { return x.Where }, lim.files, report)
	}
	writeSkipped(&b, r.Skipped, lim.files, report)

	p("## References to removed paths in records and docs\n\n")
	if len(records) == 0 {
		p("None.\n\n")
	} else {
		p("Evidence logs, research notes and docs that name a removed path (%d reference(s) in %d file(s)); they read at %s. Counted, not listed:\n\n",
			len(records), countFiles(records), LiftTag)
		writeCounts(&b, records, nil, lim.files, report)
	}
	if len(r.KeptApex) > 0 {
		p("Left under `_apex/` (not apex-v0 framework tables): %s\n\n", "`"+strings.Join(r.KeptApex, "`, `")+"`")
	}

	p("## Readings at %s\n\n", LiftTag)
	if len(r.Readings) == 0 {
		p("None found.\n")
	}
	for _, s := range r.Readings {
		p("- %s\n", s)
	}
	// No regression runs: the section says why, in the lift commit (RG-6).
	if lf.regression != nil {
		writeRegression(&b, lf.regression, report, false)
	}
	return b.String()
}

// writeCandidates is the disposition's binding-rule table, grouped by
// source and section (RG-4) and numbered through, every candidate (D274).
// Its first line says how many rows there are and how big the file is
// (sizeKB): past Claude Code's per-Read cap the file is read in parts, and
// a reader that stopped short can tell.
func writeCandidates(b *strings.Builder, cands []LiftCandidate, sizeKB int) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	if len(cands) == 0 {
		p("None found (no CLAUDE.md list items, no project-context or team-memory rules).\n\n")
		return
	}
	p("This file lists every candidate: %d rows, numbered 1–%d (about %d KB). If a Read stops short, read on with offset/limit until row %d.\n\n",
		len(cands), len(cands), sizeKB, len(cands))
	group := ""
	for i, c := range cands {
		if g := candidateGroup(c); i == 0 || g != group {
			if i > 0 {
				p("\n")
			}
			p("### %s\n\n| # | Candidate | Line | Disposition |\n|---|---|---|---|\n", g)
			group = g
		}
		// The group heading names the file: the row keeps its line (Q136).
		p("| %d | %s | `%s` | |\n", i+1, cell(c.Text), strings.Join(append([]string{candidateLine(c.Source)}, c.AlsoIn...), "` · `"))
	}
	p("\n")
}

// writeRegression is the disposition file's `## Regression` (RG-6): the
// lift's regression verdict and note, in the names the v1.0.0 migration
// entry reads, then one line per command with its exits and logs, each
// with its note. compact (when the file would pass its ceiling) keeps only
// the worst command's note — the one that entry branches on ("failing as
// it did there", "the output changed after the lift"), which the .md must
// carry, the JSON being in the lifting checkout only (RG-6a, D259); the
// report has every one.
func writeRegression(b *strings.Builder, rec *LiftRegressionRecord, report string, compact bool) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	p("\n## Regression\n\n")
	if rec.Verdict == "" {
		p("Verdict: none — %s; nothing was compared with a run at %s.\n", rec.Note, LiftTag)
		return
	}
	p("The `regression:` command ran at %s, before the lift wrote anything, and at the lift commit (%s); the second run is judged against the first. ", LiftTag, short(rec.Commit))
	p("The whole record is `regression` in `%s`, in the checkout that lifted.\n\n", report)
	worst := 0
	for i := range rec.Commands {
		if liftVerdictRank(rec.Commands[i].Verdict) == liftVerdictRank(rec.Verdict) {
			worst = i
			break
		}
	}
	if n := len(rec.Commands); n > 1 {
		// The notes are on the commands' lines: the chain's is not repeated.
		p("Verdict: `%s` — the worst of the %d commands below, each run on its own at both points, past a red one: %d, `%s`.\n\n",
			rec.Verdict, n, worst+1, strings.ReplaceAll(rec.Commands[worst].Cmd, "\n", " ⏎ "))
	} else {
		p("Verdict: `%s` — %s\n\n", rec.Verdict, rec.Note)
	}
	if len(rec.NewFailures) > 0 {
		p("regression_new_failures: `%s`\n\n", strings.Join(rec.NewFailures, "`, `"))
	}
	for i := range rec.Commands {
		c := &rec.Commands[i]
		at := "not run at " + LiftTag
		if c.ExitAtTag != nil {
			at = fmt.Sprintf("exit %d at %s", *c.ExitAtTag, LiftTag)
		}
		p("%d. `%s` — `%s`: %s, exit %d after the lift", i+1, strings.ReplaceAll(c.Cmd, "\n", " ⏎ "), c.Verdict, at, c.ExitAfter)
		if c.ExitRerun != nil {
			p(", exit %d on a re-run", *c.ExitRerun) // RG-1
		}
		if len(c.NewFailures) > 0 {
			p("; regression_new_failures: `%s`", strings.Join(c.NewFailures, "`, `"))
		}
		p(". Logs: `%s`", c.Log)
		if c.LogAtTag != "" {
			p(", `%s`", c.LogAtTag)
		}
		if c.LogRerun != "" {
			p(", `%s`", c.LogRerun)
		}
		if len(rec.Commands) > 1 && (!compact || i == worst) {
			p(". %s", c.Note)
		}
		p("\n")
	}
}

// writeADRTable is the disposition's ADR table: old → new, status and
// gaps, at most maxRows rows (all of them when maxRows < 0).
func writeADRTable(b *strings.Builder, adrs []LiftADR, maxRows int, devRel, report string) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	p("| Old | New | Status | Gaps |\n|---|---|---|---|\n")
	// Short of room, the ADRs with gaps are the ones listed: every
	// old → new is in the lift commit's message too.
	var shown, rest []*LiftADR
	for i := range adrs {
		if a := &adrs[i]; maxRows < 0 || len(a.Gaps) > 0 && len(shown) < maxRows {
			shown = append(shown, a)
		} else {
			rest = append(rest, a)
		}
	}
	for _, a := range shown {
		old := a.Old
		if a.Canon {
			old += " (canon)"
		}
		p("| %s | [%s](%s) | %s | %s |\n", old, a.New, strings.TrimPrefix(a.Path, devRel+"/"), cell(a.Status), cell(strings.Join(a.Gaps, "; ")))
	}
	if len(rest) > 0 {
		p("\n… and %d more ADR(s); every old → new is in the lift commit's message and `%s`\n", len(rest), report)
	}
	p("\n")
}

// writeSkipped summarises the files the lift did not edit: a count per
// reason, then the files left for a reason other than their type.
func writeSkipped(b *strings.Builder, skipped []LiftSkip, maxFiles int, report string) {
	if len(skipped) == 0 {
		return
	}
	reasons := map[string]int{}
	var order, other []string
	for _, sk := range skipped {
		why := sk.Reason
		if i := strings.Index(why, "):"); i > 0 && strings.HasPrefix(why, "not tokenized") {
			why = why[:i+1]
		} else {
			other = append(other, fmt.Sprintf("%#q — %s (%d id(s), %d ADR citation(s))", sk.File, sk.Reason, sk.IDs, sk.ADRCitations))
		}
		if reasons[why] == 0 {
			order = append(order, why)
		}
		reasons[why]++
	}
	fmt.Fprintf(b, "Files not edited (%d), by reason:\n\n", len(skipped))
	for _, why := range order {
		fmt.Fprintf(b, "- %d × %s\n", reasons[why], why)
	}
	b.WriteString("\n")
	if len(other) == 0 {
		return
	}
	b.WriteString("Left for a reason other than the file type:\n\n")
	for i, l := range other {
		if i == maxFiles {
			fmt.Fprintf(b, "- … and %d more (in `%s`)\n", len(other)-maxFiles, report)
			break
		}
		fmt.Fprintf(b, "- %s\n", l)
	}
	b.WriteString("\n")
}

// splitRefs parts references to removed paths into those in code and CI
// (to act on) and those in records and docs: the development folder,
// docs/, and prose and log files.
func (lf *lifter) splitRefs(refs []LiftIDRef) (code, records []LiftIDRef) {
	for _, r := range refs {
		ext := strings.ToLower(path.Ext(r.File))
		switch {
		case strings.HasPrefix(r.File, lf.devRel+"/"), strings.HasPrefix(r.File, "docs/"),
			ext == ".md", ext == ".markdown", ext == ".txt", ext == ".log", ext == ".jsonl", ext == ".rst", ext == ".adoc":
			records = append(records, r)
		default:
			code = append(code, r)
		}
	}
	return code, records
}

func countFiles(refs []LiftIDRef) int {
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.File] = true
	}
	return len(seen)
}

// byFile groups refs by file, files sorted by count (most first), then
// name.
func byFile(refs []LiftIDRef) (files []string, m map[string][]LiftIDRef) {
	m = map[string][]LiftIDRef{}
	for _, r := range refs {
		if _, ok := m[r.File]; !ok {
			files = append(files, r.File)
		}
		m[r.File] = append(m[r.File], r)
	}
	sort.Slice(files, func(i, j int) bool {
		if a, b := len(m[files[i]]), len(m[files[j]]); a != b {
			return a > b
		}
		return files[i] < files[j]
	})
	return files, m
}

// writeFirstRefs writes one line per file, at most maxFiles: its count
// and its first reference.
func writeFirstRefs(b *strings.Builder, refs []LiftIDRef, maxFiles int, report string) {
	files, m := byFile(refs)
	for fi, f := range files {
		if fi == maxFiles {
			fmt.Fprintf(b, "- … and %d more file(s) (in `%s`)\n", len(files)-maxFiles, report)
			break
		}
		first := slices.MinFunc(m[f], func(x, y LiftIDRef) int { return x.Line - y.Line })
		fmt.Fprintf(b, "- `%s` — %d, first at line %d: `%s`\n", f, len(m[f]), first.Line, strings.ReplaceAll(first.Text, "`", "'"))
	}
	b.WriteString("\n")
}

// writeCounts writes one line per file: its count, and with key the
// count of each key ("identifier ×3, string ×2").
func writeCounts(b *strings.Builder, refs []LiftIDRef, key func(LiftIDRef) string, maxFiles int, report string) {
	files, m := byFile(refs)
	for fi, f := range files {
		if fi == maxFiles {
			rest := 0
			for _, g := range files[maxFiles:] {
				rest += len(m[g])
			}
			fmt.Fprintf(b, "- … and %d more in %d more file(s) (in `%s`)\n", rest, len(files)-maxFiles, report)
			break
		}
		rs := m[f]
		if key == nil {
			fmt.Fprintf(b, "- `%s` — %d\n", f, len(rs))
			continue
		}
		counts := map[string]int{}
		var order []string
		for _, r := range rs {
			k := strings.ReplaceAll(key(r), "`", "'")
			if counts[k] == 0 {
				order = append(order, k)
			}
			counts[k]++
		}
		parts := make([]string, 0, len(order))
		for _, k := range order {
			if counts[k] > 1 {
				parts = append(parts, fmt.Sprintf("%s ×%d", k, counts[k]))
			} else {
				parts = append(parts, k)
			}
		}
		fmt.Fprintf(b, "- `%s` — %d: %s\n", f, len(rs), strings.Join(parts, ", "))
	}
	b.WriteString("\n")
}

func writeList(b *strings.Builder, items []string, maxItems int, report string) {
	for i, it := range items {
		if i == maxItems {
			fmt.Fprintf(b, "- … and %d more (in `%s`)\n", len(items)-maxItems, report)
			break
		}
		fmt.Fprintf(b, "- `%s`\n", it)
	}
	b.WriteString("\n")
}

// writeHashed lists the hashed edits: those a lock file or a hasher names
// one line per file, at most hashedCap and maxFiles; then those only a
// folder ties to a hasher, one line per hasher and folder (R22: mdpdf's
// 57 files beside one guard that hashes its fixtures, not its sources,
// were 50 lines of the file).
func (lf *lifter) writeHashed(b *strings.Builder, maxFiles int, report string) {
	var named []LiftHashed
	type group struct {
		by, dir string
		n       int
	}
	var groups []*group
	at := map[[2]string]*group{}
	for _, h := range lf.hashedAll {
		if lf.hashedRank[h.File] != hashedByFolder {
			named = append(named, h)
			continue
		}
		dir := path.Dir(h.File)
		for _, by := range h.By {
			k := [2]string{by, dir}
			if at[k] == nil {
				at[k] = &group{by: by, dir: dir}
				groups = append(groups, at[k])
			}
			at[k].n++
		}
	}
	shown := min(len(named), hashedCap, max(maxFiles, 1))
	for _, h := range named[:shown] {
		fmt.Fprintf(b, "- `%s` — %s\n", h.File, "`"+strings.Join(h.By, "`, `")+"`")
	}
	if more := len(named) - shown; more > 0 {
		fmt.Fprintf(b, "- … and %d more (in `%s`)\n", more, report)
	}
	for _, g := range groups {
		if path.Dir(g.by) == g.dir {
			fmt.Fprintf(b, "- %s in `%s` share a folder with `%s` (folder match only)\n", nFiles(g.n), g.dir, path.Base(g.by))
		} else {
			fmt.Fprintf(b, "- %s in `%s` sit in a folder `%s` names near a hash (folder match only)\n", nFiles(g.n), g.dir, g.by)
		}
	}
}

// nFiles is "1 file" or "n files".
func nFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// v0ReaderReadsShown is how many paths a reader's line names; the report
// has every one.
const v0ReaderReadsShown = 10

// writeV0Readers is the disposition's section on code and tests that read
// deleted v0 state (D141): one line per file with what it reads, never cut
// to fit the cap; as counts, saying so, past the ceiling.
func writeV0Readers(b *strings.Builder, readers []LiftV0Reader, counts bool, report string) {
	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	p("## Code and tests that read deleted apex-v0 state\n\n")
	if len(readers) == 0 {
		p("None.\n\n")
		return
	}
	p("Kept code, tests, scripts and CI files that name apex-v0 state the lift deletes outside their comments — the tracker, ADR folders, deferred records — so they fail once it is gone: re-point each to its apex-v1 state, or retire it (%s; it reads it at %s):\n\n",
		nFiles(len(readers)), LiftTag)
	if !counts {
		for _, v := range readers {
			shown := v.Reads[:min(len(v.Reads), v0ReaderReadsShown)]
			line := "`" + strings.Join(shown, "`, `") + "`"
			if more := len(v.Reads) - len(shown); more > 0 {
				line += fmt.Sprintf(" and %d more (in %#q)", more, report)
			}
			p("- `%s` reads %s\n", v.File, line)
		}
		p("\n")
		return
	}
	n := map[string]int{}
	var paths []string
	for _, v := range readers {
		for _, r := range v.Reads {
			if n[r] == 0 {
				paths = append(paths, r)
			}
			n[r]++
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		if a, b := n[paths[i]], n[paths[j]]; a != b {
			return a > b
		}
		return paths[i] < paths[j]
	})
	p("Counted, not listed, past this file's %d KB ceiling: every file and what it reads is in `%s` (v0_state_readers), in the checkout that lifted. By what they read:\n\n", liftDispositionCeiling>>10, report)
	for i, r := range paths {
		if i == v0ReaderReadsShown {
			p("- … and %d more path(s)\n", len(paths)-i)
			break
		}
		p("- `%s` — %s\n", r, nFiles(n[r]))
	}
	p("\n")
}

// standardSourceNote names where the standard's text came from.
func standardSourceNote(sa *StandardADRResult) string {
	if sa.Source == StandardSourceApe {
		return "ape's own copy: no framework payload was installed"
	}
	if sa.FrameworkVersion != "" {
		return sa.Source + ", framework " + sa.FrameworkVersion
	}
	return sa.Source
}

// candidateLine is a candidate's line number from its source
// (`<file>:<line>`), the file being its group's heading (Q136); the
// source whole when it has no line.
func candidateLine(src string) string {
	if i := strings.LastIndex(src, ":"); i > 0 && i < len(src)-1 {
		return src[i+1:]
	}
	return src
}

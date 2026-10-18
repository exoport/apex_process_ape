package apexv1

import (
	"go/format"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// LiftIDRef is one id the lift found and did not edit, or edited text
// that needs a reader: where says which (identifier, string, code,
// directive, continuation, jsx-inline, generated).
type LiftIDRef struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Text  string `json:"text"`
	Where string `json:"where"`
}

// liftFileEdit is what the lift does to one code file.
type liftFileEdit struct {
	Rel string
	Old string
	New string
	// IDsRemoved counts the comments that lost an id; ADRsRewritten the
	// ADR citations renumbered in comments.
	IDsRemoved    int
	ADRsRewritten int
	Left          []LiftIDRef
	// Unresolved are ADR citations in comments that name no ADR of the
	// project's.
	Unresolved []LiftIDRef
}

var (
	directiveRe = regexp.MustCompile(`^(?://[a-z][a-z0-9_.-]*:|//line |//export |//extern |#!|(?://+|#+|/\*+)\s*(?i:\+build|eslint|nolint|noqa|shellcheck|prettier-ignore|istanbul|biome-ignore|clang-format|ignore:|ignore_for_file:|@ts-|@flow|@jsx|@dart|#__pure__|<reference|sourcemappingurl|yamllint|yaml-language-server|pylint:|mypy:|nosec|pyright:|isort:|fmt:|type:|pragma|-\*-|iwyu))`)
	generatedRe = regexp.MustCompile(`(?i)code generated .*do not edit|@generated\b`)
	lineMarkRe  = regexp.MustCompile(`^(//+!?|#+|--)`)
	blockDecoRe = regexp.MustCompile(`^[ \t]*\*+(?:[ \t]|$)`)
)

// generatedHead is how much of a file is searched for a generator's
// marker.
const generatedHead = 2048

// isGenerated reports a file a generator owns: its edits would be undone.
func isGenerated(src string) bool {
	head := src
	if len(head) > generatedHead {
		head = head[:generatedHead]
	}
	return generatedRe.MatchString(head)
}

// lineAt is the 1-based line of byte offset off.
func lineAt(src string, off int) int { return strings.Count(src[:off], "\n") + 1 }

// editComments strips ids from, and renumbers ADR citations in, the
// comments of one file, and lists the ids it leaves in code and strings.
// An error means the file is left as it is (and listed).
//
//nolint:gocyclo // one pass over a file's segments: each branch is a reason a comment is left as written or how its removal is spliced
func editComments(rel, src string, lang liftLang, vocab *liftVocab, adr *adrRewriter) (*liftFileEdit, error) {
	res := &liftFileEdit{Rel: rel, Old: src, New: src}
	var segs []tokSeg
	var idents []goIdent
	var err error
	if lang == langGo {
		segs, idents, err = tokenizeGo(src)
	} else {
		segs, err = tokenize(lang, src)
	}
	if err != nil {
		return nil, err
	}
	left := func(off int, text, where string) {
		res.Left = append(res.Left, LiftIDRef{File: rel, Line: lineAt(src, off), Text: text, Where: where})
	}
	listIDs := func(off int, text, where string) {
		for _, sp := range vocab.idSpans(text) {
			left(off+sp[0], text[sp[0]:sp[1]], where)
		}
		for _, m := range adrCiteRe.FindAllStringIndex(text, -1) {
			left(off+m[0], text[m[0]:m[1]], where+" (ADR id, not renumbered)")
		}
	}
	generated := isGenerated(src)

	// Code between the segments: identifiers (and, outside Go, whatever
	// the tokenizer leaves as code — a YAML plain scalar, a recipe line).
	if lang == langGo {
		for _, id := range idents {
			for _, m := range identIDs(id.Name) {
				left(id.Start, m, "identifier")
			}
		}
	} else {
		prev := 0
		for _, s := range append(slices.Clone(segs), tokSeg{Start: len(src), End: len(src)}) {
			gap := src[prev:s.Start]
			for _, m := range identWordRe.FindAllStringIndex(gap, -1) {
				for _, id := range identIDs(gap[m[0]:m[1]]) {
					left(prev+m[0], id, "identifier")
				}
			}
			listIDs(prev, gap, "code")
			prev = s.End
		}
	}

	// Each comment is cleaned on its own, then the runs of line comments
	// are fixed as one text (fixParens), then spliced.
	var cs []*liftComment
	for _, s := range segs {
		if s.Kind == segComment && !s.Block && s.End > s.Start && src[s.End-1] == '\r' {
			s.End-- // a CRLF line keeps its CR
		}
		text := src[s.Start:s.End]
		if s.Kind == segString {
			listIDs(s.Start, text, "string")
			continue
		}
		if generated {
			listIDs(s.Start, text, "generated")
			continue
		}
		if directiveRe.MatchString(text) {
			listIDs(s.Start, text, "directive")
			continue
		}
		if isDataComment(text) {
			listIDs(s.Start, text, "data comment")
			continue
		}
		lineStart := strings.LastIndexByte(src[:s.Start], '\n') + 1
		le := lineEnd(src, s.End)
		c := &liftComment{
			seg: s, text: text, out: text, lineStart: lineStart, le: le,
			alone: strings.TrimSpace(src[lineStart:s.Start]) == "" && strings.TrimSpace(src[s.End:le]) == "",
		}
		if !s.Block {
			c.marker = lineMarkRe.FindString(text)
		}
		// R2: "Story 15.4's own deliverable" has no rewrite that keeps
		// its meaning ("'s own deliverable" was 1,941 lines in cortex):
		// the comment is listed and left as written, its ADR citations
		// still renumbered. A removed path's possessive likewise (R10),
		// and its paths are listed with its ids.
		if vocab.possessive(text) {
			for _, sp := range vocab.spans(text) {
				left(s.Start+sp[0], text[sp[0]:sp[1]], "possessive")
			}
			c.keep = true
			var unresolved []string
			c.out, c.nADR, unresolved = adr.rewrite(text)
			for _, u := range unresolved {
				res.Unresolved = append(res.Unresolved, LiftIDRef{File: rel, Line: lineAt(src, s.Start), Text: u, Where: "comment"})
			}
			cs = append(cs, c)
			continue
		}
		var unresolved []string
		c.out, c.emptied, c.nIDs, c.nADR, unresolved = cleanComment(text, s.Block, vocab, adr)
		for _, u := range unresolved {
			res.Unresolved = append(res.Unresolved, LiftIDRef{File: rel, Line: lineAt(src, s.Start), Text: u, Where: "comment"})
		}
		if c.out != text || c.emptied {
			if lang == langJSX && strings.TrimSpace(src[lineStart:s.Start]) != "" {
				// Markup text can hold `//`: only a comment that begins its
				// line is surely a comment.
				listIDs(s.Start, text, "jsx-inline")
				continue
			}
			if c.emptied && c.alone && c.continued(src) {
				// Deleting a line after a continued one would join the next
				// line onto it.
				listIDs(s.Start, text, "continuation")
				continue
			}
		}
		cs = append(cs, c)
	}
	fixParens(src, cs)

	repls := commentRepls(src, cs, res)
	if len(repls) == 0 {
		return res, nil
	}
	var b strings.Builder
	prev := 0
	for _, r := range repls {
		b.WriteString(src[prev:r.start])
		b.WriteString(r.text)
		prev = r.end
	}
	b.WriteString(src[prev:])
	res.New = b.String()
	if err := verifyCommentOnly(lang, src, res.New); err != nil {
		return nil, err
	}
	return res, nil
}

// commentRepl is one splice of editComments: src[start:end] becomes text.
type commentRepl struct {
	start, end int
	text       string
}

// commentRepls is how each comment the lift changed is spliced into src,
// in order: rewritten in place, or deleted with its line, its leading
// blanks, or down to the separator it kept two tokens apart with. The ids
// and ADR citations those comments drop are counted into res.
func commentRepls(src string, cs []*liftComment, res *liftFileEdit) []commentRepl {
	var repls []commentRepl
	for _, c := range cs {
		s, text, newText, lineStart, le := c.seg, c.text, c.out, c.lineStart, c.le
		if newText == text && !c.emptied {
			continue
		}
		res.IDsRemoved += c.nIDs
		res.ADRsRewritten += c.nADR
		switch {
		case !c.emptied:
			repls = append(repls, commentRepl{s.Start, s.End, newText})
		case c.alone:
			repls = append(repls, commentRepl{lineStart, min(le+1, len(src)), ""})
		case !s.Block:
			ws := s.Start
			for ws > lineStart && (src[ws-1] == ' ' || src[ws-1] == '\t') {
				ws--
			}
			repls = append(repls, commentRepl{ws, s.End, ""})
		default:
			ws := s.Start
			for ws > lineStart && (src[ws-1] == ' ' || src[ws-1] == '\t') {
				ws--
			}
			sep := ""
			switch {
			case strings.Contains(text, "\n"):
				sep = "\n"
			case ws > 0 && s.End < len(src) && isIdentByte(src[ws-1]) && isIdentByte(src[s.End]):
				sep = " "
			}
			repls = append(repls, commentRepl{ws, s.End, sep})
		}
	}
	return repls
}

// liftComment is one comment of a file the lift edits: as written, as
// cleaned, and where its line is.
type liftComment struct {
	seg       tokSeg
	text, out string
	// marker is a line comment's ("//", "#"); "" for a block.
	marker        string
	lineStart, le int
	// alone: nothing but the comment on its line; emptied: it goes.
	alone, emptied bool
	// keep: left as written (a possessive id); its brackets still count.
	keep       bool
	nIDs, nADR int
}

// continued reports a comment line after a line ending in a backslash:
// deleting it would join the next line onto that one.
func (c *liftComment) continued(src string) bool {
	return c.lineStart > 0 && strings.HasSuffix(strings.TrimRight(src[:c.lineStart-1], " \t\r"), "\\")
}

// runsInto reports b as the line comment right below a, both alone on
// their lines with the same marker: one paragraph.
func (c *liftComment) runsInto(b *liftComment) bool {
	return c.marker != "" && c.alone && b.alone && c.marker == b.marker && b.lineStart == c.le+1
}

// fixParens closes what a removal left open across lines (R2). An aside
// that opens on one comment line and closes on another is cleaned a line
// at a time: "Bounded (Story 3.2," leaves "Bounded (" over "// Task 4)
// holds", and a line of nothing but ids takes the ")" of the line above
// with it — on rc.3, 26–34 comment lines per project began with an
// orphaned ")". So a "(" whose id went goes with its ")", and in a run of
// line comments whose brackets balanced at the tag, a bracket left
// without its pair, or a pair left around nothing, goes too; a line left
// with no word goes. Only a run some id was stripped from is touched,
// and a kept (possessive) line is never edited.
func fixParens(src string, cs []*liftComment) {
	for i := 0; i < len(cs); {
		j := i + 1
		for j < len(cs) && cs[j-1].runsInto(cs[j]) {
			j++
		}
		run := cs[i:j]
		i = j
		if run[0].marker == "" || !slices.ContainsFunc(run, func(c *liftComment) bool { return c.nIDs > 0 }) {
			continue
		}
		fixRunParens(src, run)
	}
}

func fixRunParens(src string, run []*liftComment) {
	n := len(run)
	old, cur := make([]string, n), make([]string, n)
	dead, touched := make([]bool, n), make([]bool, n)
	for k, c := range run {
		old[k], dead[k] = c.text[len(c.marker):], c.emptied
		if !c.emptied {
			cur[k] = c.out[len(c.marker):]
		}
	}
	drop := func(at [][2]int) {
		sort.Slice(at, func(i, j int) bool {
			if at[i][0] != at[j][0] {
				return at[i][0] < at[j][0]
			}
			return at[i][1] > at[j][1]
		})
		for _, d := range at {
			if !run[d[0]].keep {
				cur[d[0]], touched[d[0]] = dropParen(cur[d[0]], d[1]), true
			}
		}
	}
	// A "(" its id was stripped from, left at the end of its line, goes
	// with its ")".
	var first [][2]int
	pairs := parenScan(cur, dead).pairs
	for k, c := range run {
		t := strings.TrimRight(cur[k], " \t")
		if dead[k] || c.keep || !strings.HasSuffix(t, "(") || strings.HasSuffix(strings.TrimRight(old[k], " \t"), "(") {
			continue
		}
		at := [2]int{k, len(t) - 1}
		first = append(first, at)
		if cl, ok := pairs[at]; ok {
			first = append(first, cl)
		}
	}
	drop(first)
	// Then, where the run balanced at the tag, a bracket left without its
	// pair, or a pair left around no word across lines.
	o, c := parenScan(old, make([]bool, n)), parenScan(cur, dead)
	var then [][2]int
	if len(o.closers) == 0 {
		then = append(then, c.closers...)
	}
	if len(o.openers) == 0 {
		then = append(then, c.openers...)
	}
	if len(o.empty) == 0 {
		then = append(then, c.empty...)
	}
	drop(then)
	carryLeadPunct(run, old, cur, dead, touched)
	for k, c := range run {
		if !touched[k] {
			continue
		}
		if !hasWordRune(cur[k]) && (!c.alone || !c.continued(src)) {
			c.emptied = true
			continue
		}
		// Never a marker glued to its text where the tag had a blank
		// between them: "//." fails gocritic's commentFormatting (R7).
		if lead := leadBlanks(old[k]); lead != "" && leadBlanks(cur[k]) == "" {
			cur[k] = lead + cur[k]
		}
		c.out = c.marker + strings.TrimRight(cur[k], " \t")
	}
}

// leadPunct is what a removal can leave a comment line opening on: the
// closer of an aside that opened on the line above, the mark that ended
// it, a separator.
const leadPunct = ")].!?,;:"

// carryLeadPunct moves the punctuation a removal left at the start of a
// line to the end of the live line above (R7, R8): a closer and a
// sentence's mark go there, a separator goes, and so does the separator
// the line above ended on before a closer. "REPLACE anything (Story 3.7;"
// over "Story 7.2). Every other" is "REPLACE anything." over "Every
// other", not "//. Every other"; "chain (ADR-0008," over "Story 1.3),
// extracted" is "chain (ADR-0008)" over "extracted". With no line of
// words above that the lift may edit (the run's first, a blank "//", a
// kept one), it all goes.
func carryLeadPunct(run []*liftComment, old, cur []string, dead, touched []bool) {
	gone := func(a int) bool { return dead[a] || touched[a] && !hasWordRune(cur[a]) }
	for k, c := range run {
		if dead[k] || c.keep {
			continue
		}
		rest := cur[k][len(leadBlanks(cur[k])):]
		was := strings.TrimLeft(old[k], " \t")
		if rest == "" || strings.IndexByte(leadPunct, rest[0]) < 0 || was != "" && strings.IndexByte(leadPunct, was[0]) >= 0 {
			continue
		}
		n := len(rest) - len(strings.TrimLeft(rest, leadPunct+" \t"))
		var moved strings.Builder
		for _, r := range rest[:n] {
			if strings.ContainsRune(")].!?", r) {
				moved.WriteRune(r)
			}
		}
		cur[k], touched[k] = leadBlanks(old[k])+rest[n:], true
		a := k - 1
		for a >= 0 && gone(a) {
			a--
		}
		if a < 0 || run[a].keep || !hasWordRune(cur[a]) || moved.Len() == 0 {
			continue
		}
		above, m := strings.TrimRight(cur[a], " \t"), moved.String()
		if strings.IndexAny(m, ")]") == 0 {
			above = strings.TrimRight(above, " \t,;:—–")
		}
		if strings.ContainsAny(above[len(above)-1:], ".!?") {
			m = strings.TrimLeft(m, ".!?")
		}
		cur[a], touched[a] = above+m, true
	}
}

// leadBlanks is the spaces and tabs s opens on.
func leadBlanks(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

// parenSpans are the brackets of a run of comment lines, as [line, byte]:
// closers with no opener, openers with no closer, the pairs that span
// lines around nothing with a word in it, and every pair, opener →
// closer.
type parenSpans struct {
	closers, openers, empty [][2]int
	pairs                   map[[2]int][2]int
}

func parenScan(lines []string, dead []bool) parenSpans {
	type open struct {
		at   [2]int
		word bool
	}
	out := parenSpans{pairs: map[[2]int][2]int{}}
	var stack []open
	for k, l := range lines {
		if dead[k] {
			continue
		}
		for x, r := range l {
			switch {
			case r == '(':
				stack = append(stack, open{at: [2]int{k, x}})
			case r == ')':
				if len(stack) == 0 {
					out.closers = append(out.closers, [2]int{k, x})
					continue
				}
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				out.pairs[top.at] = [2]int{k, x}
				if !top.word && top.at[0] != k {
					out.empty = append(out.empty, top.at, [2]int{k, x})
				}
				if len(stack) > 0 {
					stack[len(stack)-1].word = stack[len(stack)-1].word || top.word
				}
			case unicode.IsLetter(r) || unicode.IsDigit(r):
				if len(stack) > 0 {
					stack[len(stack)-1].word = true
				}
			}
		}
	}
	for _, o := range stack {
		out.openers = append(out.openers, o.at)
	}
	return out
}

// glueTailRe is what an opener may be followed by to its line's end
// when the id it led into went to the next line: "(see".
var glueTailRe = regexp.MustCompile(`^\(\s*(?i:` + glueWords + `)?\s*$`)

// dropParen removes the bracket at x — an opener with only glue after it
// ("(see") to the end of its line — and a space it leaves before a mark,
// another space or the end.
func dropParen(s string, x int) string {
	if glueTailRe.MatchString(s[x:]) {
		return strings.TrimRight(s[:x], " \t")
	}
	out := s[:x] + s[x+1:]
	if x > 0 && out[x-1] == ' ' && (x == len(out) || strings.IndexByte(" .,;:!?)", out[x]) >= 0) {
		out = out[:x-1] + out[x:]
	}
	return out
}

var (
	// dataLeadRe is a comment line that opens with a lowercase key —
	// "# owner: …", a commented-out setting; dataKeyRe one key: value
	// field of a record.
	dataLeadRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]*:\s+\S`)
	dataKeyRe  = regexp.MustCompile(`(?:^|[|,;]\s*)[A-Za-z][A-Za-z0-9_.-]*:\s+\S`)
)

// isDataComment reports a comment that carries data rather than prose:
// a line opening with a lowercase key ("# pending: <signal> | owner: …",
// which atomix's alert-pack test parses), or a record of two or more
// key: value fields. Stripping an id from one leaves "owner: |" for its
// reader, so the ids in it are listed, never stripped (L7).
func isDataComment(text string) bool {
	for l := range strings.SplitSeq(text, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimSpace(strings.TrimLeft(l, "/#*-;!"))
		if dataLeadRe.MatchString(l) || len(dataKeyRe.FindAllString(l, 3)) >= 2 && strings.ContainsAny(l, "|;") {
			return true
		}
	}
	return false
}

// gofmtLifted formats an edited Go file as gofmt would (L5): stripping an
// id can leave a short line that the doc-comment rules read as a heading,
// or an indent they read as a code block, and lint-gated projects went red
// on exactly that. A file that was gofmt-clean at the tag comes back
// formatted; one that was not is left as edited ("" and no reason), so the
// lift does not reformat code it did not touch. why is set when the edit
// does not format, or formatting changed the token stream: the file is
// then left unedited, never written broken.
func gofmtLifted(oldSrc, newSrc string) (out, why string) {
	if was, err := format.Source([]byte(oldSrc)); err != nil || string(was) != oldSrc {
		return "", ""
	}
	got, err := format.Source([]byte(newSrc))
	if err != nil {
		return "", "the edited file does not format (" + err.Error() + ")"
	}
	a, errA := goTokens(oldSrc)
	b, errB := goTokens(string(got))
	if errA != nil || errB != nil || !slices.Equal(a, b) {
		return "", "formatting the edited file changed its token stream"
	}
	return string(got), ""
}

// identWordRe is an identifier-shaped word.
var identWordRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// cleanComment rewrites one comment: ADR citations renumbered, ids
// stripped. emptied: nothing but ids was in it, and it goes.
func cleanComment(text string, block bool, vocab *liftVocab, adr *adrRewriter) (out string, emptied bool, nIDs, nADR int, unresolved []string) {
	cr := ""
	if !block && strings.HasSuffix(text, "\r") {
		text, cr = strings.TrimSuffix(text, "\r"), "\r"
	}
	text, nADR, unresolved = adr.rewrite(text)
	if !block {
		marker := lineMarkRe.FindString(text)
		content := text[len(marker):]
		spans := vocab.spans(content)
		if len(spans) == 0 {
			return text + cr, false, 0, nADR, unresolved
		}
		nc, empty := stripIDsFromText(content, spans)
		if empty {
			return "", true, 1, nADR, unresolved
		}
		return marker + nc + cr, false, 1, nADR, unresolved
	}
	// A block comment: each line on its own. A line that held only ids
	// goes, unless it carries a delimiter.
	open := "/*"
	for strings.HasPrefix(text[len(open):], "*") || strings.HasPrefix(text[len(open):], "!") {
		open += text[len(open) : len(open)+1]
	}
	inner := strings.TrimSuffix(text[len(open):], "*/")
	lines := strings.Split(inner, "\n")
	words, changed := false, false
	var kept []string
	for i, l := range lines {
		lcr := ""
		if trimmed, ok := strings.CutSuffix(l, "\r"); ok {
			l, lcr = trimmed, "\r"
		}
		deco := blockDecoRe.FindString(l)
		content := l[len(deco):]
		spans := vocab.spans(content)
		if len(spans) > 0 {
			changed = true
			tail := content[len(strings.TrimRight(content, " \t")):]
			nc, empty := stripIDsFromText(content, spans)
			if empty {
				if i == 0 || i == len(lines)-1 {
					kept = append(kept, deco+tail)
				}
				continue
			}
			l = deco + nc + tail
		}
		if hasWordRune(content) {
			words = words || hasWordRune(l)
		}
		kept = append(kept, l+lcr)
	}
	if !changed {
		return text, false, 0, nADR, unresolved
	}
	if !words {
		return "", true, 1, nADR, unresolved
	}
	return open + strings.Join(kept, "\n") + "*/", false, 1, nADR, unresolved
}

// verifyCommentOnly proves the edit touched comments only. Go: the token
// stream without comments (automatic semicolons included) is unchanged.
// Other languages: re-tokenized with the same tokenizer, the text outside
// comments is unchanged, ignoring whitespace-only lines and trailing
// whitespace — which a removed comment-only line or trailing comment
// leaves. It cannot prove the tokenizer agrees with the compiler; the
// tokenizers refuse what they cannot read instead of guessing.
func verifyCommentOnly(lang liftLang, oldSrc, newSrc string) error {
	if lang == langGo {
		a, errA := goTokens(oldSrc)
		b, errB := goTokens(newSrc)
		if errA != nil || errB != nil || !slices.Equal(a, b) {
			return untok("the Go token stream changed (a bug in the lift): left unedited")
		}
		return nil
	}
	oldSegs, err := tokenize(lang, oldSrc)
	if err != nil {
		return err
	}
	newSegs, err := tokenize(lang, newSrc)
	if err != nil {
		return untok("the edited file no longer tokenizes (a bug in the lift): left unedited")
	}
	minimal := lang == langJS || lang == langJSX || lang == langDart || lang == langC
	if canonicalCode(oldSrc, oldSegs, minimal) != canonicalCode(newSrc, newSegs, minimal) {
		return untok("the text outside comments changed (a bug in the lift): left unedited")
	}
	return nil
}

// canonicalCode is src without its comments, blank lines and trailing
// whitespace. With minimal (the languages with inline /* */ comments,
// where whitespace between tokens means nothing), horizontal whitespace
// after the indentation collapses to one space, and to none beside a
// bracket or separator; string literals stay exact.
func canonicalCode(src string, segs []tokSeg, minimal bool) string {
	var b strings.Builder
	code := func(t string) {
		if minimal {
			t = collapseWS(t)
		}
		b.WriteString(t)
	}
	prev := 0
	for _, s := range segs {
		code(src[prev:s.Start])
		if s.Kind == segComment {
			code(" ")
		} else {
			b.WriteString(src[s.Start:s.End])
		}
		prev = s.End
	}
	code(src[prev:])
	var out []string
	for l := range strings.SplitSeq(b.String(), "\n") {
		if l = strings.TrimRight(l, " \t\r"); strings.TrimSpace(l) != "" {
			if minimal {
				l = tightenWS(l)
			}
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

var wsRunRe = regexp.MustCompile(`([^\n \t])[ \t]+`)

// collapseWS turns every run of spaces and tabs that follows a non-space
// on its line into one space; indentation is kept.
func collapseWS(t string) string { return wsRunRe.ReplaceAllString(t, "$1 ") }

var wsBesideSepRe = regexp.MustCompile(`([^ \t]) +([(){}\[\];,.])|([(){}\[\];,.]) +`)

// tightenWS drops the single spaces next to a bracket or separator,
// keeping the indentation.
func tightenWS(l string) string {
	ind := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
	rest := l[len(ind):]
	for {
		next := wsBesideSepRe.ReplaceAllString(rest, "$1$2$3")
		if next == rest {
			return ind + rest
		}
		rest = next
	}
}

package apexv1

import (
	"regexp"
	"slices"
	"strings"
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

	type repl struct {
		start, end int
		text       string
	}
	var repls []repl
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
		newText, emptied, nIDs, nADR, unresolved := cleanComment(text, s.Block, vocab, adr)
		for _, u := range unresolved {
			res.Unresolved = append(res.Unresolved, LiftIDRef{File: rel, Line: lineAt(src, s.Start), Text: u, Where: "comment"})
		}
		if newText == text && !emptied {
			continue
		}
		lineStart := strings.LastIndexByte(src[:s.Start], '\n') + 1
		le := lineEnd(src, s.End)
		alone := strings.TrimSpace(src[lineStart:s.Start]) == "" && strings.TrimSpace(src[s.End:le]) == ""
		if lang == langJSX && strings.TrimSpace(src[lineStart:s.Start]) != "" {
			// Markup text can hold `//`: only a comment that begins its
			// line is surely a comment.
			listIDs(s.Start, text, "jsx-inline")
			continue
		}
		if emptied && alone && lineStart > 0 && strings.HasSuffix(strings.TrimRight(src[:lineStart-1], " \t\r"), "\\") {
			// Deleting a line after a continued one would join the next
			// line onto it.
			listIDs(s.Start, text, "continuation")
			continue
		}
		res.IDsRemoved += nIDs
		res.ADRsRewritten += nADR
		switch {
		case !emptied:
			repls = append(repls, repl{s.Start, s.End, newText})
		case alone:
			repls = append(repls, repl{lineStart, min(le+1, len(src)), ""})
		case !s.Block:
			ws := s.Start
			for ws > lineStart && (src[ws-1] == ' ' || src[ws-1] == '\t') {
				ws--
			}
			repls = append(repls, repl{ws, s.End, ""})
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
			repls = append(repls, repl{ws, s.End, sep})
		}
	}
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

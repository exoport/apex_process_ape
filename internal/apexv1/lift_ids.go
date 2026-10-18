package apexv1

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The ids the lift strips from code comments (D37): epic and story ids,
// deferred-record ids and paths, story-file paths. v1 has none of these
// things, so a comment citing one points at nothing the tree holds; the
// explanation around it stays. Old ids stay findable through
// `git log --grep` at the apex-v0-final tag.

// liftVocab is one project's id vocabulary: the patterns are fixed, the
// paths and the story keys come from its v0 corpus.
type liftVocab struct {
	// storyKeys are the corpus's story keys ("3-2"), from the story files
	// and the tracker. A bare "(3-2)" or "3-2_slug" is stripped only when
	// it names one of them: "(0-1)" in a comment is usually a range.
	storyKeys map[string]bool
	// pathRes match a path under a removed id-bearing folder (the deferred
	// store, the implementation folder).
	pathRes []*regexp.Regexp
}

var (
	// "Story 3.2", "stories 3.1 and 3.2", "story 35-1 AC4", with the story
	// fragments next to it — "Story 3.2, Task 2", "Subtask 3.1 of Story
	// 5.10" — which mean nothing once the story is gone (L9).
	storyIDRe = regexp.MustCompile(`(?i)\b(?:` + storyFrag + `\s+(?:of|in|for|from)\s+)?(?:user\s+)?stor(?:y|ies)\s+#?\d+[.-]\d+[a-z]?(?:\s*(?:,|&|/|\band\b|\bor\b)\s*#?\d+[.-]\d+[a-z]?)*(?:\s*(?:[,/;]|—|–)?\s*` + storyFrag + `)*\b`)
	// "Epic 12", "epics 3, 4".
	epicIDRe = regexp.MustCompile(`(?i)\bepics?\s+#?\d+(?:\s*(?:,|&|/|\band\b|\bor\b)\s*#?\d+)*\b`)
	// The last item of a story's or an epic's list: "Story 26.1, 2026-08"
	// read "2026-08" of a date as a second story (R16).
	storyListTailRe = regexp.MustCompile(`\s*(?:,|&|/|\band\b|\bor\b)\s*#?\d+(?:[.-]\d+[a-z]?)?$`)
	// "DW-20261001-ab12_slug", "DW-42".
	deferredIDRe = regexp.MustCompile(`\bDW-\d[0-9A-Za-z_-]*[0-9A-Za-z](?:\.md)?\b|\bDW-\d\b`)
	// "(3-2)": a story key in parentheses.
	parenKeyRe = regexp.MustCompile(`\((\d+)-(\d+)\)`)
	// "3-2_retry-publish": a story file's stem.
	storyStemRe = regexp.MustCompile(`\b(\d+)-(\d+)_[a-z0-9][a-z0-9-]*(?:\.md)?\b`)
	// ADR citations: "ADR-0017", "ADR-17", "ADR-0001/0002". Only the
	// four-digit form is the project's own (its records' ids, ADR-NNNN);
	// a shorter one cites another decision log (L8).
	adrCiteRe = regexp.MustCompile(`\bADR-(\d{1,4})((?:/\d{4})*)\b`)
	// ids inside identifiers: TestStory35_1, epic12Gate, DW42, ADR0017.
	identIDRe = regexp.MustCompile(`(Story|STORY|story|Epic|EPIC|epic)_?\d+|(DW|Dw|dw)_?\d+|(ADR|Adr|adr)_?\d{2,4}`)
)

// storyFrag is a part of a story: a task, subtask or acceptance
// criterion, "Task 2", "Subtask 3.1", "AC4", "ACs 1 and 2".
const storyFrag = `(?:sub-?tasks?|tasks?|ACs?)\s*#?\d+(?:\.\d+)*(?:\s*(?:,|&|/|\band\b)\s*(?:(?:sub-?tasks?|tasks?|ACs?)\s*)?#?\d+(?:\.\d+)*)*`

func newLiftVocab(storyKeys map[string]bool, removedIDPaths []string) *liftVocab {
	v := &liftVocab{storyKeys: storyKeys}
	for _, rel := range removedIDPaths {
		if rel == "" || rel == "." {
			continue
		}
		v.pathRes = append(v.pathRes, regexp.MustCompile(`(?:^|[\s("'`+"`"+`\[])((?:\./)?`+regexp.QuoteMeta(rel)+`(?:/[^\s)\]"'`+"`"+`,;]*)?)`))
	}
	return v
}

// spans returns the merged byte spans of the strippable ids in s: the ids
// and the paths into removed id-bearing folders.
func (v *liftVocab) spans(s string) [][2]int { return v.find(s, true) }

// idSpans is spans without the paths, which the references-to-removed-
// paths list already reports.
func (v *liftVocab) idSpans(s string) [][2]int { return v.find(s, false) }

func (v *liftVocab) find(s string, paths bool) [][2]int {
	var out [][2]int
	add := func(a, b int) { out = append(out, [2]int{a, b}) }
	for _, re := range []*regexp.Regexp{storyIDRe, epicIDRe, deferredIDRe} {
		for _, m := range re.FindAllStringIndex(s, -1) {
			// "epic 12-hour": a number glued to a word is not an id.
			if m[1] < len(s)-1 && s[m[1]] == '-' && unicode.IsLetter(rune(s[m[1]+1])) {
				continue
			}
			// "(Story 26.1, 2026-08-08)": a list item glued to more digits
			// is a date or a version, and the story's span ends before it.
			for re != deferredIDRe && gluedDigits(s, m[1]) {
				tail := storyListTailRe.FindStringIndex(s[m[0]:m[1]])
				if tail == nil {
					break
				}
				m[1] = m[0] + tail[0]
			}
			add(m[0], m[1])
		}
	}
	for _, m := range parenKeyRe.FindAllStringSubmatchIndex(s, -1) {
		if v.storyKeys[s[m[2]:m[3]]+"-"+s[m[4]:m[5]]] {
			add(m[0], m[1])
		}
	}
	for _, m := range storyStemRe.FindAllStringSubmatchIndex(s, -1) {
		if v.storyKeys[s[m[2]:m[3]]+"-"+s[m[4]:m[5]]] {
			add(m[0], m[1])
		}
	}
	for _, re := range v.pathRes {
		if !paths {
			break
		}
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			a, b := m[2], m[3]
			for b > a && strings.IndexByte(".:", s[b-1]) >= 0 {
				b--
			}
			add(a, b)
		}
	}
	return mergeSpans(out)
}

// possessiveRe is an id's possessive: "Story 15.4's own deliverable",
// or a bracketed one's, "(Story 2.3)'s own" (R21).
var possessiveRe = regexp.MustCompile(`^[)\]]?['’]s\b`)

// possessive reports an id in s that the sentence owns through it ("Story
// 15.4's own deliverable"): no removal keeps such a sentence whole (R2).
// A removed path owns one the same way ("development/deferred/'s Gates
// paragraph" left "'s Gates paragraph", R10).
func (v *liftVocab) possessive(s string) bool {
	for _, sp := range v.spans(s) {
		if possessiveRe.MatchString(s[sp[1]:]) {
			return true
		}
	}
	return false
}

// gluedDigits reports s[i:] opening on "-" or "." and a digit.
func gluedDigits(s string, i int) bool {
	return i < len(s)-1 && (s[i] == '-' || s[i] == '.') && s[i+1] >= '0' && s[i+1] <= '9'
}

// storySpan reports a span of s that opens on a story id, "Story 3.2" or
// "Task 2 of Story 3.2".
func storySpan(s string, sp [2]int) bool {
	loc := storyIDRe.FindStringIndex(s[sp[0]:sp[1]])
	return len(loc) > 0 && loc[0] == 0
}

func mergeSpans(in [][2]int) [][2]int {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i][0] < in[j][0] })
	out := [][2]int{in[0]}
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s[0] <= last[1] {
			last[1] = max(last[1], s[1])
			continue
		}
		out = append(out, s)
	}
	return out
}

// identIDs returns the ids inside an identifier-ish text: a match counts
// only at a word boundary — start, after `_`, or a camel-case hump — so
// "History2" is not "story2".
func identIDs(s string) []string {
	var out []string
	for _, m := range identIDRe.FindAllStringIndex(s, -1) {
		a := m[0]
		if a > 0 {
			prev := rune(s[a-1])
			first := rune(s[a])
			hump := unicode.IsUpper(first) && (unicode.IsLower(prev) || unicode.IsDigit(prev))
			if prev != '_' && !hump && (unicode.IsLetter(prev) || unicode.IsDigit(prev)) {
				continue
			}
		}
		out = append(out, s[m[0]:m[1]])
	}
	return out
}

// ---- ADR citations ----------------------------------------------------

// adrRewriter renumbers ADR citations: old number → new number, and old
// record paths → new decision paths.
type adrRewriter struct {
	num   map[int]int
	paths map[string]string // old project-relative path → new
	// pathRe matches a path to a v0 ADR record ("…/adrs/adr-0004_slug.md").
	pathRe *regexp.Regexp
}

func newADRRewriter(num map[int]int, paths map[string]string, adrsRel string) *adrRewriter {
	r := &adrRewriter{num: num, paths: paths}
	if adrsRel != "" {
		r.pathRe = regexp.MustCompile(`(?:\./)?` + regexp.QuoteMeta(adrsRel) + `/[A-Za-z0-9_.-]+\.md`)
	}
	return r
}

// adrOwnDigits is the width of the v0 records' own ids: ADR-NNNN.
const adrOwnDigits = 4

// rewrite renumbers every citation in s; n counts the ids changed, and
// unresolved lists, as written, the citations it left: a number no ADR
// has, or a short form (ADR-08) — another log's id, never the project's
// own, so it is never rewritten even when the project has an ADR-0008.
func (r *adrRewriter) rewrite(s string) (out string, n int, unresolved []string) {
	if r == nil {
		return s, 0, nil
	}
	if r.pathRe != nil {
		s = r.pathRe.ReplaceAllStringFunc(s, func(m string) string {
			if np, ok := r.paths[strings.TrimPrefix(m, "./")]; ok {
				n++
				return np
			}
			return m
		})
	}
	out = adrCiteRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := strings.Split(strings.TrimPrefix(m, "ADR-"), "/")
		changed := false
		for i, p := range parts {
			if len(p) != adrOwnDigits {
				unresolved = append(unresolved, "ADR-"+p)
				continue
			}
			old, _ := strconv.Atoi(p)
			nw, ok := r.num[old]
			if !ok {
				unresolved = append(unresolved, "ADR-"+p)
				continue
			}
			if s := adrNum(nw); s != p {
				parts[i] = s
				changed = true
			}
		}
		if !changed {
			return m
		}
		n++
		return "ADR-" + strings.Join(parts, "/")
	})
	return out, n, unresolved
}

func adrNum(n int) string { return fmtNum4(n) }

func fmtNum4(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// citedADRs returns the ADR numbers cited in s in the project's own
// form (a short ADR-08 is another log's).
func citedADRs(s string) []int {
	var out []int
	for _, m := range adrCiteRe.FindAllStringSubmatch(s, -1) {
		for p := range strings.SplitSeq(m[1]+m[2], "/") {
			if len(p) != adrOwnDigits {
				continue
			}
			n, _ := strconv.Atoi(p)
			out = append(out, n)
		}
	}
	return out
}

// ---- comment text -------------------------------------------------------

const (
	idMark  = "\x01" // an id, before it is removed
	cutMark = "\x02" // where text was removed
	// storyMark is a story id until the asides are cut: a story's aside
	// never goes whole (R16).
	storyMark = "\x07"
)

// holdPair holds the brackets of a pair that was empty as written — the
// "()" of "run()'s", the "[]" of "[]byte" — while the ids are stripped,
// and releasePair puts them back: only a pair a removal emptied goes
// (R9; rc.4 stripped every empty pair on a line it edited).
var (
	holdPair    = strings.NewReplacer("(", "\x03", ")", "\x04", "[", "\x05", "]", "\x06")
	releasePair = strings.NewReplacer("\x03", "(", "\x04", ")", "\x05", "[", "\x06", "]")
)

// glue words that only lead into an id: "see", "per", "tracked in".
const glueWords = `(?:see also|see|per|cf\.?|refs?:?|from|in|by|via|since|for|under|also|tracked (?:in|as|by)|follow-?ups?(?: in)?|as of|after|until|before|was|formerly)`

var (
	idListRe = regexp.MustCompile(idMark + `(?:\s*(?:,|;|/|&|\+|\band\b|\bor\b)\s*` + idMark + `)+`)
	// An aside that starts with an id ("(DW-42 tracks the gap)") goes
	// whole, up to a separator: "(DW-42, 2026-08-08)" keeps its date.
	// Never a story's, whose span ends at its fragments, nor one citing
	// an ADR (R16).
	idAsideRe    = regexp.MustCompile(`[ \t]*[(\[]\s*(?i:` + glueWords + `\s+)?` + idMark + `[^()\[\]\x03-\x07,;/]*[)\]]`)
	idParenRe    = regexp.MustCompile(`[ \t]*[(\[]\s*(?i:` + glueWords + `\s+)?` + idMark + `\s*[)\]]`)
	idSentenceRe = regexp.MustCompile(`([.!?:;])\s+(?i:` + glueWords + `\s+)?` + idMark + `\s*[.;]`)
	idLeadRe     = regexp.MustCompile(`^(?i:` + glueWords + `\s+)?` + idMark + `\s*(?:[:,;./]|—|–|--?)?\s*`)
	idTrailRe    = regexp.MustCompile(`\s*(?:[,;:]|—|–|--?)?\s*(?i:(?:` + glueWords + `|with|to|at|of|on|against|into)\s+)?` + idMark + `\s*([.!?]?)\s*$`)
	// A sentence that only points at an id ("See DW-42 for the history.")
	// goes whole.
	idPointerRe = regexp.MustCompile(`(^|[.!?]\s+)(?i:see(?: also)?|cf\.?|refs?:?)\s+` + idMark + `[^.!?` + idMark + `]*(?:[.!?]|$)\s*`)
	// idSepRe is an id with the separator after it: what a pointer that
	// also cites an ADR loses ("See Story 4.2, ADR-0019 for why", R16).
	idSepRe = regexp.MustCompile(idMark + `\s*(?:[,;/&+]|\b(?i:and|or)\b)?\s*`)
	// idThenADRRe is an id listed before an ADR citation, which takes its
	// place: "per Story 4.2, ADR-0019" is "per ADR-0019" (R16).
	idThenADRRe = regexp.MustCompile(idMark + `\s*(?:[,;/&+]|\b(?i:and|or)\b)\s*(ADR-\d)`)
	idMidGlueRe = regexp.MustCompile(`\s+(?i:` + glueWords + `)\s+` + idMark)
	cutRe       = regexp.MustCompile(`[ \t]*[` + idMark + cutMark + `]+[ \t]*`)
	// The separator a removal leaves next to a bracket or a punctuation
	// mark goes with it (L9): "(ADR-0019,)", "(, ADR-0017", "ADR-0011 /,".
	openSepRe   = regexp.MustCompile(`([(\[])` + cutMark + `(?:[,;:/&+]|—|–|--?|(?i:and|or)\b)?[ \t]*`)
	sepCloseRe  = regexp.MustCompile(`[ \t]*(?:[,;/&+]|\b(?i:and|or))[ \t]*` + cutMark + `([)\]])`)
	sepPunctRe  = regexp.MustCompile(`[ \t]*[,;/&+][ \t]*` + cutMark + `([,;:.!?])`)
	emptyPairRe = regexp.MustCompile(`\(\s*\)|\[\s*\]`)
	// bulletRe is a list item's marker: "- ", "* ", "2. ".
	bulletRe = regexp.MustCompile(`^(?:[-*+•]|\d+[.)])[ \t]+`)
	// emptiedPairRe is a pair a removal emptied, with the blanks before it:
	// "Bounded ([Story 3.2]) twice" is "Bounded twice".
	emptiedPairRe = regexp.MustCompile(`[ \t]*(?:\(\s*\)|\[\s*\])`)
	dupPunctRe    = regexp.MustCompile(`([,;:])\s*([.,;:])`)
	spaceBefRe    = regexp.MustCompile(`[ \t]+([.,;:!?)\]])`)
)

// stripIDsFromText removes the id spans from one line of comment text,
// keeping its explanation. empty reports a line that held nothing but ids
// (and glue); the caller drops it.
func stripIDsFromText(s string, spans [][2]int) (out string, empty bool) {
	if len(spans) == 0 {
		return s, false
	}
	lead := s[:len(s)-len(strings.TrimLeft(s, " \t"))]
	held := []byte(s)
	for _, m := range emptyPairRe.FindAllStringIndex(s, -1) {
		copy(held[m[0]:], holdPair.Replace(s[m[0]:m[0]+1]))
		copy(held[m[1]-1:], holdPair.Replace(s[m[1]-1:m[1]]))
	}
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.Write(held[prev:sp[0]])
		if storySpan(s, sp) {
			b.WriteString(storyMark)
		} else {
			b.WriteString(idMark)
		}
		prev = sp[1]
	}
	b.Write(held[prev:])
	t := strings.TrimLeft(b.String(), " \t")
	origTrim := strings.TrimSpace(s)
	// A list item keeps its marker: trimmed with the separators, "- Does
	// NOT modify …" was no longer a list item, and gofmt made the list a
	// code block (R19). The item goes only when its text does.
	bullet := bulletRe.FindString(t)
	t = t[len(bullet):]
	mark := trailingMark(s, spans)
	pre := strings.ReplaceAll(t, storyMark, idMark)
	startsWithID := strings.HasPrefix(pre, idMark) || idLeadRe.MatchString(pre)

	t = idListRe.ReplaceAllString(t, idMark)
	t = idAsideRe.ReplaceAllStringFunc(t, func(m string) string {
		if adrCiteRe.MatchString(m) {
			return m
		}
		return cutMark
	})
	t = strings.ReplaceAll(t, storyMark, idMark)
	t = idListRe.ReplaceAllString(t, idMark)
	t = idThenADRRe.ReplaceAllString(t, "$1")
	t = idPointerRe.ReplaceAllStringFunc(t, func(m string) string {
		if adrCiteRe.MatchString(m) {
			return idSepRe.ReplaceAllString(m, "")
		}
		return idPointerRe.ReplaceAllString(m, "$1")
	})
	t = idParenRe.ReplaceAllString(t, cutMark)
	t = idSentenceRe.ReplaceAllString(t, "$1"+cutMark)
	t = idLeadRe.ReplaceAllString(t, "")
	t = idTrailRe.ReplaceAllString(t, "$1")
	t = idMidGlueRe.ReplaceAllString(t, cutMark)
	t = cutRe.ReplaceAllStringFunc(t, func(string) string { return cutMark })
	t = openSepRe.ReplaceAllString(t, "$1")
	t = sepCloseRe.ReplaceAllString(t, "$1")
	t = sepPunctRe.ReplaceAllString(t, "$1")
	t = joinCuts(t)
	t = emptiedPairRe.ReplaceAllString(t, "")
	t = dupPunctRe.ReplaceAllString(t, "$2")
	t = spaceBefRe.ReplaceAllString(t, "$1")
	t = strings.TrimSpace(t)
	t = strings.TrimLeft(t, ",;:—–- ")
	t = strings.TrimRight(t, ",;:—– ")
	t = releasePair.Replace(strings.TrimSpace(t))
	if !hasWordRune(t) {
		return "", true
	}
	if startsWithID {
		r, size := utf8.DecodeRuneInString(t)
		if unicode.IsLower(r) {
			t = string(unicode.ToUpper(r)) + t[size:]
		}
	}
	if strings.HasSuffix(origTrim, ".") && !strings.ContainsAny(t[len(t)-1:], ".!?:") {
		t += "."
	}
	if mark != "" && !strings.ContainsAny(t[len(t)-1:], ".!?,;:") {
		t += mark
	}
	return lead + bullet + t, false
}

// trailingMark is the "," ";" or ":" that s ends on when no id is right
// before it: the sentence's own, which the edit keeps — "proves," runs on
// into the next line, "bump:" introduces it. One right after an id goes
// with it (R20; rc.5 trimmed it from every edited line).
func trailingMark(s string, spans [][2]int) string {
	body := strings.TrimRight(s, " \t")
	if body == "" || strings.IndexByte(",;:", body[len(body)-1]) < 0 {
		return ""
	}
	before := len(strings.TrimRight(body[:len(body)-1], " \t"))
	for _, sp := range spans {
		if sp[1] == before {
			return ""
		}
	}
	return body[len(body)-1:]
}

// joinCuts closes each gap left by a removal: nothing next to punctuation
// or a bracket, one space between words.
func joinCuts(t string) string {
	for {
		i := strings.Index(t, cutMark)
		if i < 0 {
			return t
		}
		before, after := t[:i], t[i+len(cutMark):]
		sep := " "
		switch {
		case before == "" || after == "":
			sep = ""
		case strings.ContainsAny(after[:1], ".,;:!?)]"+cutMark):
			sep = ""
		case strings.ContainsAny(before[len(before)-1:], "(["):
			sep = ""
		}
		t = before + sep + after
	}
}

func hasWordRune(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

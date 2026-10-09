// Package mdscan holds the markdown primitives more than one package
// needs to read an APEX document the same way.
//
// Everything here was extracted verbatim from `internal/story/shape.go`
// and `internal/apexdoc/shard.go`, which remain the only reason each
// piece has the shape it does — the rationale comments came with them
// because they are the expensive part. The extraction exists so a third
// reader (the spec graph) reuses the SAME fence handling, the same
// heading normalisation and the same File List grammar rather than
// reimplementing them slightly differently. Two parsers that disagree
// about what a heading is produce two answers about the same document
// and no way to tell which is wrong.
//
// The originals keep one-line aliases (`var stripFences =
// mdscan.StripFences`) so every existing call site — and every existing
// test, including the ones that call the unexported name directly — is
// textually unchanged. That is what makes this refactor provably
// behaviour-preserving rather than merely believed to be.
//
// Deliberately NOT moved here:
//
//   - `internal/deferred`'s own `headingRe` (`^##\s+\S`). It answers a
//     different question — "is this any level-2 heading" — and that
//     package already keeps a considered private copy of `atomicfile`
//     for the same reason. Unifying two regexes because they share a
//     name is how a shared helper acquires a caller it was never right
//     for.
//   - `apexdoc.Slugify`. It is already exported, has a golden test, and
//     moving it would break every existing caller for no gain.
//
// Stdlib only, on purpose: this is the layer every document reader sits
// on, so it must not be able to pull a dependency into one of them.
package mdscan

import (
	"regexp"
	"strconv"
	"strings"
)

// HeadingRe matches an ATX heading and captures its level and text.
var HeadingRe = regexp.MustCompile(`(?m)^(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$`)

// WSRun collapses internal whitespace so `## Tasks  /  Subtasks` and
// `## Tasks / Subtasks` are the same heading. Spacing is formatting, and
// Prettier rewrites it; the heading's identity is its words.
var WSRun = regexp.MustCompile(`[ \t]+`)

// FileListEntryRe matches a File List bullet and captures the path plus
// what follows it.
//
// The backticks are OPTIONAL on purpose. The canonical entry wraps the
// path in them, but an entry that forgot to is still an entry, and
// skipping it here would let the commonest malformed line — a bare path
// with no marker at all — pass unexamined. Missing backticks are not the
// marker-vocabulary class's finding; being unable to see the entry would
// be.
var FileListEntryRe = regexp.MustCompile(
	"^[-*][ \t]+(?:`([^`]+)`|([^\\s`]+))[ \t]*(.*)$",
)

// MarkerRe matches the canonical marker position: parens immediately
// after the backticked path.
var MarkerRe = regexp.MustCompile(`^\(([a-z]+)\)`)

// GCCLineRe is the declared Governance Compliance Criteria form:
//
//   - [ ] **[ID]** {instruction} -- {scope}
//
// The checkbox may be ticked, and review may append an N/A reason, so
// the state is captured loosely and the ID and separator strictly.
//
// NOTE for a new reader: this matches the checkbox and does not CAPTURE
// it. Where the checkbox STATE is the thing being read — "an unticked
// task", as opposed to "a line of the declared form" — the pattern to
// copy is `internal/deferred`'s `deferBulletRe`, which captures
// `([ xX])`. The two are different questions and the repo has one regex
// for each; reading this one for state is a mistake that has been made.
var GCCLineRe = regexp.MustCompile(`^[-*][ \t]+\[[ xX]\][ \t]+\*\*\[([A-Z]+-\d+)\]\*\*[ \t]+(.*)$`)

// Markdown link forms, moved together rather than one at a time.
//
// The spec graph needs SectionEntryRe for its `shard_of` edges, and it
// would have been easy to export only that one — or only MDLinkRe. Both
// would have left a reader blind to link forms `apexdoc` already
// handles: a reference-style link, or an HTML `src=`. A document reader
// that sees fewer links than the sharding tool whose regexes it borrowed
// is worse than one that wrote its own, because its gaps look like the
// document's.
var (
	// MDLinkRe matches an inline link or image, capturing the label
	// (with its brackets) and the target.
	MDLinkRe = regexp.MustCompile(`(!?\[(?:[^\[\]]|\[[^\]]*\])*\])\(([^)]+)\)`)
	// MDRefRe matches a reference-style link definition line.
	MDRefRe = regexp.MustCompile(`(?m)^(\s*\[[^\]]+\]:\s+)(\S+)(.*)`)
	// HTMLSrcRe matches an HTML src attribute.
	HTMLSrcRe = regexp.MustCompile(`(?i)(src=)(["']?)([^"'>\s]+)(["']?)`)
	// URISchemeRe matches an absolute URI scheme prefix, which is what
	// separates a link to rewrite from a link to leave alone.
	URISchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+\-.]*:`)
	// SectionEntryRe parses `- [Title](file.md)` out of a shard index.
	SectionEntryRe = regexp.MustCompile(`(?m)^\s*-\s+\[.*?\]\(([^)]+)\)`)
)

// StripFences removes fenced code blocks from a body.
//
// # Why every structural class needs this
//
// A fenced block is an EXAMPLE of a shape, not an instance of it. The
// story template's `### File List` carries its canonical entry shape
// inside a ```markdown fence:
//
//   - `path/to/file.ext` (marker) — short note
//
// That line is written verbatim into every minted story, and
// `apex-dev-story` often leaves it in place. Read as an entry it reports
// `(marker)` as an unknown marker — which is how this turned into 51 of
// 60 failing fixture stories, 140 of one real project's 483 and 2 of
// another's 296. The validator would have been reporting the template
// against itself.
//
// The same trap exists for every other body class: a fenced `## Story`
// in Dev Notes must not satisfy the derived section set, a fenced GCC
// line must not be checked for its separator, a fenced compliance table
// must not be read as the story's own, and a fenced placeholder is not
// residue.
//
// Tag matching deliberately reads the UNSTRIPPED text: the digest
// algorithm's step 1 is instructed to be inclusive ("extra entries cost
// little, missed entries are expensive"), and a tag mentioned inside an
// example is still the story talking about that subject. So this is a
// function a caller chooses, never something applied on the way in.
//
// The fence rules: an opening fence is three or more backticks or tildes,
// indented at most three spaces, optionally followed by an info string;
// the block ends at a fence of the SAME character that is at least as long
// and carries no info string, or at end of document. Matching the
// character and length is what lets a ```` ``` ```` example sit inside a
// ```` ```` ```` block.
//
// A fence inside a block quote is a fence, as CommonMark has it: `> ```bash`
// opens one. It is found by stripping the quote markers first, and the
// block remembers the depth it opened at — its closing fence must sit at
// that same depth, and it also ends when its block quote does, because a
// fence cannot outlive its container. That second rule is the one that
// matters: without it, an unclosed quoted fence would run to the end of
// the document and strip every real section after it.
//
// NOT handled: a fence nested in a list item and indented four or more
// spaces. CommonMark measures that indent from the item's content column;
// this measures it from the margin, so such a block stays in the prose.
func StripFences(text string) string {
	lines := strings.Split(text, "\n")
	fenced := fencedLines(lines)
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if !fenced[i] {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// fencedLines reports, line by line, whether each line belongs to a fenced
// code block, its opening and closing fence lines included. The rules are
// the ones StripFences documents.
func fencedLines(lines []string) []bool {
	fenced := make([]bool, len(lines))
	var (
		inFence bool
		depth   int // the block-quote depth the open fence sits at
		char    byte
		width   int
	)
	for i, line := range lines {
		if inFence {
			inner, stillQuoted := stripQuoteMarkers(line, depth)
			if stillQuoted {
				// Inside the block, or its closing line: fenced either way.
				fenced[i] = true
				marker, markerChar, markerWidth, info := fenceMarker(inner)
				if marker && markerChar == char && markerWidth >= width && info == "" {
					inFence = false
				}
				continue
			}
			// The block quote holding the fence ended, so the fence did too,
			// and this line is read afresh.
			inFence = false
		}
		lineDepth, inner := quoteDepth(line)
		if marker, markerChar, markerWidth, _ := fenceMarker(inner); marker {
			// The opening fence line is part of the block.
			fenced[i] = true
			inFence, depth, char, width = true, lineDepth, markerChar, markerWidth
		}
	}
	return fenced
}

// Placeholder delimiters for MapOutsideCode: private-use code points, which
// no link pattern matches and real documents do not carry.
const (
	codeOpen  = "\uE000"
	codeClose = "\uE001"
)

// MapOutsideCode applies f to text with every fenced code block and inline
// code span hidden, then puts the code back byte for byte.
//
// Code is an example, not a reference: `<img src=x onerror=alert(1)>` in a
// test case, or a ```markdown block showing a link's shape. A rewrite that
// reads it as a real link edits the example — `ape doc shard` turned that
// XSS payload into `src=../x` in an epics file, so the shard and its source
// disagreed about the test.
//
// The code is MASKED rather than split out, because links contain code:
// [`main.go`](main.go) is one link, and splitting at the code span would
// hand f a `[` and a `](main.go)` that are no longer a link. Each block or
// span becomes one placeholder, so f still sees the link around it.
//
// Fences follow StripFences' rules. An inline code span is a run of N
// backticks closed by the next run of exactly N, within the same paragraph
// (a blank line ends the search); an unclosed run is literal backticks.
// NOT handled, as in StripFences: indented (four-space) code blocks, which
// list continuations make ambiguous. Text that already contains the
// placeholder characters is passed to f unmasked.
func MapOutsideCode(text string, f func(string) string) string {
	if strings.Contains(text, codeOpen) || strings.Contains(text, codeClose) {
		return f(text)
	}
	var codes []string
	mask := func(code string) string {
		codes = append(codes, code)
		return codeOpen + strconv.Itoa(len(codes)-1) + codeClose
	}

	lines := strings.Split(text, "\n")
	fenced := fencedLines(lines)
	var b strings.Builder
	for i := 0; i < len(lines); {
		if i > 0 {
			b.WriteByte('\n')
		}
		j := i
		for j < len(lines) && fenced[j] == fenced[i] {
			j++
		}
		run := strings.Join(lines[i:j], "\n")
		if fenced[i] {
			b.WriteString(mask(run))
		} else {
			b.WriteString(maskCodeSpans(run, mask))
		}
		i = j
	}

	out := f(b.String())
	for k, code := range codes {
		out = strings.Replace(out, codeOpen+strconv.Itoa(k)+codeClose, code, 1)
	}
	return out
}

// maskCodeSpans replaces each inline code span in prose with mask(span).
func maskCodeSpans(prose string, mask func(string) string) string {
	var b strings.Builder
	i := 0
	for i < len(prose) {
		if prose[i] != '`' {
			b.WriteByte(prose[i])
			i++
			continue
		}
		n := backtickRun(prose, i)
		end := closingRun(prose, i+n, n)
		if end < 0 {
			// No closer: the backticks are literal.
			b.WriteString(prose[i : i+n])
			i += n
			continue
		}
		b.WriteString(mask(prose[i : end+n]))
		i = end + n
	}
	return b.String()
}

// backtickRun is the length of the backtick run starting at i.
func backtickRun(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// closingRun finds the next run of exactly n backticks at or after from,
// before a blank line ends the paragraph. -1 when there is none.
func closingRun(s string, from, n int) int {
	limit := len(s)
	if k := strings.Index(s[from:], "\n\n"); k >= 0 {
		limit = from + k
	}
	for i := from; i < limit; {
		if s[i] != '`' {
			i++
			continue
		}
		m := backtickRun(s, i)
		if m == n && i+m <= limit {
			return i
		}
		i += m
	}
	return -1
}

// maxBlockIndent is how far a block-quote marker or a fence may be
// indented: four spaces is an indented code block.
const maxBlockIndent = 3

// quoteMarker strips one block-quote marker — up to three spaces, `>`, and
// one optional space — reporting whether there was one.
func quoteMarker(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > maxBlockIndent || !strings.HasPrefix(trimmed, ">") {
		return line, false
	}
	return strings.TrimPrefix(trimmed[1:], " "), true
}

// quoteDepth strips every leading block-quote marker and reports how many
// there were.
func quoteDepth(line string) (depth int, inner string) {
	inner = line
	for {
		rest, ok := quoteMarker(inner)
		if !ok {
			return depth, inner
		}
		depth, inner = depth+1, rest
	}
}

// stripQuoteMarkers strips exactly depth markers, reporting false when the
// line has fewer — the block quote at that depth has ended.
func stripQuoteMarkers(line string, depth int) (string, bool) {
	for range depth {
		inner, ok := quoteMarker(line)
		if !ok {
			return line, false
		}
		line = inner
	}
	return line, true
}

// fenceMarker reports whether line opens or closes a fence, with the
// fence character, its run length, and any info string.
func fenceMarker(line string) (isFence bool, char byte, width int, info string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > maxBlockIndent {
		// More than three spaces of indent is an indented code block, not
		// a fence.
		return false, 0, 0, ""
	}
	if len(trimmed) < 3 {
		return false, 0, 0, ""
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return false, 0, 0, ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 {
		return false, 0, 0, ""
	}
	rest := strings.TrimSpace(trimmed[n:])
	if c == '`' && strings.Contains(rest, "`") {
		// A backtick fence's info string may not contain a backtick;
		// that shape is inline code, not a fence.
		return false, 0, 0, ""
	}
	return true, c, n, rest
}

// Section returns the text under heading, up to the next heading of the
// same or shallower level. Empty when the heading is absent.
//
// `heading` is given in its normalised `### Text` form — the same form
// Headings returns — and the comparison normalises whitespace on both
// sides, so a document whose spacing a formatter rewrote still matches.
func Section(text, heading string) string {
	level := strings.Count(strings.SplitN(heading, " ", 2)[0], "#")
	lines := strings.Split(text, "\n")
	var (
		out      []string
		inside   bool
		wantNorm = heading
	)
	for _, line := range lines {
		m := HeadingRe.FindStringSubmatch(line)
		if m != nil {
			got := m[1] + " " + WSRun.ReplaceAllString(strings.TrimSpace(m[2]), " ")
			if inside && len(m[1]) <= level {
				break
			}
			if got == wantNorm {
				inside = true
				continue
			}
		}
		if inside {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// Headings returns the document's headings, normalised to `### Text`
// form.
func Headings(text string) []string {
	matches := HeadingRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1]+" "+WSRun.ReplaceAllString(strings.TrimSpace(m[2]), " "))
	}
	return out
}

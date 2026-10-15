package apexv1

import (
	"regexp"
	"strconv"
	"strings"
)

// Finding is one validation failure: the file it is about (project-relative),
// the line when one applies (1-based, 0 when not), and what is wrong.
type Finding struct {
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

func (f Finding) String() string {
	if f.Line > 0 {
		return f.File + ":" + strconv.Itoa(f.Line) + ": " + f.Message
	}
	return f.File + ": " + f.Message
}

// mdLine is one line of a Markdown document with the facts the parsers
// need: its 1-based number and whether it sits inside a fenced code block,
// where nothing is a heading.
type mdLine struct {
	N      int
	Text   string
	InCode bool
}

// splitLines splits a document into mdLines, tracking ``` and ~~~ fences.
func splitLines(doc string) []mdLine {
	raw := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	out := make([]mdLine, 0, len(raw))
	fence := ""
	for i, text := range raw {
		trimmed := strings.TrimSpace(text)
		isFence := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		switch {
		case fence == "" && isFence:
			fence = trimmed[:3]
			out = append(out, mdLine{N: i + 1, Text: text, InCode: true})
			continue
		case fence != "" && isFence && strings.HasPrefix(trimmed, fence):
			fence = ""
			out = append(out, mdLine{N: i + 1, Text: text, InCode: true})
			continue
		}
		out = append(out, mdLine{N: i + 1, Text: text, InCode: fence != ""})
	}
	return out
}

// headingLevel returns the ATX heading level of a line outside code, and
// its text; 0 when the line is not a heading.
func headingLevel(l mdLine) (level int, text string) {
	if l.InCode {
		return 0, ""
	}
	t := strings.TrimRight(l.Text, " \t")
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(t) || t[n] != ' ' {
		return 0, ""
	}
	return n, strings.TrimSpace(t[n+1:])
}

// section is the body of a heading: the lines after it up to the next
// heading of the same or a higher level.
type section struct {
	Level int
	Title string
	Line  int
	Body  []mdLine
}

// h2Sections returns every level-2 heading with its body — the only level
// the v1 documents are sectioned by.
func h2Sections(lines []mdLine) []section {
	const level = 2
	var out []section
	cur := -1
	for _, l := range lines {
		lv, title := headingLevel(l)
		if lv > 0 && lv <= level {
			if lv == level {
				out = append(out, section{Level: lv, Title: title, Line: l.N})
				cur = len(out) - 1
			} else {
				cur = -1
			}
			continue
		}
		if cur >= 0 {
			out[cur].Body = append(out[cur].Body, l)
		}
	}
	return out
}

// hasProse reports whether a section body has any text beyond blank lines
// and HTML comments — the skeleton ape writes is comments only, so this is
// what tells "filled in" from "still the template".
func hasProse(body []mdLine) bool {
	inComment := false
	for _, l := range body {
		t := strings.TrimSpace(l.Text)
		for t != "" {
			if inComment {
				end := strings.Index(t, "-->")
				if end < 0 {
					break
				}
				inComment = false
				t = strings.TrimSpace(t[end+3:])
				continue
			}
			start := strings.Index(t, "<!--")
			if start < 0 {
				return true
			}
			if strings.TrimSpace(t[:start]) != "" {
				return true
			}
			inComment = true
			t = t[start+4:]
		}
	}
	return false
}

func joinBody(body []mdLine) string {
	var b strings.Builder
	for _, l := range body {
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// checkLineRe matches a scenario or task check line's value.
var checkLineRe = regexp.MustCompile(`^\s*(?:[-*]\s+)?check:\s*(.*)$`)

// HumanReview is the check value that says no command can prove it.
const HumanReview = "human-review"

// slugRe is a capability or ADR slug: kebab-case.
var slugRe = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// ChangeIDRe is the change id rule from the brief.
var ChangeIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,39}$`)

// titleFromSlug turns "add-tenant-delete" into "Add tenant delete".
func titleFromSlug(slug string) string {
	s := strings.ReplaceAll(slug, "-", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// sizeKB renders a byte count the way the caps are stated.
func sizeKB(n int) string {
	return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " KB"
}

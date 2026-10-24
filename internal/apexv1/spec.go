package apexv1

import (
	"fmt"
	"strconv"
	"strings"
)

// RequirementPrefix and ScenarioPrefix are the heading texts a requirement
// and a scenario are recognised by (OpenSpec's shape).
const (
	RequirementPrefix = "Requirement:"
	ScenarioPrefix    = "Scenario:"
)

// Scenario is one `#### Scenario:` block.
type Scenario struct {
	Name   string
	Line   int
	Checks []string
	// Kinds are each check line's kind (W2): "" for `check:`, else the
	// word in `check (<kind>):`; KindLines where each sits.
	Kinds     []string
	KindLines []int
	// tagged marks the lines written `check (…):`, kind "" included.
	tagged []bool
}

// Full reports a scenario whose check is `check (full):`, or `check
// (live):`, its v1.2 name (D512).
func (s *Scenario) Full() bool { return len(s.Kinds) > 0 && isFullKind(s.Kinds[0]) }

// Requirement is one `### Requirement:` block. Text is the block verbatim,
// heading included, with trailing blank lines trimmed: it is what a delta
// merge moves around.
type Requirement struct {
	Name      string
	Line      int
	Text      string
	Scenarios []Scenario
	// Stray reports `check:` lines that sit in the requirement but outside
	// any scenario.
	Stray []int
}

// Spec is a parsed capability spec.
type Spec struct {
	Title        string
	Purpose      string
	Preamble     string
	Requirements []Requirement
	// Other reports level-3 headings that are not requirements, by line.
	Other []int
}

// ParseSpec parses a capability spec. It never fails: shape problems are
// what ValidateSpec reports.
func ParseSpec(doc string) Spec {
	var s Spec
	lines := splitLines(doc)
	var pre []mdLine
	cur := -1
	var curLines []mdLine
	flush := func() {
		if cur < 0 {
			return
		}
		s.Requirements[cur] = buildRequirement(s.Requirements[cur], curLines)
		curLines = nil
	}
	for _, l := range lines {
		lv, title := headingLevel(l)
		if lv > 0 && lv <= 3 {
			if lv == 3 && strings.HasPrefix(title, RequirementPrefix) {
				flush()
				s.Requirements = append(s.Requirements, Requirement{
					Name: strings.TrimSpace(strings.TrimPrefix(title, RequirementPrefix)),
					Line: l.N,
				})
				cur = len(s.Requirements) - 1
				curLines = []mdLine{l}
				continue
			}
			if lv == 3 {
				s.Other = append(s.Other, l.N)
			}
			if cur >= 0 && lv < 3 {
				// A higher-level heading ends the requirement; anything
				// after it is free-form and stays attached to the last
				// requirement so a merge never drops it.
				curLines = append(curLines, l)
				continue
			}
		}
		if cur >= 0 {
			curLines = append(curLines, l)
		} else {
			pre = append(pre, l)
		}
	}
	flush()
	for i, l := range pre {
		if lv, title := headingLevel(l); lv == 1 && s.Title == "" {
			s.Title = title
			continue
		}
		if t := strings.TrimSpace(l.Text); !l.InCode && strings.HasPrefix(t, "Purpose:") && s.Purpose == "" {
			s.Purpose, _ = purposeBlock(pre, i)
		}
	}
	s.Preamble = strings.TrimRight(joinBody(pre), "\n")
	return s
}

// purposeBlock reads the Purpose block that starts at lines[i], a
// `Purpose:` line (Q44): that line and every following line up to the
// first blank line, heading or fenced code. It returns the text after
// `Purpose:`, one line per line of the block joined with "\n", and how
// many lines the block spans. A change's ADDED block and a spec's preamble
// are read by the same rule, so a two-line Purpose is never split across
// a requirement heading.
func purposeBlock(lines []mdLine, i int) (text string, n int) {
	parts := []string{strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i].Text), "Purpose:"))}
	for n = 1; i+n < len(lines); n++ {
		l := lines[i+n]
		if lv, _ := headingLevel(l); l.InCode || lv > 0 || strings.TrimSpace(l.Text) == "" {
			break
		}
		parts = append(parts, strings.TrimSpace(l.Text))
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), n
}

func buildRequirement(r Requirement, lines []mdLine) Requirement {
	r.Text = strings.TrimRight(joinBody(lines), "\n")
	sc := -1
	for _, l := range lines[1:] {
		if lv, title := headingLevel(l); lv == 4 && strings.HasPrefix(title, ScenarioPrefix) {
			r.Scenarios = append(r.Scenarios, Scenario{
				Name: strings.TrimSpace(strings.TrimPrefix(title, ScenarioPrefix)),
				Line: l.N,
			})
			sc = len(r.Scenarios) - 1
			continue
		} else if lv > 0 && lv < 4 {
			sc = -1
			continue
		}
		if l.InCode {
			continue
		}
		if m := checkLineRe.FindStringSubmatchIndex(l.Text); m != nil {
			if sc < 0 {
				r.Stray = append(r.Stray, l.N)
				continue
			}
			s := &r.Scenarios[sc]
			kind := ""
			if m[2] >= 0 {
				kind = strings.TrimSpace(l.Text[m[2]:m[3]])
			}
			s.Checks = append(s.Checks, strings.TrimSpace(l.Text[m[4]:m[5]]))
			s.Kinds = append(s.Kinds, kind)
			s.KindLines = append(s.KindLines, l.N)
			s.tagged = append(s.tagged, m[2] >= 0)
		}
	}
	return r
}

// Find returns the requirement named name, or nil.
func (s *Spec) Find(name string) *Requirement {
	for i := range s.Requirements {
		if s.Requirements[i].Name == name {
			return &s.Requirements[i]
		}
	}
	return nil
}

// Render writes the spec back: preamble, then each requirement separated
// by one blank line.
func (s *Spec) Render() string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(s.Preamble, "\n"))
	b.WriteString("\n")
	for _, r := range s.Requirements {
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(r.Text, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// validateRequirementBody checks the rules every requirement obeys, in a
// spec or in a delta: at least one scenario, exactly one check per
// scenario, no check outside a scenario.
func validateRequirementBody(file string, r Requirement) []Finding {
	var out []Finding
	if r.Name == "" {
		out = append(out, Finding{File: file, Line: r.Line, Message: "requirement has no name"})
	}
	if len(r.Scenarios) == 0 {
		out = append(out, Finding{
			File: file, Line: r.Line,
			Message: "requirement \"" + r.Name + "\" has no `#### Scenario:` — every requirement needs at least one",
		})
	}
	for _, sc := range r.Scenarios {
		for i := range sc.Kinds {
			if why := badCheckKind("scenario \""+sc.Name+"\"'s check", sc.Kinds[i], sc.Checks[i], sc.tagged[i]); why != "" {
				out = append(out, Finding{File: file, Line: sc.KindLines[i], Message: why})
			}
		}
		switch len(sc.Checks) {
		case 1:
			if sc.Checks[0] == "" {
				out = append(out, Finding{
					File: file, Line: sc.Line,
					Message: "scenario \"" + sc.Name + "\" has an empty `check:` — give a command or human-review",
				})
			}
		case 0:
			out = append(out, Finding{
				File: file, Line: sc.Line,
				Message: "scenario \"" + sc.Name + "\" has no `- check: <command> | human-review` line",
			})
		default:
			out = append(out, Finding{
				File: file, Line: sc.Line,
				Message: "scenario \"" + sc.Name + "\" has more than one `check:` line — exactly one per scenario",
			})
		}
	}
	for _, n := range r.Stray {
		out = append(out, Finding{File: file, Line: n, Message: "`check:` line outside any scenario"})
	}
	return out
}

// ValidateSpec checks a capability spec's shape and its cap (maxBytes,
// caps.spec).
func ValidateSpec(file, doc string, maxBytes int) []Finding {
	var out []Finding
	if len(doc) > maxBytes {
		out = append(out, Finding{File: file, Message: "spec is " + sizeKB(len(doc)) +
			", over the " + capSize(maxBytes) + " cap (caps.spec) — split into two capabilities (a change like any other)"})
	}
	s := ParseSpec(doc)
	if s.Title == "" {
		out = append(out, Finding{File: file, Line: 1, Message: "missing `# <Capability title>`"})
	}
	if s.Purpose == "" {
		out = append(out, Finding{File: file, Message: "missing `Purpose:` line before the first requirement"})
	}
	seen := map[string]int{}
	for _, r := range s.Requirements {
		if prev, dup := seen[r.Name]; dup {
			out = append(out, Finding{
				File: file, Line: r.Line,
				Message: "duplicate requirement \"" + r.Name + "\" (first at line " + strconv.Itoa(prev) + ")",
			})
		}
		seen[r.Name] = r.Line
		out = append(out, validateRequirementBody(file, r)...)
	}
	for _, n := range s.Other {
		out = append(out, Finding{
			File: file, Line: n,
			Message: "level-3 heading that is not `### Requirement: <name>`",
		})
	}
	return out
}

// nearMiss returns the requirement in spec whose name most nearly matches
// name — equal after folding case and whitespace, or within a small edit
// distance — or "" (item 8, OpenSpec G4: a precise "did you mean" saves a
// model a turn per typo).
func nearMiss(spec Spec, name string) string {
	fold := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	want := fold(name)
	best, bestDist := "", -1
	for i := range spec.Requirements {
		have := spec.Requirements[i].Name
		if have == name {
			continue
		}
		if fold(have) == want {
			return have
		}
		limit := max(len(want)/5, 2)
		if d := editDistance(want, fold(have)); d <= limit && (bestDist < 0 || d < bestDist) {
			best, bestDist = have, d
		}
	}
	return best
}

// nearMissFix is the remediation for a delta naming a missing requirement.
func nearMissFix(spec Spec, name string) string {
	if near := nearMiss(spec, name); near != "" {
		return fmt.Sprintf("did you mean %q? Name the heading exactly", near)
	}
	return "name it exactly"
}

// editDistance is the Levenshtein distance between a and b, by rune.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

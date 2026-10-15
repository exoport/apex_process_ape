package apexv1

import (
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
}

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
	for _, l := range pre {
		if lv, title := headingLevel(l); lv == 1 && s.Title == "" {
			s.Title = title
			continue
		}
		if t := strings.TrimSpace(l.Text); strings.HasPrefix(t, "Purpose:") && s.Purpose == "" {
			s.Purpose = strings.TrimSpace(strings.TrimPrefix(t, "Purpose:"))
		}
	}
	s.Preamble = strings.TrimRight(joinBody(pre), "\n")
	return s
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
		if m := checkLineRe.FindStringSubmatch(l.Text); m != nil {
			if sc < 0 {
				r.Stray = append(r.Stray, l.N)
				continue
			}
			r.Scenarios[sc].Checks = append(r.Scenarios[sc].Checks, strings.TrimSpace(m[1]))
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
		out = append(out, Finding{File: file, Line: r.Line,
			Message: "requirement \"" + r.Name + "\" has no `#### Scenario:` — every requirement needs at least one"})
	}
	for _, sc := range r.Scenarios {
		switch len(sc.Checks) {
		case 1:
			if sc.Checks[0] == "" {
				out = append(out, Finding{File: file, Line: sc.Line,
					Message: "scenario \"" + sc.Name + "\" has an empty `check:` — give a command or human-review"})
			}
		case 0:
			out = append(out, Finding{File: file, Line: sc.Line,
				Message: "scenario \"" + sc.Name + "\" has no `- check: <command> | human-review` line"})
		default:
			out = append(out, Finding{File: file, Line: sc.Line,
				Message: "scenario \"" + sc.Name + "\" has more than one `check:` line — exactly one per scenario"})
		}
	}
	for _, n := range r.Stray {
		out = append(out, Finding{File: file, Line: n, Message: "`check:` line outside any scenario"})
	}
	return out
}

// ValidateSpec checks a capability spec's shape and cap.
func ValidateSpec(file, doc string) []Finding {
	var out []Finding
	if len(doc) > SpecMaxBytes {
		out = append(out, Finding{File: file, Message: "spec is " + sizeKB(len(doc)) +
			", over the 20 KB cap — split into two capabilities (a change like any other)"})
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
			out = append(out, Finding{File: file, Line: r.Line,
				Message: "duplicate requirement \"" + r.Name + "\" (first at line " + strconv.Itoa(prev) + ")"})
		}
		seen[r.Name] = r.Line
		out = append(out, validateRequirementBody(file, r)...)
	}
	for _, n := range s.Other {
		out = append(out, Finding{File: file, Line: n,
			Message: "level-3 heading that is not `### Requirement: <name>`"})
	}
	return out
}

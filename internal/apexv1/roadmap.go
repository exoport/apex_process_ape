package apexv1

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Milestone statuses.
const (
	MilestoneProposed = "proposed"
	MilestoneApproved = "approved"
	MilestoneDone     = "done"
)

// Milestone is one `## M<n> — <title>` section of roadmap.md.
type Milestone struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Line       int      `json:"line"`
	Status     string   `json:"status"` // proposed | approved | done | "" (missing) | raw text when unrecognised
	StatusText string   `json:"status_text"`
	StatusLine int      `json:"-"`
	Gate       string   `json:"gate"`
	Budget     *float64 `json:"budget,omitempty"`
	BudgetRaw  string   `json:"-"`
	Goal       string   `json:"goal"`
	OutOfScope []string `json:"out_of_scope,omitempty"`
	// ApprovedAt is the timestamp inside `approved (by <who> <ts>)`.
	ApprovedAt string `json:"approved_at,omitempty"`
}

// Roadmap is a parsed roadmap.md.
type Roadmap struct {
	Title      string
	Milestones []Milestone
	lines      []string
	headErrs   []Finding
}

var (
	milestoneHeadRe = regexp.MustCompile(`^(M\d+)\s*(?:—|–|-|:)\s*(.+)$`)
	approvedRe      = regexp.MustCompile(`^approved\s*\(by\s+(.+?)\s+(\S+)\)$`)
	doneRe          = regexp.MustCompile(`^done\s*\((\S+)\s+([0-9a-f]{7,40})\)$`)
)

// stripComment drops a trailing ` # comment` from a field value.
func stripComment(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// ParseRoadmap parses roadmap.md; shape problems are ValidateRoadmap's.
func ParseRoadmap(file, doc string) *Roadmap {
	r := &Roadmap{lines: strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")}
	lines := splitLines(doc)
	for _, l := range lines {
		if lv, t := headingLevel(l); lv == 1 {
			r.Title = t
			break
		}
	}
	for _, s := range sections(lines, 2) {
		m := milestoneHeadRe.FindStringSubmatch(s.Title)
		if m == nil {
			r.headErrs = append(r.headErrs, Finding{File: file, Line: s.Line,
				Message: "roadmap section must be `## M<n> — <title>`"})
			continue
		}
		ms := Milestone{ID: m[1], Title: strings.TrimSpace(m[2]), Line: s.Line}
		inOOS := false
		for _, l := range s.Body {
			if l.InCode {
				continue
			}
			t := strings.TrimSpace(l.Text)
			key, val, ok := strings.Cut(t, ":")
			if ok && !strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "*") {
				inOOS = false
				val = strings.TrimSpace(val)
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "status":
					ms.StatusLine = l.N
					ms.StatusText = stripComment(val)
					ms.Status, ms.ApprovedAt = classifyMilestoneStatus(ms.StatusText)
					continue
				case "gate":
					ms.Gate = strings.Trim(val, "`")
					continue
				case "budget":
					ms.BudgetRaw = stripComment(val)
					raw := strings.TrimPrefix(strings.TrimSpace(strings.TrimSuffix(ms.BudgetRaw, "USD")), "$")
					if f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
						ms.Budget = &f
					}
					continue
				case "goal":
					ms.Goal = val
					continue
				case "out of scope":
					inOOS = true
					if val != "" {
						ms.OutOfScope = append(ms.OutOfScope, val)
					}
					continue
				}
			}
			if inOOS && (strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")) {
				ms.OutOfScope = append(ms.OutOfScope, strings.TrimSpace(t[2:]))
			}
		}
		r.Milestones = append(r.Milestones, ms)
	}
	return r
}

func classifyMilestoneStatus(text string) (status, approvedAt string) {
	switch {
	case text == MilestoneProposed:
		return MilestoneProposed, ""
	case approvedRe.MatchString(text):
		return MilestoneApproved, approvedRe.FindStringSubmatch(text)[2]
	case doneRe.MatchString(text):
		return MilestoneDone, ""
	}
	return text, ""
}

// Find returns the milestone with id, or nil.
func (r *Roadmap) Find(id string) *Milestone {
	for i := range r.Milestones {
		if r.Milestones[i].ID == id {
			return &r.Milestones[i]
		}
	}
	return nil
}

// Current is the first milestone not yet done, or nil when all are.
func (r *Roadmap) Current() *Milestone {
	for i := range r.Milestones {
		if r.Milestones[i].Status != MilestoneDone {
			return &r.Milestones[i]
		}
	}
	return nil
}

// NextID is the id `ape milestone new` allocates.
func (r *Roadmap) NextID() string {
	maxN := 0
	for _, m := range r.Milestones {
		if n, err := strconv.Atoi(strings.TrimPrefix(m.ID, "M")); err == nil && n > maxN {
			maxN = n
		}
	}
	return "M" + strconv.Itoa(maxN+1)
}

// WithStatus returns the document with milestone id's Status line set to
// text, inserting the line under the heading when it is missing.
func (r *Roadmap) WithStatus(id, text string) (string, error) {
	m := r.Find(id)
	if m == nil {
		return "", fmt.Errorf("no milestone %s in roadmap.md", id)
	}
	lines := append([]string(nil), r.lines...)
	if m.StatusLine > 0 {
		lines[m.StatusLine-1] = "Status: " + text
	} else {
		at := m.Line // insert after the heading line (index m.Line)
		lines = append(lines[:at], append([]string{"Status: " + text}, lines[at:]...)...)
	}
	return strings.Join(lines, "\n"), nil
}

// AppendMilestone renders a new milestone section onto doc.
func AppendMilestone(doc, id, title, gate string, budget *float64, goal string, outOfScope []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(doc, "\n"))
	b.WriteString("\n\n## " + id + " — " + title + "\n")
	b.WriteString("Status: " + MilestoneProposed + "\n")
	b.WriteString("Gate: " + gate + "\n")
	if budget != nil {
		b.WriteString("Budget: " + strconv.FormatFloat(*budget, 'f', -1, 64) + "\n")
	}
	if goal == "" {
		goal = "<!-- one paragraph: what is true when this milestone is done -->"
	}
	b.WriteString("Goal: " + goal + "\n")
	b.WriteString("Out of scope:\n")
	for _, o := range outOfScope {
		b.WriteString("- " + o + "\n")
	}
	return b.String()
}

// ValidateRoadmap checks roadmap.md's shape.
func ValidateRoadmap(file, doc string) []Finding {
	r := ParseRoadmap(file, doc)
	out := append([]Finding(nil), r.headErrs...)
	if r.Title != "Roadmap" {
		out = append(out, Finding{File: file, Line: 1, Message: "first heading must be `# Roadmap`"})
	}
	if len(r.Milestones) == 0 {
		out = append(out, Finding{File: file,
			Message: "no milestones — add one with `ape milestone new \"<title>\" --gate \"<cmd>\"`"})
	}
	seen := map[string]bool{}
	for _, m := range r.Milestones {
		if seen[m.ID] {
			out = append(out, Finding{File: file, Line: m.Line, Message: "duplicate milestone " + m.ID})
		}
		seen[m.ID] = true
		switch m.Status {
		case MilestoneProposed, MilestoneApproved, MilestoneDone:
		case "":
			out = append(out, Finding{File: file, Line: m.Line, Message: m.ID + " has no `Status:` line"})
		default:
			out = append(out, Finding{File: file, Line: m.StatusLine,
				Message: m.ID + " status \"" + m.StatusText + "\" is not proposed | approved (by <who> <ts>) | done (<ts> <sha>) — only ape writes it"})
		}
		if m.Gate == "" {
			out = append(out, Finding{File: file, Line: m.Line,
				Message: m.ID + " has no `Gate:` — one command that is red until the milestone is done"})
		}
		if m.BudgetRaw != "" && m.Budget == nil {
			out = append(out, Finding{File: file, Line: m.Line, Message: m.ID + " budget \"" + m.BudgetRaw + "\" is not a number (USD)"})
		}
		if strings.TrimSpace(m.Goal) == "" || !hasProse([]mdLine{{Text: m.Goal}}) {
			out = append(out, Finding{File: file, Line: m.Line, Message: m.ID + " has no `Goal:` paragraph"})
		}
	}
	return out
}

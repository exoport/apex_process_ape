package apexv1

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
)

// MilestoneStatus is the current milestone as `ape status` shows it.
type MilestoneStatus struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	StatusText string   `json:"status_text"`
	Gate       string   `json:"gate"`
	Budget     *float64 `json:"budget,omitempty"`
	GateLast   *GateRun `json:"gate_last,omitempty"`
	// SpentUSD is the spend of runs started since the milestone was
	// approved (all runs when it is not approved yet).
	SpentUSD float64 `json:"spent_usd"`
}

// ChangeStatus is one change as `ape status` shows it.
type ChangeStatus struct {
	ID            string   `json:"id"`
	Title         string   `json:"title,omitempty"`
	Milestone     string   `json:"milestone,omitempty"`
	Status        string   `json:"status"`
	BlockedReason string   `json:"blocked_reason,omitempty"`
	PendingHuman  []string `json:"pending_human,omitempty"`
	Failing       []string `json:"failing,omitempty"`
	VerifySHA     string   `json:"verify_sha,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// Spend is the cost recorded in the project's run manifests.
type Spend struct {
	TotalUSD float64 `json:"total_usd"`
	Runs     int     `json:"runs"`
	// Note says what the figure leaves out.
	Note string `json:"note"`
}

// MilestoneLine is one roadmap entry in the status overview.
type MilestoneLine struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// StatusReport is `ape status`.
type StatusReport struct {
	Project     string           `json:"project"`
	Root        string           `json:"root"`
	Initialized bool             `json:"initialized"`
	Milestone   *MilestoneStatus `json:"milestone"`
	Milestones  []MilestoneLine  `json:"milestones"`
	Changes     []ChangeStatus   `json:"changes"`
	Archived    int              `json:"archived"`
	Spend       Spend            `json:"spend"`
	Next        string           `json:"next"`
}

// Status builds the report.
func (p *Project) Status() *StatusReport {
	r := &StatusReport{Root: p.Root, Initialized: p.IsV1(), Changes: []ChangeStatus{}, Milestones: []MilestoneLine{}}
	if cfg, err := LoadConfig(p.Root); err == nil && cfg.ProjectName != "" {
		r.Project = cfg.ProjectName
	} else {
		r.Project = baseName(p.Root)
	}
	runs, _ := cost.RunCosts(p.Root)
	for _, run := range runs {
		r.Spend.TotalUSD += run.CostUSD
		r.Spend.Runs++
	}
	r.Spend.TotalUSD = round2(r.Spend.TotalUSD)
	r.Spend.Note = "sum of finished ape task/pipeline run manifests; a run still in progress is not counted yet"

	if rm := p.roadmap(); rm != nil {
		for i := range rm.Milestones {
			m := &rm.Milestones[i]
			r.Milestones = append(r.Milestones, MilestoneLine{ID: m.ID, Title: m.Title, Status: m.Status})
		}
		if m := rm.Current(); m != nil {
			r.Milestone = p.milestoneStatus(m, runs)
		}
	}

	for _, c := range p.Changes() {
		cs := ChangeStatus{ID: c.ID}
		if c.Doc != nil {
			cs.Title, cs.Milestone = c.Doc.Title, c.Doc.Meta.Milestone
		}
		if c.State == nil {
			cs.Status = "invalid"
			if c.Err != nil {
				cs.Error = c.Err.Error()
			}
			r.Changes = append(r.Changes, cs)
			continue
		}
		cs.Status = c.State.Status
		if c.State.BlockedReason != nil {
			cs.BlockedReason = *c.State.BlockedReason
		}
		if c.Doc != nil {
			cs.PendingHuman = c.State.PendingHuman(c.Doc.HumanTasks())
		}
		if v := c.State.Verify; v != nil {
			cs.VerifySHA = v.SHA
			for _, res := range v.Results {
				if res.Exit != 0 {
					cs.Failing = append(cs.Failing, fmt.Sprintf("%s (exit %d)", res.Task, res.Exit))
				}
			}
		}
		r.Changes = append(r.Changes, cs)
	}
	r.Archived = len(p.ArchivedIDs())
	r.Next = nextStep(r)
	return r
}

// milestoneStatus is the current milestone with its last gate run and
// the spend of runs started since its approval.
func (p *Project) milestoneStatus(m *Milestone, runs []cost.RunCost) *MilestoneStatus {
	ms := &MilestoneStatus{ID: m.ID, Title: m.Title, Status: m.Status, StatusText: m.StatusText, Gate: m.Gate, Budget: m.Budget}
	if recs, err := p.LoadMilestoneRecords(); err == nil && recs[m.ID] != nil {
		ms.GateLast = recs[m.ID].Last
	}
	since := time.Time{}
	if t, err := time.Parse(time.RFC3339, m.ApprovedAt); err == nil {
		since = t
	}
	for _, run := range runs {
		if !run.StartedAt.Before(since) {
			ms.SpentUSD += run.CostUSD
		}
	}
	ms.SpentUSD = round2(ms.SpentUSD)
	return ms
}

// nextStep is a deterministic hint: what the way of working says comes
// next, given only the recorded state.
func nextStep(r *StatusReport) string {
	if !r.Initialized {
		return "run `ape project init`"
	}
	m := r.Milestone
	if m == nil {
		if len(r.Milestones) == 0 {
			return "plan: no milestone yet (apex-plan, then `ape milestone new`)"
		}
		return "every milestone is done — propose the next one (apex-plan)"
	}
	if m.Status != MilestoneApproved {
		return fmt.Sprintf("%s awaits approval: a person runs `ape milestone approve %s --by <who>`", m.ID, m.ID)
	}
	for i := range r.Changes {
		c := &r.Changes[i]
		if c.Milestone != m.ID && c.Milestone != "" {
			continue
		}
		switch c.Status {
		case StatusReviewed:
			return "archive " + c.ID + ": `ape change archive " + c.ID + "`"
		case StatusVerified:
			return "review " + c.ID + " (apex-review)"
		case StatusInProgress:
			return "apply " + c.ID + " until `ape change verify " + c.ID + "` is green (apex-apply)"
		case StatusDraft:
			return "apply " + c.ID + ": `ape change check " + c.ID + "`, then `ape change start " + c.ID + "` (apex-apply)"
		}
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; c.Status == StatusBlocked {
			return "blocked: " + c.ID + " — " + c.BlockedReason + " (re-plan, or a person answers; `ape change start " + c.ID + "` resumes)"
		}
	}
	if m.GateLast != nil && m.GateLast.Exit == 0 && m.GateLast.SHA != "" {
		return fmt.Sprintf("%s gate was green at %s — `ape milestone close %s`", m.ID, short(m.GateLast.SHA), m.ID)
	}
	return fmt.Sprintf("plan the next change toward %s (apex-change), or `ape milestone gate %s` if the goal looks met", m.ID, m.ID)
}

// briefReasonMax caps a blocked reason in the brief, so a long question
// does not push the rest past 25 lines on a narrow terminal.
const briefReasonMax = 90

// Brief renders the report in at most 25 lines, for the SessionStart hook.
func (r *StatusReport) Brief() string {
	var b []string
	b = append(b, "APEX status — "+r.Project)
	if !r.Initialized {
		b = append(b, "not an APEX v1 project yet: run `ape project init`")
		return strings.Join(b, "\n") + "\n"
	}
	if m := r.Milestone; m != nil {
		b = append(b, m.briefLines()...)
	} else if len(r.Milestones) == 0 {
		b = append(b, "No milestones yet.")
	} else {
		b = append(b, "All milestones done.")
	}
	const maxChanges = 12
	var open, blocked []ChangeStatus
	for i := range r.Changes {
		if r.Changes[i].Status == StatusBlocked {
			blocked = append(blocked, r.Changes[i])
		} else {
			open = append(open, r.Changes[i])
		}
	}
	if len(open) == 0 {
		b = append(b, fmt.Sprintf("Changes: none open (%d archived)", r.Archived))
	} else {
		b = append(b, fmt.Sprintf("Changes (%d archived):", r.Archived))
	}
	shown := 0
groups:
	for gi, group := range [][]ChangeStatus{open, blocked} {
		if gi == 1 && len(blocked) > 0 {
			b = append(b, "Blocked:")
		}
		for i := range group {
			c := &group[i]
			if shown == maxChanges {
				b = append(b, fmt.Sprintf("  … %d more (`ape status --json`)", len(r.Changes)-maxChanges))
				break groups
			}
			shown++
			line := fmt.Sprintf("  %-28s %-11s %s", c.ID, c.Status, c.Milestone)
			switch {
			case c.BlockedReason != "":
				line += " — " + truncate(c.BlockedReason, briefReasonMax)
			case len(c.Failing) > 0 && c.Status == StatusInProgress:
				line += " — red: " + strings.Join(c.Failing, ", ")
			}
			if len(c.PendingHuman) > 0 {
				line += " — human-review pending: " + strings.Join(c.PendingHuman, ",")
			}
			b = append(b, line)
		}
	}
	b = append(b, fmt.Sprintf("Spend: $%.2f over %d runs", r.Spend.TotalUSD, r.Spend.Runs), "Next: "+r.Next)
	return strings.Join(b, "\n") + "\n"
}

// briefLines is the milestone's three lines in the brief.
func (m *MilestoneStatus) briefLines() []string {
	gate := "  gate `" + m.Gate + "`"
	switch {
	case m.GateLast == nil:
		gate += " — not run yet"
	case m.GateLast.Exit == 0:
		gate += fmt.Sprintf(" — last green at %s (%s)", m.GateLast.At, short(m.GateLast.SHA))
	default:
		gate += fmt.Sprintf(" — last red (exit %d) at %s (%s)", m.GateLast.Exit, m.GateLast.At, short(m.GateLast.SHA))
	}
	spend := fmt.Sprintf("  spent $%.2f", m.SpentUSD)
	if m.Budget != nil {
		spend += fmt.Sprintf(" of $%.2f budget", *m.Budget)
	}
	return []string{fmt.Sprintf("Milestone %s — %s: %s", m.ID, m.Title, m.StatusText), gate, spend}
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	if sha == "" {
		return "no sha"
	}
	return sha
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

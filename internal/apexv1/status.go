package apexv1

import (
	"fmt"
	"math"
	"strings"

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
	// approved (all runs when it is not approved yet); a session stopped
	// at the budget counts at the cost ape measured when it stopped it.
	SpentUSD float64 `json:"spent_usd"`
	// BudgetEnforced: limits.budget is enforce and the milestone has a
	// Budget:, so ape task stops at it (backlog 30).
	BudgetEnforced bool `json:"budget_enforced"`
	// Stops are the sessions ape stopped at the milestone's budget,
	// oldest first.
	Stops []LimitStop `json:"stops"`
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
	// Attempts / AttemptsCap: red verifies since the change started, and
	// limits.attempts (backlog 30). Set for a started change.
	Attempts    int `json:"attempts"`
	AttemptsCap int `json:"attempts_cap,omitempty"`
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
	// PreExistingRed is what was red at a change's baseline that is not
	// the change's own: the planner's to roadmap (backlog 37).
	PreExistingRed []PreExistingRed `json:"pre_existing_red"`
	Spend          Spend            `json:"spend"`
	Next           string           `json:"next"`
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

	changes := p.Changes()
	attemptsCap := p.limits().AttemptsCap()
	r.PreExistingRed = p.preExistingRed(changes)
	for _, c := range changes {
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
		cs.Attempts = c.State.Attempts
		if c.State.Active() || c.State.Attempts > 0 {
			cs.AttemptsCap = attemptsCap
		}
		if c.State.BlockedReason != nil {
			cs.BlockedReason = *c.State.BlockedReason
		}
		if c.Doc != nil {
			cs.PendingHuman = c.State.PendingHuman(c.Doc.HumanTasks())
		}
		if v := c.State.Verify; v != nil {
			cs.VerifySHA = v.SHA
			for i := range v.Results {
				if v.Results[i].Red() {
					cs.Failing = append(cs.Failing, fmt.Sprintf("%s (exit %d)", v.Results[i].Task, v.Results[i].Exit))
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
	ms.GateLast = p.LastGateRun(m.ID)
	ms.Stops = p.milestoneStops(m.ID)
	if ms.Stops == nil {
		ms.Stops = []LimitStop{}
	}
	ms.SpentUSD = round2(milestoneSpend(m, runs, ms.Stops))
	ms.BudgetEnforced = p.limits().BudgetEnforced() && m.Budget != nil
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
	if m.GateLast != nil && m.GateLast.Green() && m.GateLast.SHA != "" {
		return fmt.Sprintf("%s gate was green at %s — `ape milestone close %s`", m.ID, short(m.GateLast.SHA), m.ID)
	}
	return fmt.Sprintf("plan the next change toward %s (apex-change), or `ape milestone gate %s` if the goal looks met", m.ID, m.ID)
}

// briefReasonMax caps a blocked reason in the brief, so a long question
// does not push the rest past 25 lines on a narrow terminal.
const briefReasonMax = 90

// briefLineMax caps the brief's red-at-baseline line.
const briefLineMax = 160

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
			if c.AttemptsCap > 0 && c.Attempts > 0 {
				line += fmt.Sprintf(" (%d/%d attempts)", c.Attempts, c.AttemptsCap)
			}
			b = append(b, line)
		}
	}
	if line := r.preExistingRedLine(); line != "" {
		b = append(b, line)
	}
	b = append(b, fmt.Sprintf("Spend: $%.2f over %d runs", r.Spend.TotalUSD, r.Spend.Runs), "Next: "+r.Next)
	return strings.Join(b, "\n") + "\n"
}

// preExistingRedLine is one line naming what was red at a baseline and is
// no change's own, or "" when nothing was.
func (r *StatusReport) preExistingRedLine() string {
	if len(r.PreExistingRed) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var refs []string
	for _, e := range r.PreExistingRed {
		ref := e.Ref
		if e.Kind == KindRegression {
			ref = "regression `" + e.Cmd + "`"
		}
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return truncate("Red at baseline, not any change's own (roadmap it): "+strings.Join(refs, ", "), briefLineMax)
}

// briefLines is the milestone's lines in the brief: three, and a fourth
// naming its last limit stop when it has one.
func (m *MilestoneStatus) briefLines() []string {
	gate := "  gate `" + m.Gate + "`"
	switch {
	case m.GateLast == nil:
		gate += " — not run yet"
	case m.GateLast.Green():
		gate += fmt.Sprintf(" — last green at %s (%s)", m.GateLast.At, short(m.GateLast.SHA))
	default:
		gate += fmt.Sprintf(" — last red (%s) at %s (%s)", m.GateLast.Why(), m.GateLast.At, short(m.GateLast.SHA))
	}
	spend := fmt.Sprintf("  spent $%.2f", m.SpentUSD)
	if m.Budget != nil {
		spend += fmt.Sprintf(" of $%.2f budget", *m.Budget)
		if m.BudgetEnforced {
			spend += " (enforced)"
		}
	}
	lines := []string{fmt.Sprintf("Milestone %s — %s: %s", m.ID, m.Title, m.StatusText), gate, spend}
	if n := len(m.Stops); n > 0 {
		line := "  " + m.Stops[n-1].Describe()
		if n > 1 {
			line = fmt.Sprintf("  %d limit stops, last %s", n, m.Stops[n-1].Describe())
		}
		lines = append(lines, truncate(line, briefLineMax))
	}
	return lines
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

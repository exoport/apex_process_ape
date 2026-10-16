package apexv1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"gopkg.in/yaml.v3"
)

// DefaultAttempts is limits.attempts when the config sets none. The loop
// limits (backlog 30) are ceilings ape enforces itself, so a loop that does
// not converge ends with a recorded reason instead of running on:
//
//	limits:
//	  budget: enforce      # optional; default off
//	  attempts: 5          # red verifies per change start; default 5
const DefaultAttempts = 5

// StopBudget is the limit-stop reason milestones.json and the run
// manifest's termination kind record for a session stopped at the
// milestone's budget.
const StopBudget = "budget"

// Limits is _apex/config.yaml's limits: block. Unset values take their
// defaults; a value ape cannot use fails the config's parse, like any
// unparseable config (Open refuses it rather than guess).
type Limits struct {
	// Budget is "enforce" or "off" (the default, also when absent).
	Budget string `yaml:"budget"`
	// Attempts caps red verifies per change start: the red verify that
	// reaches it blocks the change.
	Attempts *int `yaml:"attempts"`
}

// UnmarshalYAML validates the block as it parses.
func (l *Limits) UnmarshalYAML(n *yaml.Node) error {
	type plain Limits
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	switch strings.TrimSpace(p.Budget) {
	case "", "off", "enforce":
		p.Budget = strings.TrimSpace(p.Budget)
	default:
		return fmt.Errorf("limits.budget: %q is not enforce or off", p.Budget)
	}
	if p.Attempts != nil && *p.Attempts < 1 {
		return fmt.Errorf("limits.attempts: %d is not a positive whole number", *p.Attempts)
	}
	*l = Limits(p)
	return nil
}

// BudgetEnforced reports limits.budget: enforce.
func (l Limits) BudgetEnforced() bool { return l.Budget == "enforce" }

// AttemptsCap is limits.attempts, or DefaultAttempts.
func (l Limits) AttemptsCap() int {
	if l.Attempts != nil {
		return *l.Attempts
	}
	return DefaultAttempts
}

// limits is the project's limits, defaults when the config cannot be read
// (Open has already refused an unparseable one).
func (p *Project) limits() Limits {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return Limits{}
	}
	return cfg.Limits
}

// LimitStop is one session ape stopped at a milestone limit, as
// milestones.json records it under the milestone's "stops".
type LimitStop struct {
	Reason string `json:"reason"`
	At     string `json:"at"`
	// Run is the stopped session's run id (its manifest under
	// _apex/state/runs/tasks/<skill>/<run>/), Skill its skill.
	Run   string `json:"run"`
	Skill string `json:"skill"`
	// The milestone's spend when it was stopped (recorded runs plus this
	// session), its budget, and this session's own cost — which its
	// manifest does not hold, since a manifest counts only completed steps.
	SpentUSD   float64 `json:"spent_usd"`
	BudgetUSD  float64 `json:"budget_usd"`
	SessionUSD float64 `json:"session_usd"`
}

// Describe is the stop in one line.
func (s *LimitStop) Describe() string {
	if s.Reason == StopBudget {
		return fmt.Sprintf("stopped at budget %s: $%.2f of $%.2f (%s run %s)", s.At, s.SpentUSD, s.BudgetUSD, s.Skill, s.Run)
	}
	return "stopped (" + s.Reason + ") " + s.At + " run " + s.Run
}

// LimitStopsJSON is _apex/state/limit-stops.json: milestone id → the
// sessions ape stopped at a limit. Ignored by git, like attempts.json: a
// stop is a run event, not a status change, and the stopped session may be
// on a change branch (D38), where a commit to a milestone record would
// break D11 (milestone commits stay on the base branch) and conflict at
// the change's rebase. The run manifest (termination "budget") is the
// run's own record of it.
func (p *Project) LimitStopsJSON() string { return filepath.Join(p.StateDir(), "limit-stops.json") }

func (p *Project) loadLimitStops() map[string][]LimitStop {
	out := map[string][]LimitStop{}
	data, err := os.ReadFile(p.LimitStopsJSON())
	if err != nil {
		return out
	}
	if json.Unmarshal(data, &out) != nil || out == nil {
		return map[string][]LimitStop{}
	}
	return out
}

// RecordLimitStop appends a stop to the milestone's list in
// limit-stops.json. It commits nothing.
func (p *Project) RecordLimitStop(milestone string, stop LimitStop) error {
	all := p.loadLimitStops()
	all[milestone] = append(all[milestone], stop)
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	p.ensureState()
	return writeFileAtomic(p.LimitStopsJSON(), string(data)+"\n")
}

// milestoneStops is the recorded limit stops of a milestone: those an
// earlier build committed into milestones.json, then this checkout's.
func (p *Project) milestoneStops(id string) []LimitStop {
	var out []LimitStop
	if recs, err := p.LoadMilestoneRecords(); err == nil && recs[id] != nil {
		out = append(out, recs[id].Stops...)
	}
	return append(out, p.loadLimitStops()[id]...)
}

// approvedSince is when m was approved; zero when it was not.
func approvedSince(m *Milestone) time.Time {
	t, err := time.Parse(time.RFC3339, m.ApprovedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// milestoneSpend is the spend of the runs started since m's approval
// (every run when it is not approved): each run's manifest cost, or what
// ape measured when a limit stopped it, whichever is more — a stopped
// session's manifest counts only its completed steps, which for `ape task`
// is none. A fix's runs — whose --args names a change of `milestone:
// none` (D40) — count toward no milestone.
func milestoneSpend(m *Milestone, runs []cost.RunCost, stops []LimitStop, fixes map[string]bool) float64 {
	since := approvedSince(m)
	stopped := map[string]float64{}
	for i := range stops {
		if stops[i].SessionUSD > stopped[stops[i].Run] {
			stopped[stops[i].Run] = stops[i].SessionUSD
		}
	}
	total := 0.0
	for _, run := range runs {
		if run.StartedAt.Before(since) || fixes[RunChangeArg(run.Args)] {
			continue
		}
		c := run.CostUSD
		if s, ok := stopped[run.RunID]; ok && s > c {
			c = s
		}
		total += c
	}
	return total
}

// TaskLimits is what `ape task` enforces on one session of a v1 project:
// the current milestone's budget, only with limits.budget: enforce and a
// Budget: line.
type TaskLimits struct {
	Milestone string
	Skill     string
	// Budget is the milestone's budget; SpentUSD the milestone's recorded
	// spend when the session started.
	Budget   float64
	SpentUSD float64
}

// LimitHit is a limit a running session reached.
type LimitHit struct {
	Reason  string
	Message string
	Stop    LimitStop
}

// TaskLimits decides the limits of a session of skill, or refuses to start
// it: FailError milestone.budget-exceeded when the milestone's spend is
// already at its enforced budget. The milestone is the current one as
// `ape status` derives it, and only when it is approved; with none, or
// with no budget enforced, nothing applies and it returns nil.
func (p *Project) TaskLimits(skill string) (*TaskLimits, error) {
	return p.TaskLimitsFor(context.Background(), skill, "")
}

// TaskLimitsFor is TaskLimits for a session started with --args args: a
// fix's session (args names a change of `milestone: none`, D40) counts
// toward no milestone, so no milestone budget applies to it.
func (p *Project) TaskLimitsFor(ctx context.Context, skill, args string) (*TaskLimits, error) {
	if p.IsFixRun(ctx, args) {
		return nil, nil //nolint:nilnil // a fix's session: no budget applies
	}
	var m *Milestone
	if rm := p.roadmap(); rm != nil {
		m = rm.Current()
	}
	if m == nil || m.Status != MilestoneApproved || m.Budget == nil || !p.limits().BudgetEnforced() {
		return nil, nil //nolint:nilnil // no budget applies: the documented nil
	}
	runs, _ := cost.RunCosts(p.Root)
	tl := &TaskLimits{
		Milestone: m.ID, Skill: skill, Budget: *m.Budget,
		SpentUSD: milestoneSpend(m, runs, p.milestoneStops(m.ID), p.fixIDs(ctx)),
	}
	if tl.SpentUSD >= tl.Budget {
		return nil, &FailError{Code: "milestone.budget-exceeded", Msg: fmt.Sprintf(
			"%s has spent $%.2f of its $%.2f budget — a person raises Budget: in roadmap.md or re-scopes the milestone; no session starts until then",
			m.ID, tl.SpentUSD, tl.Budget)}
	}
	return tl, nil
}

// Check judges the running session by its cost so far: nil while the
// milestone's spend stays under its budget.
func (l *TaskLimits) Check(sessionUSD float64) *LimitHit {
	if l == nil {
		return nil
	}
	spent := l.SpentUSD + sessionUSD
	if spent < l.Budget {
		return nil
	}
	return &LimitHit{
		Reason: StopBudget,
		Message: fmt.Sprintf("%s budget reached: $%.2f of $%.2f ($%.2f recorded + $%.2f this session) — a person raises Budget: in roadmap.md or re-scopes the milestone",
			l.Milestone, spent, l.Budget, l.SpentUSD, sessionUSD),
		Stop: LimitStop{
			Reason: StopBudget, At: Stamp(), Skill: l.Skill,
			SpentUSD: round2(spent), BudgetUSD: l.Budget, SessionUSD: round2(sessionUSD),
		},
	}
}

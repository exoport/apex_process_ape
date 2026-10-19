package apexv1

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
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
	// BaseSHA is the milestone's start (D130 Q60): HEAD at its first
	// approval, what $APE_MILESTONE_BASE holds in a gate ape runs; absent
	// before it is approved.
	BaseSHA string `json:"base_sha,omitempty"`
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
	// ConsecutiveBlocks is the milestone's changes blocked in a row, and
	// ConsecutiveBlocksCap limits.consecutive_blocks (H19).
	ConsecutiveBlocks    int `json:"consecutive_blocks"`
	ConsecutiveBlocksCap int `json:"consecutive_blocks_cap"`
	// Diagnostics are the limits that stop work on the milestone, in the
	// shape of a --json failure's: milestone.blocks-exceeded (action stop)
	// when ConsecutiveBlocks reached its cap (D49). Always present.
	Diagnostics []Diagnostic `json:"diagnostics"`
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
	// AttemptsTotal is every red verify of the change, never reset. All
	// three come from this checkout's _apex/state/attempts.json.
	Attempts      int `json:"attempts"`
	AttemptsTotal int `json:"attempts_total"`
	AttemptsCap   int `json:"attempts_cap,omitempty"`
	// Branch is the change's branch, change/<id>, when it has one (D38).
	Branch string `json:"branch,omitempty"`
	// Lane is "fix" for a change of `milestone: none` (D40), else
	// "milestone". Approved is a fix's approval; a fix without one awaits
	// `ape change approve <id> --by <who>`.
	Lane     string    `json:"lane"`
	Approved *Approval `json:"approved,omitempty"`
	// Flaky lists the checks that passed only on a retry at the change's
	// last verify (H12, retries:): green, but for review to see.
	Flaky []string `json:"flaky,omitempty"`
	// AwaitingHuman is the change's human-review tasks no person has
	// accepted yet once only the person is left (D47): a reviewed change,
	// or an archived one (archived past them by a person,
	// --allow-pending-human). What waits for a person, not for the
	// orchestrator: it skips such a change. Always present.
	AwaitingHuman []string `json:"awaiting_human"`
	// Answer is a person's answer to what blocked the change (H9): set on
	// an answered change, and kept once it is started again.
	Answer *Answer `json:"answer,omitempty"`
	// Tools are the change's design-tool tasks (`— tool: <name>`) and
	// ReviewTools its review_tools: (D59 Q4, D74), each resolved to the
	// skill tools: names ("" when it names none: change check refuses
	// it). The orchestrator reads them here, not in change.md. Always
	// present.
	Tools       []ChangeTool `json:"tools"`
	ReviewTools []ChangeTool `json:"review_tools"`
	// Accepted is the change's human-review acceptances, the base's
	// included (D52 Q18): {task, by, at, evidence}, and for one a delegate
	// recorded (D75) by "delegate:<skill>", its run.
	Accepted []Accept `json:"accepted,omitempty"`
	// Delegate is the newest delegate verdict per human-review task (D75),
	// from this checkout's _apex/state/runs/delegate/: a fail leaves the
	// task awaiting a person, and says why.
	Delegate map[string]DelegateStatus `json:"delegate,omitempty"`
	// Delegable are the awaiting human-review tasks a delegate may take
	// now (D152 Q71), each with why: DelegableConfigured (human_review
	// delegates) or DelegableDocsDefault (the docs default of a docs
	// change). A task whose last delegate verdict is a fail or voided is
	// a person's and not listed. Omitted when none.
	Delegable []DelegableTask `json:"delegable,omitempty"`
}

// DelegableTask is one task `change accept --delegate` may take now.
type DelegableTask struct {
	Task   string `json:"task"`
	Reason string `json:"reason"`
}

// ChangeTool is one tool a change calls for. Task and Done are a design
// tool's: Done is its task green at the change's latest verify of the
// current plan (a human-review task: accepted by a person).
type ChangeTool struct {
	Task  string `json:"task,omitempty"`
	Name  string `json:"name"`
	Skill string `json:"skill"`
	Done  *bool  `json:"done,omitempty"`
	// Args (the unfilled template), Model and Effort are the tools: entry's
	// (D77); empty when it sets none.
	Args   string `json:"args,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// changeTools resolves a change's design and review tools against the
// config's tools: slots.
func changeTools(tools ToolsConfig, c *Change, st *State, lv *LastVerify, pending []string) (design, review []ChangeTool) {
	design, review = []ChangeTool{}, []ChangeTool{}
	if c == nil {
		return design, review
	}
	entry := func(slot, name string) ChangeTool {
		ct := ChangeTool{Name: name}
		if t := tools.Find(slot, name); t != nil {
			ct.Skill, ct.Args, ct.Model, ct.Effort = t.Skill, t.Args, t.Model, t.Effort
		}
		return ct
	}
	for _, t := range c.Tasks {
		if t.Tool == "" {
			continue
		}
		done := taskDone(t, st, lv, pending)
		ct := entry("design", t.Tool)
		ct.Task, ct.Done = t.ID, &done
		design = append(design, ct)
	}
	for _, name := range c.Meta.ReviewTools {
		review = append(review, entry("review", name))
	}
	return design, review
}

// taskDone: a human-review task accepted (not pending), a command task
// green at the latest verify of the current plan.
func taskDone(t Task, st *State, lv *LastVerify, pending []string) bool {
	if t.Human() {
		return st != nil && !slices.Contains(pending, t.ID)
	}
	if lv != nil {
		_, red := lv.Red[t.ID]
		return !red
	}
	if st == nil || st.Verify == nil {
		return false
	}
	for i := range st.Verify.Results {
		if r := &st.Verify.Results[i]; r.Task == t.ID {
			return !r.Red()
		}
	}
	return false
}

// DelegateStatus is a task's newest delegate verdict as `ape status`
// shows it. Voided lists why ape voided it, when it did.
type DelegateStatus struct {
	Verdict string   `json:"verdict"`
	Reason  string   `json:"reason"`
	Run     string   `json:"run"`
	At      string   `json:"at"`
	Voided  []string `json:"voided,omitempty"`
}

// delegateStatuses is change id's newest delegate verdict per task.
func (p *Project) delegateStatuses(id string) map[string]DelegateStatus {
	vs := p.lastDelegateVerdicts(id)
	if len(vs) == 0 {
		return nil
	}
	out := make(map[string]DelegateStatus, len(vs))
	for task, v := range vs {
		out[task] = DelegateStatus{Verdict: v.Verdict, Reason: v.Reason, Run: v.Run, At: v.At, Voided: v.Voided}
	}
	return out
}

// delegateMayTry reports a task a delegate may still try: no verdict
// yet, or a pass not recorded. A fail or a voided run is a person's to
// look at.
func (c *ChangeStatus) delegateMayTry(task string) bool {
	d, ok := c.Delegate[task]
	return !ok || d.Verdict == "pass" && len(d.Voided) == 0
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
	// AcceptRed: a person closed it with its gate red (H22).
	AcceptRed *AcceptRed `json:"accept_red,omitempty"`
	// BaseSHA is the milestone's start (D130 Q60): HEAD at its first
	// approval; absent before it is approved.
	BaseSHA string `json:"base_sha,omitempty"`
	// LastGate is the milestone's newest gate run (D170 A9): where it ran,
	// its exit, when and its log — the record a skill reads instead of
	// ape's state files. Absent before the gate ever ran.
	LastGate *GateSummary `json:"last_gate,omitempty"`
}

// GateSummary is a gate run in short (LastGateRun).
type GateSummary struct {
	SHA  string `json:"sha"`
	Exit int    `json:"exit"`
	At   string `json:"at"`
	Log  string `json:"log"`
}

// StatusReport is `ape status`.
type StatusReport struct {
	Project     string `json:"project"`
	Root        string `json:"root"`
	Initialized bool   `json:"initialized"`
	// Branch is the checked-out branch ("" when detached or outside git);
	// a change branch is change/<id> (D38).
	Branch     string           `json:"branch"`
	Milestone  *MilestoneStatus `json:"milestone"`
	Milestones []MilestoneLine  `json:"milestones"`
	Changes    []ChangeStatus   `json:"changes"`
	Archived   int              `json:"archived"`
	// Fixes are the approved fixes not archived nor blocked, in the order
	// they run (D40): started ones first, then by approval time. The first
	// runs ahead of milestone work, at the loop's next change boundary —
	// or alone, `ape task apex-loop --args <id>`.
	Fixes []string `json:"fixes"`
	// PreExistingRed is what was red at a change's baseline that is not
	// the change's own: the planner's to roadmap (backlog 37).
	PreExistingRed []PreExistingRed `json:"pre_existing_red"`
	Spend          Spend            `json:"spend"`
	Next           string           `json:"next"`
	// NextWhy is next's reason, one line (Q100, OI-7): what in the
	// recorded state makes it the next step, so a reader explains it
	// without re-deriving it. next stays one string.
	NextWhy string `json:"next_why"`
	// RoadmapNext is the roadmap's Next items (H26, D60): findings noticed
	// out of scope, one line each without the list marker, from the base
	// branch's roadmap.md. Always present ([] when none).
	RoadmapNext []string `json:"roadmap_next"`
	// Docs is _apex/config.yaml's docs: block (H21), always present ({}
	// when unset): the changelog and user docs a user-visible change
	// updates, for apex-change and apex-review to read.
	Docs DocsConfig `json:"docs"`
	// BaseUpstream is the base branch (the checked-out branch, or the
	// base of the change whose branch is checked out) against its
	// upstream, from the local remote-tracking ref; null when it has none
	// (H6, D45).
	BaseUpstream *BaseUpstream `json:"base_upstream"`
	// OutsideFlow are the base branch's commits since the last archive
	// squash (else the v1 epoch) that touch code with no `Change:` trailer
	// and no `apex:` subject: work that bypassed the change flow (H7).
	// Shown, never failed.
	OutsideFlow []OutsideFlow `json:"outside_flow"`
	// ArchiveJournal is an archive in flight, or one that crashed (H28):
	// the next writing command rolls a crashed one back. Status only
	// reports it. ArchiveRunning: its process is still alive.
	ArchiveJournal *ArchiveJournal `json:"archive_journal,omitempty"`
	ArchiveRunning bool            `json:"archive_running,omitempty"`
	// LatestRun is the newest ape task run's outcome (H20): a usage-limit
	// stop carries its reset. Absent before the first run.
	LatestRun *LatestRun `json:"latest_run,omitempty"`
	// Sessions are the model sessions ape launched that run in the project
	// now, with their depth and parent run (D62): an apex-loop run at
	// depth 1 and its one child at depth 2. Always present ([] when none).
	Sessions []LiveSession `json:"sessions"`
	// Config holds the effective values skills read from ape rather than
	// restate (D120, Q55): the defaults filled in.
	Config StatusConfig `json:"config"`
	// delegate is human_review's delegate skill, "" when it configures
	// none (D75): next names a delegate first (delegateNext).
	delegate string
}

// StatusConfig is the effective commit types and release scheme (D120).
type StatusConfig struct {
	CommitTypes   []string `json:"commit_types"`
	ReleaseScheme string   `json:"release_scheme"`
	// ReleaseDocs is release.docs, else README.md and docs/ where they
	// exist (D129).
	ReleaseDocs []string `json:"release_docs"`
}

// Status builds the report.
//
//nolint:gocyclo // one section per part of the report, each guarded by what it reads
func (p *Project) Status() *StatusReport {
	r := &StatusReport{Root: p.Root, Initialized: p.IsV1(), Changes: []ChangeStatus{}, Milestones: []MilestoneLine{}, Fixes: []string{}, OutsideFlow: []OutsideFlow{}, RoadmapNext: []string{}}
	cfg, cfgErr := LoadConfig(p.Root)
	if cfgErr == nil && cfg.ProjectName != "" {
		r.Project = cfg.ProjectName
	} else {
		r.Project = baseName(p.Root)
	}
	r.Docs = cfg.Docs
	r.Config = StatusConfig{CommitTypes: cfg.Commit.TypeList(), ReleaseScheme: cfg.Release.SchemeName(), ReleaseDocs: cfg.Release.DocsList(p.Root)}
	if cfg.HumanReview.Delegates() {
		r.delegate = cfg.HumanReview.Skill()
	}
	runs, _ := cost.RunCosts(p.Root)
	for _, run := range runs {
		r.Spend.TotalUSD += run.CostUSD
		r.Spend.Runs++
	}
	r.Spend.TotalUSD = round2(r.Spend.TotalUSD)
	r.Spend.Note = "sum of finished ape task/pipeline run manifests; a run still in progress is not counted yet"
	r.LatestRun = p.LatestRun()
	r.Sessions = p.LiveSessions()

	ctx := context.Background()
	if rm := p.roadmap(); rm != nil {
		recs, _ := p.LoadMilestoneRecords()
		for i := range rm.Milestones {
			line := milestoneLine(&rm.Milestones[i], recs[rm.Milestones[i].ID])
			if run := p.LastGateRun(line.ID); run != nil {
				line.LastGate = &GateSummary{SHA: run.SHA, Exit: run.Exit, At: run.At, Log: run.Log}
			}
			r.Milestones = append(r.Milestones, line)
		}
		if m := rm.Current(); m != nil {
			r.Milestone = p.milestoneStatus(m, runs, p.fixIDs(ctx))
			if rec := recs[m.ID]; rec != nil {
				r.Milestone.BaseSHA = rec.Approval.Base()
			}
		}
	}

	r.Branch = CurrentBranch(ctx, p.Root)
	branches := map[string]bool{}
	if p.isWorkTree(ctx) {
		branches = p.changeBranches(ctx)
	}
	changes := p.Changes()
	attemptsCap := p.limits().AttemptsCap()
	attempts := p.loadAttempts()
	r.PreExistingRed = p.preExistingRed(changes)
	for _, c := range changes {
		cs := ChangeStatus{ID: c.ID, Lane: LaneMilestone}
		if c.Doc != nil {
			cs.Title, cs.Milestone = c.Doc.Title, c.Doc.Meta.Milestone
			if c.Doc.IsFix() {
				cs.Lane = LaneFix
			}
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
		if cs.Lane == LaneFix {
			cs.Approved = c.State.Approved
		}
		if branches[c.ID] {
			cs.Branch = ChangeBranch(c.ID)
		}
		if c.State.Status != StatusDraft {
			a := attempts[c.ID]
			cs.Attempts, cs.AttemptsTotal = a.Since, a.Total
			if c.State.Active() || a.Since > 0 {
				cs.AttemptsCap = attemptsCap
			}
		}
		if c.State.BlockedReason != nil {
			cs.BlockedReason = *c.State.BlockedReason
		}
		cs.Answer = c.State.Answer
		var pendingHuman []string
		if c.Doc != nil {
			// Acceptances recorded on the base count (D52 Q18).
			acc := p.withBaseAccepted(ctx, c.ID, c.State)
			cs.PendingHuman = acc.PendingHuman(c.Doc.HumanTasks())
			cs.AwaitingHuman = awaitingHuman(acc, c.Doc)
			pendingHuman = cs.PendingHuman
			cs.Accepted = acc.Accepted
			cs.Delegate = p.delegateStatuses(c.ID)
			if cfgErr == nil {
				cs.Delegable = p.delegableTasks(ctx, cfg, c, &cs)
			}
		}
		cs.Tools, cs.ReviewTools = changeTools(cfg.Tools, c.Doc, c.State, p.currentLastVerify(c.ID, c.State), pendingHuman)
		if v := c.State.Verify; v != nil {
			cs.VerifySHA = v.SHA
			for i := range v.Results {
				if v.Results[i].Red() {
					cs.Failing = append(cs.Failing, fmt.Sprintf("%s (%s)", v.Results[i].Task, v.Results[i].Outcome()))
				}
			}
		}
		// A red verify of an in-progress change is not in state.json (D5):
		// the checkout's last-verify record has it, vacuous and unrun
		// checks included (H11).
		if lv := p.currentLastVerify(c.ID, c.State); lv != nil {
			cs.Failing, cs.Flaky = lv.Failing, lv.Flaky
		} else if v := c.State.Verify; v != nil {
			for i := range v.Results {
				if v.Results[i].Flaky {
					cs.Flaky = append(cs.Flaky, v.Results[i].Task)
				}
			}
		}
		r.Changes = append(r.Changes, cs)
	}
	sortFixesFirst(r.Changes)
	// Archived changes a person has yet to accept human-review tasks of
	// (H4): listed last, so they stay visible after the archive.
	r.Changes = append(r.Changes, p.archivedAwaitingHuman()...)
	for i := range r.Changes {
		if r.Changes[i].AwaitingHuman == nil {
			r.Changes[i].AwaitingHuman = []string{}
		}
		if r.Changes[i].Tools == nil {
			r.Changes[i].Tools, r.Changes[i].ReviewTools = changeTools(cfg.Tools, nil, nil, nil, nil)
		}
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; c.runnableFix() {
			r.Fixes = append(r.Fixes, c.ID)
		}
	}
	r.Archived = len(p.ArchivedIDs())
	base := p.statusBase(r.Branch)
	r.BaseUpstream = p.upstreamOf(ctx, base)
	r.OutsideFlow = p.outsideFlow(ctx, base)
	r.RoadmapNext = p.roadmapNext(ctx, base)
	if j := p.LoadArchiveJournal(); j != nil {
		r.ArchiveJournal, r.ArchiveRunning = j, j.Running()
	}
	r.Next, r.NextWhy = nextStepWhy(r)
	return r
}

// statusBase is the base branch status measures: the checked-out branch,
// or, on a change's branch, the base it records; "" when detached.
func (p *Project) statusBase(branch string) string {
	id := ChangeOfBranch(branch)
	if id == "" {
		return branch
	}
	if st, err := LoadState(p.StateFile(id)); err == nil {
		return st.BaseBranch
	}
	return ""
}

// runnableFix reports an approved fix that is neither blocked nor
// unreadable: one the orchestrator runs ahead of milestone work (D40).
func (c *ChangeStatus) runnableFix() bool {
	return c.Lane == LaneFix && c.Approved != nil && c.Status != StatusBlocked && c.Status != "invalid" && c.Status != StatusArchived
}

// sortFixesFirst orders the changes as `ape status` lists them (D40):
// approved fixes first — started ones before drafts, then by approval
// time — then milestone changes, then fixes awaiting approval; by id
// within each group.
func sortFixesFirst(cs []ChangeStatus) {
	group := func(c *ChangeStatus) int {
		switch {
		case c.Lane == LaneFix && c.Approved != nil:
			return 0
		case c.Lane == LaneFix:
			return 2
		}
		return 1
	}
	rank := func(c *ChangeStatus) int {
		switch c.Status {
		case StatusReviewed, StatusVerified, StatusInProgress:
			return 0
		case StatusDraft:
			return 1
		}
		return 2
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := &cs[i], &cs[j]
		if ga, gb := group(a), group(b); ga != gb {
			return ga < gb
		}
		if group(a) == 0 {
			if ra, rb := rank(a), rank(b); ra != rb {
				return ra < rb
			}
			if a.Approved.At != b.Approved.At {
				return a.Approved.At < b.Approved.At
			}
		}
		return a.ID < b.ID
	})
}

// milestoneStatus is the current milestone with its last gate run and
// the spend of runs started since its approval.
func (p *Project) milestoneStatus(m *Milestone, runs []cost.RunCost, fixes map[string]bool) *MilestoneStatus {
	ms := &MilestoneStatus{ID: m.ID, Title: m.Title, Status: m.Status, StatusText: m.StatusText, Gate: m.Gate, Budget: m.Budget}
	ms.GateLast = p.LastGateRun(m.ID)
	ms.Stops = p.milestoneStops(m.ID)
	if ms.Stops == nil {
		ms.Stops = []LimitStop{}
	}
	ms.SpentUSD = round2(milestoneSpend(m, runs, ms.Stops, fixes))
	ms.BudgetEnforced = p.limits().BudgetEnforced() && m.Budget != nil
	ctx := context.Background()
	ms.ConsecutiveBlocks, ms.ConsecutiveBlocksCap = p.consecutiveBlocks(ctx, m.ID), p.limits().ConsecutiveBlocksCap()
	ms.Diagnostics = Diagnostics(p.blocksExceeded(ctx, m.ID))
	if ms.Diagnostics == nil {
		ms.Diagnostics = []Diagnostic{}
	}
	return ms
}

// milestoneLine is m's line in the report: its start once approved
// (D130), and an accept-red close.
func milestoneLine(m *Milestone, rec *MilestoneRecord) MilestoneLine {
	line := MilestoneLine{ID: m.ID, Title: m.Title, Status: m.Status}
	if rec != nil {
		line.BaseSHA = rec.Approval.Base()
		if m.Status == MilestoneDone {
			line.AcceptRed = rec.AcceptRed
		}
	}
	return line
}

// nextStep is a deterministic hint: what the way of working says comes
// next, given only the recorded state.
func nextStep(r *StatusReport) string {
	next, _ := nextStepWhy(r)
	return next
}

// nextStepWhy is nextStep with its reason, one line (Q100).
//
//nolint:gocyclo // one branch per state the way of working names
func nextStepWhy(r *StatusReport) (next, why string) {
	if !r.Initialized {
		return "run `ape project init`", "this directory has no APEX v1 layout yet (no _apex/state/)"
	}
	// An approved fix runs ahead of milestone work (D40).
	for i := range r.Changes {
		if c := &r.Changes[i]; c.runnableFix() {
			return fixNext(c, r.Branch), "an approved fix runs ahead of milestone work (D40), one at a time, in approval order"
		}
	}
	m := r.Milestone
	if m == nil {
		if len(r.Milestones) == 0 {
			return "plan: no milestone yet (apex-plan, then `ape milestone new`)", "the roadmap has no milestone, so nothing is planned to run"
		}
		return "every milestone is done — propose the next one (apex-plan)", "every milestone in the roadmap is done"
	}
	if m.Status != MilestoneApproved {
		return fmt.Sprintf("%s awaits approval: a person runs `ape milestone approve %s --by <who>`", m.ID, m.ID),
			"nothing of " + m.ID + " runs until a person approves its plan and its red gate"
	}
	if len(m.Diagnostics) > 0 {
		return "stopped: " + m.Diagnostics[0].Message + " — " + m.Diagnostics[0].Fix,
			"a limit stopped " + m.ID + "'s run (" + m.Diagnostics[0].Code + "), and only a person resets it"
	}
	for i := range r.Changes {
		c := &r.Changes[i]
		if c.Milestone != m.ID && c.Milestone != "" {
			continue
		}
		// A started change is worked on its branch (D38).
		on := ""
		if c.Branch != "" && r.Branch != c.Branch {
			on = " — on " + c.Branch + " (`ape change start " + c.ID + "` checks it out)"
		}
		switch c.Status {
		case StatusReviewed:
			if len(c.AwaitingHuman) > 0 {
				if next := r.delegateNext(c); next != "" {
					return next, c.ID + "'s human-review tasks may go to a delegate (D75, D152) before a person is asked"
				}
				continue // a person's to accept first (D45): the loop moves on
			}
			return "archive " + c.ID + ": `ape change archive " + c.ID + "` (squash-merges " + ChangeBranch(c.ID) + " into its base branch)",
				c.ID + " passed its review and every human-review task is accepted, so it can land"
		case StatusVerified:
			return "review " + c.ID + " (apex-review)" + on, c.ID + "'s checks are green, and a review pass comes before the archive"
		case StatusInProgress:
			return "apply " + c.ID + " until `ape change verify " + c.ID + "` is green (apex-apply)" + on, c.ID + " is started and its checks are not green yet"
		case StatusDraft:
			return "apply " + c.ID + ": `ape change check " + c.ID + "`, then `ape change start " + c.ID + "` (apex-apply)", c.ID + " is planned but not started"
		}
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; c.Status == StatusAnswered && c.Answer != nil {
			return "answered: " + c.ID + " — " + c.Answer.By + " answered; re-plan it with the answer as the hint (apex-change), then `ape change start " + c.ID + "`",
				"a person answered " + c.ID + "'s block, and the answer changes its plan before it resumes"
		}
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; c.Status == StatusBlocked {
			return "blocked: " + c.ID + " — " + c.BlockedReason + " (re-plan, or a person answers: `ape change answer " + c.ID + " --text \"…\" --by <who>`; `ape change start " + c.ID + "` resumes)",
				"no other change of " + m.ID + " can go on, and " + c.ID + " is blocked"
		}
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; c.Status == StatusReviewed && len(c.AwaitingHuman) > 0 {
			return "a person reviews " + c.ID + ": `ape change accept " + c.ID + " --task " + c.AwaitingHuman[0] + " --by <who>` for each of " +
					strings.Join(c.AwaitingHuman, ", ") + ", then `ape change archive " + c.ID + "`",
				"what is left of " + m.ID + " waits for a person's human-review acceptance of " + c.ID
		}
	}
	if m.GateLast != nil && m.GateLast.Green() && m.GateLast.SHA != "" {
		return fmt.Sprintf("%s gate was green at %s — `ape milestone close %s`", m.ID, short(m.GateLast.SHA), m.ID),
			m.ID + "'s last gate run was green and no change of it is open"
	}
	return fmt.Sprintf("plan the next change toward %s (apex-change), or `ape milestone gate %s` if the goal looks met", m.ID, m.ID),
		m.ID + " is approved and no change of it is open"
}

// delegateNext is the next step for a reviewed change awaiting a person
// when a delegate may take one of its tasks (D75, D152): its first
// delegable task; "" when there is none.
func (r *StatusReport) delegateNext(c *ChangeStatus) string {
	if len(c.Delegable) == 0 {
		return ""
	}
	d, skill := c.Delegable[0], r.delegate
	if d.Reason == DelegableDocsDefault {
		skill = DefaultDelegateSkill
	}
	return "delegate " + d.Task + " of " + c.ID + " to " + skill + ": `ape change accept " + c.ID + " " + d.Task + " --delegate --wait 9m`"
}

// delegableTasks is cs's awaiting tasks a delegate may take now (D152
// Q71): every one the configured delegate has not failed, or, without
// one, a docs change's under the docs default when its diff stays in the
// docs (Config.DocsDefaultScope) and the development folder (prepareDocsDelegate's rule;
// the config guard is --delegate's to apply).
func (p *Project) delegableTasks(ctx context.Context, cfg Config, c ChangeInfo, cs *ChangeStatus) []DelegableTask {
	hr := cfg.HumanReview
	reason := DelegableConfigured
	if !hr.Delegates() {
		if c.Doc == nil || !c.Doc.Meta.Docs || !hr.DocsDefault() || len(cs.AwaitingHuman) == 0 {
			return nil
		}
		if outside, err := p.docsScopeOutside(ctx, cfg.DocsDefaultScope(), c.ID, c.State); err != nil || len(outside) > 0 {
			return nil
		}
		reason = DelegableDocsDefault
	}
	var out []DelegableTask
	for _, task := range cs.AwaitingHuman {
		if cs.delegateMayTry(task) {
			out = append(out, DelegableTask{Task: task, Reason: reason})
		}
	}
	return out
}

// fixNext is the next step for an approved fix: it runs ahead of
// milestone work, at the orchestrator's next change boundary (never in
// parallel), or alone when no milestone run is active.
func fixNext(c *ChangeStatus, branch string) string {
	what := "fix " + c.ID + " (approved by " + c.Approved.By + ", " + c.Status
	if c.Branch != "" && branch != c.Branch {
		what += ", on " + c.Branch
	}
	return what + ") runs ahead of milestone work: the loop takes it at its next change boundary; " +
		"with no milestone run active, `ape task " + OrchestratorSkill + " --args " + c.ID + "` runs it alone"
}

// briefReasonMax caps a blocked reason in the brief, so a long question
// does not push the rest past 25 lines on a narrow terminal.
const briefReasonMax = 90

// briefLineMax caps the brief's red-at-baseline line.
const briefLineMax = 160

// Brief renders the report in at most 25 lines, for the SessionStart hook.
//
//nolint:gocyclo // one line per part of the report, each shown only when it has something to say
func (r *StatusReport) Brief() string {
	var b []string
	title := "APEX status — " + r.Project
	if r.Branch != "" {
		title += " (on " + r.Branch + ")"
	}
	// On the title line, so the brief keeps its 25-line cap.
	if j := r.ArchiveJournal; j != nil {
		if r.ArchiveRunning {
			title += fmt.Sprintf(" — archive of %s in progress (pid %d, %s step)", j.ID, j.PID, j.Step)
		} else {
			title += fmt.Sprintf(" — archive of %s INTERRUPTED at its %s step: the next ape command that writes rolls it back (archive.interrupted), then archive again", j.ID, j.Step)
		}
	}
	b = append(b, title)
	if !r.Initialized {
		b = append(b, "not an APEX v1 project yet: run `ape project init`")
		return strings.Join(b, "\n") + "\n"
	}
	if line := r.acceptedRedLine(); line != "" {
		b = append(b, line)
	}
	if m := r.Milestone; m != nil {
		b = append(b, m.briefLines()...)
	} else if len(r.Milestones) == 0 {
		b = append(b, "No milestones yet.")
	} else {
		b = append(b, "All milestones done.")
	}
	const maxChanges = 12
	var open, blocked, archived []ChangeStatus
	for i := range r.Changes {
		switch r.Changes[i].Status {
		case StatusBlocked:
			blocked = append(blocked, r.Changes[i])
		case StatusArchived:
			archived = append(archived, r.Changes[i])
		default:
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
	for gi, group := range [][]ChangeStatus{open, blocked, archived} {
		switch {
		case gi == 1 && len(blocked) > 0:
			b = append(b, "Blocked:")
		case gi == 2 && len(archived) > 0:
			b = append(b, "Archived, human review not accepted (`ape change accept <id> --task <T> --by <who>`):")
		}
		for i := range group {
			c := &group[i]
			if shown == maxChanges {
				b = append(b, fmt.Sprintf("  … %d more (`ape status --json`)", len(r.Changes)-maxChanges))
				break groups
			}
			shown++
			line := fmt.Sprintf("  %-28s %-11s %s", c.ID, c.Status, c.laneLabel())
			switch {
			case c.Status == StatusAnswered && c.Answer != nil:
				line += " — answered by " + c.Answer.By + ": " + truncate(c.Answer.Text, briefReasonMax)
			case c.BlockedReason != "":
				line += " — " + truncate(c.BlockedReason, briefReasonMax)
			case len(c.Failing) > 0 && c.Status == StatusInProgress:
				line += " — red: " + strings.Join(c.Failing, ", ")
			}
			if len(c.PendingHuman) > 0 {
				var pending strings.Builder
				pending.WriteString(" — human-review pending: " + strings.Join(c.PendingHuman, ","))
				for _, task := range c.PendingHuman {
					if d, ok := c.Delegate[task]; ok && (d.Verdict == "fail" || len(d.Voided) > 0) {
						pending.WriteString(" (" + task + " " + d.describe() + ")")
					}
				}
				line += pending.String()
			}
			if len(c.Flaky) > 0 {
				line += " — flaky: " + strings.Join(c.Flaky, ", ")
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
	if u := r.BaseUpstream; u != nil && u.Behind > 0 {
		b = append(b, truncate("Base: "+u.Describe(), briefLineMax))
	}
	b = append(b, fmt.Sprintf("Spend: $%.2f over %d runs", r.Spend.TotalUSD, r.Spend.Runs), "Next: "+r.Next)
	return strings.Join(b, "\n") + "\n"
}

// describe is a delegate verdict in a few words for the brief.
func (d DelegateStatus) describe() string {
	if len(d.Voided) > 0 {
		return "delegate voided: " + truncate(strings.Join(d.Voided, ", "), briefReasonMax)
	}
	return "delegate " + d.Verdict + ": " + truncate(d.Reason, briefReasonMax)
}

// acceptedRedLine names the last done milestone when a person closed it
// with its gate red (H22), "" otherwise.
func (r *StatusReport) acceptedRedLine() string {
	for i := len(r.Milestones) - 1; i >= 0; i-- {
		m := &r.Milestones[i]
		if m.Status != MilestoneDone {
			continue
		}
		if a := m.AcceptRed; a != nil {
			why := ""
			if a.Gate != nil {
				why = " (" + a.Gate.Why() + ")"
			}
			return truncate(m.ID+" was closed with its gate red"+why+", accepted by "+a.By+": "+a.Reason, briefLineMax)
		}
		return ""
	}
	return ""
}

// laneLabel is the brief's third column: the milestone, or the fix lane
// and its approval (D40).
func (c *ChangeStatus) laneLabel() string {
	switch {
	case c.Lane != LaneFix:
		return c.Milestone
	case c.Approved != nil:
		return "fix (approved by " + c.Approved.By + ")"
	case c.Status == StatusDraft:
		return "fix — awaiting approval: `ape change approve " + c.ID + " --by <who>`"
	}
	return "fix"
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
	if m.ConsecutiveBlocks > 0 {
		lines = append(lines, fmt.Sprintf("  %d of %d changes blocked in a row (limits.consecutive_blocks)", m.ConsecutiveBlocks, m.ConsecutiveBlocksCap))
	}
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

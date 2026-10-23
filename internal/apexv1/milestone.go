package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GateRun is one run of a milestone gate.
type GateRun struct {
	Exit         int      `json:"exit"`
	At           string   `json:"at"`
	SHA          string   `json:"sha"`
	Secs         float64  `json:"secs"`
	OutputSHA256 string   `json:"output_sha256"`
	Cmd          string   `json:"cmd"`
	Log          string   `json:"log,omitempty"`
	Tail         []string `json:"tail,omitempty"`
	// SpecChecks is how many scenario checks of the whole spec ran beside
	// the gate command — the milestone's regression suite (backlog item 2)
	// — and SpecFailed the red ones.
	SpecChecks int           `json:"spec_checks"`
	SpecFailed []CheckResult `json:"spec_failed,omitempty"`
	// StrayFiles: what the gate and its checks left in the tree outside
	// the development folder and ape undid (item 19).
	StrayFiles []string `json:"stray_files,omitempty"`
	// StraysKept: where a copy of each is kept (Q144).
	StraysKept []string `json:"strays_kept,omitempty"`
	// Regression judges the regression command against the milestone's
	// baseline (backlog 32); absent when none is configured.
	Regression *RegressionVerdict `json:"regression,omitempty"`
	// Reason and Detail: the gate command was vacuous or unrun (H11).
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// ShellNote: the gate command exited 127 on a bash/zsh builtin (RN-1).
	ShellNote string `json:"shell_note,omitempty"`
	// Flaky lists what passed only on a retry (H12): the gate command
	// ("gate-<M>"), scenarios, the regression command.
	Flaky []string `json:"flaky,omitempty"`
	// LiveSkipped counts the live scenarios not run because the Gate:
	// command was red (W2 Part 2); Regression is then skipped "gate-red".
	// A close runs them all.
	LiveSkipped int `json:"live_skipped,omitempty"`
	// ScenarioSecs is each scenario check's run time, by ref (W2): what
	// project check's test-cost notices read. Kept only in the gitignored
	// logs/gate-<M>.json (gateTimings); never in milestones.json, the
	// gate's output or ape status.
	ScenarioSecs map[string]float64 `json:"scenario_secs,omitempty"`
}

// Green reports a passing gate: its command exited 0, every scenario
// check of the spec passed, and the regression command is not newly red.
func (r *GateRun) Green() bool {
	return r.Exit == 0 && len(r.SpecFailed) == 0 && (r.Regression == nil || !r.Regression.Failed)
}

// Why describes a red gate in one phrase.
func (r *GateRun) Why() string {
	var parts []string
	switch {
	case r.Reason != "":
		parts = append(parts, "gate command "+r.Reason+": "+r.Detail)
	case r.Exit != 0:
		parts = append(parts, "gate command exit "+strconv.Itoa(r.Exit))
	}
	if n := len(r.SpecFailed); n > 0 {
		names := make([]string, 0, n)
		for i := range r.SpecFailed {
			name := r.SpecFailed[i].Task
			if reason := r.SpecFailed[i].Reason; reason != "" {
				name += " (" + reason + ")"
			}
			if len(r.SpecFailed[i].StaleNames) > 0 {
				name += " (checks what ape decision standard removed: retire it, never re-supersede ADR-0001)"
			}
			names = append(names, name)
		}
		parts = append(parts, strconv.Itoa(n)+" spec scenario(s) red: "+strings.Join(limit(names, 5), ", "))
	}
	if g := r.Regression; g != nil && g.Failed {
		switch {
		case g.Reason != "":
			parts = append(parts, "regression command "+g.Reason+": "+g.Detail)
		case g.BaselineExit == nil:
			parts = append(parts, "regression command exit "+strconv.Itoa(g.Exit)+", and no change of the milestone has a baseline for it")
		case *g.BaselineExit != 0:
			parts = append(parts, "regression command exit "+strconv.Itoa(g.Exit)+", red at baseline ("+g.BaselineChange+") too, but with "+
				strconv.Itoa(g.NewFailuresTotal)+" failing test(s) the baseline did not have: "+strings.Join(limit(g.NewFailures, 5), ", "))
		default:
			parts = append(parts, "regression command exit "+strconv.Itoa(g.Exit)+", green at baseline ("+g.BaselineChange+")")
		}
	}
	return strings.Join(parts, "; ")
}

// Gate results as milestones.json records them.
const (
	GateGreen = "green"
	GateRed   = "red"
)

// MilestoneRecord is ape's committed record of one milestone's gate: its
// result and the run that produced it. It changes only when the result
// changes, so a gate re-run with the same result leaves the tree clean
// (D11); the latest run, whatever its result, is in the gitignored
// logs/gate-<M>.json.
type MilestoneRecord struct {
	Result string   `json:"result,omitempty"`
	Since  *GateRun `json:"since,omitempty"`
	// Approval is a person's approval of the milestone and its gate (H2):
	// what change start, gate and close trust, never the Status line alone.
	Approval *MilestoneApproval `json:"approval,omitempty"`
	// DoneAt is when `ape milestone close` wrote the milestone done.
	DoneAt string `json:"done_at,omitempty"`
	// AcceptRed is a person's close of the milestone with its gate red
	// (H22): `ape milestone close --accept-red`.
	AcceptRed *AcceptRed `json:"accept_red,omitempty"`
	// ConsecutiveBlocks counts the milestone's changes blocked since one
	// of them last archived, or a person last approved it (H19).
	ConsecutiveBlocks int `json:"consecutive_blocks,omitempty"`
	// Stops are the sessions ape stopped at the milestone's enforced
	// budget (backlog 30).
	Stops []LimitStop `json:"stops,omitempty"`
	// GateArchived are the gate folders close moved to the archive (D155
	// Q74), one entry per folder: Approval.GateFiles keep the paths as
	// approved, and ArchivedPath maps one to where it is now.
	GateArchived []GateArchive `json:"gate_archived,omitempty"`
}

// MilestoneRecords is _apex/state/milestones.json.
type MilestoneRecords map[string]*MilestoneRecord

// LoadMilestoneRecords reads milestones.json; absent is empty.
func (p *Project) LoadMilestoneRecords() (MilestoneRecords, error) {
	out := MilestoneRecords{}
	data, err := os.ReadFile(p.MilestonesJSON())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Project) saveMilestoneRecords(r MilestoneRecords) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p.ensureState()
	return writeFileAtomic(p.MilestonesJSON(), string(data)+"\n")
}

func (p *Project) lastGatePath(id string) string {
	return filepath.Join(p.LogsDir(), "gate-"+id+".json")
}

// LastGateRun is the milestone's most recent gate run: the gitignored
// latest-run record, else the committed one.
func (p *Project) LastGateRun(id string) *GateRun {
	if run := p.lastGateRecord(id); run != nil {
		run.ScenarioSecs = nil
		return run
	}
	if recs, err := p.LoadMilestoneRecords(); err == nil && recs[id] != nil {
		return recs[id].Since
	}
	return nil
}

// lastGateRecord is logs/gate-<M>.json as written, ScenarioSecs
// included; nil when absent or unreadable.
func (p *Project) lastGateRecord(id string) *GateRun {
	data, err := os.ReadFile(p.lastGatePath(id))
	if err != nil {
		return nil
	}
	var run GateRun
	if json.Unmarshal(data, &run) != nil {
		return nil
	}
	return &run
}

// latestGate is the newest gate run of any milestone of the roadmap that
// recorded its scenario timings (W2), nil when none did: "the last gate"
// of project check's test-cost notices.
func (p *Project) latestGate() (string, *GateRun) {
	rm := p.roadmap()
	if rm == nil {
		return "", nil
	}
	var id string
	var best *GateRun
	for i := range rm.Milestones {
		run := p.lastGateRecord(rm.Milestones[i].ID)
		if run == nil || run.ScenarioSecs == nil {
			continue
		}
		if best == nil || run.At > best.At {
			id, best = rm.Milestones[i].ID, run
		}
	}
	return id, best
}

// milestonePaths are the files a milestone commit records.
func (p *Project) milestonePaths() []string {
	paths := []string{p.Rel(p.RoadmapMD())}
	if fileExists(p.MilestonesJSON()) {
		paths = append(paths, p.Rel(p.MilestonesJSON()))
	}
	return paths
}

// milestoneOwn is the milestone files as they are before ape edits them
// (R28): a refused milestone commit undoes the edit.
func (p *Project) milestoneOwn(ctx context.Context) ownEdits {
	return p.beforeOwnEdit(ctx, p.Rel(p.RoadmapMD()), p.Rel(p.MilestonesJSON()))
}

// commitMilestone commits the milestone files and extra; own (nil: none)
// is milestoneOwn from before ape's edit.
func (p *Project) commitMilestone(ctx context.Context, own ownEdits, id, what string, extra ...string) (string, error) {
	sha, err := p.commitOwnRecord(ctx, own, "apex: "+id+" "+what, "Milestone: "+id, append(p.milestonePaths(), extra...)...)
	if err != nil {
		return "", commitFailed(err, "roadmap written but its commit failed: "+err.Error()+" — fix git (identity, lock) and commit "+strings.Join(p.milestonePaths(), " "))
	}
	return sha, nil
}

func (p *Project) loadRoadmapDoc() (string, *Roadmap, error) {
	doc, err := readFileString(p.RoadmapMD())
	if err != nil {
		return "", nil, &FailError{Code: "roadmap.missing", Msg: "no " + p.Rel(p.RoadmapMD()) + " — run `ape project init`"}
	}
	return doc, ParseRoadmap(p.Rel(p.RoadmapMD()), doc), nil
}

// NewMilestone appends a milestone to roadmap.md and returns its id.
func (p *Project) NewMilestone(ctx context.Context, title, gate string, budget *float64, goal string) (string, error) {
	if err := p.recoverArchive(ctx); err != nil {
		return "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", &UsageError{Code: "usage.title", Msg: "a milestone needs a title"}
	}
	if strings.TrimSpace(gate) == "" {
		return "", &UsageError{Code: "usage.gate", Msg: "--gate is required: one command that is red until the milestone is done"}
	}
	if strings.Contains(gate, "\n") || strings.Contains(title, "\n") {
		return "", &UsageError{Code: "usage.one-line", Msg: "title and gate are one line each"}
	}
	if err := p.refuseOnChangeBranch(ctx, "a new milestone"); err != nil {
		return "", err
	}
	doc, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return "", err
	}
	id := rm.NextID()
	own := p.milestoneOwn(ctx)
	if err := writeFileAtomic(p.RoadmapMD(), AppendMilestone(doc, id, title, gate, budget, goal, nil)); err != nil {
		return "", err
	}
	if _, err := p.commitMilestone(ctx, own, id, MilestoneProposed); err != nil {
		return id, err
	}
	return id, nil
}

// ApproveMilestone writes `Status: approved (by <who> <ts>)` and records
// the approval in milestones.json (H2): who, when, and the gate approved
// — its command and its files' content. Both are committed together. On
// an approved milestone it records the approval again when ape holds none,
// the gate changed since, or changes of it were blocked in a row (the
// count goes back to 0; the Status line is kept); otherwise it is a no-op.
// Before it records anything it runs the gate once (D70 B,
// gateSelfCheck), printing its progress to progress (nil: nowhere): a
// green or broken gate is refused, a red one's run is recorded with the
// approval.
func (p *Project) ApproveMilestone(ctx context.Context, id, by string, progress io.Writer) (*Milestone, error) {
	m, _, err := p.approveMilestone(ctx, id, by, "", progress)
	return m, err
}

// ApproveMilestoneOutcome is ApproveMilestone — with acceptGreen set,
// ApproveMilestoneAcceptGreen with its reason — that also reports whether
// it recorded an approval: false is the no-op on an approved milestone
// whose record holds its gate as it is (v1.2 F4: approve --json
// "approval": "unchanged").
func (p *Project) ApproveMilestoneOutcome(ctx context.Context, id, by string, acceptGreen *string, progress io.Writer) (*Milestone, bool, error) {
	if acceptGreen == nil {
		return p.approveMilestone(ctx, id, by, "", progress)
	}
	reason := strings.TrimSpace(*acceptGreen)
	if reason == "" || strings.Contains(reason, "\n") {
		return nil, false, &UsageError{Code: "usage.reason", Msg: "--accept-green needs a one-line reason: why the gate is green before this re-approval (the milestone's work is done and the gate was fixed after it)"}
	}
	return p.approveMilestone(ctx, id, by, reason, progress)
}

// ApproveMilestoneAcceptGreen is a re-approval by a person who states
// that the milestone's work is done and its gate was fixed after it
// (RK-5, Q145): a green self-check is then recorded, with the reason
// (approval.accept_green), not refused. On a first approval it is a usage
// error — a new gate must be red — and on a red re-approval the reason is
// not needed and not recorded.
func (p *Project) ApproveMilestoneAcceptGreen(ctx context.Context, id, by, reason string, progress io.Writer) (*Milestone, error) {
	m, _, err := p.ApproveMilestoneOutcome(ctx, id, by, &reason, progress)
	return m, err
}

func (p *Project) approveMilestone(ctx context.Context, id, by, acceptGreen string, progress io.Writer) (*Milestone, bool, error) {
	// A model never approves its own milestone (D47 Q6).
	if err := requirePerson(ctx, ActMilestoneApprove, "ape milestone approve"); err != nil {
		return nil, false, err
	}
	if err := p.recoverArchive(ctx); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(by) == "" {
		return nil, false, &UsageError{Code: "usage.by", Msg: "--by names who approves"}
	}
	if err := p.refuseOnChangeBranch(ctx, "a milestone approval"); err != nil {
		return nil, false, err
	}
	doc, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return nil, false, err
	}
	m := rm.Find(id)
	if m == nil {
		return nil, false, &FailError{Code: CodeMilestoneNotFound, Msg: "no milestone " + id + " in roadmap.md"}
	}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil, false, err
	}
	rec := recs[id]
	switch m.Status {
	case MilestoneDone:
		return nil, false, &FailError{Code: "milestone.done", Msg: id + " is already done"}
	case MilestoneApproved:
		// Re-approval: only when ape holds no record of the approval, the
		// gate changed since it, or it runs a script the record does not
		// hold (created since: not drift, but approving freezes it).
		if rec != nil && rec.Approval != nil && len(p.gateDrift(m, rec.Approval)) == 0 && !p.gateScriptUnrecorded(m, rec.Approval) && rec.ConsecutiveBlocks == 0 {
			return m, false, nil
		}
	}
	if fs := ValidateRoadmap(p.Rel(p.RoadmapMD()), doc); len(filterMilestone(fs, m)) > 0 {
		return nil, false, &FailError{Code: "milestone.malformed", Msg: id + " is not well-formed — fix roadmap.md before approving", Findings: filterMilestone(fs, m)}
	}
	// The milestone's start (D130 Q60): HEAD now, at its first approval;
	// a re-approval keeps the first one, as the Status line does.
	base := HeadSHA(ctx, p.Root)
	if rec != nil && rec.Approval.Base() != "" {
		base = rec.Approval.Base()
	}
	reapproval := m.Status == MilestoneApproved
	if acceptGreen != "" && !reapproval {
		return nil, false, &UsageError{Code: "usage.accept-green", Msg: "--accept-green is for a re-approval of an approved milestone: " + id + " is " + m.StatusText + ", and a new gate must be red until the milestone's work is done"}
	}
	selfCheck, err := p.gateSelfCheck(ctx, m, base, reapproval, acceptGreen != "", progress)
	if err != nil {
		return nil, false, err
	}
	what := MilestoneApproved
	own := p.milestoneOwn(ctx)
	if m.Status == MilestoneApproved {
		// The Status line keeps the first approval: the milestone's spend
		// counts from it, and a re-approval must not reset that.
		what = "re-approved"
	} else {
		updated, err := rm.WithStatus(id, "approved (by "+strings.TrimSpace(by)+" "+Stamp()+")")
		if err != nil {
			return nil, false, err
		}
		if err := writeFileAtomic(p.RoadmapMD(), updated); err != nil {
			return nil, false, err
		}
		m = ParseRoadmap("", updated).Find(id)
	}
	if rec == nil {
		rec = &MilestoneRecord{}
		recs[id] = rec
	}
	rec.Approval = p.newApproval(m, strings.TrimSpace(by))
	rec.Approval.SelfCheck = selfCheck
	rec.Approval.BaseSHA = base
	if selfCheck.Exit == 0 {
		// Q145: only a re-approval with --accept-green gets here green.
		rec.Approval.AcceptGreen = &AcceptGreen{Reason: acceptGreen, By: rec.Approval.By, At: rec.Approval.At}
		what = "re-approved (gate green: " + acceptGreen + ")"
	}
	// A person's approval resets the run of blocked changes (H19).
	rec.ConsecutiveBlocks = 0
	if err := p.saveMilestoneRecords(recs); err != nil {
		return nil, false, err
	}
	if _, err := p.commitMilestone(ctx, own, id, what); err != nil {
		return m, true, err
	}
	return m, true, nil
}

// filterMilestone keeps the findings that name milestone m. Status
// findings are dropped: approving is what fixes them.
func filterMilestone(fs []Finding, m *Milestone) []Finding {
	var out []Finding
	for _, f := range fs {
		if strings.HasPrefix(f.Message, m.ID+" ") && !strings.Contains(f.Message, "status") && !strings.Contains(f.Message, "Status") {
			out = append(out, f)
		}
	}
	return out
}

// GateResult is what `ape milestone gate|close` reports.
type GateResult struct {
	Milestone string   `json:"milestone"`
	Run       GateRun  `json:"run"`
	Status    string   `json:"status"`
	InFlight  []string `json:"in_flight,omitempty"`
	// ResultChanged reports that this run flipped the recorded result
	// (or recorded the first one), and so was committed.
	ResultChanged bool   `json:"result_changed"`
	Commit        string `json:"commit,omitempty"`
	// Evidence is the folder a close wrote the gate's evidence to
	// (evidence: archive, H23).
	Evidence string `json:"evidence,omitempty"`
	// checks are every check the run ran, the gate command first: what
	// the evidence is written from.
	checks []CheckResult
	// AcceptRed: a person closed the milestone with its gate red.
	AcceptRed *AcceptRed `json:"accept_red,omitempty"`
	// GateArchived: the gate folders a close moved to the archive (D155
	// Q74), as milestones.json records them.
	GateArchived []GateArchive `json:"gate_archived,omitempty"`
}

// RunGate runs a milestone's gate and records it; the committed record
// (and a commit `apex: <M> gate green|red`) changes only when the result
// does.
func (p *Project) RunGate(ctx context.Context, id string, opt RunOptions) (*GateResult, error) {
	if err := p.recoverArchive(ctx); err != nil {
		return nil, err
	}
	if err := p.refuseOnChangeBranch(ctx, "a milestone gate run"); err != nil {
		return nil, err
	}
	if _, rm, err := p.loadRoadmapDoc(); err == nil {
		if m := rm.Find(id); m != nil {
			switch m.Status {
			case MilestoneDone:
				// A done milestone's gate was recorded at its close, and its
				// gate folder archived (D155): run now, the Gate: line names
				// a script that is gone, and a red would be recorded and
				// committed against a closed milestone (Q162, D350).
				// Refused before anything runs or is written.
				return nil, doneMilestoneGate(m)
			case MilestoneApproved:
				// An approved milestone's gate is the one a person approved (H2).
				if err := p.requireApprovalRecord(m, true); err != nil {
					return nil, err
				}
			}
		}
	}
	// Only milestones.json is the run's own edit: the gate may run long,
	// and roadmap.md is a person's to edit meanwhile.
	own := p.beforeOwnEdit(ctx, p.Rel(p.MilestonesJSON()))
	g, err := p.runGate(ctx, id, opt, false)
	if err != nil || !g.ResultChanged {
		return g, err
	}
	g.Commit, err = p.commitMilestone(context.WithoutCancel(ctx), own, id, "gate "+gateResult(&g.Run))
	if err == nil {
		p.emitGateGreen(context.WithoutCancel(ctx), g)
	}
	return g, err
}

// doneMilestoneGate is `ape milestone gate`'s refusal of a done milestone:
// approve's code (milestone.done), with its published action, stop.
func doneMilestoneGate(m *Milestone) *FailError {
	return &FailError{Code: "milestone.done", Msg: m.ID + " is done (Status: " + m.StatusText + "): it is closed, and its gate was recorded at the close " +
		"in _apex/state/milestones.json — a done milestone's gate does not run again; nothing to run"}
}

// emitGateGreen logs gate-green (W3) for a run that turned the recorded
// result green — red, or never run, before — once that result is
// committed.
func (p *Project) emitGateGreen(ctx context.Context, g *GateResult) {
	if g.ResultChanged && g.Run.Green() {
		p.Emit(ctx, Event{Event: EventGateGreen, Milestone: g.Milestone, Reason: "gate green at " + short(g.Run.SHA)}, nil)
	}
}

func gateResult(run *GateRun) string {
	if run.Green() {
		return GateGreen
	}
	return GateRed
}

// runGate runs the gate and writes the records, committing nothing. A
// Gate: command that is red already fails the gate, so the gate skips the
// live scenarios and the regression command (W2 Part 2) — unless full, as
// close runs it: everything runs.
func (p *Project) runGate(ctx context.Context, id string, opt RunOptions, full bool) (*GateResult, error) {
	_, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	m := rm.Find(id)
	if m == nil {
		return nil, &FailError{Code: CodeMilestoneNotFound, Msg: "no milestone " + id + " in roadmap.md"}
	}
	if m.Gate == "" {
		return nil, &FailError{Code: "milestone.no-gate", Msg: id + " has no Gate: line"}
	}
	logPath := filepath.Join(p.LogsDir(), "gate-"+id+".log")
	var base string
	if recs, err := p.LoadMilestoneRecords(); err == nil && recs[id] != nil {
		base = recs[id].Approval.Base()
	}
	gate := p.RunChecks(ctx, filepath.Dir(logPath), "", []PlannedCheck{{Ref: "gate-" + id, Kind: KindGate, Cmd: m.Gate, Env: gateEnv(id, base)}}, opt)[0]
	if err := interrupted(ctx, "the gate run of "+id); err != nil {
		return nil, err
	}
	r := gate
	run := GateRun{
		Exit: r.Exit, At: Stamp(), SHA: HeadSHA(ctx, p.Root), Secs: r.Secs,
		OutputSHA256: r.OutputSHA256, Cmd: m.Gate, Log: r.Log, Tail: lastN(r.Tail, 15),
		Reason: r.Reason, Detail: r.Detail, ShellNote: r.ShellNote,
	}
	// The whole spec runs as the regression suite beside the gate, and
	// the project's own regression command with it (backlog 32), judged
	// by the milestone's earliest baseline for that command.
	spec := p.EffectiveSpecChecks(nil)
	skip := gate.Exit != 0 && !full
	if skip {
		spec, run.LiveSkipped = withoutLive(spec)
	}
	run.SpecChecks = len(spec)
	checks := spec
	regressionCmd := p.milestoneRegressionCmd()
	if regressionCmd != "" && skip {
		run.Regression = &RegressionVerdict{Cmd: regressionCmd, Skipped: RegressionSkippedGateRed}
		regressionCmd = ""
	}
	if regressionCmd != "" {
		checks = append(append([]PlannedCheck(nil), spec...), PlannedCheck{Ref: regressionRef, Kind: KindRegression, Cmd: regressionCmd})
	}
	results := p.RunChecks(ctx, filepath.Join(p.LogsDir(), "gate-"+id), "", checks, opt)
	if err := interrupted(ctx, "the gate run of "+id); err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx)
	ran := append([]CheckResult{gate}, results...)
	run.StrayFiles = StrayFiles(ran)
	run.StraysKept = StraysKept(ran)
	for i := range ran {
		if ran[i].Flaky {
			run.Flaky = append(run.Flaky, ran[i].Task)
		}
	}
	if regressionCmd != "" {
		change, base := p.milestoneRegressionBaseline(ctx, id, regressionCmd)
		r := &results[len(results)-1]
		run.Regression = judgeRegression(r, base, p.checkOutput(r))
		run.Regression.BaselineChange = change
		results = results[:len(results)-1]
	}
	// rc.7d (rc7d-std): a red scenario that checks what `ape decision
	// standard` removed says so (Q131).
	p.markStale(results)
	for i := range results {
		if results[i].Exit != 0 {
			results[i].Tail = lastN(results[i].Tail, 15)
			run.SpecFailed = append(run.SpecFailed, results[i])
		}
	}
	run.ScenarioSecs = map[string]float64{}
	for i := range results {
		run.ScenarioSecs[results[i].Task] = results[i].Secs
	}
	if data, err := json.MarshalIndent(run, "", "  "); err == nil {
		_ = writeFileAtomic(p.lastGatePath(id), string(data)+"\n")
	}
	run.ScenarioSecs = nil
	g := &GateResult{Milestone: id, Run: run, Status: m.Status, checks: ran}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil, err
	}
	result := gateResult(&run)
	if rec := recs[id]; rec != nil && rec.Result == result {
		return g, nil
	}
	rec := recs[id]
	if rec == nil {
		rec = &MilestoneRecord{}
		recs[id] = rec
	}
	rec.Result, rec.Since = result, &run
	if err := p.saveMilestoneRecords(recs); err != nil {
		return nil, err
	}
	g.ResultChanged = true
	return g, nil
}

// withoutLive is checks without the live ones, and how many there were.
func withoutLive(checks []PlannedCheck) (kept []PlannedCheck, live int) {
	kept = make([]PlannedCheck, 0, len(checks))
	for i := range checks {
		if checks[i].Live {
			live++
			continue
		}
		kept = append(kept, checks[i])
	}
	return kept, live
}

// SpecOnlyResult is what `ape milestone gate <M> --spec-only` reports
// (W2 Part 4): the spec's scenario checks run alone, recording nothing.
type SpecOnlyResult struct {
	Milestone string `json:"milestone"`
	// SpecOnly and Mode both mark the partial run (D333 answer 2, and the
	// W2 build brief's "mode").
	SpecOnly bool   `json:"spec_only"`
	Mode     string `json:"mode"`
	// SpecChecks is how many scenario checks ran, LiveSkipped how many
	// live ones did not: a spec-only run runs no live check (W2 Part 2).
	SpecChecks  int           `json:"spec_checks"`
	LiveSkipped int           `json:"live_skipped"`
	SpecFailed  []CheckResult `json:"spec_failed"`
	// Log is the folder the checks' output went to.
	Log string `json:"log"`
	At  string `json:"at"`
	SHA string `json:"sha"`
}

// Green reports that every scenario check passed.
func (r *SpecOnlyResult) Green() bool { return len(r.SpecFailed) == 0 }

// Err is the red result's failure (gate.spec-red), nil when it is green.
func (r *SpecOnlyResult) Err() error {
	if r.Green() {
		return nil
	}
	names := make([]string, 0, len(r.SpecFailed))
	for i := range r.SpecFailed {
		names = append(names, r.SpecFailed[i].Task)
	}
	return &FailError{Code: CodeGateSpecRed, Msg: fmt.Sprintf("%s: %d scenario check(s) of the spec are red now (%s) — fix the plan's scenarios or their checks before the milestone is approved",
		r.Milestone, len(r.SpecFailed), strings.Join(limit(names, 5), ", "))}
}

// specOnlyPath is the spec-only run's record: beside its logs, never read
// as the gate's (lastGatePath).
func (p *Project) specOnlyPath(id string) string {
	return filepath.Join(p.LogsDir(), "gate-"+id+"-spec.json")
}

// RunSpecOnly runs the effective spec's scenario checks of a milestone
// the way its gate runs them, without the Gate: command and the
// regression command, and records nothing: no milestones.json, no last
// gate run, no commit (W2 Part 4; apex-plan's step 8). Refused like the
// gate on a change branch, on a done milestone (Q162) and, for an
// approved milestone, without its approval record or with a changed
// gate; allowed on a proposed one, which is when apex-plan runs it.
func (p *Project) RunSpecOnly(ctx context.Context, id string, opt RunOptions) (*SpecOnlyResult, error) {
	if err := p.recoverArchive(ctx); err != nil {
		return nil, err
	}
	if err := p.refuseOnChangeBranch(ctx, "a milestone gate run"); err != nil {
		return nil, err
	}
	_, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	m := rm.Find(id)
	if m == nil {
		return nil, &FailError{Code: CodeMilestoneNotFound, Msg: "no milestone " + id + " in roadmap.md"}
	}
	switch m.Status {
	case MilestoneDone:
		// As the gate (Q162, D350): a done milestone's gate was recorded at
		// its close and its gate folder archived; refused before anything
		// runs or is written.
		return nil, doneMilestoneGate(m)
	case MilestoneApproved:
		if err := p.requireApprovalRecord(m, true); err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(p.LogsDir(), "gate-"+id+"-spec")
	spec, live := withoutLive(p.EffectiveSpecChecks(nil))
	res := &SpecOnlyResult{
		Milestone: id, SpecOnly: true, Mode: "spec-only", SpecChecks: len(spec), LiveSkipped: live, Log: p.Rel(dir),
		At: Stamp(), SHA: HeadSHA(ctx, p.Root), SpecFailed: []CheckResult{},
	}
	results := p.RunChecks(ctx, dir, "", spec, opt)
	if err := interrupted(ctx, "the spec-only run of "+id); err != nil {
		return nil, err
	}
	p.markStale(results)
	for i := range results {
		if results[i].Exit != 0 {
			results[i].Tail = lastN(results[i].Tail, 15)
			res.SpecFailed = append(res.SpecFailed, results[i])
		}
	}
	// The log's record (gitignored); never the gate's gate-<M>.json.
	if data, err := json.MarshalIndent(res, "", "  "); err == nil {
		_ = writeFileAtomic(p.specOnlyPath(id), string(data)+"\n")
	}
	return res, nil
}

// markDone records in milestones.json that ape closed the milestone, the
// red gate a person accepted, if any, and the gate folders it archived.
func (p *Project) markDone(id, at string, red *AcceptRed, archived []GateArchive) error {
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return err
	}
	if recs[id] == nil {
		recs[id] = &MilestoneRecord{}
	}
	recs[id].DoneAt = at
	if red != nil {
		recs[id].AcceptRed = red
	}
	if len(archived) > 0 {
		recs[id].GateArchived = archived
	}
	return p.saveMilestoneRecords(recs)
}

func lastN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[len(xs)-n:]
	}
	return xs
}

// CloseMilestone runs the gate and, on exit 0 with no change of this
// milestone still in flight, writes `Status: done (<ts> <sha>)`.
func (p *Project) CloseMilestone(ctx context.Context, id string, opt RunOptions) (*GateResult, error) {
	return p.closeMilestone(ctx, id, opt, nil)
}

// AcceptRed is a person closing a milestone whose gate is red (H22,
// D45), in milestones.json under the milestone's "accept_red": the
// reason, who and when, and the red gate run it accepted.
type AcceptRed struct {
	Reason string   `json:"reason"`
	By     string   `json:"by"`
	At     string   `json:"at"`
	Gate   *GateRun `json:"gate"`
}

// CloseMilestoneAcceptRed is close for a person who accepts a red gate:
// as CloseMilestone — approval record, gate as approved, no change in
// flight — and when the gate is red, the milestone is done anyway, the
// acceptance recorded (accept_red) and the Status line saying so: `done
// (accepted red by <who> <ts> <sha>)`. A green gate closes it as usual,
// accepting nothing. A person's flag: refused in a model session.
func (p *Project) CloseMilestoneAcceptRed(ctx context.Context, id, reason, by string, opt RunOptions) (*GateResult, error) {
	if err := requirePerson(ctx, ActAcceptRed, "--accept-red"); err != nil {
		return nil, err
	}
	reason, by = strings.TrimSpace(reason), strings.TrimSpace(by)
	if reason == "" || strings.Contains(reason, "\n") {
		return nil, &UsageError{Code: "usage.reason", Msg: "--accept-red needs a one-line reason: why the milestone is done with its gate red"}
	}
	if by == "" {
		return nil, &UsageError{Code: "usage.by", Msg: "--by names the person who accepts the red gate"}
	}
	return p.closeMilestone(ctx, id, opt, &AcceptRed{Reason: reason, By: by})
}

// closeMilestone is close; red, when set, is a person's acceptance of a
// red gate.
//
//nolint:gocyclo // an ordered list of refusals before the close is written, each a few lines
func (p *Project) closeMilestone(ctx context.Context, id string, opt RunOptions, red *AcceptRed) (*GateResult, error) {
	if err := p.recoverArchive(ctx); err != nil {
		return nil, err
	}
	if err := p.refuseOnChangeBranch(ctx, "closing a milestone"); err != nil {
		return nil, err
	}
	_, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	m := rm.Find(id)
	if m == nil {
		return nil, &FailError{Code: CodeMilestoneNotFound, Msg: "no milestone " + id + " in roadmap.md"}
	}
	switch m.Status {
	case MilestoneDone:
		return &GateResult{Milestone: id, Status: MilestoneDone}, nil
	case MilestoneApproved:
		if err := p.requireApprovalRecord(m, true); err != nil {
			return nil, err
		}
	default:
		return nil, &FailError{Code: CodeMilestoneNotApproved, Msg: id + " is " + m.StatusText + ", not approved — nothing to close"}
	}
	var inFlight, awaiting []string
	for _, c := range p.changes(ctx) {
		if c.Doc == nil || c.State == nil || c.Doc.Meta.Milestone != id {
			continue
		}
		switch c.State.Status {
		case StatusInProgress, StatusVerified, StatusReviewed:
			inFlight = append(inFlight, c.ID+" ("+c.State.Status+")")
			if pending := awaitingHuman(p.withBaseAccepted(ctx, c.ID, c.State), c.Doc); len(pending) > 0 {
				awaiting = append(awaiting, c.ID+" ("+strings.Join(pending, ", ")+")")
			}
		}
	}
	if len(inFlight) > 0 {
		fe := &FailError{Code: "milestone.changes-in-flight", Msg: id + " has changes in flight: " + strings.Join(inFlight, ", ") + " — archive or block them, then close"}
		if len(awaiting) > 0 {
			// A change that waits for a person's acceptance is not the
			// orchestrator's to move (D49).
			fe.Action = ActionPerson
			fe.Msg = id + " has changes in flight: " + strings.Join(inFlight, ", ") + "; awaiting a person's acceptance: " + strings.Join(awaiting, ", ") +
				" — a person accepts them (`ape change accept <id> --task <T> --by <who>`), the changes are archived, then close"
		}
		return &GateResult{Milestone: id, Status: m.Status, InFlight: inFlight}, fe
	}
	// Where its gate folders go (D155 Q74), refused before the gate runs.
	gatePlan, err := p.gateArchivePlan(id)
	if err != nil {
		return nil, err
	}
	// Only milestones.json is the run's own edit: the gate may run long,
	// and roadmap.md is a person's to edit meanwhile.
	own := p.beforeOwnEdit(ctx, p.Rel(p.MilestonesJSON()))
	g, err := p.runGate(ctx, id, opt, true)
	if err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx) // the gate ran: what it found is recorded
	if !g.Run.Green() && red == nil {
		if g.ResultChanged {
			if g.Commit, err = p.commitMilestone(ctx, own, id, "gate "+GateRed); err != nil {
				return g, err
			}
		}
		return g, &FailError{Code: "gate.red", Msg: id + " gate is red (" + g.Run.Why() + ") — the milestone is not done"}
	}
	if g.Run.Green() {
		red = nil // nothing red to accept
	}
	_, rm, err = p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	// The gate folders move to the archive in the close commit, first, so
	// a move that fails leaves the milestone open.
	moved, err := p.archiveGateFolders(ctx, gatePlan)
	if err != nil {
		return nil, err
	}
	sha := g.Run.SHA
	if sha == "" {
		sha = "0000000"
	}
	status, what := "done ("+g.Run.At+" "+sha+")", MilestoneDone
	if red != nil {
		red.At, red.Gate = Stamp(), &g.Run
		status, what = "done (accepted red by "+red.By+" "+red.At+" "+sha+")", MilestoneDone+" (accepted red)"
	}
	updated, err := rm.WithStatus(id, status)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p.RoadmapMD(), updated); err != nil {
		return nil, err
	}
	if err := p.markDone(id, g.Run.At, red, gatePlan); err != nil {
		return nil, err
	}
	g.Status, g.AcceptRed, g.GateArchived = MilestoneDone, red, gatePlan
	extra := moved
	if p.evidenceOn() {
		if g.Evidence, err = p.writeMilestoneEvidence(id, g.checks); err != nil {
			return g, err
		}
		extra = append(extra, g.Evidence)
	}
	// The close moves the gate folders, which are not ape's: never undone.
	if g.Commit, err = p.commitMilestone(ctx, nil, id, what, extra...); err != nil {
		return g, err
	}
	p.emitGateGreen(ctx, g)
	reason := "closed, gate green at " + short(g.Run.SHA)
	if red != nil {
		reason = "closed with its gate red, accepted by " + red.By + ": " + red.Reason
	}
	p.Emit(ctx, Event{Event: EventMilestoneDone, Milestone: id, Reason: reason}, nil)
	return g, nil
}

package apexv1

import (
	"context"
	"encoding/json"
	"errors"
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
	// Regression judges the regression command against the milestone's
	// baseline (backlog 32); absent when none is configured.
	Regression *RegressionVerdict `json:"regression,omitempty"`
}

// Green reports a passing gate: its command exited 0, every scenario
// check of the spec passed, and the regression command is not newly red.
func (r *GateRun) Green() bool {
	return r.Exit == 0 && len(r.SpecFailed) == 0 && (r.Regression == nil || !r.Regression.Failed)
}

// Why describes a red gate in one phrase.
func (r *GateRun) Why() string {
	var parts []string
	if r.Exit != 0 {
		parts = append(parts, "gate command exit "+strconv.Itoa(r.Exit))
	}
	if n := len(r.SpecFailed); n > 0 {
		names := make([]string, 0, n)
		for i := range r.SpecFailed {
			names = append(names, r.SpecFailed[i].Task)
		}
		parts = append(parts, strconv.Itoa(n)+" spec scenario(s) red: "+strings.Join(limit(names, 5), ", "))
	}
	if g := r.Regression; g != nil && g.Failed {
		if g.BaselineExit == nil {
			parts = append(parts, "regression command exit "+strconv.Itoa(g.Exit)+", and no change of the milestone has a baseline for it")
		} else {
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
	Result string   `json:"result"`
	Since  *GateRun `json:"since"`
	// Stops are the sessions ape stopped at the milestone's enforced
	// budget (backlog 30).
	Stops []LimitStop `json:"stops,omitempty"`
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
	if data, err := os.ReadFile(p.lastGatePath(id)); err == nil {
		var run GateRun
		if json.Unmarshal(data, &run) == nil {
			return &run
		}
	}
	if recs, err := p.LoadMilestoneRecords(); err == nil && recs[id] != nil {
		return recs[id].Since
	}
	return nil
}

// milestonePaths are the files a milestone commit records.
func (p *Project) milestonePaths() []string {
	paths := []string{p.Rel(p.RoadmapMD())}
	if fileExists(p.MilestonesJSON()) {
		paths = append(paths, p.Rel(p.MilestonesJSON()))
	}
	return paths
}

func (p *Project) commitMilestone(ctx context.Context, id, what string, extra ...string) (string, error) {
	sha, err := p.commitRecord(ctx, "apex: "+id+" "+what, "Milestone: "+id, append(p.milestonePaths(), extra...)...)
	if err != nil {
		return "", &FailError{Code: CodeCommitFailed, Msg: "roadmap written but its commit failed: " + err.Error() + " — fix git (identity, lock) and commit " + strings.Join(p.milestonePaths(), " ")}
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
	if err := writeFileAtomic(p.RoadmapMD(), AppendMilestone(doc, id, title, gate, budget, goal, nil)); err != nil {
		return "", err
	}
	if _, err := p.commitMilestone(ctx, id, MilestoneProposed); err != nil {
		return id, err
	}
	return id, nil
}

// ApproveMilestone writes `Status: approved (by <who> <ts>)`.
func (p *Project) ApproveMilestone(ctx context.Context, id, by string) (*Milestone, error) {
	if err := p.recoverArchive(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(by) == "" {
		return nil, &UsageError{Code: "usage.by", Msg: "--by names who approves"}
	}
	if err := p.refuseOnChangeBranch(ctx, "a milestone approval"); err != nil {
		return nil, err
	}
	doc, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	m := rm.Find(id)
	if m == nil {
		return nil, &FailError{Code: CodeMilestoneNotFound, Msg: "no milestone " + id + " in roadmap.md"}
	}
	switch m.Status {
	case MilestoneDone:
		return nil, &FailError{Code: "milestone.done", Msg: id + " is already done"}
	case MilestoneApproved:
		return m, nil
	}
	if fs := ValidateRoadmap(p.Rel(p.RoadmapMD()), doc); len(filterMilestone(fs, m)) > 0 {
		return nil, &FailError{Code: "milestone.malformed", Msg: id + " is not well-formed — fix roadmap.md before approving", Findings: filterMilestone(fs, m)}
	}
	updated, err := rm.WithStatus(id, "approved (by "+strings.TrimSpace(by)+" "+Stamp()+")")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p.RoadmapMD(), updated); err != nil {
		return nil, err
	}
	m = ParseRoadmap("", updated).Find(id)
	if _, err := p.commitMilestone(ctx, id, MilestoneApproved); err != nil {
		return m, err
	}
	return m, nil
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
	g, err := p.runGate(ctx, id, opt)
	if err != nil || !g.ResultChanged {
		return g, err
	}
	g.Commit, err = p.commitMilestone(ctx, id, "gate "+gateResult(&g.Run))
	return g, err
}

func gateResult(run *GateRun) string {
	if run.Green() {
		return GateGreen
	}
	return GateRed
}

// runGate runs the gate and writes the records, committing nothing.
func (p *Project) runGate(ctx context.Context, id string, opt RunOptions) (*GateResult, error) {
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
	gate := p.RunChecks(ctx, filepath.Dir(logPath), "", []PlannedCheck{{Ref: "gate-" + id, Kind: "gate", Cmd: m.Gate}}, opt)[0]
	r := gate
	run := GateRun{
		Exit: r.Exit, At: Stamp(), SHA: HeadSHA(ctx, p.Root), Secs: r.Secs,
		OutputSHA256: r.OutputSHA256, Cmd: m.Gate, Log: r.Log, Tail: lastN(r.Tail, 15),
	}
	// The whole spec runs as the regression suite beside the gate, and
	// the project's own regression command with it (backlog 32), judged
	// by the milestone's earliest baseline for that command.
	spec := p.EffectiveSpecChecks(nil)
	run.SpecChecks = len(spec)
	checks := spec
	regressionCmd := p.regressionCmd()
	if regressionCmd != "" {
		checks = append(append([]PlannedCheck(nil), spec...), PlannedCheck{Ref: regressionRef, Kind: KindRegression, Cmd: regressionCmd})
	}
	results := p.RunChecks(ctx, filepath.Join(p.LogsDir(), "gate-"+id), "", checks, opt)
	ran := append([]CheckResult{gate}, results...)
	run.StrayFiles = StrayFiles(append([]CheckResult{gate}, results...))
	if regressionCmd != "" {
		change, base := p.milestoneRegressionBaseline(ctx, id, regressionCmd)
		run.Regression = judgeRegression(&results[len(results)-1], base)
		run.Regression.BaselineChange = change
		results = results[:len(results)-1]
	}
	for i := range results {
		if results[i].Exit != 0 {
			results[i].Tail = lastN(results[i].Tail, 15)
			run.SpecFailed = append(run.SpecFailed, results[i])
		}
	}
	if data, err := json.MarshalIndent(run, "", "  "); err == nil {
		_ = writeFileAtomic(p.lastGatePath(id), string(data)+"\n")
	}
	g := &GateResult{Milestone: id, Run: run, Status: m.Status, checks: ran}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil, err
	}
	result := gateResult(&run)
	if rec := recs[id]; rec != nil && rec.Result == result {
		return g, nil
	}
	var stops []LimitStop
	if rec := recs[id]; rec != nil {
		stops = rec.Stops
	}
	recs[id] = &MilestoneRecord{Result: result, Since: &run, Stops: stops}
	if err := p.saveMilestoneRecords(recs); err != nil {
		return nil, err
	}
	g.ResultChanged = true
	return g, nil
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
	default:
		return nil, &FailError{Code: CodeMilestoneNotApproved, Msg: id + " is " + m.StatusText + ", not approved — nothing to close"}
	}
	var inFlight []string
	for _, c := range p.changes(ctx) {
		if c.Doc == nil || c.State == nil || c.Doc.Meta.Milestone != id {
			continue
		}
		switch c.State.Status {
		case StatusInProgress, StatusVerified, StatusReviewed:
			inFlight = append(inFlight, c.ID+" ("+c.State.Status+")")
		}
	}
	if len(inFlight) > 0 {
		return &GateResult{Milestone: id, Status: m.Status, InFlight: inFlight},
			&FailError{Code: "milestone.changes-in-flight", Msg: id + " has changes in flight: " + strings.Join(inFlight, ", ") + " — archive or block them, then close"}
	}
	g, err := p.runGate(ctx, id, opt)
	if err != nil {
		return nil, err
	}
	if !g.Run.Green() {
		if g.ResultChanged {
			if g.Commit, err = p.commitMilestone(ctx, id, "gate "+GateRed); err != nil {
				return g, err
			}
		}
		return g, &FailError{Code: "gate.red", Msg: id + " gate is red (" + g.Run.Why() + ") — the milestone is not done"}
	}
	_, rm, err = p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	sha := g.Run.SHA
	if sha == "" {
		sha = "0000000"
	}
	updated, err := rm.WithStatus(id, "done ("+g.Run.At+" "+sha+")")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p.RoadmapMD(), updated); err != nil {
		return nil, err
	}
	g.Status = MilestoneDone
	var extra []string
	if p.evidenceOn() {
		if g.Evidence, err = p.writeMilestoneEvidence(id, g.checks); err != nil {
			return g, err
		}
		extra = append(extra, g.Evidence)
	}
	if g.Commit, err = p.commitMilestone(ctx, id, MilestoneDone, extra...); err != nil {
		return g, err
	}
	return g, nil
}

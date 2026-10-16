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
}

// Green reports a passing gate: its command exited 0 and every scenario
// check of the spec passed.
func (r *GateRun) Green() bool { return r.Exit == 0 && len(r.SpecFailed) == 0 }

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
}

// MilestoneRecords is development/.apex/milestones.json.
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
	return writeFileAtomic(p.MilestonesJSON(), string(data)+"\n")
}

func (p *Project) lastGatePath(id string) string {
	return filepath.Join(p.ApexDir(), "logs", "gate-"+id+".json")
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

func (p *Project) commitMilestone(ctx context.Context, id, what string) (string, error) {
	sha, err := p.commitRecord(ctx, "apex: "+id+" "+what, "Milestone: "+id, p.milestonePaths()...)
	if err != nil {
		return "", &FailError{Msg: "roadmap written but its commit failed: " + err.Error() + " — fix git (identity, lock) and commit " + strings.Join(p.milestonePaths(), " ")}
	}
	return sha, nil
}

func (p *Project) loadRoadmapDoc() (string, *Roadmap, error) {
	doc, err := readFileString(p.RoadmapMD())
	if err != nil {
		return "", nil, &FailError{Msg: "no " + p.Rel(p.RoadmapMD()) + " — run `ape project init`"}
	}
	return doc, ParseRoadmap(p.Rel(p.RoadmapMD()), doc), nil
}

// NewMilestone appends a milestone to roadmap.md and returns its id.
func (p *Project) NewMilestone(ctx context.Context, title, gate string, budget *float64, goal string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", &UsageError{Msg: "a milestone needs a title"}
	}
	if strings.TrimSpace(gate) == "" {
		return "", &UsageError{Msg: "--gate is required: one command that is red until the milestone is done"}
	}
	if strings.Contains(gate, "\n") || strings.Contains(title, "\n") {
		return "", &UsageError{Msg: "title and gate are one line each"}
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
	if strings.TrimSpace(by) == "" {
		return nil, &UsageError{Msg: "--by names who approves"}
	}
	doc, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	m := rm.Find(id)
	if m == nil {
		return nil, &FailError{Msg: "no milestone " + id + " in roadmap.md"}
	}
	switch m.Status {
	case MilestoneDone:
		return nil, &FailError{Msg: id + " is already done"}
	case MilestoneApproved:
		return m, nil
	}
	if fs := ValidateRoadmap(p.Rel(p.RoadmapMD()), doc); len(filterMilestone(fs, m)) > 0 {
		return nil, &FailError{Msg: id + " is not well-formed — fix roadmap.md before approving", Findings: filterMilestone(fs, m)}
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
}

// RunGate runs a milestone's gate and records it; the committed record
// (and a commit `apex: <M> gate green|red`) changes only when the result
// does.
func (p *Project) RunGate(ctx context.Context, id string, opt RunOptions) (*GateResult, error) {
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
		return nil, &FailError{Msg: "no milestone " + id + " in roadmap.md"}
	}
	if m.Gate == "" {
		return nil, &FailError{Msg: id + " has no Gate: line"}
	}
	logPath := filepath.Join(p.ApexDir(), "logs", "gate-"+id+".log")
	gate := p.RunChecks(ctx, filepath.Dir(logPath), "", []PlannedCheck{{Ref: "gate-" + id, Kind: "gate", Cmd: m.Gate}}, opt)[0]
	r := gate
	run := GateRun{
		Exit: r.Exit, At: Stamp(), SHA: HeadSHA(ctx, p.Root), Secs: r.Secs,
		OutputSHA256: r.OutputSHA256, Cmd: m.Gate, Log: r.Log, Tail: lastN(r.Tail, 15),
	}
	// The whole spec runs as the regression suite beside the gate.
	spec := p.EffectiveSpecChecks(nil)
	run.SpecChecks = len(spec)
	results := p.RunChecks(ctx, filepath.Join(p.ApexDir(), "logs", "gate-"+id), "", spec, opt)
	run.StrayFiles = StrayFiles(append([]CheckResult{gate}, results...))
	for i := range results {
		if results[i].Exit != 0 {
			results[i].Tail = lastN(results[i].Tail, 15)
			run.SpecFailed = append(run.SpecFailed, results[i])
		}
	}
	if data, err := json.MarshalIndent(run, "", "  "); err == nil {
		_ = writeFileAtomic(p.lastGatePath(id), string(data)+"\n")
	}
	g := &GateResult{Milestone: id, Run: run, Status: m.Status}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil, err
	}
	result := gateResult(&run)
	if rec := recs[id]; rec != nil && rec.Result == result {
		return g, nil
	}
	recs[id] = &MilestoneRecord{Result: result, Since: &run}
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
	_, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	m := rm.Find(id)
	if m == nil {
		return nil, &FailError{Msg: "no milestone " + id + " in roadmap.md"}
	}
	switch m.Status {
	case MilestoneDone:
		return &GateResult{Milestone: id, Status: MilestoneDone}, nil
	case MilestoneApproved:
	default:
		return nil, &FailError{Msg: id + " is " + m.StatusText + ", not approved — nothing to close"}
	}
	var inFlight []string
	for _, c := range p.Changes() {
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
			&FailError{Msg: id + " has changes in flight: " + strings.Join(inFlight, ", ") + " — archive or block them, then close"}
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
		return g, &FailError{Msg: id + " gate is red (" + g.Run.Why() + ") — the milestone is not done"}
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
	if g.Commit, err = p.commitMilestone(ctx, id, MilestoneDone); err != nil {
		return g, err
	}
	return g, nil
}

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
	Exit         int     `json:"exit"`
	At           string  `json:"at"`
	SHA          string  `json:"sha"`
	Secs         float64 `json:"secs"`
	OutputSHA256 string  `json:"output_sha256"`
	Cmd          string  `json:"cmd"`
	Log          string  `json:"log,omitempty"`
	Tail         []string `json:"tail,omitempty"`
}

// MilestoneRecord is ape's record of one milestone's gate.
type MilestoneRecord struct {
	Last  *GateRun `json:"last,omitempty"`
	Runs  int      `json:"runs"`
	Green int      `json:"green"`
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

func (p *Project) loadRoadmapDoc() (string, *Roadmap, error) {
	doc, err := readFileString(p.RoadmapMD())
	if err != nil {
		return "", nil, &FailError{Msg: "no " + p.Rel(p.RoadmapMD()) + " — run `ape project init`"}
	}
	return doc, ParseRoadmap(p.Rel(p.RoadmapMD()), doc), nil
}

// NewMilestone appends a milestone to roadmap.md and returns its id.
func (p *Project) NewMilestone(title, gate string, budget *float64, goal string) (string, error) {
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
	return id, nil
}

// ApproveMilestone writes `Status: approved (by <who> <ts>)`.
func (p *Project) ApproveMilestone(id, by string) (*Milestone, error) {
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
	return ParseRoadmap("", updated).Find(id), nil
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
}

// RunGate runs a milestone's gate and records it.
func (p *Project) RunGate(ctx context.Context, id string, opt RunOptions) (*GateResult, error) {
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
	r := RunCheck(ctx, p.Root, m.Gate, logPath, opt.Timeout)
	run := GateRun{Exit: r.Exit, At: Stamp(), SHA: HeadSHA(ctx, p.Root), Secs: r.Secs,
		OutputSHA256: r.OutputSHA256, Cmd: m.Gate, Log: p.Rel(logPath), Tail: lastN(r.Tail, 15)}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil, err
	}
	rec := recs[id]
	if rec == nil {
		rec = &MilestoneRecord{}
		recs[id] = rec
	}
	rec.Last = &run
	rec.Runs++
	if run.Exit == 0 {
		rec.Green++
	}
	if err := p.saveMilestoneRecords(recs); err != nil {
		return nil, err
	}
	return &GateResult{Milestone: id, Run: run, Status: m.Status}, nil
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
	g, err := p.RunGate(ctx, id, opt)
	if err != nil {
		return nil, err
	}
	if g.Run.Exit != 0 {
		return g, &FailError{Msg: id + " gate is red (exit " + strconv.Itoa(g.Run.Exit) + "): `" + m.Gate + "` — the milestone is not done"}
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
	return g, nil
}

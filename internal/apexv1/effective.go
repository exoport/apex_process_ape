package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
)

// Check kinds in a run of checks.
const (
	KindTask     = "task"
	KindScenario = "scenario"
)

// PlannedCheck is one command a verify, baseline or gate runs.
type PlannedCheck struct {
	// Ref identifies the check across runs: a task id ("T1"), or
	// "<capability>/<requirement>/<scenario>" for a scenario.
	Ref         string
	Kind        string
	Capability  string
	Requirement string
	Scenario    string
	// Delta reports a scenario of the change's own ADDED/MODIFIED blocks,
	// as opposed to one the change leaves untouched (regression).
	Delta bool
	Cmd   string
}

func scenarioRef(capName, req, scen string) string { return capName + "/" + req + "/" + scen }

// scenarioChecks lists the command-checked scenarios of reqs.
func scenarioChecks(capName string, reqs []Requirement, delta bool) []PlannedCheck {
	var out []PlannedCheck
	for i := range reqs {
		r := &reqs[i]
		for j := range r.Scenarios {
			s := &r.Scenarios[j]
			cmd := firstCheck(s)
			if cmd == "" || cmd == HumanReview {
				continue
			}
			out = append(out, PlannedCheck{
				Ref: scenarioRef(capName, r.Name, s.Name), Kind: KindScenario,
				Capability: capName, Requirement: r.Name, Scenario: s.Name, Delta: delta, Cmd: cmd,
			})
		}
	}
	return out
}

// EffectiveSpecChecks are the scenario checks of the spec as it will be
// once c is archived: every current requirement except those c MODIFIES
// or REMOVES, plus the scenarios of c's own ADDED and MODIFIED blocks.
// With c nil it is the whole current spec — the milestone regression suite.
func (p *Project) EffectiveSpecChecks(c *Change) []PlannedCheck {
	replaced := map[reqKey]bool{}
	byCap := map[string][]Requirement{}
	if c != nil {
		for i := range c.Deltas {
			d := &c.Deltas[i]
			if d.Op != OpAdded {
				replaced[d.key()] = true
			}
			if d.Op != OpRemoved {
				byCap[d.Capability] = append(byCap[d.Capability], d.Req)
			}
		}
	}
	var out []PlannedCheck
	for _, capName := range p.Capabilities() {
		spec, _, ok := p.LoadSpec(capName)
		if !ok {
			continue
		}
		var kept []Requirement
		for i := range spec.Requirements {
			if !replaced[reqKey{capName, spec.Requirements[i].Name}] {
				kept = append(kept, spec.Requirements[i])
			}
		}
		out = append(out, scenarioChecks(capName, kept, false)...)
	}
	if c != nil {
		for i := range c.Deltas {
			d := &c.Deltas[i]
			if reqs, ok := byCap[d.Capability]; ok {
				out = append(out, scenarioChecks(d.Capability, reqs, true)...)
				delete(byCap, d.Capability)
			}
		}
	}
	return out
}

// ChangeChecks is what verify runs for a change: its command tasks, then
// the effective spec's scenarios.
func (p *Project) ChangeChecks(c *Change) []PlannedCheck {
	var out []PlannedCheck
	for _, t := range c.CommandTasks() {
		out = append(out, PlannedCheck{Ref: t.ID, Kind: KindTask, Cmd: t.Check})
	}
	return append(out, p.EffectiveSpecChecks(c)...)
}

// logName is a check's log file name: the task id, or a short hash of a
// scenario ref (refs carry spaces and punctuation).
func (pc *PlannedCheck) logName(prefix string) string {
	if pc.Kind != KindScenario {
		return prefix + pc.Ref + ".log"
	}
	sum := sha256.Sum256([]byte(pc.Ref))
	return prefix + "S-" + hex.EncodeToString(sum[:])[:12] + ".log"
}

// RunChecks runs each planned check from the project root, logging under
// logDir. A command shared by several checks runs once: a scenario whose
// check is also a task's is not paid for twice. What a check leaves in the
// tree outside the development folder is undone and reported in its
// StrayFiles (item 19).
func (p *Project) RunChecks(ctx context.Context, logDir, prefix string, checks []PlannedCheck, opt RunOptions) []CheckResult {
	done := map[string]CheckResult{}
	out := make([]CheckResult, 0, len(checks))
	inGit := p.isWorkTree(ctx)
	for i := range checks {
		pc := &checks[i]
		r, ok := done[pc.Cmd]
		if !ok {
			logPath := filepath.Join(logDir, pc.logName(prefix))
			var before treeSnapshot
			if inGit {
				before = p.snapshot(ctx)
			}
			r = RunCheck(ctx, p.Root, pc.Cmd, logPath, opt.Timeout)
			r.Log = p.Rel(logPath)
			if inGit {
				r.StrayFiles = p.undoStrays(ctx, before)
			}
			done[pc.Cmd] = r
		}
		r.Task = pc.Ref
		r.Kind = pc.Kind
		r.Capability, r.Requirement, r.Scenario, r.Delta = pc.Capability, pc.Requirement, pc.Scenario, pc.Delta
		out = append(out, r)
	}
	return out
}

// StrayFiles is the sorted union of the paths checks left and ape undid.
func StrayFiles(results []CheckResult) []string {
	seen := map[string]bool{}
	out := []string{}
	for i := range results {
		for _, f := range results[i].StrayFiles {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

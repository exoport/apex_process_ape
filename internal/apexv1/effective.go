package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Check kinds in a run of checks.
const (
	KindTask     = "task"
	KindScenario = "scenario"
	// KindGate is a milestone's Gate: command.
	KindGate = "gate"
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

// ChangeChecks is what a change's baseline and verify run (H10): its
// command tasks, then the effective spec's scenarios of the capabilities
// it touches (touches:, which names every capability its delta does),
// plus any other scenario whose command is one of its tasks' — free, as a
// shared command runs once. The rest of the spec is the milestone gate's
// to run (EffectiveSpecChecks(nil)), so a verify does not pay for a spec
// of hundreds of scenarios the change cannot have broken.
func (p *Project) ChangeChecks(c *Change) []PlannedCheck {
	var out []PlannedCheck
	taskCmds := map[string]bool{}
	for _, t := range c.CommandTasks() {
		out = append(out, PlannedCheck{Ref: t.ID, Kind: KindTask, Cmd: t.Check})
		taskCmds[t.Check] = true
	}
	touched := map[string]bool{}
	for _, capName := range c.Meta.Touches {
		touched[capName] = true
	}
	for _, pc := range p.EffectiveSpecChecks(c) {
		if pc.Delta || touched[pc.Capability] || taskCmds[pc.Cmd] {
			out = append(out, pc)
		}
	}
	return out
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
// check is also a task's is not paid for twice. A check whose requires:
// the machine lacks is not run (unrun), and a green one whose output says
// it ran nothing is vacuous (H11): both red. What a check leaves in the
// tree outside the development folder is undone and reported in its
// StrayFiles (item 19).
func (p *Project) RunChecks(ctx context.Context, logDir, prefix string, checks []PlannedCheck, opt RunOptions) []CheckResult {
	done := map[string]CheckResult{}
	out := make([]CheckResult, 0, len(checks))
	p.ensureState() // the logs go under _apex/state/
	inGit := p.isWorkTree(ctx)
	cfg, _ := LoadConfig(p.Root) // Open refused an unparseable one
	timeouts, vacuous := cfg.Timeouts, cfg.Checks.Vacuous()
	for i := range checks {
		pc := &checks[i]
		r, ok := done[pc.Cmd]
		if !ok && ctx.Err() != nil {
			// Interrupted (D58 2.2): the rest is not started; the caller
			// records nothing of this run.
			r, ok = CheckResult{Cmd: pc.Cmd, Exit: 130, Tail: []string{"[ape] not run: interrupted"}}, true
		}
		if !ok {
			logPath := filepath.Join(logDir, pc.logName(prefix))
			if missing := unmetRequirement(pc.Cmd, cfg.Requires); missing != "" {
				r = unrunResult(pc.Cmd, logPath, missing)
				r.Log = p.Rel(logPath)
			} else {
				progressf(opt.Progress, "%s %s started: %s (output: %s)", Stamp(), pc.Ref, pc.Cmd, p.Rel(logPath))
				r = p.runWithRetries(ctx, pc, logPath, timeouts.Limits(pc.Kind, opt.Timeout), vacuous, int(cfg.Retries), inGit)
				verdict := "green"
				if r.Exit != 0 {
					verdict = "red (" + r.Outcome() + ")"
				}
				progressf(opt.Progress, "%s %s %s in %.1fs", Stamp(), pc.Ref, verdict, r.Secs)
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

// progressf writes one progress line to w, when there is one.
func progressf(w io.Writer, format string, args ...any) {
	if w != nil {
		_, _ = fmt.Fprintf(w, "[ape] "+format+"\n", args...)
	}
}

// runWithRetries runs one check, and runs a red one again up to retries
// times (H12): the first green run ends it, flaky when it was not the
// first. Each run's strays are undone after it; the earlier runs' logs
// stay beside the last as <name>.try<k>.log.
func (p *Project) runWithRetries(ctx context.Context, pc *PlannedCheck, logPath string, lim TierTimeout, vacuous []string, retries int, inGit bool) CheckResult {
	var r CheckResult
	var strays []string
	for try := 0; ; try++ {
		path := logPath
		if try < retries {
			// Every run but the last possible one may be kept as a try log:
			// write it under its own name, rename it after a green run.
			path = strings.TrimSuffix(logPath, ".log") + ".try" + strconv.Itoa(try+1) + ".log"
		}
		var before treeSnapshot
		if inGit {
			before = p.snapshot(ctx)
		}
		r = runCheck(ctx, p.Root, pc.Cmd, path, lim, vacuous)
		if inGit {
			strays = append(strays, p.undoStrays(ctx, before)...)
		}
		retry := r.Exit != 0 && r.Reason == "" && try < retries && ctx.Err() == nil
		if !retry {
			if path != logPath {
				_ = os.Rename(path, logPath)
			}
			r.Log = p.Rel(logPath)
			if try > 0 {
				r.Tries = try + 1
				r.Flaky = r.Exit == 0
			}
			break
		}
	}
	r.StrayFiles = strays
	return r
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

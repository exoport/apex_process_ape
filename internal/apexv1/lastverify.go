package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LastVerify is a change's latest verify, red or green, as
// _apex/state/logs/<id>/last-verify.json (ignored) records it. state.json
// keeps only a green verify, and a status change's: a red verify of an
// in-progress change writes nothing there (D5), so the stop hook could not
// learn from it which checks were red (H10).
type LastVerify struct {
	SHA string `json:"sha"`
	At  string `json:"at"`
	// PlanDigest is the change's frozen plan at the verify: a re-plan
	// makes the record describe checks the plan no longer has.
	PlanDigest string `json:"plan_digest"`
	Green      bool   `json:"green"`
	// Red maps each red check's ref to its command; Failing describes
	// each, as `ape status` lists them ("T1 (exit 1)", "T2 (vacuous: …)").
	Red     map[string]string `json:"red"`
	Failing []string          `json:"failing,omitempty"`
}

func (p *Project) lastVerifyPath(id string) string {
	return filepath.Join(p.LogDir(id), "last-verify.json")
}

// saveLastVerify records a verify's outcome; a write that fails loses only
// the stop hook's narrowing (it falls back to the task checks).
func (p *Project) saveLastVerify(id string, st *State, sha string, green bool, results []CheckResult) {
	lv := LastVerify{SHA: sha, At: Stamp(), PlanDigest: st.PlanDigest, Green: green, Red: map[string]string{}}
	for i := range results {
		r := &results[i]
		if r.Red() {
			lv.Red[r.Task] = r.Cmd
			lv.Failing = append(lv.Failing, r.Task+" ("+r.Outcome()+")")
		}
	}
	data, err := json.MarshalIndent(lv, "", "  ")
	if err != nil {
		return
	}
	p.ensureState()
	if os.MkdirAll(p.LogDir(id), 0o755) == nil {
		_ = writeFileAtomic(p.lastVerifyPath(id), string(data)+"\n")
	}
}

// currentLastVerify is the change's last verify record when it is of the
// change's current plan and no older than the verify state.json holds;
// nil otherwise.
func (p *Project) currentLastVerify(id string, st *State) *LastVerify {
	lv := p.lastVerify(id)
	if lv == nil || lv.PlanDigest != st.PlanDigest || (st.Verify != nil && st.Verify.At > lv.At) {
		return nil
	}
	return lv
}

// lastVerify is the change's latest verify record, nil when there is none
// or it does not parse.
func (p *Project) lastVerify(id string) *LastVerify {
	data, err := os.ReadFile(p.lastVerifyPath(id))
	if err != nil {
		return nil
	}
	var lv LastVerify
	if json.Unmarshal(data, &lv) != nil {
		return nil
	}
	return &lv
}

// stopChecks are the checks the stop hook re-runs for an in-progress
// change (H10): those red at its last verify of the current plan — the
// regression command among them when it was newly red — and, before any
// verify of this plan, its command tasks. done is true when the last
// verify had nothing red: the hook then has nothing to run.
func (p *Project) stopChecks(ci ChangeInfo) (checks []PlannedCheck, done bool) {
	lv := p.lastVerify(ci.ID)
	if lv == nil || lv.PlanDigest != ci.State.PlanDigest {
		for _, t := range ci.Doc.CommandTasks() {
			checks = append(checks, PlannedCheck{Ref: t.ID, Kind: KindTask, Cmd: t.Check})
		}
		return checks, false
	}
	if len(lv.Red) == 0 {
		return nil, true
	}
	for _, pc := range p.ChangeChecks(ci.Doc) {
		if _, red := lv.Red[pc.Ref]; red {
			checks = append(checks, pc)
		}
	}
	if _, red := lv.Red[regressionRef]; red {
		if cmd := p.regressionCmd(); cmd != "" {
			checks = append(checks, PlannedCheck{Ref: regressionRef, Kind: KindRegression, Cmd: cmd})
		}
	}
	return checks, len(checks) == 0
}

package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The full run (v1.4, D508, D561 B3, D563). "Full" is everything that
// does not run on every change: the spec's `check (full):` scenarios and
// the milestone's regression command. The gate runs them only once its
// Gate: command is green (a red one is red already; W2 Part 2) and records
// them as the milestone's full run, which a gate run again and
// `ape milestone close` take instead of running them again while it is
// green and current: the same working tree (ContentDigest: the content of every
// file git does not ignore, ape's state aside — the config and the specs
// are in it — whatever was committed in between), the same approval of
// the same gate, the same full checks and the same regression command.
// --rerun runs them whatever is recorded. `ape milestone full <M>` runs and
// records a full run on demand. What the tree digest cannot see — an
// ignored input, the machine's environment — is the same residual as F1's.

// CodeFullRed: `ape milestone full` found a full check or the regression
// command red.
const CodeFullRed = "milestone.full-red"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeFullRed, 1, "apply", "`ape milestone full <M>` (v1.4): a full scenario check is red, or the regression command is newly red against the milestone's baseline — plan the change that makes it green; the run is recorded, and a gate at the same tree takes it"},
	)
}

// FullRun is a milestone's recorded full run, in logs/full-<M>.json.
type FullRun struct {
	Milestone string `json:"milestone"`
	At        string `json:"at"`
	SHA       string `json:"sha"`
	// Tree, Approval and Checks are what it is current against: the
	// working tree's digest when it ended, the approval it ran under, and
	// the digest of the full checks and the regression command it ran.
	Tree     string `json:"tree"`
	Approval string `json:"approval"`
	Checks   string `json:"checks"`
	// Results are the full scenario checks'; Regression the regression
	// command's result and Verdict its judgement.
	Results    []CheckResult      `json:"results"`
	Regression *CheckResult       `json:"regression,omitempty"`
	Verdict    *RegressionVerdict `json:"verdict,omitempty"`
	Secs       float64            `json:"secs"`
	// Tails keep each red result's last lines, by task: a result's tail is
	// not part of its JSON, and a gate that takes the run reports them.
	Tails map[string][]string `json:"tails,omitempty"`
	// By is what ran it: "gate", "close" or "full".
	By string `json:"by"`
}

// Green reports a full run whose checks all passed and whose regression
// command is not newly red.
func (f *FullRun) Green() bool {
	for i := range f.Results {
		if f.Results[i].Red() {
			return false
		}
	}
	return f.Verdict == nil || !f.Verdict.Failed
}

func (p *Project) fullRunPath(id string) string {
	return filepath.Join(p.LogsDir(), "full-"+id+".json")
}

// LoadFullRun is milestone id's recorded full run, nil when there is none.
func (p *Project) LoadFullRun(id string) *FullRun {
	data, err := os.ReadFile(p.fullRunPath(id))
	if err != nil {
		return nil
	}
	var f FullRun
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return &f
}

// approvalKey is what a full run is current against of id's approval: when
// it was recorded and the gate files it froze.
func (p *Project) approvalKey(id string) string {
	recs, err := p.LoadMilestoneRecords()
	if err != nil || recs[id] == nil || recs[id].Approval == nil {
		return ""
	}
	a := recs[id].Approval
	return a.At + " " + a.GateFilesSHA256
}

// fullChecksKey digests the full checks and the regression command.
func fullChecksKey(checks []PlannedCheck, regressionCmd string) string {
	lines := make([]string, 0, len(checks)+1)
	for i := range checks {
		lines = append(lines, checks[i].Ref+"\t"+checks[i].Cmd)
	}
	sort.Strings(lines)
	lines = append(lines, "regression\t"+regressionCmd)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// currentFullRun is id's recorded full run when it is current for these
// full checks and regression command, else nil.
func (p *Project) currentFullRun(ctx context.Context, id string, checks []PlannedCheck, regressionCmd string) *FullRun {
	f := p.LoadFullRun(id)
	// Only a green one: a red full run may be red for what is not in the
	// tree — a service down, a flake — and a gate at the same tree that
	// took it would stay red until the tree changed (the F1 concern, D455).
	if f != nil && !f.Green() {
		return nil
	}
	// The tree, not HEAD: the gate's own record commit (`apex: <M> gate
	// green`) moves HEAD and changes nothing a check reads.
	if f == nil || f.Checks != fullChecksKey(checks, regressionCmd) {
		return nil
	}
	if key := p.approvalKey(id); key == "" || key != f.Approval {
		return nil
	}
	if tree, err := p.ContentDigest(ctx); err != nil || tree != f.Tree {
		return nil
	}
	for i := range f.Results {
		f.Results[i].Tail = f.Tails[f.Results[i].Task]
	}
	return f
}

// runFull runs the full checks and the regression command of milestone id
// and records them as its full run; by says what ran it.
func (p *Project) runFull(ctx context.Context, id string, checks []PlannedCheck, regressionCmd, by string, opt RunOptions) (*FullRun, error) {
	all := append([]PlannedCheck(nil), checks...)
	if regressionCmd != "" {
		all = append(all, PlannedCheck{Ref: regressionRef, Kind: KindRegression, Cmd: regressionCmd})
	}
	results := p.RunChecks(ctx, filepath.Join(p.LogsDir(), "full-"+id), "", all, opt)
	if err := interrupted(ctx, "the full run of "+id); err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx)
	f := &FullRun{Milestone: id, At: Stamp(), SHA: HeadSHA(ctx, p.Root), Approval: p.approvalKey(id), Checks: fullChecksKey(checks, regressionCmd), By: by}
	if regressionCmd != "" {
		change, base := p.milestoneRegressionBaseline(ctx, id, regressionCmd)
		r := results[len(results)-1]
		f.Regression = &r
		f.Verdict = judgeRegression(&r, base, p.checkOutput(&r))
		f.Verdict.BaselineChange = change
		results = results[:len(results)-1]
	}
	p.markStale(results)
	f.Results = results
	for i := range results {
		if results[i].Exit != 0 && len(results[i].Tail) > 0 {
			if f.Tails == nil {
				f.Tails = map[string][]string{}
			}
			f.Tails[results[i].Task] = lastN(results[i].Tail, 15)
		}
	}
	for i := range f.Results {
		f.Secs += f.Results[i].Secs
	}
	if f.Regression != nil {
		f.Secs += f.Regression.Secs
	}
	if tree, err := p.ContentDigest(ctx); err == nil {
		f.Tree = tree
	}
	if data, err := json.MarshalIndent(f, "", "  "); err == nil {
		_ = writeFileAtomic(p.fullRunPath(id), string(data)+"\n")
	}
	return f, nil
}

// splitFull is checks as the plain ones and the full ones.
func splitFull(checks []PlannedCheck) (plain, full []PlannedCheck) {
	for i := range checks {
		if checks[i].Full {
			full = append(full, checks[i])
		} else {
			plain = append(plain, checks[i])
		}
	}
	return plain, full
}

// RunFull is `ape milestone full <M>`: the milestone's full run, on demand,
// recorded for a gate at the same tree to take. It records no gate result.
func (p *Project) RunFull(ctx context.Context, id string, opt RunOptions) (*FullRun, error) {
	if err := p.GatePreflight(ctx, id); err != nil {
		return nil, err
	}
	_, rm, err := p.loadRoadmapDoc()
	if err != nil {
		return nil, err
	}
	if rm.Find(id) == nil {
		return nil, &FailError{Code: CodeMilestoneNotFound, Msg: "no milestone " + id + " in roadmap.md"}
	}
	p.ensureState()
	_, full := splitFull(p.EffectiveSpecChecks(nil))
	f, err := p.runFull(ctx, id, full, p.milestoneRegressionCmd(), "full", opt)
	if err != nil {
		return nil, err
	}
	if !f.Green() {
		var red []string
		for i := range f.Results {
			if f.Results[i].Red() {
				red = append(red, f.Results[i].Task+" ("+f.Results[i].Outcome()+")")
			}
		}
		if f.Verdict != nil && f.Verdict.Failed {
			red = append(red, "the regression command (newly red)")
		}
		return f, &FailError{Code: CodeFullRed, Msg: id + "'s full run is red: " + strings.Join(limit(red, 8), "; ") +
			" — plan the change that makes it green; a gate at this tree takes this run"}
	}
	return f, nil
}

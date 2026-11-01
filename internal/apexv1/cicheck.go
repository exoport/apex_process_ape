package apexv1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The merge queue's check (v1.5 stage 1a, proposal sections 5.1–5.5): CI
// runs `ape ci check --base <sha> --head <sha>` on each merge group, with
// the group's head checked out. ape reads what the group carries from its
// commits' trailers and runs what each kind needs, the union when the
// queue batched several PRs: a change or a take-in, the regression
// command and the spec's scenario checks (no full ones); a close, the
// milestone gate with its full run, where an accepted red passes only on
// that red set (OQ26); records alone, the project check. A group with no
// commit of the flow is red: main and milestone branches take nothing
// from outside it.

// CI codes.
const (
	// CodeCIRed: a check the group needs is red.
	CodeCIRed = "ci.red"
	// CodeCIOutsideFlow: the group carries no commit of the flow.
	CodeCIOutsideFlow = "ci.outside-flow"
)

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeCIRed, 1, ActionStop, "ape ci check (the merge queue's check, v1.5 stage 1a): a check the merge group needs is red — the regression or a spec scenario for a change or take-in, the milestone gate for a close (an accepted red passes only on the red set it accepted, OQ26), the project check for records; the queue ejects the group, and the waiting ape catches up and re-enqueues (an attempt)"},
		CodeDoc{CodeCIOutsideFlow, 1, ActionPerson, "ape ci check: the merge group carries no commit of the flow — no Change: or Take-in: trailer, no close record, no apex: record — so nothing says what to check; main and milestone branches take only ape's PRs (v1.5 stage 1a, section 5.4)"},
	)
}

// CIResult is what `ape ci check` reports.
type CIResult struct {
	Base string `json:"base"`
	Head string `json:"head"`
	// What the group carries.
	Changes []string `json:"changes,omitempty"`
	TakeIns []string `json:"take_ins,omitempty"`
	Closes  []string `json:"closes,omitempty"`
	Records []string `json:"records,omitempty"`
	// What ran.
	Regression *CheckResult  `json:"regression,omitempty"`
	SpecChecks int           `json:"spec_checks,omitempty"`
	SpecFailed []CheckResult `json:"spec_failed,omitempty"`
	Gates      []*GateResult `json:"gates,omitempty"`
	Findings   []Finding     `json:"findings,omitempty"`
	Green      bool          `json:"green"`
	// Red says what was red, one line each.
	Red []string `json:"red,omitempty"`
}

// ciLogFields is classifyGroup's fields per commit: subject, Change:,
// Take-in:, Milestone:.
const ciLogFields = 4

// classifyGroup reads base..head's commits: every Change:, Take-in: and
// close record, and the apex: records.
func (p *Project) classifyGroup(ctx context.Context, base, head string, res *CIResult) error {
	out, err := gitRaw(ctx, p.Root, "log", "--format=%s%x1f%(trailers:key=Change,valueonly,separator=%x2c)%x1f%(trailers:key=Take-in,valueonly,separator=%x2c)%x1f%(trailers:key=Milestone,valueonly,separator=%x2c)%x1e", base+".."+head)
	if err != nil {
		return &FailError{Code: CodeGitStatusFailed, Msg: "cannot read the merge group's commits " + short(base) + ".." + short(head) + ": " + err.Error() + " — check out with full history (fetch-depth: 0)"}
	}
	add := func(list *[]string, v string) {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(*list, v) {
			*list = append(*list, v)
		}
	}
	for rec := range strings.SplitSeq(out, "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) < ciLogFields {
			continue
		}
		subject, change, takeIn, ms := f[0], f[1], f[2], strings.TrimSpace(f[3])
		switch {
		case change != "":
			add(&res.Changes, change)
		case takeIn != "":
			add(&res.TakeIns, takeIn)
		case ms != "" && strings.HasPrefix(subject, "apex: "+ms+" done"):
			add(&res.Closes, ms)
		case strings.HasPrefix(subject, "apex: "):
			add(&res.Records, subject)
		}
	}
	if len(res.Changes)+len(res.TakeIns)+len(res.Closes)+len(res.Records) == 0 {
		return &FailError{Code: CodeCIOutsideFlow, Msg: "the merge group " + short(base) + ".." + short(head) + " carries no commit of the flow (no Change: or Take-in: trailer, no close record, no apex: record) — main and milestone branches take only ape's pull requests"}
	}
	return nil
}

// CICheck is `ape ci check`: it runs what the merge group base..head needs,
// with head checked out, and reports. A red check is CodeCIRed beside the
// result.
func (p *Project) CICheck(ctx context.Context, base, head string, opt RunOptions) (*CIResult, error) {
	res := &CIResult{Base: base, Head: head}
	if err := p.classifyGroup(ctx, base, head, res); err != nil {
		return res, err
	}
	dir := filepath.Join(p.LogsDir(), "ci-"+short(head))
	if len(res.Changes)+len(res.TakeIns) > 0 {
		if cmd := p.regressionCmd(); cmd != "" {
			r := p.runRegression(ctx, cmd, dir, "", opt)
			res.Regression = &r
			if r.Exit != 0 {
				res.Red = append(res.Red, "regression: "+cmd+" exited "+strconv.Itoa(r.Exit))
			}
		}
		spec, _ := withoutFull(p.EffectiveSpecChecks(nil))
		res.SpecChecks = len(spec)
		results := p.RunChecks(ctx, dir, "", spec, opt)
		for i := range results {
			if r := &results[i]; r.Exit != 0 {
				r.Tail = lastN(r.Tail, 15)
				res.SpecFailed = append(res.SpecFailed, *r)
				res.Red = append(res.Red, "scenario: "+r.Task)
			}
		}
	}
	for _, ms := range res.Closes {
		red, err := p.ciGate(ctx, ms, opt, res)
		if err != nil {
			return res, err
		}
		res.Red = append(res.Red, red...)
	}
	if len(res.Records) > 0 {
		res.Findings = p.Check() //nolint:contextcheck // the project check reads files only, as `ape project check` runs it
		for _, f := range res.Findings {
			res.Red = append(res.Red, "project check: "+f.File+": "+f.Message)
		}
	}
	res.Green = len(res.Red) == 0
	if !res.Green {
		return res, &FailError{Code: CodeCIRed, Msg: "the merge group is red: " + strings.Join(limit(res.Red, 8), "; ")}
	}
	return res, nil
}

// ciGate runs milestone ms's gate with its full run for a close in the
// group, its gate folders put back where the close moved them from (the
// gate runs as approved). Red is what is red; a person's accepted red
// passes only when the red set is the one accepted (OQ26).
func (p *Project) ciGate(ctx context.Context, ms string, opt RunOptions, res *CIResult) ([]string, error) {
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil, err
	}
	rec := recs[ms]
	if rec != nil {
		for _, a := range rec.GateArchived {
			from, to := filepath.Join(p.Root, filepath.FromSlash(a.From)), filepath.Join(p.Root, filepath.FromSlash(a.To))
			if _, err := os.Stat(from); err == nil {
				continue
			}
			if err := os.CopyFS(from, os.DirFS(to)); err != nil {
				return nil, fmt.Errorf("putting the gate folder %s back from %s: %w", a.From, a.To, err)
			}
		}
	}
	g, err := p.runGate(ctx, ms, opt, true)
	if err != nil {
		return nil, err
	}
	res.Gates = append(res.Gates, g)
	if g.Run.Green() {
		return nil, nil
	}
	now := gateRedSet(&g.Run)
	if rec != nil && rec.AcceptRed != nil && rec.AcceptRed.Gate != nil {
		accepted := gateRedSet(rec.AcceptRed.Gate)
		if slices.Equal(now, accepted) {
			return nil, nil
		}
		return []string{fmt.Sprintf("%s gate: red on %s, accepted red only on %s", ms, strings.Join(now, ", "), strings.Join(accepted, ", "))}, nil
	}
	return []string{ms + " gate: " + g.Run.Why()}, nil
}

// gateRedSet is what is red in a gate run, sorted: the gate command, each
// red scenario, the regression.
func gateRedSet(r *GateRun) []string {
	var out []string
	if r.Exit != 0 {
		out = append(out, "gate command")
	}
	for i := range r.SpecFailed {
		out = append(out, "scenario "+r.SpecFailed[i].Task)
	}
	if r.Regression != nil && r.Regression.Failed {
		out = append(out, "regression")
	}
	sort.Strings(out)
	return out
}

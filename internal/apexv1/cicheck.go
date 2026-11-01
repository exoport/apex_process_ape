package apexv1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Main's merge queue check (v1.5 stage 1a, sections 5.3–5.5; D702: the
// queue is main's only): CI runs `ape ci check --base <sha> --head <sha>`
// on each merge group, with the group's head checked out. ape reads what
// the group carries from its commits' trailers: a milestone's close (its
// changes and take-ins ride it), the milestone gate with its full run,
// where an accepted red passes only on that red set (OQ26); records alone,
// the project check. Anything else is red: main takes nothing from outside
// the flow.

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
	Gates    []*GateResult `json:"gates,omitempty"`
	Findings []Finding     `json:"findings,omitempty"`
	Green    bool          `json:"green"`
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
	// D702: main's queue takes a milestone's close PR (its changes and
	// take-ins ride it) and record PRs; a change or take-in on its own
	// reaches main only inside its milestone's close.
	if len(res.Closes) == 0 && len(res.Changes)+len(res.TakeIns) > 0 {
		return res, &FailError{Code: CodeCIOutsideFlow, Msg: "the merge group " + short(base) + ".." + short(head) + " carries a change or take-in (" + strings.Join(append(append([]string{}, res.Changes...), res.TakeIns...), ", ") + ") without its milestone's close — changes and take-ins land on milestone/<M>, and reach main only through the close PR"}
	}
	// R4's required check refuses a PR that is not ape's (D703, research
	// finding 3): a subject alone proves nothing.
	if err := p.requireApeCommits(ctx, base, head, res); err != nil {
		return res, err
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

// recordSubjectRe is a roadmap record's subject: what a record PR's commits
// say.
var recordSubjectRe = regexp.MustCompile(`^apex: [A-Za-z0-9._-]+ (proposed|approved|re-approved.*|claimed by \S+)$`)

// requireApeCommits refuses a merge group into main that carries any
// commit not ape's (D703): every non-merge commit must be on
// origin/milestone/<M> of a close the group carries — a branch only
// apex-member writes (R1) — or a record commit whose subject is a roadmap
// record and whose diff touches only roadmap.md and milestones.json
// (nothing, for a claim). The milestone branches are fetched first when
// the checkout has a remote.
func (p *Project) requireApeCommits(ctx context.Context, base, head string, res *CIResult) error {
	remote := p.trunkRemote(ctx)
	if len(res.Closes) > 0 {
		_, _ = gitWithEnv(ctx, p.Root, noPromptEnv, "fetch", "--quiet", "--no-tags", remote,
			"+refs/heads/"+MilestoneBranchPrefix+"*:refs/remotes/"+remote+"/"+MilestoneBranchPrefix+"*")
	}
	out, err := git(ctx, p.Root, "rev-list", "--no-merges", base+".."+head)
	if err != nil {
		return &FailError{Code: CodeGitStatusFailed, Msg: "cannot list the merge group's commits: " + err.Error()}
	}
	allowed := map[string]bool{p.Rel(p.RoadmapMD()): true, p.Rel(p.MilestonesJSON()): true}
	var foreign []string
	for sha := range strings.FieldsSeq(out) {
		if p.onClosedMilestone(ctx, remote, sha, res.Closes) || p.isRecordCommit(ctx, sha, allowed) {
			continue
		}
		subject, _ := git(ctx, p.Root, "log", "-1", "--format=%s", sha)
		foreign = append(foreign, short(sha)+" "+subject)
	}
	if len(foreign) == 0 {
		return nil
	}
	return &FailError{Code: CodeCIOutsideFlow, Msg: "the merge group " + short(base) + ".." + short(head) + " carries commits that are not ape's: " + strings.Join(limit(foreign, 5), "; ") +
		" — main takes only a milestone's close PR (its commits on milestone/<M>) and record PRs"}
}

// onClosedMilestone reports sha on remote/milestone/<M> for a close in the
// group.
func (p *Project) onClosedMilestone(ctx context.Context, remote, sha string, closes []string) bool {
	for _, ms := range closes {
		if _, err := git(ctx, p.Root, "merge-base", "--is-ancestor", sha, "refs/remotes/"+remote+"/"+MilestoneBranch(ms)); err == nil {
			return true
		}
	}
	return false
}

// isRecordCommit reports a roadmap record: its subject, and a diff only of
// the allowed paths.
func (p *Project) isRecordCommit(ctx context.Context, sha string, allowed map[string]bool) bool {
	subject, err := git(ctx, p.Root, "log", "-1", "--format=%s", sha)
	if err != nil || !recordSubjectRe.MatchString(subject) {
		return false
	}
	files, err := git(ctx, p.Root, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", sha)
	if err != nil {
		return false
	}
	for f := range strings.FieldsSeq(files) {
		if !allowed[f] {
			return false
		}
	}
	return true
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

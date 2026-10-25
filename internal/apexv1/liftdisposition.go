package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
)

// `ape project lift-disposition --check` (v1.4, B10, framework ANSWER-W6,
// D567): the check of the framework's migration v1.4.0_seq-01, which
// supersedes v1.0.0_seq-02..05. Their shell check matched any
// **/lift-disposition.md in the repository, so a project with a tracked
// testdata/lift-disposition.md read as half-applied for ever (D467). This
// reads only the project's own <development_folder>/lift-disposition.md:
//
//   - not lifted (no commit whose message starts `apex: lift` reachable
//     from HEAD): the lift's dry run decides — lift.not-v0 (not a v0
//     project, or v1 already) is applied (0), anything else not applied
//     (1), a dry run that cannot run 2;
//   - lifted (any such commit, whatever came after): applied (0) when the
//     disposition file is not in git's index — a staged `git rm` counts,
//     an untracked copy does not — else not applied (1).
//
// _apex/state/lift-disposition.json plays no part.

// CodeLiftDispositionLeft: the check's "not applied".
const CodeLiftDispositionLeft = "lift.disposition-left"

// CodeLiftCheckFailed: the check could not tell (exit 2).
const CodeLiftCheckFailed = "lift.check-failed"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeLiftDispositionLeft, 1, "replan", "`ape project lift-disposition --check` (v1.4, the framework migration's check: 0 applied, 1 not applied, 2 the check failed): a lifted project still tracks <development_folder>/lift-disposition.md, or a v0 project is not lifted yet — apex-plan disposes of the list (the migration's judged body)"},
		CodeDoc{"usage.check-only", 2, "stop", "`ape project lift-disposition` without --check: the verb only checks (the disposing is the migration's judged body)"},
		CodeDoc{CodeLiftCheckFailed, 2, "stop", "`ape project lift-disposition --check`: git or the lift's dry run could not run, so it cannot tell — never read as \"not applied\""},
	)
}

// LiftDispositionCheck is the check; nil when applied.
func (p *Project) LiftDispositionCheck(ctx context.Context) error {
	failed := func(why string) error {
		return &FailError{Code: CodeLiftCheckFailed, Msg: why + " — the check cannot tell whether the migration is applied"}
	}
	if !p.isWorkTree(ctx) {
		return failed("not a git repository")
	}
	lifted, err := git(ctx, p.Root, "log", "-1", "--format=%H", "--grep=^"+LiftSubjectPrefix)
	if err != nil {
		return failed("git log failed: " + err.Error())
	}
	if lifted == "" {
		return p.unliftedDisposition(ctx)
	}
	rel := path.Join(p.DevRel, "lift-disposition.md")
	out, err := git(ctx, p.Root, "ls-files", "--", rel)
	if err != nil {
		return failed("git ls-files failed: " + err.Error())
	}
	if strings.TrimSpace(out) != "" {
		return &FailError{Code: CodeLiftDispositionLeft, Msg: rel + " is still tracked — the lift's disposition list awaits review: apex-plan disposes of it and removes it in its own commit"}
	}
	return nil
}

// unliftedDisposition is the check for a project no lift commit reaches:
// the lift's dry run decides — lift.not-v0 is applied.
func (p *Project) unliftedDisposition(ctx context.Context) error {
	res, lerr := Lift(ctx, p.Root, LiftOptions{DryRun: true})
	var fe *FailError
	if errors.As(lerr, &fe) && fe.Code == "lift.not-v0" {
		return nil
	}
	if res != nil {
		if data, err := json.Marshal(res); err == nil && strings.Contains(string(data), `"lift.not-v0"`) {
			return nil
		}
	}
	if res == nil && lerr != nil && fe == nil {
		return &FailError{Code: CodeLiftCheckFailed, Msg: "the lift's dry run failed: " + lerr.Error() + " — the check cannot tell whether the migration is applied"}
	}
	return &FailError{Code: CodeLiftDispositionLeft, Msg: "an APEX v0 project not lifted yet — `ape project lift` first; the migration applies after it"}
}

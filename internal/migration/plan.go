package migration

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Applied is one row of the ledger in `_apex/framework.yaml`.
//
// An ORDERED list, not a set. "Which migrations ran, in what order" is
// what an operator asks when one half-applies, and a set cannot answer
// it.
type Applied struct {
	ID        string `json:"id"         yaml:"id"`
	Version   string `json:"version"    yaml:"version"`
	AppliedAt string `json:"applied_at" yaml:"applied_at"`
}

// Row is one entry's place in the plan.
type Row struct {
	Entry `json:",inline" yaml:",inline"`

	State string `json:"state" yaml:"state"`
	Check string `json:"check" yaml:"check"`
	// CheckDetail carries why a check could not run, or its exit code.
	CheckDetail string `json:"check_detail,omitempty" yaml:"check_detail,omitempty"`
	// Source is `ledger` or `check` on an applied row, naming what
	// decided it. Empty otherwise.
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	// AppliedAt is the ledger's stamp, when the ledger is the source.
	AppliedAt string `json:"applied_at,omitempty" yaml:"applied_at,omitempty"`
	// SupersededBy names the present entry that supersedes this one.
	SupersededBy string `json:"superseded_by,omitempty" yaml:"superseded_by,omitempty"`
	// Runnable is the single predicate the runner acts on: a derivable,
	// pending, non-superseded, non-unrunnable entry. Computed once here so
	// `--plan` and the run cannot disagree about what would happen.
	Runnable bool `json:"runnable" yaml:"runnable"`
}

// Plan is the whole projection.
type Plan struct {
	Dir string `json:"dir" yaml:"dir"`
	// Present is false when the project has no migration folder — a
	// normal state on a framework that ships no list, and NOT a finding.
	Present bool  `json:"present" yaml:"present"`
	Rows    []Row `json:"rows"    yaml:"rows"`
	// Cycle names the entries in an `after:` cycle. Non-empty means the
	// order is undefined and every entry in it is unrunnable.
	Cycle []string `json:"cycle,omitempty" yaml:"cycle,omitempty"`
}

// Counts summarises a plan for a one-line report.
type Counts struct {
	Pending      int `json:"pending"       yaml:"pending"`
	Applied      int `json:"applied"       yaml:"applied"`
	HalfApplied  int `json:"half_applied"  yaml:"half_applied"`
	CannotTell   int `json:"cannot_tell"   yaml:"cannot_tell"`
	Superseded   int `json:"superseded"    yaml:"superseded"`
	Runnable     int `json:"runnable"      yaml:"runnable"`
	JudgedToRun  int `json:"judged_to_run" yaml:"judged_to_run"`
	BlockingOpen int `json:"blocking_open" yaml:"blocking_open"`
}

// Counts tallies the plan.
func (p *Plan) Counts() Counts {
	var c Counts
	for i := range p.Rows {
		r := &p.Rows[i]
		switch r.State {
		case StateApplied:
			c.Applied++
		case StateHalfApplied:
			c.HalfApplied++
		case StateCannotTell:
			c.CannotTell++
		case StateSuperseded:
			c.Superseded++
		case StatePending:
			c.Pending++
			if r.Blocking {
				c.BlockingOpen++
			}
			if r.Runnable {
				c.Runnable++
			} else if r.IsJudged() {
				c.JudgedToRun++
			}
		}
	}
	return c
}

// Runner executes a shell line in a directory and reports its exit code.
//
// An interface so the plan is testable without a shell, and so a caller
// that must not execute anything (`--plan` with checks off) can pass one
// that refuses.
type Runner interface {
	// Run returns the command's exit code. A non-nil error means the
	// command could not be run AT ALL — not that it exited non-zero. That
	// distinction is the whole difference between `unsatisfied` and
	// `cannot-run`, and collapsing it is what turns a missing `jq` into a
	// migration that re-applies forever.
	Run(ctx context.Context, dir, line string) (exitCode int, err error)
}

// CheckTimeout bounds a `check:`. A check is a question about the project
// on disk, so it has no business taking longer; a `command:` is
// deliberately NOT bounded here, because a derivable entry may dispatch a
// whole Claude session.
const CheckTimeout = 2 * time.Minute

// ShellRunner runs a line through the platform shell.
//
// A shell is required rather than an argv split: the framework's own
// entries pipe into `jq` and quote embedded JSON, and splitting those on
// whitespace would silently run something else.
type ShellRunner struct{}

func (ShellRunner) Run(ctx context.Context, dir, line string) (int, error) {
	name, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		name, flag = "cmd", "/c"
	}
	cmd := exec.CommandContext(ctx, name, flag, line)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		// The command ran and said no. That is a verdict, not a failure
		// to obtain one.
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// refusingRunner is what `--plan --no-check` uses: every check reports
// not-run rather than a verdict nobody obtained.
type refusingRunner struct{}

func (refusingRunner) Run(context.Context, string, string) (int, error) {
	return -1, errors.New("checks disabled")
}

// NoCheckRunner returns a Runner that executes nothing.
func NoCheckRunner() Runner { return refusingRunner{} }

// BuildPlan resolves every entry's state.
//
// runChecks false skips the checks entirely and reports every row's check
// as `not-run` — which is NOT `cannot-run`: one says nobody asked, the
// other says the question was asked and could not be answered. A row
// whose check was not run falls back to the ledger alone.
func BuildPlan(ctx context.Context, dir, projectRoot string, entries []Entry, ledger []Applied,
	runner Runner, runChecks bool,
) *Plan {
	ordered, cycle := Order(entries)
	p := &Plan{Dir: dir, Present: dir != "", Cycle: cycle}

	inLedger := make(map[string]Applied, len(ledger))
	for _, a := range ledger {
		inLedger[a.ID] = a
	}
	superseder := map[string]string{}
	for i := range ordered {
		for _, id := range ordered[i].Supersedes {
			superseder[id] = ordered[i].Label()
		}
	}

	for i := range ordered {
		p.Rows = append(p.Rows, resolveRow(ctx, projectRoot, &ordered[i],
			inLedger, superseder, runner, runChecks))
	}
	return p
}

func resolveRow(ctx context.Context, projectRoot string, e *Entry,
	inLedger map[string]Applied, superseder map[string]string,
	runner Runner, runChecks bool,
) Row {
	r := Row{Entry: *e, Check: CheckNone}

	if by, ok := superseder[e.ID]; ok && e.ID != "" {
		r.State, r.SupersededBy = StateSuperseded, by
		return r
	}

	r.Check, r.CheckDetail = runCheck(ctx, projectRoot, e, runner, runChecks)

	applied, recorded := inLedger[e.ID]
	switch {
	case recorded && r.Check == CheckUnsatisfied:
		// The ledger says it ran; the project says the post-condition does
		// not hold. Both facts are real and neither is discarded.
		r.State, r.Source, r.AppliedAt = StateHalfApplied, SourceLedger, applied.AppliedAt
	case recorded:
		r.State, r.Source, r.AppliedAt = StateApplied, SourceLedger, applied.AppliedAt
	case r.Check == CheckSatisfied:
		// Already in the shape, with no ledger row — a project upgraded by
		// hand, or one born after the change. Reported as applied with the
		// check named as the source; the ledger is NOT backfilled, because
		// ape records what ape did and nothing else.
		r.State, r.Source = StateApplied, SourceCheck
	case r.Check == CheckCannotRun:
		// Unapplied and unverifiable. Never acted on: acting here is the
		// re-application this state exists to prevent.
		r.State = StateCannotTell
	default:
		r.State = StatePending
	}

	r.Runnable = r.State == StatePending && !e.IsJudged() && !e.Unrunnable
	return r
}

// ExitAnsweredNo is the only non-zero exit read as a VERDICT.
//
// This distinction is load-bearing and was not obvious. A check runs
// through a shell, and a shell reports a missing binary as exit 127
// rather than as a failure to start — so a project without `jq`, running
// the framework's own `… | jq -e '…'` check, would come back non-zero.
// Reading every non-zero code as "unsatisfied" would then make the entry
// PENDING and the runner would apply it, which is exactly the
// re-application the cannot-tell state exists to prevent, arriving
// through the one door nobody watches.
//
// So the mapping follows the convention every tool these checks are built
// from already uses — `grep -q`, `jq -e`, `test`: **1 means the question
// was answered "no"; 2 and above mean the check itself malfunctioned**
// (2 usage, 3 jq compile error, 126 not executable, 127 not found). An
// entry whose check malfunctioned is unapplied AND unverifiable, and is
// never run.
//
// A framework check that wants to report "not applied" must therefore
// exit 1, which is what `jq -e` and `grep -q` already do.
const ExitAnsweredNo = 1

func runCheck(ctx context.Context, projectRoot string, e *Entry, runner Runner, runChecks bool,
) (verdict, detail string) {
	if strings.TrimSpace(e.Check) == "" {
		return CheckNone, ""
	}
	if !runChecks || runner == nil {
		return CheckNotRun, ""
	}
	cctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	code, err := runner.Run(cctx, projectRoot, e.Check)
	switch {
	case err != nil:
		return CheckCannotRun, err.Error()
	case code == 0:
		return CheckSatisfied, ""
	case code == ExitAnsweredNo:
		return CheckUnsatisfied, "exit 1"
	default:
		return CheckCannotRun, fmt.Sprintf(
			"exit %d — 1 means \"not applied\"; anything above it means the check itself failed", code)
	}
}

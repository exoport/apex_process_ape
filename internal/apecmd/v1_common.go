package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

// The APEX v1 commands (project, status, milestone, change, decision,
// hook) share one shape: --cwd and --json on every leaf, exit 0 ok, 1 a
// check failed, 2 usage, 3 conflict, and a one-line remediation on every
// failure. None of them is a run, so none refuses under APE_SESSION:
// skills call them inside `ape task` sessions all the time.

// Exit 3 for the v1 commands: a conflict with another change or with the
// specs, nothing written. Command-local like `ape framework`'s codes.
const exitV1Conflict = 3

type v1Flags struct {
	cwd     string
	json    bool
	timeout time.Duration
}

func addV1Flags(cmd *cobra.Command, f *v1Flags) {
	cmd.Flags().StringVar(&f.cwd, "cwd", "", "project directory (default: the nearest parent with _apex/config.yaml or development/)")
	cmd.Flags().BoolVar(&f.json, "json", false, "machine output (JSON on stdout)")
}

func addV1TimeoutFlag(cmd *cobra.Command, f *v1Flags) {
	cmd.Flags().DurationVar(&f.timeout, "timeout", 0, "limit for each command run, overriding _apex/config.yaml timeouts: "+
		"(default: timeouts.check, else 15m, per check; timeouts.regression, else 30m, for the regression command; "+
		"timeouts.gate, else 30m, for a gate command); exit 124 when exceeded")
}

func (f *v1Flags) runOptions() apexv1.RunOptions { return apexv1.RunOptions{Timeout: f.timeout} }

// v1Project resolves the project a v1 command acts on.
func v1Project(f *v1Flags) (*apexv1.Project, error) {
	dir := f.cwd
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = apexv1.FindRoot(wd)
	}
	p, err := apexv1.Open(dir)
	if err != nil {
		return nil, usageErr(fmt.Errorf("cannot read %s: %w — fix the YAML", apexv1.ConfigPath(dir), err))
	}
	return p, nil
}

// v1RequireInit refuses a command that needs the development folder, and
// any v1 command in an APEX v0 project (backlog 38).
func v1RequireInit(cmd *cobra.Command, f *v1Flags, p *apexv1.Project) error {
	if err := v1RefuseV0(cmd, f, p); err != nil {
		return err
	}
	if !p.IsV1() {
		return v1Fail(cmd, f, &apexv1.UsageError{Code: "project.not-initialized", Msg: fmt.Sprintf("%s has no %s/ — run `ape project init` first", p.Root, p.DevRel)}, nil)
	}
	return nil
}

// v1RefuseV0 refuses a v1 command in an APEX v0 project: its pinned v0 ape
// runs it, or the project is lifted first.
func v1RefuseV0(cmd *cobra.Command, f *v1Flags, p *apexv1.Project) error {
	if apexv1.IsV0Project(p.Root) {
		return v1Fail(cmd, f, apexv1.V0ProjectError(), nil)
	}
	return nil
}

// v1Emit writes a successful result: JSON when asked, else human text.
func v1Emit(w io.Writer, f *v1Flags, v any, human func(io.Writer)) error {
	if f.json {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	human(w)
	return nil
}

// v1Fail maps an apexv1 error onto the exit table, printing the findings
// and the one-line remediation (to stdout as JSON under --json, else to
// stderr). partial is any result worth showing beside the failure.
func v1Fail(cmd *cobra.Command, f *v1Flags, err error, partial any) error {
	if err == nil {
		return nil
	}
	code := ExitRunFailed
	var findings []apexv1.Finding
	var conflicts []string
	var fe *apexv1.FailError
	var ue *apexv1.UsageError
	var ce *apexv1.ConflictError
	var ee *exitError
	switch {
	case errors.As(err, &ee):
		return err
	case errors.As(err, &fe):
		findings = fe.Findings
	case errors.As(err, &ue):
		code = ExitUsage
	case errors.As(err, &ce):
		code = exitV1Conflict
		conflicts = ce.Conflicts
	}
	if f.json {
		out := map[string]any{"ok": false, "exit": code, "error": err.Error(), "diagnostics": apexv1.Diagnostics(err)}
		if len(findings) > 0 {
			out["findings"] = apexv1.ClassifyFindings(findings)
		}
		if len(conflicts) > 0 {
			out["conflicts"] = conflicts
		}
		if partial != nil {
			out["result"] = partial
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(out); encErr != nil {
			return encErr
		}
		return reportedErr(code, err)
	}
	stderr := cmd.ErrOrStderr()
	for _, fd := range findings {
		fmt.Fprintln(stderr, "  "+fd.String())
	}
	for _, c := range conflicts {
		fmt.Fprintln(stderr, "  conflict: "+c)
	}
	if ce != nil {
		fmt.Fprintln(stderr, "Error: "+ce.Msg)
	} else {
		fmt.Fprintln(stderr, "Error: "+err.Error())
	}
	return reportedErr(code, err)
}

// printChecks prints every task check, and of the effective spec's
// scenario checks only the red ones and a count of the green.
func printChecks(w io.Writer, rs []apexv1.CheckResult, tails bool) {
	greenScenarios := 0
	for i := range rs {
		r := &rs[i]
		if r.Kind == apexv1.KindScenario && r.Exit == 0 && !r.Flaky {
			greenScenarios++
			continue
		}
		verdict := "green"
		switch {
		case r.PreExistingRed:
			verdict = fmt.Sprintf("red (%s), red at baseline too: not a failure", r.Outcome())
		case r.Reason != "":
			verdict = "RED (" + r.Reason + ")"
		case r.Exit != 0:
			verdict = fmt.Sprintf("RED (exit %d)", r.Exit)
		case r.Flaky:
			verdict = fmt.Sprintf("green (flaky: %d tries)", r.Tries)
		}
		label := r.Task
		if r.Kind == apexv1.KindScenario {
			label = "scenario " + r.Task
			if r.Delta {
				label += " (this change)"
			}
		}
		fmt.Fprintf(w, "  %-4s %-14s %5.1fs  %s\n", label, verdict, r.Secs, r.Cmd)
		if r.Reason != "" {
			fmt.Fprintln(w, "       "+r.Reason+": "+r.Detail)
		}
		if tails && r.Red() {
			for _, l := range lastLinesOf(r.Tail, 15) {
				fmt.Fprintln(w, "       | "+l)
			}
			if r.Log != "" {
				fmt.Fprintln(w, "       full output: "+r.Log)
			}
		}
	}
	if greenScenarios > 0 {
		fmt.Fprintf(w, "  %d scenario check(s) of the effective spec green\n", greenScenarios)
	}
}

// printRegression is one line on the regression command's verdict at a
// milestone gate.
func printRegression(w io.Writer, v *apexv1.RegressionVerdict) {
	if v == nil {
		return
	}
	base := "no baseline for it in this milestone"
	if v.BaselineExit != nil {
		base = fmt.Sprintf("baseline exit %d (%s)", *v.BaselineExit, v.BaselineChange)
	}
	verdict := "green"
	switch {
	case v.Failed && v.Reason != "":
		verdict = "RED (" + v.Reason + ": " + v.Detail + ")"
	case v.Failed:
		verdict = fmt.Sprintf("RED (exit %d)", v.Exit)
	case v.PreExistingRed:
		verdict = fmt.Sprintf("red (exit %d), red at baseline too: not a failure", v.Exit)
	}
	fmt.Fprintf(w, "  regression %s — %s: %s\n", verdict, base, v.Cmd)
	if v.Exit != 0 && v.Log != "" {
		fmt.Fprintln(w, "       full output: "+v.Log)
	}
}

func lastLinesOf(xs []string, n int) []string {
	if len(xs) > n {
		return xs[len(xs)-n:]
	}
	return xs
}

package apexv1

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The plan-time gate self-test (v1.4, D541, D543 B2). apex-plan writes a
// gate before the milestone exists, and checks it two ways: the gate run
// with --stub <file> passes, and the plain gate is red. Run from the
// planner's own Bash tool, the plain run is outside ape's reaping, and a
// gate whose own lifecycle code lost its server's PID left the server
// running (taskforge M4, v1.3 row 3). `ape milestone gate-test <folder>`
// runs both through the check runner instead — the gate tier's timeouts,
// its own process group, F15's leftover ending — and records nothing: a
// self-test is no gate run of any milestone. The pre-tool guard
// (GateGuard) denies the plain run from a session's Bash, and names this.

// CodeGateStubRed: the stub run of a gate self-test is red.
const CodeGateStubRed = "milestone.gate-stub-red"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeGateStubRed, 1, "replan", "`ape milestone gate-test` (v1.4): the gate run with --stub <file> is red — a gate must be able to pass, so the gate or its stub is wrong: decide which from the brief, fix it, and run the self-test again"},
		CodeDoc{"usage.gate-folder", 2, "stop", "`ape milestone gate-test`: the folder is not a gate folder under <dev>/gates/ holding an executable gate.sh, or --stub names no file"},
	)
}

// gateFolderMilestoneRe is a gate folder's milestone prefix, M4-time-entries.
var gateFolderMilestoneRe = regexp.MustCompile(`^(M\d+)-`)

// GateTestRun is one run of a self-test.
type GateTestRun struct {
	// Mode is "stub" or "plain".
	Mode      string   `json:"mode"`
	Cmd       string   `json:"cmd"`
	Exit      int      `json:"exit"`
	Secs      float64  `json:"secs"`
	Log       string   `json:"log"`
	Reason    string   `json:"reason,omitempty"`
	Leftovers []string `json:"leftovers,omitempty"`
	// Verdict is "pass" (stub green, plain red), else "fail".
	Verdict string   `json:"verdict"`
	Tail    []string `json:"tail,omitempty"`
}

// GateTestResult is what `ape milestone gate-test` reports.
type GateTestResult struct {
	Folder    string        `json:"folder"`
	Milestone string        `json:"milestone,omitempty"`
	Runs      []GateTestRun `json:"runs"`
	OK        bool          `json:"ok"`
}

// GateTest is `ape milestone gate-test <folder> [--stub <file>]`: the
// stub run (when the folder has a stub, or --stub names one), then the
// plain run. The stub run must exit 0; the plain run must be red, and
// runnable: green is milestone.gate-green, a missing or unparseable
// script, a missing requirement, 126, 127 or a timeout is
// milestone.gate-broken — the same verdicts approve's self-check gives.
func (p *Project) GateTest(ctx context.Context, folder, stub string, progress io.Writer) (*GateTestResult, error) {
	if progress == nil {
		progress = io.Discard
	}
	usage := func(msg string) error {
		return &UsageError{Code: "usage.gate-folder", Action: ActionStop, Msg: msg}
	}
	abs := folder
	switch {
	case !strings.ContainsAny(folder, `/\`):
		// A bare folder name, M4-time-entries: the long run's worker
		// re-runs the command with it (its scope).
		abs = filepath.Join(p.GatesDir(), folder)
	case !filepath.IsAbs(abs):
		wd, _ := os.Getwd()
		abs = filepath.Join(wd, abs)
	}
	rel, err := filepath.Rel(p.GatesDir(), abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(filepath.ToSlash(rel), "/") {
		return nil, usage(folder + " is not a gate folder: a gate folder is " + p.Rel(p.GatesDir()) + "/<M>-<slug>/")
	}
	dir := p.Rel(abs)
	script := dir + "/gate.sh"
	if st, err := os.Stat(filepath.Join(abs, "gate.sh")); err != nil || !st.Mode().IsRegular() {
		return nil, usage(dir + " holds no gate.sh")
	}
	res := &GateTestResult{Folder: dir, Runs: []GateTestRun{}}
	if m := gateFolderMilestoneRe.FindStringSubmatch(rel); m != nil {
		res.Milestone = m[1]
	}
	if stub == "" {
		if st, err := os.Stat(filepath.Join(abs, "stub")); err == nil && st.Mode().IsRegular() {
			stub = dir + "/stub"
		}
	} else {
		sabs := stub
		if !filepath.IsAbs(sabs) {
			wd, _ := os.Getwd()
			sabs = filepath.Join(wd, sabs)
		}
		if st, err := os.Stat(sabs); err != nil || !st.Mode().IsRegular() {
			return nil, usage("--stub " + stub + " is not a file")
		}
		stub = p.Rel(sabs)
	}
	cfg, _ := LoadConfig(p.Root)
	lim := cfg.Timeouts.Limits(KindGate, 0)
	env := gateEnv(res.Milestone, HeadSHA(ctx, p.Root))
	p.ensureState()
	name := res.Milestone
	if name == "" {
		name = filepath.Base(abs)
	}
	run := func(mode, cmd string) GateTestRun {
		fmt.Fprintf(progress, "%s: the gate's %s run (timeout %s): %s\n", dir, mode, lim.Max, cmd)
		pc := PlannedCheck{Ref: "gate-test-" + name + "-" + mode, Kind: KindGate, Cmd: cmd, Env: env}
		r := p.runWithRetries(ctx, &pc, filepath.Join(p.LogsDir(), "gate-test-"+name+"-"+mode+".log"), lim, nil, 0, p.isWorkTree(ctx))
		return GateTestRun{Mode: mode, Cmd: cmd, Exit: r.Exit, Secs: r.Secs, Log: r.Log, Reason: r.Reason, Leftovers: r.Leftovers, Tail: lastN(r.Tail, 15)}
	}
	last := func(r GateTestRun) string {
		if len(r.Tail) == 0 {
			return ""
		}
		return ". Last output (" + r.Log + "): " + strings.Join(lastN(r.Tail, 5), " | ")
	}
	// A gate that cannot run is broken whichever way it is run.
	if why := p.gateUnrunnable(ctx, "./"+script); why != "" {
		return res, &FailError{Code: CodeGateBroken, Msg: dir + "'s gate cannot run: " + why + " — re-plan the gate so every script it names exists and runs"}
	}
	var failure error
	if stub != "" {
		r := run("stub", "./"+script+" --stub "+stub)
		if err := interrupted(ctx, dir+"'s gate self-test"); err != nil {
			return nil, err
		}
		r.Verdict = "pass"
		if r.Exit != 0 {
			r.Verdict = "fail"
			failure = &FailError{Code: CodeGateStubRed, Msg: fmt.Sprintf("%s's stub run is red (exit %d in %.1fs)", dir, r.Exit, r.Secs) +
				" — a gate must be able to pass: decide from the brief whether the gate or the stub is wrong, fix it, and run the self-test again" + last(r)}
		}
		res.Runs = append(res.Runs, r)
	}
	plainCmd := "./" + script
	r := run("plain", plainCmd)
	if err := interrupted(ctx, dir+"'s gate self-test"); err != nil {
		return nil, err
	}
	r.Verdict = "pass"
	var plainFail error
	switch r.Exit {
	case 0:
		plainFail = &FailError{Code: CodeGateGreen, Msg: fmt.Sprintf("%s's plain gate is already green (exit 0 in %.1fs)", dir, r.Secs) +
			" — a green gate proves nothing the milestone adds: re-plan the gate so it is red until the milestone's work is done" + last(r)}
	case ExitTimeout:
		plainFail = &FailError{Code: CodeGateBroken, Msg: dir + "'s plain gate timed out (" + r.Reason + ", timeouts.gate) — re-plan the gate so it ends" + last(r)}
	case exitNotExecutable, exitNotFound:
		plainFail = &FailError{Code: CodeGateBroken, Msg: fmt.Sprintf("%s's plain gate exited %d (a command it runs was not found or is not executable)", dir, r.Exit) +
			" — re-plan the gate so every command it runs exists" + last(r)}
	}
	if plainFail != nil {
		r.Verdict = "fail"
		if failure == nil {
			failure = plainFail
		}
	}
	res.Runs = append(res.Runs, r)
	res.OK = failure == nil
	return res, failure
}

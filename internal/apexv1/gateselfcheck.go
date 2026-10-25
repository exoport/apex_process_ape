package apexv1

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The gate self-check (D70 B). `ape milestone approve` runs the gate once
// against the current tree before it records the approval: a gate must run
// and must be red, since it is red until the milestone's work is done. A
// gate already green proves nothing about that work (milestone.gate-green);
// one that cannot run — a script it names is missing or fails `bash -n`,
// it exits 126 or 127 (not executable, not found), a requirement is
// missing, or it times out — is broken (milestone.gate-broken). Both are
// refused, action replan: apex-plan writes the gate again. Any other
// non-zero exit is red: the approval proceeds and records the run. The run
// is synchronous (Q7): a person runs approve from a terminal, so it prints
// its progress there and holds no detached worker.

// Codes of the self-check.
const (
	CodeGateGreen  = "milestone.gate-green"
	CodeGateBroken = "milestone.gate-broken"
)

// GateSelfCheck is the gate's run at approval, recorded with the approval.
type GateSelfCheck struct {
	At   string  `json:"at"`
	SHA  string  `json:"sha"`
	Exit int     `json:"exit"`
	Secs float64 `json:"secs"`
	// Log is the project-relative path of its full output (ignored).
	Log          string `json:"log"`
	OutputSHA256 string `json:"output_sha256"`
}

// ApprovalSelfCheck is the self-check milestones.json records with id's
// approval (D224: approve's JSON carries it), nil when it records none.
func (p *Project) ApprovalSelfCheck(id string) *GateSelfCheck {
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil
	}
	if rec := recs[id]; rec != nil && rec.Approval != nil {
		return rec.Approval.SelfCheck
	}
	return nil
}

// gateSelfCheckProgress is how often a running self-check says it still is.
var gateSelfCheckProgress = time.Minute

// gateSelfCheck runs m's gate once and classifies it (see above), with
// base as its $APE_MILESTONE_BASE (gateEnv). progress gets a line before,
// while and after the run (nil: none).
// reapproval is an approval of an approved milestone, acceptGreen a
// person's --accept-green on it (Q145): a green run is then recorded.
func (p *Project) gateSelfCheck(ctx context.Context, m *Milestone, base string, reapproval, acceptGreen bool, progress io.Writer) (*GateSelfCheck, error) {
	if progress == nil {
		progress = io.Discard
	}
	broken := func(what string) error {
		return &FailError{Code: CodeGateBroken, Msg: m.ID + "'s gate cannot run: " + what +
			" — a gate must run and be red until the milestone's work is done: re-plan the gate (apex-plan) so every script it names exists and runs, then approve again"}
	}
	if why := p.gateUnrunnable(ctx, m.Gate); why != "" {
		return nil, broken(why)
	}
	cfg, _ := LoadConfig(p.Root) // Open refused an unparseable one
	lim := cfg.Timeouts.Limits(KindGate, 0)
	timeout := lim.Max
	sha := HeadSHA(ctx, p.Root)
	fmt.Fprintf(progress, "%s: running the gate once against %.7s before approving (D70; timeout %s): %s\n", m.ID, sha, timeout, m.Gate)
	done := make(chan struct{})
	go func() {
		start := time.Now()
		t := time.NewTicker(gateSelfCheckProgress)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Fprintf(progress, "%s: the gate is still running (%s)\n", m.ID, time.Since(start).Round(time.Second))
			}
		}
	}()
	logPath := filepath.Join(p.LogsDir(), "gate-"+m.ID+"-approve.log")
	p.ensureState()
	pc := PlannedCheck{Ref: "gate-" + m.ID + "-approve", Kind: KindGate, Cmd: m.Gate, Env: gateEnv(m.ID, base)}
	r := p.runWithRetries(ctx, &pc, logPath, lim, nil, 0, p.isWorkTree(ctx))
	close(done)
	if err := interrupted(ctx, m.ID+"'s gate self-check"); err != nil {
		return nil, err // D58 2.2: an interrupted run judges nothing
	}
	sc := &GateSelfCheck{At: Stamp(), SHA: sha, Exit: r.Exit, Secs: r.Secs, Log: r.Log, OutputSHA256: r.OutputSHA256}
	last := ""
	if len(r.Tail) > 0 {
		last = ". Last output (" + r.Log + "): " + strings.Join(lastN(r.Tail, 5), " | ")
	}
	switch r.Exit {
	case 0:
		if reapproval && acceptGreen {
			fmt.Fprintf(progress, "%s: the gate is GREEN (exit 0 in %.1fs) — re-approving, as accepted (--accept-green)\n", m.ID, r.Secs)
			return sc, nil
		}
		fmt.Fprintf(progress, "%s: the gate is GREEN (exit 0 in %.1fs) — refused\n", m.ID, r.Secs)
		msg := fmt.Sprintf("%s's gate is already green on the current tree (exit 0 in %.1fs at %.7s)", m.ID, r.Secs, sha) +
			" — a green gate proves nothing the milestone adds: re-plan the gate (apex-plan) so it is red until the milestone's work is done, then approve again"
		if reapproval {
			// RK-5 (Q145): a gate fixed after the work is done is green.
			msg += "; if the milestone's work is already done and the gate was fixed after it, a person re-approves with `ape milestone approve " + m.ID + " --by <who> --accept-green \"<reason>\"`"
		}
		return nil, &FailError{Code: CodeGateGreen, Msg: msg + last}
	case ExitTimeout:
		why := "it reached its ceiling of " + lim.Max.String() + " (timeouts.gate max)"
		if r.Reason == ReasonIdle {
			why = "it made no progress (no output, no CPU) for " + lim.Idle.String() + " (timeouts.gate idle)"
		}
		fmt.Fprintf(progress, "%s: the gate timed out — refused: %s\n", m.ID, why)
		return nil, broken(why + last)
	case exitNotExecutable, exitNotFound:
		fmt.Fprintf(progress, "%s: the gate exited %d — refused\n", m.ID, r.Exit)
		why := "it exited 127 (a command it runs was not found)"
		if r.ShellNote != "" {
			why = "it exited 127: " + r.ShellNote // RN-1
		}
		if r.Exit == exitNotExecutable {
			why = "it exited 126 (a command it runs is not executable)"
		}
		return nil, broken(why + last)
	}
	fmt.Fprintf(progress, "%s: the gate is red (exit %d in %.1fs), as a new gate must be — approving\n", m.ID, r.Exit, r.Secs)
	return sc, nil
}

// gateUnrunnable is why the gate command cannot run before it is run: a
// script it names is missing or is not valid shell, or a requires: entry
// it needs is missing; "" when none of that holds.
func (p *Project) gateUnrunnable(ctx context.Context, gate string) string {
	for _, rel := range gateScripts(gate) {
		path := filepath.Join(p.Root, filepath.FromSlash(rel))
		if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
			return "it runs " + rel + ", which does not exist (a gate's scripts exist at approval: the milestone's work makes them pass, it does not write them)"
		}
		if out, failed := shellSyntax(ctx, path); failed {
			return rel + " is not valid shell (`bash -n`: " + out + ")"
		}
	}
	cfg, _ := LoadConfig(p.Root)
	if missing := unmetRequirement(gate, cfg.Requires); missing != "" {
		return missing + " (requires:)"
	}
	return ""
}

// shellSyntax parses a gate script that is a POSIX or bash script — its
// #! names sh, bash or dash (directly or through env), or it has none and
// ends in .sh or .bash — with `bash -n` (sh -n without bash): failed, and
// the parser's first line, when it does not parse. Any other script
// (python, zsh, …), or a machine with neither shell, is not judged.
func shellSyntax(ctx context.Context, path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	first, _ := bufio.NewReader(f).ReadString('\n')
	_ = f.Close()
	if interp, ok := strings.CutPrefix(strings.TrimSpace(first), "#!"); ok {
		words := strings.Fields(interp)
		if len(words) > 1 && filepath.Base(words[0]) == "env" {
			words = words[1:]
		}
		if len(words) == 0 || !map[string]bool{"sh": true, "bash": true, "dash": true}[filepath.Base(words[0])] {
			return "", false
		}
	} else if !strings.HasSuffix(path, ".sh") && !strings.HasSuffix(path, ".bash") {
		return "", false
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		if shell, err = exec.LookPath("sh"); err != nil {
			return "", false
		}
	}
	out, err := exec.CommandContext(ctx, shell, "-n", path).CombinedOutput()
	if err == nil {
		return "", false
	}
	msg := strings.TrimSpace(string(out))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if msg == "" {
		msg = err.Error()
	}
	return msg, true
}

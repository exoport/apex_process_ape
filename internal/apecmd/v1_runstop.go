package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

// `ape run status|stop <scope>` on a long run (V15-2D-STOP-1): a long
// verb's worker (`ape __run`, v1_longrun.go) is in its own session by
// design, so no shell, sub-agent or loop that started it can stop it, and
// before this nothing of ape's could either — only `kill -TERM <pid>` from
// its lock. The scope (a change id, M<n>, take-in-M<n>) names the run when
// no --detach handle has that id. Stopping a loop's run does not stop the
// long runs its session started: they are not its descendants, and each is
// stopped by its own scope.

// probeWorkerFn is the one look at a long run's worker (probeWorker, per
// platform); a test swaps it.
var probeWorkerFn = probeWorker

// longRunScope opens the project at root and returns it when scope names
// a long run there: its lock, or the result its last run left. nil when
// it does not (not a v1 project, no such scope, a scope that is not a
// plain name).
func longRunScope(root, scope string) *apexv1.Project {
	if scope == "" || scope != filepath.Base(scope) || strings.HasPrefix(scope, ".") {
		return nil
	}
	p, err := apexv1.Open(root)
	if err != nil {
		return nil
	}
	if _, err := os.Stat(p.RunLockPath(scope)); err == nil {
		return p
	}
	if p.LoadRunResult(scope) != nil {
		return p
	}
	return nil
}

// longRunReport is `ape run status <scope>`'s object.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type longRunReport struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Verb  string `json:"verb"`
	Scope string `json:"scope"`
	PID   int    `json:"pid,omitempty"`
	Since string `json:"since"`
	Log   string `json:"log,omitempty"`
	Alive bool   `json:"alive"`
	// ExitCode is the run's, once it has ended.
	ExitCode *int   `json:"exit_code"`
	Ended    string `json:"ended,omitempty"`
}

// runStatusLong prints scope's long run: its lock while there is one —
// alive when its process is still the run's worker — else its last
// recorded result. A look that cannot tell reports it running (F14).
func runStatusLong(cmd *cobra.Command, p *apexv1.Project, scope string, jsonOut bool) error {
	r := longRunReport{Scope: scope}
	if l := p.LoadRunLock(scope); l != nil {
		r.ID, r.Verb, r.PID, r.Since, r.Log = l.ID, l.Verb, l.PID, l.Started, l.Log
		r.Alive = probeWorkerFn(cmd.Context(), l) != supervisorNotOurs
		r.State = runRunning
		if !r.Alive {
			r.State = runLost
		}
	} else if res := p.LoadRunResult(scope); res != nil {
		r.ID, r.Verb, r.Since, r.Log, r.Ended = res.ID, res.Verb, res.Started, res.Log, res.Ended
		r.State, r.ExitCode = runEnded, &res.Exit
	}
	if !jsonOut {
		switch r.State {
		case runEnded:
			r.State = fmt.Sprintf("ended, exit %d", *r.ExitCode)
		case runLost:
			r.State = "gone without recording a result (the next " + "`ape " + r.Verb + "`" + " reports run.interrupted)"
		}
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// stopRefused prints a refusal of `ape run stop` and exits code.
func stopRefused(cmd *cobra.Command, code int, err error) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "Error: %s\n", err)
	return reportedErr(code, err)
}

// closeLeftovers names what a close stopped past its fences may leave,
// from the last fence its log holds ("" before any).
func closeLeftovers(l *apexv1.RunLock, fences []string) string {
	if len(fences) == 0 {
		return ""
	}
	ms := l.Scope
	if strings.Contains(fences[len(fences)-1], "close PR: enqueueing under") {
		return "its close PR into main may be open and queued in main's merge queue, and milestone/" + ms + " pushed to the remote"
	}
	return "milestone/" + ms + " may be pushed to the remote (main taken in), and its close PR into main open"
}

// runStopLong stops scope's long run: the lock's process checked to be
// the run's worker immediately before (lookTwice), SIGTERM to it alone,
// then up to timeout for it to release its lock. It exits with the run's
// own recorded exit — run.interrupted (1) for a run the signal stopped,
// whatever it recorded for one that ended first — or 76 when the worker
// ended without recording a result; 75 when it has not ended in time or
// its process could not be checked. A coordinated `milestone close` whose
// log has a fence line (its take-in and push, or its PR's enqueue) is
// refused unless force: stopping it there can leave a pushed branch or an
// open, queued PR behind.
func runStopLong(cmd *cobra.Command, p *apexv1.Project, scope string, timeout time.Duration, force bool) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	l := p.LoadRunLock(scope)
	if l == nil {
		r := p.LoadRunResult(scope)
		fmt.Fprintf(out, "the run of `ape %s %s` had already ended with exit %d (%s)\n", r.Verb, r.Scope, r.Exit, r.Ended)
		return nil
	}
	if l.Verb == verbMilestoneClose && !force {
		if left := closeLeftovers(l, p.RunFenceLines(l)); left != "" {
			return stopRefused(cmd, exitV1Conflict, fmt.Errorf(
				"%s is past its fence (its log %s: %q): stopping it now may leave work half delivered — %s. Nothing was signalled; let it finish (`ape run status %s`), or `ape run stop %s --force` and see to what is left",
				l.Command(), l.Log, lastFence(p.RunFenceLines(l)), left, scope, scope))
		}
	}
	switch lookTwice(ctx, func() supervisorCheck { return probeWorkerFn(ctx, l) }) {
	case supervisorOurs:
	case supervisorUnknown:
		return stopRefused(cmd, exitCodeStillRunning, fmt.Errorf("pid %d: %w (its command line could not be read); nothing was signalled — run it again", l.PID, errIdentityUnknown))
	case supervisorNotOurs:
		if r := endedRun(p, l); r != nil {
			fmt.Fprintf(out, "the run of %s had already ended with exit %d (%s)\n", l.Command(), r.Exit, r.Ended)
			return nil
		}
		msg := fmt.Sprintf("pid %d is not the worker of %s (it is gone, and the pid may belong to another process now); nothing was signalled", l.PID, l.Command())
		if !l.Alive() {
			msg += " — " + p.RemoveStaleRunLock(l).Error()
		}
		return stopRefused(cmd, exitCodeRunLost, errors.New(msg))
	}
	if err := termWorker(l.PID); err != nil {
		return stopRefused(cmd, exitCodeRunLost, fmt.Errorf("pid %d (the worker of %s): %w", l.PID, l.Command(), err))
	}
	deadline := time.Now().Add(timeout)
	for {
		cur := p.LoadRunLock(scope)
		if cur == nil || cur.ID != l.ID || !cur.Alive() {
			return stoppedRun(cmd, p, l)
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(out, "%s was asked to stop (SIGTERM to pid %d) and has not ended yet; `ape run status %s`\n", l.Command(), l.PID, scope)
			return reportedErr(exitCodeStillRunning, errors.New("still running"))
		}
		if !sleepCtx(ctx, 100*time.Millisecond) {
			return reportedErr(exitCodeStillRunning, errors.New("still running"))
		}
	}
}

// endedRun is l's recorded result, nil while it has none.
func endedRun(p *apexv1.Project, l *apexv1.RunLock) *apexv1.RunResult {
	if r := p.LoadRunResult(l.Scope); r != nil && r.ID == l.ID {
		return r
	}
	return nil
}

// stoppedRun reports a run that released its lock (or died) after the
// stop: its recorded outcome, marked printed — the next call of its verb
// runs again — or 76 when it recorded none.
func stoppedRun(cmd *cobra.Command, p *apexv1.Project, l *apexv1.RunLock) error {
	r := endedRun(p, l)
	if r == nil {
		err := p.RemoveStaleRunLock(l)
		return stopRefused(cmd, exitCodeRunLost, fmt.Errorf("stopped; %w", err))
	}
	r.Reported = true
	_ = p.SaveRunResult(r)
	fmt.Fprintf(cmd.OutOrStdout(), "the run of %s stopped; it ended with exit %d", l.Command(), r.Exit)
	if r.Err != "" {
		fmt.Fprintf(cmd.OutOrStdout(), ": %s", firstLine(r.Err))
	}
	fmt.Fprintln(cmd.OutOrStdout())
	if r.Exit == 0 {
		return nil
	}
	return reportedErr(r.Exit, errors.New(r.Err))
}

func lastFence(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

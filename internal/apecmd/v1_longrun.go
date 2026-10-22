package apecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Long runs (D58 2.1–2.3): `ape change start|verify|archive` and `ape
// milestone gate|close|approve` (approve since OI-12: its self-check runs
// the gate) run their checks in a DETACHED worker and wait for
// it at most --wait. Claude Code cuts, backgrounds or reaps a Bash call
// that outlives its timeout — and a synchronous sub-agent's backgrounded
// call is killed when the sub-agent replies — so the call that runs the
// checks must not be the call the model made:
//
//  1. the call takes the scope's run lock (apexv1.TakeRunLock: the change
//     id, or M<n>); a call of the same verb finding it held attaches,
//     another verb is refused (run.busy), a stale one is run.interrupted;
//  2. it starts `ape __run --spawn`, which starts the worker `ape __run
//     <run-id> <verb> <scope> …` in its own session (setsid; on Windows
//     CREATE_NEW_PROCESS_GROUP|DETACHED_PROCESS), stdout and stderr to
//     _apex/state/runs/<scope>.log, and exits at once — so the worker is
//     an orphan, no descendant of the call: killing the call, or the shell
//     around it, stops nothing;
//  3. the worker runs the verb's in-process body (the code path the verb
//     always had), records the outcome rendered both ways
//     (<scope>.result.json), ends its log with it, and only then releases
//     the lock;
//  4. the call waits up to --wait and prints that outcome with the verb's
//     own exit code and --json shape, or exits 75: still running, run the
//     same command again to attach.
//
// The worker records at the sha it started at, as every verify does; the
// rules for a HEAD that moved since are the ones that always held. Its
// checks never outlive it (apexv1.runCheck), and SIGTERM to it records
// nothing (run.interrupted).

const (
	// defaultLongWait keeps one call under Claude Code's 10-minute Bash
	// cap; maxLongWait is the most a call may wait.
	defaultLongWait = 9 * time.Minute
	maxLongWait     = 9*time.Minute + 30*time.Second
)

// v1DetachDefault: the six verbs run in a detached worker unless
// --no-detach. The package's tests run them in-process (their binary is
// not ape); the detach tests turn it back on.
var v1DetachDefault = true

// workerKey marks, in the command's context, the worker running run id.
type workerKey struct{}

// longRunHelp is the paragraph every long verb's help ends with.
const longRunHelp = `
Detached runs (D58 2.3): the command takes the run lock of its change (or milestone),
_apex/state/runs/<id|M>.json, and runs in a detached worker (` + "`ape __run`" + `, its own
session, output in _apex/state/runs/<id|M>.log), so the call that started it can be cut,
backgrounded or killed without stopping it. The call waits up to --wait (default 9m, at
most 9m30s; 0 returns at once); a run that ends in time is printed as always, with the same
exit code and --json shape. Otherwise the call exits 75, still running: "… still running
since <time> — run the same command again to attach" (--json: {"running": true, "verb",
"scope", "since", "log", "pid", "log_tail"}, the shape ` + "`ape change review --run`" + ` has).
Running the same command again attaches: it waits again and prints the run's result once it
ends — never a second run — or exits 75 again with the log lines the run wrote since the
last call (stderr; --json: "log_tail"). A result no call
printed yet is printed by the next call of the same verb while HEAD has not moved. While a
run holds the lock, another of these verbs on the same change or milestone is refused:
run.busy (exit 3, action retry; the message names the running verb and since when). A lock
whose process is gone (killed, the machine restarted) is removed and reported as
run.interrupted (exit 1, action retry) — never an attempt. A run stopped by SIGTERM/SIGINT
kills its checks and records nothing — no attempt, no state commit, no gate run — and
reports run.interrupted. No check outlives the worker. Never run these with
run_in_background, and never end a turn on an exit 75: run the same command again.
Beside the exit codes above: 75 still running · 3 run.busy · 1 run.interrupted · 2 a
--wait outside 0–9m30s (usage.wait).`

func addV1WaitFlags(cmd *cobra.Command, f *v1Flags) {
	f.long = true
	cmd.Long += "\n" + longRunHelp
	cmd.Flags().DurationVar(&f.wait, "wait", defaultLongWait,
		"how long this call waits for the detached run (at most 9m30s; 0 returns at once); exit 75 while it still runs — run the same command again to attach")
	cmd.Flags().BoolVar(&f.noDetach, "no-detach", false, "run in this process instead of a detached worker")
	_ = cmd.Flags().MarkHidden("no-detach")
}

// validateWait refuses a --wait one call cannot honour under Claude
// Code's Bash cap.
func validateWait(d time.Duration) error {
	if d < 0 || d > maxLongWait {
		return &apexv1.UsageError{Code: "usage.wait", Msg: fmt.Sprintf("--wait %s is outside 0–%s — one call must stay under Claude Code's 10-minute Bash cap; to wait longer, run the same command again (it attaches)", d, maxLongWait)}
	}
	return nil
}

// renderFn prints a finished run the way its verb always has — to cmd's
// out and err, honouring f.json — and returns the verb's error.
type renderFn func(cmd *cobra.Command, f *v1Flags) error

// longCompute runs a long verb's body in this process — the checks and
// every record — and returns how to print its outcome.
type longCompute func(ctx context.Context, opt apexv1.RunOptions) renderFn

// runningPayload is what a call prints under --json when its run is still
// going (exit 75). `change review --run` reports the same fields.
//
//nolint:tagliatelle // the v1 --json payloads are snake_case by contract
type runningPayload struct {
	Running bool     `json:"running"`
	Verb    string   `json:"verb"`
	Scope   string   `json:"scope"`
	Since   string   `json:"since"`
	Log     string   `json:"log"`
	PID     int      `json:"pid,omitempty"`
	LogTail []string `json:"log_tail,omitempty"`
}

// stillRunning reports a run that has not ended within the call's wait:
// payload under --json, else msg; exit 75.
func stillRunning(cmd *cobra.Command, f *v1Flags, payload any, msg string) error {
	if f.json {
		_ = v1Emit(cmd.OutOrStdout(), f, payload, nil)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), msg)
	}
	return reportedErr(exitCodeStillRunning, errors.New(msg))
}

// runLong runs one of the six long verbs on scope: as the worker when
// this process is one, else under the scope's run lock — attached to a
// run of the same verb, detached (the default), or in this process.
func runLong(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, scope string, compute longCompute) error {
	verb := cmd.Parent().Name() + " " + cmd.Name()
	ctx := cmd.Context()
	if id, ok := ctx.Value(workerKey{}).(string); ok {
		return runAsWorker(cmd, f, p, verb, scope, id, compute)
	}
	if err := validateWait(f.wait); err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	lock, held, err := p.TakeRunLock(ctx, verb, scope)
	if err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	if held != nil {
		if held.Verb != verb {
			return v1Fail(cmd, f, apexv1.RunBusy(held), nil)
		}
		// A person act attaches only to a run of that very act (D179,
		// T13): never to a plain run that would drop the person's flag.
		if f.person != nil && !held.PersonAct.Same(*f.person) {
			return v1Fail(cmd, f, apexv1.RunBusyPersonAct(held, *f.person), nil)
		}
		return awaitRun(cmd, f, p, held)
	}
	// A run that ended after its call stopped waiting: its result is the
	// answer while nothing moved since (a person act's, only to that act).
	if r := p.LoadRunResult(scope); r != nil && !r.Reported && r.Verb == verb && r.HeadAfter == apexv1.HeadSHA(ctx, p.Root) &&
		(f.person == nil || r.PersonAct.Same(*f.person)) {
		p.ReleaseRunLock(lock)
		return deliverRun(cmd, f, p, r)
	}
	// A new run of a person act: its grant is taken now, under the lock —
	// never on pre-tool's verdict alone (the allow → print race) — and the
	// act recorded in the lock, for a re-run to attach by and the worker
	// to act on.
	if f.person != nil {
		a, err := takePersonAct(ctx, p, *f.person)
		if err != nil {
			p.ReleaseRunLock(lock)
			return v1Fail(cmd, f, err, nil)
		}
		if a != nil {
			lock.PersonAct = a
			p.UpdateRunLock(lock)
			cmd.SetContext(apexv1.WithPersonAct(ctx, a))
		}
	}
	if f.noDetach || !v1DetachDefault {
		r, _ := runHere(cmd, f, p, lock, compute, nil, nil) // printed here: a record that failed to save is not needed
		return deliverRun(cmd, f, p, r)
	}
	if err := launchWorker(cmd, p, lock, workerArgv(cmd, p, scope)); err != nil {
		p.ReleaseRunLock(lock)
		return v1Fail(cmd, f, &apexv1.FailError{Code: "run.start-failed", Msg: "could not start the detached worker of `ape " + verb + " " + scope + "`: " + err.Error() +
			" — run the command again"}, nil)
	}
	return awaitRun(cmd, f, p, lock)
}

// runHere runs the verb's body in this process under lock, records its
// outcome rendered both ways, and releases the lock. progress, when set,
// gets a line per check; ended, when set, is called with the recorded
// result while the lock is still held. The result is returned even when
// recording it failed (the error; ended is not called then).
func runHere(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, lock *apexv1.RunLock, compute longCompute, progress io.Writer,
	ended func(*apexv1.RunResult),
) (*apexv1.RunResult, error) {
	defer p.ReleaseRunLock(lock)
	f.ranHere = true
	opt := f.runOptions()
	opt.Progress = progress
	render := compute(cmd.Context(), opt)
	r := &apexv1.RunResult{
		ID: lock.ID, Verb: lock.Verb, Scope: lock.Scope, SHA: lock.SHA, Started: lock.Started,
		Ended: apexv1.Stamp(), HeadAfter: apexv1.HeadSHA(context.WithoutCancel(cmd.Context()), p.Root),
		PersonAct: lock.PersonAct,
	}
	var err error
	r.Human, err = renderAs(cmd, f, render, false)
	r.JSON, _ = renderAs(cmd, f, render, true)
	r.Exit, r.Silent = ExitCode(err)
	if err != nil {
		r.Err = err.Error()
	}
	if err := p.SaveRunResult(r); err != nil {
		return r, err
	}
	if ended != nil {
		ended(r)
	}
	return r, nil
}

// renderAs renders a run's outcome to buffers, as --json or not.
func renderAs(cmd *cobra.Command, f *v1Flags, render renderFn, asJSON bool) (apexv1.RunOutput, error) {
	var out, errb bytes.Buffer
	o, e := cmd.OutOrStdout(), cmd.ErrOrStderr()
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	fl := *f
	fl.json = asJSON
	err := render(cmd, &fl)
	cmd.SetOut(o)
	cmd.SetErr(e)
	return apexv1.RunOutput{Stdout: out.String(), Stderr: errb.String()}, err
}

// deliverRun prints a recorded outcome as the call asked (--json or not),
// marks it printed, and returns the verb's exit.
func deliverRun(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, r *apexv1.RunResult) error {
	out := r.Human
	if f.json {
		out = r.JSON
	}
	_, _ = io.WriteString(cmd.OutOrStdout(), out.Stdout)
	_, _ = io.WriteString(cmd.ErrOrStderr(), out.Stderr)
	if !r.Reported {
		r.Reported = true
		_ = p.SaveRunResult(r)
	}
	if r.Exit == 0 {
		return nil
	}
	return &exitError{code: r.Exit, err: errors.New(r.Err), reported: r.Silent}
}

// awaitRun polls the run lock every awaitPollFirst at first, doubling up to
// awaitPollMax.
const (
	awaitPollFirst = 50 * time.Millisecond
	awaitPollMax   = 500 * time.Millisecond
)

// awaitRun waits up to --wait for the run holding lock to end, and prints
// its result; exit 75 when it is still running.
func awaitRun(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, lock *apexv1.RunLock) error {
	ctx := cmd.Context()
	deadline := time.Now().Add(f.wait)
	poll := awaitPollFirst
	for {
		cur := p.LoadRunLock(lock.Scope)
		if cur == nil || cur.ID != lock.ID || !cur.Alive() {
			if r := p.LoadRunResult(lock.Scope); r != nil && r.ID == lock.ID {
				if cur != nil && cur.ID == lock.ID {
					p.ReleaseRunLock(cur)
				}
				return deliverRun(cmd, f, p, r)
			}
			if cur == nil || cur.ID != lock.ID {
				return v1Fail(cmd, f, apexv1.RunGone(lock), nil)
			}
			return v1Fail(cmd, f, p.RemoveStaleRunLock(cur), nil)
		}
		if !time.Now().Before(deadline) {
			return longStillRunning(cmd, f, p, cur)
		}
		select {
		case <-ctx.Done():
			return longStillRunning(cmd, f, p, cur)
		case <-time.After(poll):
		}
		poll = min(poll*2, awaitPollMax)
	}
}

// longStillRunning is exit 75 for a long verb, with the log lines the run
// wrote since the last call printed them.
func longStillRunning(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, l *apexv1.RunLock) error {
	tail := newLogLines(p, l.Scope)
	payload := runningPayload{Running: true, Verb: l.Verb, Scope: l.Scope, Since: l.Started, Log: l.Log, PID: l.PID, LogTail: lastLinesOf(tail, 20)}
	if !f.json {
		for _, line := range tail {
			fmt.Fprintln(cmd.ErrOrStderr(), line)
		}
	}
	return stillRunning(cmd, f, payload, fmt.Sprintf("`ape %s %s` is still running since %s (pid %d, log %s) — run the same command again to attach",
		l.Verb, l.Scope, l.Started, l.PID, l.Log))
}

// newLogLines returns the lines of scope's run log written since the last
// call that printed them, and records how far it read
// (_apex/state/runs/<scope>.attached).
func newLogLines(p *apexv1.Project, scope string) []string {
	mark := filepath.Join(p.RunsDir(), scope+".attached")
	var from int64
	if data, err := os.ReadFile(mark); err == nil {
		from, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	}
	data, err := os.ReadFile(p.RunLogPath(scope))
	if err != nil || int64(len(data)) < from {
		return nil
	}
	// Up to the last full line: a line being written is printed next time.
	chunk := data[from:]
	end := bytes.LastIndexByte(chunk, '\n')
	if end < 0 {
		return nil
	}
	_ = os.WriteFile(mark, []byte(strconv.FormatInt(from+int64(end)+1, 10)+"\n"), 0o644) //nolint:gosec // ape's own ignored record
	return strings.Split(string(chunk[:end]), "\n")
}

// workerArgv is the command line the worker re-runs: the verb, its scope,
// and every flag this call set but the wait's, with --cwd the project.
func workerArgv(cmd *cobra.Command, p *apexv1.Project, scope string) []string {
	argv := []string{cmd.Parent().Name(), cmd.Name(), scope}
	cmd.Flags().Visit(func(fl *pflag.Flag) {
		switch fl.Name {
		case "wait", "no-detach", "cwd":
			return
		}
		argv = append(argv, "--"+fl.Name+"="+fl.Value.String())
	})
	return append(argv, "--cwd="+p.Root)
}

// launchWorker starts the detached worker through `ape __run --spawn`,
// which starts it in its own session and exits, and records the worker's
// pid in the lock.
func launchWorker(cmd *cobra.Command, p *apexv1.Project, lock *apexv1.RunLock, argv []string) error {
	logPath := p.RunLogPath(lock.Scope)
	if err := os.WriteFile(logPath, nil, 0o644); err != nil { //nolint:gosec // the run's log, ignored
		return err
	}
	_ = os.Remove(filepath.Join(p.RunsDir(), lock.Scope+".attached"))
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	spawn := exec.CommandContext(context.WithoutCancel(cmd.Context()), exe, append([]string{"__run", "--spawn", "--log", logPath, lock.ID}, argv...)...)
	spawn.Dir = p.Root
	var stderr bytes.Buffer
	spawn.Stderr = &stderr
	out, err := spawn.Output()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("the spawner printed %q, not a pid", strings.TrimSpace(string(out)))
	}
	// Unless the worker has recorded itself already.
	if cur := p.LoadRunLock(lock.Scope); cur != nil && cur.ID == lock.ID && cur.PID == os.Getpid() {
		cur.PID, cur.PGID = pid, pid
		p.UpdateRunLock(cur)
	}
	return nil
}

// runAsWorker is the detached worker's side: it records itself in the
// lock its call took, runs the body, records the outcome (in the result,
// and readable in the log) and exits with the verb's code.
func runAsWorker(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, verb, scope, id string, compute longCompute) error {
	lock := p.LoadRunLock(scope)
	if lock == nil || lock.ID != id || lock.Verb != verb {
		return failErr(fmt.Errorf("run %s of `ape %s %s`: its run lock is gone or another run's — not running", id, verb, scope))
	}
	lock.PID, lock.PGID = os.Getpid(), apexv1.ProcessGroup()
	if !p.UpdateRunLock(lock) {
		return failErr(fmt.Errorf("run %s of `ape %s %s`: its run lock changed — not running", id, verb, scope))
	}
	// A person act's worker acts on its lock's record of it (E2, OI-12):
	// in a model session, only one resting on its caller's consumed grant.
	if f.person != nil {
		if a := p.LockPersonAct(lock, *f.person); a != nil {
			cmd.SetContext(apexv1.WithPersonAct(cmd.Context(), a))
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "[ape] %s `ape %s %s` started (run %s, pid %d, at %.7s)\n", apexv1.Stamp(), verb, scope, id, os.Getpid(), lock.SHA)
	// The log says how the run ended before its lock is released (D380):
	// a call that sees the lock gone — and prints the result — reads a log
	// that already ends with it.
	logEnded := func(r *apexv1.RunResult) {
		w := cmd.ErrOrStderr()
		fmt.Fprintf(w, "[ape] %s ended, exit %d — result:\n", apexv1.Stamp(), r.Exit)
		_, _ = io.WriteString(w, r.Human.Stdout+r.Human.Stderr)
		if s, ok := w.(interface{ Sync() error }); ok {
			_ = s.Sync()
		}
	}
	r, err := runHere(cmd, f, p, lock, compute, cmd.ErrOrStderr(), logEnded)
	if err != nil {
		return failErr(fmt.Errorf("run %s: its result could not be recorded: %w", id, err))
	}
	if r.Exit == 0 {
		return nil
	}
	return reportedErr(r.Exit, errors.New(r.Err))
}

// newRunWorkerCmd is `ape __run`, the long verbs' plumbing (hidden):
//
//	ape __run --spawn --log <file> <run-id> <verb> <scope> [flags]
//	    starts the worker below in its own session, output to <file>,
//	    prints its pid and exits
//	ape __run <run-id> <verb> <scope> [flags]
//	    the worker: runs `ape <verb> <scope> [flags]` as run <run-id>
func newRunWorkerCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "__run",
		Hidden:             true,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) >= 4 && args[0] == "--spawn" && args[1] == "--log" {
				return spawnWorker(cmd, args[2], args[3:])
			}
			if len(args) < 3 {
				return usageErr(errors.New("__run is ape's own plumbing: __run <run-id> <verb> <scope> [flags]"))
			}
			root := newRootCmd()
			root.SetArgs(args[1:])
			root.SetOut(cmd.OutOrStdout())
			root.SetErr(cmd.ErrOrStderr())
			return root.ExecuteContext(context.WithValue(cmd.Context(), workerKey{}, args[0]))
		},
	}
}

// spawnWorker starts `ape __run <args>` orphaned — its own session, no
// terminal, stdin closed, stdout and stderr appended to logPath — prints
// its pid and returns, so this process ends at once.
func spawnWorker(cmd *cobra.Command, logPath string, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return failErr(err)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return failErr(err)
	}
	defer func() { _ = logf.Close() }()
	w := exec.Command(exe, append([]string{"__run"}, args...)...) //nolint:noctx // this binary; the worker must outlive this process
	w.Stdout, w.Stderr = logf, logf
	w.SysProcAttr = detachedProcAttr()
	if err := w.Start(); err != nil {
		return failErr(err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), w.Process.Pid)
	_ = w.Process.Release()
	return nil
}

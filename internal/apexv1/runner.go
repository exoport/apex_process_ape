package apexv1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// EnvApeGate is set to 1 in every check ape runs (v1.4, D561 Q4): verify,
// the gate, approve's self-check, the regression command. A harness that
// cannot set up its dependencies fails instead of skipping when it is set,
// so a broken harness does not read green (OQ-SKIP; a convention, not a
// rule — ape only exports it).
const EnvApeGate = "APE_GATE"

// Reaping says what reaped a check (CheckResult.Reaping, v1.4): its
// systemd scope, or its process group (on Windows its job object).
const (
	ReapingScope = "scope"
	ReapingGroup = "group"
)

// tailLines is how many trailing output lines a red check is reported by.
const tailLines = 40

// outputKeep is how much of a check's output is held in memory for its
// tail; the log file gets all of it.
const outputKeep = 4 << 20

// ExitTimeout is the exit a timed-out check records (coreutils' timeout):
// killed at its idle window (reason idle) or its ceiling (reason ceiling).
const ExitTimeout = 124

// The shell's exits for a command that could not run — 126 not executable,
// 127 not found (a check that cannot start records 127, as a shell does) —
// and 130 (128+SIGINT), a check not started because ape was interrupted.
const (
	exitNotExecutable = 126
	exitNotFound      = 127
	exitInterrupted   = 130
)

// The reasons a timed-out check records (D58 2.4).
const (
	ReasonIdle    = "idle"
	ReasonCeiling = "ceiling"
)

// progressSample is how often a running check's progress is sampled — the
// CPU time its process group has used — and its idle window judged. A var
// so tests can sample fast.
var progressSample = 10 * time.Second

// RunCheck runs cmd with `sh -c` from dir under the check tier's default
// idle window, timeout its ceiling (the default when zero). The full output
// goes to logPath when it is non-empty; the result carries its SHA-256 and
// last lines. A command that cannot start records exit 127, as a shell
// would. A command that is one multi-line ( … ) block of simple commands
// runs as its lines joined by && (checkScript, R24b).
func RunCheck(ctx context.Context, dir, cmd, logPath string, timeout time.Duration) CheckResult {
	return runCheck(ctx, dir, cmd, logPath, "", Timeouts{}.Limits(KindTask, timeout), nil)
}

// openCheckLog creates a check's log at logPath, nil when there is none or
// it cannot be created. The split note (R24b) opens the log only: the
// output's hash and tail are the command's own.
func openCheckLog(logPath, note string) *os.File {
	if logPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil
	}
	f, err := os.Create(logPath)
	if err != nil {
		return nil
	}
	if note != "" {
		_, _ = f.WriteString(note + "\n")
	}
	return f
}

// checkElapsed is how long a check ran, from its start. A variable so a
// test can give a check a duration: a fixture's `echo` takes a few
// milliseconds, which CheckResult.Secs rounds to 0.0 — and 0 reads as "no
// duration recorded" (lastRegressionRun), so a test asserting on a
// recorded duration passed only on a machine slow enough to take 50 ms.
var checkElapsed = time.Since

// runCheck is RunCheck under lim that also judges a zero exit against the
// vacuous phrases (H11): an output vacuousPhrase finds one in records
// ExitVacuous.
//
// A check is killed — its whole process group — when it makes no progress
// for lim.Idle, or when it reaches lim.Max (D58 2.4); either records exit
// 124 with reason idle or ceiling. Progress is new output bytes, or CPU
// time used by its process group: a silent `go test ./...` compiling is
// busy, a check blocked on a lock or a network peer is not. And a check
// never outlives the ape running it (2.2): on Linux and macOS a watchdog
// in its process group kills the group when ape's end of a pipe closes,
// however ape ended (SIGKILL included) — on Linux the shell also has
// Pdeathsig; on Windows the group is a job object that closes with ape.
// Nor does anything it started outlive the check itself (F15): once its
// shell exits, what is left in its group is ended and recorded
// (Leftovers), and its exit is the shell's own.
//
// The log opens with logNote, when set, in place of the split note (R30).
func runCheck(ctx context.Context, dir, cmd, logPath, logNote string, lim TierTimeout, vacuous []string, env ...string) CheckResult {
	if lim.Max <= 0 {
		lim = Timeouts{}.Limits(KindTask, 0)
	}
	ctx, cancel := context.WithTimeout(ctx, lim.Max)
	defer cancel()
	// idle cancels the run with errIdle as its cause.
	runCtx, idle := context.WithCancelCause(ctx)
	defer idle(nil)

	// A multi-line ( … ) block of simple commands runs as its lines joined
	// by && (R24b); the result keeps the command as configured.
	script, note := checkScript(cmd)
	if logNote != "" {
		note = logNote
	}
	c := exec.CommandContext(runCtx, "sh", "-c", script) //nolint:gosec // a check is a shell command the project's own change.md or config names, by design
	c.Dir = dir
	c.Stdin = nil
	// A check is a test, not a session: it must not inherit a terminal.
	// APE_GATE=1 tells a harness ape runs it (v1.4, OQ-SKIP): the
	// convention is that a harness fails rather than skip when it is set.
	// v1.4 (B1): its own TMPDIR and compose project (checkinfra.go),
	// over ape's environment and under the check's own.
	infra := newCheckInfra(ctx, dir)
	c.Env = append(append(append(os.Environ(), EnvApeGate+"=1"), infra.env()...), env...)
	g, err := prepareCheck(c, script)
	if err != nil {
		infra.discard()
		return CheckResult{Cmd: cmd, Exit: exitNotFound, Tail: []string{"[ape] " + err.Error()}}
	}
	c.Cancel = func() error { return g.kill(c) }
	c.WaitDelay = 5 * time.Second
	// The output goes through ape's own pipe, not one exec.Cmd copies
	// (F15): Wait then returns when the check's shell exits, though a
	// process it left running still holds the pipe — exec's copy would
	// wait WaitDelay for it and then report "WaitDelay expired", not the
	// check's exit (eval bf :3 M2: a green gate recorded as 127).
	pr, pw, err := os.Pipe()
	if err != nil {
		infra.discard()
		return CheckResult{Cmd: cmd, Exit: exitNotFound, Tail: []string{"[ape] " + err.Error()}}
	}
	defer func() { _ = pr.Close() }()

	var buf bytes.Buffer
	hash := sha256.New()
	var lastOut atomic.Int64
	lastOut.Store(time.Now().UnixNano())
	sinks := []io.Writer{&capped{w: &buf, max: outputKeep}, hash, progressWriter{&lastOut}}
	logFile := openCheckLog(logPath, note)
	if logFile != nil {
		sinks = append(sinks, logFile)
	}
	w := &lockedWriter{w: io.MultiWriter(sinks...)}
	c.Stdout = pw
	c.Stderr = pw

	start := time.Now()
	// Pdeathsig fires when the THREAD that started the child exits, not
	// the process: this goroutine keeps its thread until the check ends.
	runtime.LockOSThread()
	err = c.Start()
	_ = pw.Close() // the check's copies hold it now
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(w, pr)
	}()
	var obs *observer
	if err == nil {
		g.started(c)
		obs = startObserver(g)
		stop := make(chan struct{})
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			watchProgress(g, &lastOut, lim.Idle, stop, idle)
		}()
		err = c.Wait()
		close(stop)
		<-watched
	}
	runtime.UnlockOSThread()
	secs := checkElapsed(start).Seconds()
	finished := runCtx.Err() == nil
	// v1.4 (B1): a red check whose port was taken by what is not the
	// check's — judged before its leftovers are ended, while the check's
	// own holder is still in its group.
	portHeld := ""
	if finished && err != nil {
		portHeld = heldPort(ctx, w, &buf, infra.docker, g.contains)
	}
	// What a check that exited on its own left running is ended and named
	// (F15); a check killed at a timeout or an interrupt was killed whole.
	// In a scope (v1.4) the watchdog holds the scope open: its leftovers
	// are ended and its memory.peak read before the watchdog is let go.
	// v1.4 (D623): what it did while it ran, read before its leftovers end.
	observed := obs.finish()
	leftovers, peak := endCheck(g, finished)
	// v1.4 (B1): what held its TMPDIR from outside, its containers, the
	// TMPDIR itself.
	var fin CheckResult
	infra.finish(ctx, &fin, g.contains)
	leftovers = append(leftovers, fin.Leftovers...)
	if observed != nil {
		// docker's events only where they could decide a notice: a known
		// runner, nothing else seen yet.
		observed.Containers = infra.createdCount(ctx, fin.Containers, observed.Runner != "" && !observed.Needs())
	}
	// The output is read until every holder of the pipe is gone. One that
	// left the check's group is not ended above: it is given outputDrain,
	// then ape stops reading — the check's exit stands.
	held := false
	select {
	case <-copied:
	case <-time.After(outputDrain):
		held = true
		_ = pr.Close()
		<-copied
	}

	exit, reason, detail := checkExit(ctx, runCtx, err, lim, w)
	if portHeld != "" && exit != 0 && exit != ExitTimeout {
		exit, reason, detail = ExitUnrun, ReasonUnrun, portHeld+" (port-held): not ready, never red for the product"
		_, _ = io.WriteString(w, "\n[ape] unrun (port-held): "+portHeld+"\n")
	}
	sum := hex.EncodeToString(hash.Sum(nil)) // of the command's own output
	if exit == 0 {
		if ph := vacuousPhrase(buf.String(), vacuous); ph != "" {
			exit, reason = ExitVacuous, ReasonVacuous
			detail = "exit 0, but the output says " + strconv.Quote(ph) + " and nothing shows a test ran"
			_, _ = io.WriteString(w, "\n[ape] vacuous: "+detail+" (checks.vacuous_output)\n")
		}
	}
	// OQ-SKIP (v1.4): the runner's counts, and, for a check whose output
	// ape judges (vacuous non-nil: never a gate's own command), a run that
	// tested nothing is unrun.
	tests, skip, skipDetail := judgeSkips(cmd, buf.String(), exit == 0 && vacuous != nil)
	if skip != "" {
		exit, reason, detail = ExitUnrun, ReasonUnrun, skipDetail
		_, _ = io.WriteString(w, "\n[ape] unrun ("+skip+"): "+skipDetail+"\n")
	}
	shellNote := shellBuiltinNote(exit, buf.String())
	if shellNote != "" {
		_, _ = io.WriteString(w, "\n[ape] note: "+shellNote+"\n")
	}
	if len(leftovers) > 0 {
		_, _ = io.WriteString(w, "\n[ape] the check exited with "+strconv.Itoa(len(leftovers))+
			" process(es) it started still running; ape ended them: "+strings.Join(leftovers, "; ")+"\n")
	}
	if held {
		_, _ = io.WriteString(w, "\n[ape] the output was still held open "+outputDrain.String()+
			" after the check exited, by a process outside its process group; ape stopped reading it\n")
	}
	// Closed only now: the timeout, start-failure, vacuous and shell notes
	// above belong in the log too.
	if logFile != nil {
		_ = logFile.Close()
	}
	return CheckResult{
		Cmd:          cmd,
		Exit:         exit,
		Secs:         math.Round(secs*10) / 10,
		OutputSHA256: sum,
		Tail:         lastLines(buf.String(), tailLines),
		Reason:       reason,
		Detail:       detail,
		ShellNote:    shellNote,
		Leftovers:    leftovers,
		Tests:        tests,
		Skip:         skip,
		Reaping:      g.reaping(),
		ScopeUnit:    g.scopeUnit(),
		MemoryPeak:   peak,
		TmpBytes:     fin.TmpBytes,
		Compose:      fin.Compose,
		Containers:   fin.Containers,
		Observed:     observed,
	}
}

// checkExit is the exit a check records for the error its Wait returned:
// 0, its own exit, ExitTimeout at its idle window or ceiling (noted in the
// log through w), or 127 when it could not run.
func checkExit(ctx, runCtx context.Context, err error, lim TierTimeout, w io.Writer) (exit int, reason, detail string) {
	if err == nil {
		return 0, "", ""
	}
	var ee *exec.ExitError
	switch {
	case errors.Is(context.Cause(runCtx), errIdle):
		exit, reason = ExitTimeout, ReasonIdle
		detail = "no output and no CPU time for " + lim.Idle.String() + " (" + lim.key("idle") + ")"
		_, _ = io.WriteString(w, "\n[ape] check timed out: "+detail+"\n")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		exit, reason = ExitTimeout, ReasonCeiling
		detail = "still running at " + lim.Max.String() + " (" + lim.key("max") + ")"
		_, _ = io.WriteString(w, "\n[ape] check timed out: "+detail+"\n")
	case errors.As(err, &ee):
		exit = ee.ExitCode()
		if exit < 0 {
			exit = 128
		}
		if ctx.Err() != nil {
			_, _ = io.WriteString(w, "\n[ape] interrupted\n")
		}
	default:
		exit = exitNotFound
		_, _ = io.WriteString(w, "\n[ape] "+err.Error()+"\n")
	}
	return exit, reason, detail
}

// endCheck lets the check's watchdog go and ends what a check that
// exited on its own left running — in a scope, first, with its
// memory.peak read while the watchdog still holds the scope open.
func endCheck(g *checkGroup, finished bool) (leftovers []string, peak int64) {
	if g.reaping() == ReapingScope {
		if finished {
			leftovers = g.endLeftovers()
		}
		peak = g.memoryPeak()
		g.done(finished)
		return leftovers, peak
	}
	g.done(finished)
	if finished {
		leftovers = g.endLeftovers()
	}
	return leftovers, 0
}

// heldPort is why a red check is port-held (v1.4, B1), "" when its
// output names no taken port or the holder is the check's own. The output
// may still be streaming (a leftover holds the pipe): it is read under the
// writer's lock.
func heldPort(ctx context.Context, w *lockedWriter, buf *bytes.Buffer, docker bool, inCheck func(int) bool) string {
	w.mu.Lock()
	sofar := buf.String()
	w.mu.Unlock()
	port := takenPort(sofar)
	if port == 0 {
		return ""
	}
	if h := portHolder(ctx, port, docker, inCheck); h != "" {
		return "port " + strconv.Itoa(port) + " is held by " + h + ", outside the check"
	}
	return ""
}

// judgeSkips is the runner's counts of out (OQ-SKIP), and, when judge, the
// skip verdict that makes the check unrun.
func judgeSkips(cmd, out string, judge bool) (tests *TestCounts, skip, detail string) {
	c, ok := testCounts(out)
	if !ok {
		return nil, "", ""
	}
	if judge {
		skip, detail = skipVerdict(cmd, c)
	}
	return &c, skip, detail
}

// outputDrain bounds how long a check's output is read after the check
// exited and its leftovers were ended (F15). A var so tests can shorten it.
var outputDrain = 5 * time.Second

// errIdle is the cause a check killed at its idle window is cancelled with.
var errIdle = errors.New("idle")

// watchProgress samples a running check every progressSample until stop
// closes: CPU time its group used since the last sample counts as
// progress, as output does (lastOut, unix nanoseconds). A check with no
// progress for idle is cancelled with errIdle. idle <= 0 watches nothing.
//
// The idle verdict is judged by a timer at the exact deadline — the last
// progress plus idle — as well as at every sample, so the kill lands at the
// window rather than up to a sample past it (the eval measured a 15 s idle
// killed at 20.1 s on the sample tick alone). At the deadline the CPU is
// sampled once more, so a check busy since the last sample is not killed.
func watchProgress(g *checkGroup, lastOut *atomic.Int64, idle time.Duration, stop <-chan struct{}, cancel context.CancelCauseFunc) {
	if idle <= 0 {
		<-stop
		return
	}
	tick := time.NewTicker(min(progressSample, idle))
	defer tick.Stop()
	deadline := time.NewTimer(idle)
	defer deadline.Stop()
	lastCPU, _ := g.cpu()
	lastBusy := time.Now()
	for {
		var now time.Time
		select {
		case <-stop:
			return
		case now = <-tick.C:
		case now = <-deadline.C:
		}
		if cpu, ok := g.cpu(); ok && g.busy(lastCPU, cpu) {
			lastCPU, lastBusy = cpu, now
		}
		last := time.Unix(0, lastOut.Load())
		if lastBusy.After(last) {
			last = lastBusy
		}
		left := idle - now.Sub(last)
		if left <= 0 {
			cancel(errIdle)
			return
		}
		deadline.Reset(left)
	}
}

// progressWriter records when output last arrived.
type progressWriter struct{ at *atomic.Int64 }

func (p progressWriter) Write(b []byte) (int, error) {
	if len(b) > 0 {
		p.at.Store(time.Now().UnixNano())
	}
	return len(b), nil
}

// lockedWriter serialises the check's output with ape's own notes.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// unrunResult is a check not run because a requirement is missing (H11);
// the note goes to its log, so the log the result names exists.
func unrunResult(cmd, logPath, detail string) CheckResult {
	note := "[ape] unrun: " + detail + " — the check was not run (requires:)"
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
			_ = os.WriteFile(logPath, []byte(note+"\n"), 0o644) //nolint:gosec // a check's log, same mode as the logs a run writes beside it
		}
	}
	return CheckResult{Cmd: cmd, Exit: ExitUnrun, Tail: []string{note}, Reason: ReasonUnrun, Detail: detail}
}

func lastLines(s string, n int) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// capped keeps the LAST max bytes written, which is the end a tail needs.
type capped struct {
	w   *bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	c.w.Write(p)
	if over := c.w.Len() - c.max; over > 0 {
		c.w.Next(over)
	}
	return n, nil
}

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
	return runCheck(ctx, dir, cmd, logPath, Timeouts{}.Limits(KindTask, timeout), nil)
}

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
func runCheck(ctx context.Context, dir, cmd, logPath string, lim TierTimeout, vacuous []string, env ...string) CheckResult {
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
	c := exec.CommandContext(runCtx, "sh", "-c", script) //nolint:gosec // a check is a shell command the project's own change.md or config names, by design
	c.Dir = dir
	c.Stdin = nil
	// A check is a test, not a session: it must not inherit a terminal.
	c.Env = append(os.Environ(), env...)
	g, err := prepareCheck(c, script)
	if err != nil {
		return CheckResult{Cmd: cmd, Exit: exitNotFound, Tail: []string{"[ape] " + err.Error()}}
	}
	c.Cancel = func() error { return g.kill(c) }
	c.WaitDelay = 5 * time.Second

	var buf bytes.Buffer
	hash := sha256.New()
	var lastOut atomic.Int64
	lastOut.Store(time.Now().UnixNano())
	sinks := []io.Writer{&capped{w: &buf, max: outputKeep}, hash, progressWriter{&lastOut}}
	var logFile *os.File
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
			if f, err := os.Create(logPath); err == nil {
				logFile = f
				sinks = append(sinks, f)
				// The split note opens the log only: the output's hash
				// and tail are the command's own.
				if note != "" {
					_, _ = io.WriteString(f, note+"\n")
				}
			}
		}
	}
	w := &lockedWriter{w: io.MultiWriter(sinks...)}
	c.Stdout = w
	c.Stderr = w

	start := time.Now()
	// Pdeathsig fires when the THREAD that started the child exits, not
	// the process: this goroutine keeps its thread until the check ends.
	runtime.LockOSThread()
	err = c.Start()
	if err == nil {
		g.started(c)
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
	secs := time.Since(start).Seconds()
	g.done(runCtx.Err() == nil)

	exit := 0
	var reason, detail string
	if err != nil {
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
	}
	sum := hex.EncodeToString(hash.Sum(nil)) // of the command's own output
	if exit == 0 {
		if ph := vacuousPhrase(buf.String(), vacuous); ph != "" {
			exit, reason = ExitVacuous, ReasonVacuous
			detail = "exit 0, but the output says " + strconv.Quote(ph) + " and nothing shows a test ran"
			_, _ = io.WriteString(w, "\n[ape] vacuous: "+detail+" (checks.vacuous_output)\n")
		}
	}
	// Closed only now: the timeout, start-failure and vacuous notes above
	// belong in the log too.
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
	}
}

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
		if cpu, ok := g.cpu(); ok && cpu != lastCPU {
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

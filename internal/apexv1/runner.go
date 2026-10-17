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
	"strconv"
	"strings"
	"time"
)

// DefaultCheckTimeout bounds one command check.
const DefaultCheckTimeout = 15 * time.Minute

// tailLines is how many trailing output lines a red check is reported by.
const tailLines = 40

// outputKeep is how much of a check's output is held in memory for its
// tail; the log file gets all of it.
const outputKeep = 4 << 20

// ExitTimeout is the exit a timed-out check records (coreutils' timeout).
const ExitTimeout = 124

// RunCheck runs cmd with `sh -c` from dir. The full output goes to logPath
// when it is non-empty; the result carries its SHA-256 and last lines.
// A command that cannot start records exit 127, as a shell would.
func RunCheck(ctx context.Context, dir, cmd, logPath string, timeout time.Duration) CheckResult {
	return runCheck(ctx, dir, cmd, logPath, timeout, nil)
}

// runCheck is RunCheck that also judges a zero exit against the vacuous
// phrases (H11): an output vacuousPhrase finds one in records ExitVacuous.
func runCheck(ctx context.Context, dir, cmd, logPath string, timeout time.Duration, vacuous []string) CheckResult {
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	c.Stdin = nil
	// A check is a test, not a session: it must not inherit a terminal.
	c.Env = os.Environ()
	setProcessGroup(c)
	c.Cancel = func() error { return killProcessGroup(c) }
	c.WaitDelay = 5 * time.Second

	var buf bytes.Buffer
	hash := sha256.New()
	var sinks []io.Writer
	sinks = append(sinks, &capped{w: &buf, max: outputKeep}, hash)
	var logFile *os.File
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
			if f, err := os.Create(logPath); err == nil {
				logFile = f
				sinks = append(sinks, f)
			}
		}
	}
	w := io.MultiWriter(sinks...)
	c.Stdout = w
	c.Stderr = w

	start := time.Now()
	err := c.Run()
	secs := time.Since(start).Seconds()

	exit := 0
	if err != nil {
		var ee *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			exit = ExitTimeout
			_, _ = io.WriteString(w, "\n[ape] check timed out after "+timeout.String()+"\n")
		case errors.As(err, &ee):
			exit = ee.ExitCode()
			if exit < 0 {
				exit = 128
			}
		default:
			exit = 127
			_, _ = io.WriteString(w, "\n[ape] "+err.Error()+"\n")
		}
	}
	sum := hex.EncodeToString(hash.Sum(nil)) // of the command's own output
	var reason, detail string
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

// unrunResult is a check not run because a requirement is missing (H11);
// the note goes to its log, so the log the result names exists.
func unrunResult(cmd, logPath, detail string) CheckResult {
	note := "[ape] unrun: " + detail + " — the check was not run (requires:)"
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
			_ = os.WriteFile(logPath, []byte(note+"\n"), 0o644)
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

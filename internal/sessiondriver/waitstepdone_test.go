package sessiondriver

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// newTestDriver builds a Driver with no runlog and the max-duration cap
// disabled by default, so a test opts in to the ceiling explicitly.
func newTestDriver(idle time.Duration) *Driver {
	d := NewDriver(func() *runlog.Writer { return nil }, idle)
	d.SetMaxDuration(0) // disable the 3h default unless a test sets it
	return d
}

// appendEvery grows path by one line every interval until stop closes.
// Models an actively-progressing transcript.
func appendEvery(t *testing.T, path string, interval time.Duration, stop <-chan struct{}) {
	t.Helper()
	go func() {
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return
				}
				_, _ = f.WriteString(turnLine(10, 10) + "\n")
				_ = f.Close()
			}
		}
	}()
}

// TestWaitStepDone_TranscriptGrowthKeepsAlive: a transcript that grows
// faster than the idle window is NOT terminated — the growth anchor
// resets the idle timer, so the wait ends only when ctx expires (D1).
func TestWaitStepDone_TranscriptGrowthKeepsAlive(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	require.NoError(t, os.WriteFile(src, []byte(turnLine(10, 10)+"\n"), 0o600))

	d := newTestDriver(1500 * time.Millisecond) // idle window 1.5s
	d.SetActiveTranscript(src)
	d.Begin()

	stop := make(chan struct{})
	appendEvery(t, src, 400*time.Millisecond, stop) // grow every 0.4s
	defer close(stop)

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	err := d.WaitStepDone(ctx)

	// The growing transcript keeps resetting the idle anchor, so the only
	// way out is ctx expiry — NOT an idle-timeout diagnostic.
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var ite *IdleTimeoutError
	require.NotErrorAs(t, err, &ite, "growing transcript must not trip the idle backstop")
}

// TestWaitStepDone_SilentStepTerminates: a flat transcript with no hooks,
// no growth, and no PTY signal IS terminated at the idle window, and the
// diagnostic names the (absent) progress sources + child liveness (D1+D4).
func TestWaitStepDone_SilentStepTerminates(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	require.NoError(t, os.WriteFile(src, []byte(turnLine(10, 10)+"\n"), 0o600))

	d := newTestDriver(1200 * time.Millisecond)
	d.SetActiveTranscript(src)
	d.SetIdleErrLabel("interactive step")
	d.Begin()

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	err := d.WaitStepDone(ctx)

	var ite *IdleTimeoutError
	require.ErrorAs(t, err, &ite, "silent step must trip the idle backstop")
	require.Equal(t, "interactive step", ite.Label)
	require.Equal(t, progressNone, ite.LastSource)
	require.Contains(t, ite.Error(), "idle for")
	require.Contains(t, ite.Diagnostic, "transcript")
	require.Contains(t, ite.Diagnostic, "pty n/a") // no PTY probe installed
	require.Contains(t, ite.Diagnostic, "child liveness unknown")
}

// TestWaitStepDone_MaxDurationTerminatesProgressingStep: a step that
// keeps progressing (transcript growing) past the hard ceiling is
// terminated with the max-duration reason, distinct from the idle path
// (D2). The idle window is wide so only the cap can trip.
func TestWaitStepDone_MaxDurationTerminatesProgressingStep(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	require.NoError(t, os.WriteFile(src, []byte(turnLine(10, 10)+"\n"), 0o600))

	d := newTestDriver(1500 * time.Millisecond) // poll floors at 1s
	d.SetActiveTranscript(src)
	d.SetMaxDuration(2500 * time.Millisecond) // hard cap ~2.5s
	d.Begin()

	stop := make(chan struct{})
	appendEvery(t, src, 300*time.Millisecond, stop) // stays "progressing"
	defer close(stop)

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	err := d.WaitStepDone(ctx)

	var mde *MaxDurationError
	require.ErrorAs(t, err, &mde, "progressing step must trip the max-duration cap, not idle")
	require.Equal(t, 2500*time.Millisecond, mde.Max)
	require.GreaterOrEqual(t, mde.Elapsed, 2500*time.Millisecond)
	require.Contains(t, mde.Error(), "exceeded max-duration")

	var ite *IdleTimeoutError
	require.NotErrorAs(t, err, &ite, "cap must not be reported as an idle timeout")
}

// TestWaitStepDone_SubagentBoundaryResetsCeiling: a step that keeps
// finishing sub-agents (batch items) more often than the hard cap is NOT
// killed by max-duration — each SubagentStop resets the ceiling anchor via
// FeedHook, so the cap bounds an individual item, not the whole batch. The
// only way out is ctx expiry. Contrast
// TestWaitStepDone_MaxDurationTerminatesProgressingStep, where a
// progressing-but-boundary-less step DOES trip the same cap.
func TestWaitStepDone_SubagentBoundaryResetsCeiling(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	require.NoError(t, os.WriteFile(src, []byte(turnLine(10, 10)+"\n"), 0o600))

	d := newTestDriver(2 * time.Second) // idle window wide; poll floors at 1s
	d.SetActiveTranscript(src)
	d.SetMaxDuration(2 * time.Second) // hard cap ~2s — a boundary-less step would trip
	d.Begin()

	// Fire a sub-agent boundary every 600ms (well under the 2s cap) through
	// the real FeedHook path, and grow the transcript so the idle anchor
	// stays happy independently of the boundaries.
	stop := make(chan struct{})
	appendEvery(t, src, 400*time.Millisecond, stop)
	go func() {
		tk := time.NewTicker(600 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				d.FeedHook(orchestrator.HookEvent{Event: ipc.HookSubagentStop, At: time.Now()})
			}
		}
	}()
	defer close(stop)

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	err := d.WaitStepDone(ctx)

	require.ErrorIs(t, err, context.DeadlineExceeded,
		"sub-agent boundaries must keep resetting the per-item ceiling")
	var mde *MaxDurationError
	require.NotErrorAs(t, err, &mde,
		"per-item boundary reset must prevent the batch-wide cap from tripping")
}

// TestWaitStepDone_PTYProbeCountsAsProgress: with a flat transcript but a
// live PTY-output probe, PTY bytes keep the step alive past the idle
// window (D1 optional signal); the diagnostic would name pty as the last
// source were it to stop.
func TestWaitStepDone_PTYProbeCountsAsProgress(t *testing.T) {
	d := newTestDriver(1500 * time.Millisecond)

	var mu sync.Mutex
	last := time.Now()
	d.SetPTYProbe(func() (time.Time, bool) {
		mu.Lock()
		defer mu.Unlock()
		return last, true
	})
	// Advance the PTY timestamp every 400ms.
	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(400 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				mu.Lock()
				last = time.Now()
				mu.Unlock()
			}
		}
	}()
	defer close(stop)
	d.Begin()

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	err := d.WaitStepDone(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "PTY output must count as progress")
}

// TestPollInterval_TwoPhaseCadence: the selector returns 30s for the
// first 60m of a step and 60s thereafter (D6), with the default 60m idle
// window (no short-window scaling).
func TestPollInterval_TwoPhaseCadence(t *testing.T) {
	d := NewDriver(func() *runlog.Writer { return nil }, DefaultIdleTimeout)

	require.Equal(t, idlePoll, d.pollInterval(0))
	require.Equal(t, idlePoll, d.pollInterval(59*time.Minute))
	require.Equal(t, longRunPoll, d.pollInterval(longRunThreshold))
	require.Equal(t, longRunPoll, d.pollInterval(2*time.Hour))
	require.Equal(t, 30*time.Second, idlePoll)
	require.Equal(t, 60*time.Second, longRunPoll)
}

// TestPollInterval_ShortWindowScaling: a small configured idle window
// still polls at a quarter of the window (floored at 1s), independent of
// the two-phase cadence (D6 keeps the idlePollDivisor behavior).
func TestPollInterval_ShortWindowScaling(t *testing.T) {
	d := NewDriver(func() *runlog.Writer { return nil }, time.Minute) // 60s window
	// quarter = 15s < 30s base, so poll scales down to 15s even at t=0.
	require.Equal(t, 15*time.Second, d.pollInterval(0))
	// Past the long-run threshold the 15s quarter still wins over 60s.
	require.Equal(t, 15*time.Second, d.pollInterval(2*time.Hour))
}

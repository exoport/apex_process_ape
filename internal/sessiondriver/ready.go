package sessiondriver

import (
	"context"
	"errors"
	"time"
)

// WaitReady runs wait — the wait for claude's REPL to come up — bounded by
// base, or by maxDuration when that ceiling is set and shorter. A wait the
// ceiling cut short comes back as a *MaxDurationError wrapping wait's
// error, so the run is recorded as a max-duration termination rather than
// a REPL that failed to start: the operator asked for the ceiling, and
// without this a `--max-duration 10s` still waited the full base window.
// Any other outcome, including a wait the base window or ctx ended, is
// wait's own error.
func WaitReady(ctx context.Context, base, maxDuration time.Duration, label string, wait func(context.Context) error) error {
	window := base
	capped := maxDuration > 0 && maxDuration < base
	if capped {
		window = maxDuration
	}
	start := time.Now()
	readyCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	err := wait(readyCtx)
	if err != nil && capped && ctx.Err() == nil && errors.Is(readyCtx.Err(), context.DeadlineExceeded) {
		return &MaxDurationError{
			Label:      label,
			Elapsed:    time.Since(start),
			Max:        maxDuration,
			Diagnostic: "the claude REPL never became ready",
			Cause:      err,
		}
	}
	return err
}

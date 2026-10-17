package apecmd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/repl"
)

// usageLimitWatch ends an `ape task` session that a claude.ai plan's usage
// limit stopped (H20, D47 Q7). Claude Code draws its limit line in the REPL
// and the turn ends there: no hook fires and the transcript stops growing,
// so before this the run sat until the idle ceiling (60 min by default) and
// was recorded as an idle timeout. The watch reads the pane, and on the
// limit line ends the run as a usage-limit stop (exit ExitUsageLimit,
// termination usage_limit with the reset).
//
// Unmeasured live: the line is Claude Code 2.1.302's template
// (repl.ParseUsageLimit), not a captured screen.
type usageLimitWatch struct {
	mu  sync.Mutex
	hit *repl.UsageLimit
}

// usageLimitPoll is how often the watch reads the pane. The line must show
// on two reads in a row with no hook between them, so a stop lands one to
// two polls after the limit.
var usageLimitPoll = 15 * time.Second

// usageLimitPane reads a session's pane (a test seam).
var usageLimitPane = repl.CapturePane

// run polls until ctx ends. session names the step's PTY session ("" before
// one starts), lastHook is when the last hook arrived, stop fails the step.
// The line must be the same on two reads, with no hook since the first: a
// session still working is not stopped by text that merely looks like the
// limit screen.
func (w *usageLimitWatch) run(ctx context.Context, session func() string, lastHook func() time.Time, stop func(error)) {
	var seen string
	var seenAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(usageLimitPoll):
		}
		name := session()
		if name == "" {
			continue
		}
		pane, err := usageLimitPane(ctx, name)
		if err != nil {
			continue
		}
		now := time.Now()
		ul, ok := repl.ParseUsageLimit(pane, now)
		switch {
		case !ok:
			seen = ""
			continue
		case ul.Line != seen || lastHook().After(seenAt):
			seen, seenAt = ul.Line, now
			continue
		}
		w.mu.Lock()
		w.hit = &ul
		w.mu.Unlock()
		err = usageLimitStop(ul)
		for {
			stop(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// usageLimitStop is the stop as the run error: the termination record and
// the exit code come from it.
func usageLimitStop(ul repl.UsageLimit) *pipeline.LimitStopError {
	msg := "usage limit: Claude Code stopped the session (" + ul.Line + ")"
	if ul.Resets != "" {
		msg += " — run it again after the limit resets (" + ul.Resets + ")"
	} else {
		msg += " — run it again after the limit resets"
	}
	return &pipeline.LimitStopError{Kind: pipeline.TerminationUsageLimit, Message: msg, Resets: ul.Resets, ResetsAt: ul.ResetsAt}
}

// stopped is the limit line that stopped the session, or nil.
func (w *usageLimitWatch) stopped() *repl.UsageLimit {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hit
}

// isUsageLimitStop reports whether a run ended on the usage-limit watch.
func isUsageLimitStop(runErr error) bool {
	lse, ok := errors.AsType[*pipeline.LimitStopError](runErr)
	return ok && lse.Kind == pipeline.TerminationUsageLimit
}

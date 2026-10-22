package apecmd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/claudeprobe"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/usage"
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
// (repl.ParseUsageLimit), not a captured screen — which is why the first
// real one per Claude Code version is kept (keepScreen, W4-A3).
type usageLimitWatch struct {
	// claudeBin is the claude the session runs ("" is claude on PATH):
	// its version names the kept limit screen (W4-A3).
	claudeBin string

	mu  sync.Mutex
	hit *repl.UsageLimit
}

// usageLimitPoll is how often the watch reads the pane. The line must show
// on two reads in a row with no hook between them, so a stop lands one to
// two polls after the limit.
var usageLimitPoll = 15 * time.Second

// usageLimitPane reads a session's pane (a test seam).
var usageLimitPane = repl.CapturePane

// usageLimitRaw reads a session's last raw PTY bytes, and
// usageLimitVersion the installed claude's version (test seams).
var (
	usageLimitRaw     = repl.RecentOutput
	usageLimitVersion = claudeprobe.InstalledVersion
)

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
		lse := usageLimitStop(ul)
		lse.Message += w.keepScreen(ctx, name)
		err = lse
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

// keepScreen keeps the session's raw limit screen and the last usage
// reading in the user cache (W4-A3: the first per Claude Code version), and
// says where, for the stop's message; "" when nothing is kept.
func (w *usageLimitWatch) keepScreen(ctx context.Context, session string) string {
	raw := usageLimitRaw(session)
	if len(raw) == 0 {
		return ""
	}
	bin := w.claudeBin
	if bin == "" {
		bin = "claude"
	}
	path, reading, wrote, err := usage.SaveLimitScreen(usageLimitVersion(ctx, bin), raw, usage.Load())
	if err != nil || path == "" {
		return ""
	}
	msg := "; Claude Code's limit screen (raw PTY bytes): " + path
	if !wrote {
		msg += " (kept from the first limit on this Claude Code version)"
	}
	if reading != "" {
		msg += ", the last usage reading before it: " + reading
	}
	return msg
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

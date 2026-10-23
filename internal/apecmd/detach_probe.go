package apecmd

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

// supervisorCheck is what one look at a detached run's process found.
//
// A run is declared lost (exit 76, <id>.lost, run-lost) only on a definite
// negative, never on a look that could not tell (F14, eval F-LOST1): under
// host load `ps` took longer than its timeout, and a live run was reported
// lost — its supervisor went on to finish the run while the caller had
// already acted on the loss.
type supervisorCheck int

const (
	// supervisorOurs: the pid is alive and is this run's process.
	supervisorOurs supervisorCheck = iota
	// supervisorUnknown: the look could not tell (ps failed or timed out,
	// the process table could not be read). A caller keeps waiting.
	supervisorUnknown
	// supervisorNotOurs: definitely not this run's — the pid is gone
	// (ESRCH, no /proc entry, a zombie) or another program has it now.
	supervisorNotOurs
)

// probeSupervisorFn is the one look at a handle's supervisor every caller
// goes through (probeSupervisor, per platform); a test swaps it.
var probeSupervisorFn = probeSupervisor

// lostConfirmDelay separates the two definite negatives a loss needs.
// One second is `ape run wait`'s own poll interval, so every caller —
// those that look once (ape run status, ape status) and those that poll
// (ape run wait, the change verbs' session waits) — declares a loss on
// the same evidence: two looks a poll apart. It is paid only when the
// first look is already negative, and not at all for a run an earlier
// command found lost (its <id>.lost exists).
var lostConfirmDelay = time.Second

// errIdentityUnknown: a process's identity could not be read, so nothing
// was signalled; `ape run stop` exits 75, try again.
var errIdentityUnknown = errors.New("could not tell whether the process is the run's")

// lookTwice looks once and, on a definite negative, once more after
// lostConfirmDelay: a negative counts only when seen twice. A context
// ended during the delay is an unknown.
func lookTwice(ctx context.Context, look func() supervisorCheck) supervisorCheck {
	if c := look(); c != supervisorNotOurs {
		return c
	}
	if !sleepCtx(ctx, lostConfirmDelay) {
		return supervisorUnknown
	}
	return look()
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// supervisorLost reports whether the detached run at path, h as last read,
// is lost: no exit recorded and its supervisor definitely not this run's,
// seen twice. cur is the freshest read of the handle; when cur has an exit
// the run ended (the supervisor recorded it and then ended between reads)
// and is not lost. An unknown look is never a loss: the caller keeps
// waiting, or reports the run as running.
//
// A run an earlier command found lost (<id>.lost exists) needs one
// definite negative now: that is the second sighting, and a lost run's
// supervisor never comes back. A marker an older ape wrote for a live run
// (F-LOST1) is overruled by a look that finds the supervisor ours.
func supervisorLost(ctx context.Context, path string, h *detachedHandle) (lost bool, cur *detachedHandle) {
	cur = h
	if h.ExitCode != nil || h.SupervisorPID == 0 {
		return false, cur
	}
	if probeSupervisorFn(ctx, h) != supervisorNotOurs {
		return false, cur
	}
	if _, err := os.Stat(lostMarkerOfHandle(path)); err != nil {
		if !sleepCtx(ctx, lostConfirmDelay) {
			return false, cur
		}
		if h2, err := readHandle(path); err == nil {
			cur = h2
		}
		if cur.ExitCode != nil || probeSupervisorFn(ctx, cur) != supervisorNotOurs {
			return false, cur
		}
	}
	// Re-read once more: the supervisor may have recorded the exit just
	// before it ended.
	if h2, err := readHandle(path); err == nil {
		cur = h2
	}
	return cur.ExitCode == nil, cur
}

// lostMarkerOfHandle is lostMarkerPath for a handle's path.
func lostMarkerOfHandle(path string) string {
	return strings.TrimSuffix(path, ".json") + ".lost"
}

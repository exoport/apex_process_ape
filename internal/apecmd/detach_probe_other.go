//go:build !windows && !linux

package apecmd

import (
	"context"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// probeSupervisor: a pid that is gone is a definite negative; a live one's
// command line comes from ps (macOS and the BSDs have no /proc), whose
// failure or timeout is an unknown (F14).
func probeSupervisor(ctx context.Context, h *detachedHandle) supervisorCheck {
	if h.SupervisorPID <= 0 || !pidAlive(h.SupervisorPID) {
		return supervisorNotOurs
	}
	return probeSupervisorPS(ctx, h)
}

// probeWorker: a lock whose process is gone, or another's now, is a
// definite negative; a live one's command line comes from ps.
func probeWorker(ctx context.Context, l *apexv1.RunLock) supervisorCheck {
	if l.PID <= 0 || !l.Alive() {
		return supervisorNotOurs
	}
	return probeWorkerPS(ctx, l)
}

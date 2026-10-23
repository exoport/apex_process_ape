//go:build !windows && !linux

package apecmd

import "context"

// probeSupervisor: a pid that is gone is a definite negative; a live one's
// command line comes from ps (macOS and the BSDs have no /proc), whose
// failure or timeout is an unknown (F14).
func probeSupervisor(ctx context.Context, h *detachedHandle) supervisorCheck {
	if h.SupervisorPID <= 0 || !pidAlive(h.SupervisorPID) {
		return supervisorNotOurs
	}
	return probeSupervisorPS(ctx, h)
}

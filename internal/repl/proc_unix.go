//go:build linux || darwin

package repl

import (
	"syscall"
	"time"
)

// procGroupKillGrace is how long a SIGTERMed child group has to shut
// down cleanly before we escalate to SIGKILL. Mirrors the
// programmatic runner's `internal/pipeline/proc_unix.go` constant of
// the same name — 500ms is enough for a well-behaved REPL + bridge
// child to flush state, short enough that an unresponsive group
// doesn't hang the runner.
const procGroupKillGrace = 500 * time.Millisecond

// terminateGroup sends SIGTERM to the child's process group, then
// SIGKILL after a grace window. go-pty's Unix backend sets
// SysProcAttr.Setsid on the child, so its pgid equals its pid; a
// negative-pid kill addresses the whole group, reaping any
// grandchildren the REPL may have spawned (e.g. background tasks or
// subagent processes).
//
// Today claude's interactive REPL is not known to spawn long-lived
// grandchildren during a stage — the orphan-subagent gap PLAN-2 / F1
// solved was specific to `claude -p`'s Task-tool path. FE is
// defensive: if a future plan extends claude's interactive mode to
// launch background processes, KillSession already cleans them up.
func terminateGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	go func() {
		time.Sleep(procGroupKillGrace)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}()
}

//go:build linux || darwin

package service

import (
	"os/exec"
	"syscall"
	"time"
)

// procGroupKillGrace is how long a SIGTERMed child group has to exit
// cleanly before the daemon escalates to SIGKILL. Mirrors the runner's
// constant of the same name (internal/repl/proc_unix.go): long enough for a
// well-behaved ape child + its claude REPL to flush, short enough that an
// unresponsive group doesn't hang shutdown.
const procGroupKillGrace = 500 * time.Millisecond

// configureProcessGroup makes the spawned child the leader of a new process
// group (pgid == pid) so the daemon can signal the child and every
// descendant it spawns (claude, sub-agents, the bridge) with one
// negative-pid kill. Without this the child's grandchildren would outlive a
// job.stop / drain.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateGroup sends SIGTERM to the child's whole process group, then
// SIGKILL after the grace window. Because configureProcessGroup set
// Setpgid, the child's pgid equals its pid, so a negative-pid kill
// addresses the group. A non-positive pid or an already-exited group is a
// harmless no-op (kill returns ESRCH).
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

//go:build linux || darwin

package service

import (
	"context"
	"os/exec"
	"syscall"
	"time"

	"github.com/exoport/apex_process_ape/internal/proctree"
)

// procGroupKillGrace is how long a SIGTERMed child group has to exit
// cleanly before the daemon escalates to SIGKILL. Mirrors the runner's
// constant of the same name (internal/repl/proc_unix.go): long enough for a
// well-behaved ape child + its claude REPL to flush, short enough that an
// unresponsive group doesn't hang shutdown.
const procGroupKillGrace = 500 * time.Millisecond

// configureProcessGroup makes the spawned child the leader of a new process
// group (pgid == pid) so the daemon can signal the child and every
// descendant that stays in its group with one negative-pid kill. Without
// this the child's grandchildren would outlive a job.stop / drain. claude
// does not stay: terminateGroup reaches it by pid.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateGroup stops a job: its whole process group, AND every process
// below the child that left the group. The job's claude is one: ape task
// runs it in a PTY of its own session, so a group kill never reached it,
// and a claude whose ape task was SIGKILLed — before it could stop its
// session — lived on with its Bash tool's commands. SIGTERM first, then
// SIGKILL after the grace window (proctree.KillTree). It returns once the
// SIGKILL is sent: the escalation used to sleep through the grace in a
// goroutine, which a daemon exiting after a forced drain could cut short.
// A non-positive pid is a no-op; an exited group is harmless (ESRCH).
func terminateGroup(pid int) {
	proctree.KillTree(context.Background(), pid, procGroupKillGrace)
}

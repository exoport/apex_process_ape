//go:build linux || darwin

package pipeline

import (
	"os/exec"
	"syscall"
	"time"
)

// procGroupKillGrace is how long a SIGTERMed claude subagent group
// has to shut down cleanly before the cancel hook escalates to SIGKILL
// for the whole group. The escalation is what closes any pipes a
// SIGTERM-trapping grandchild was holding open, which in turn unblocks
// the scanner goroutines waiting on EOF and lets the surrounding
// cmd.Wait / wg.Wait complete.
const procGroupKillGrace = 500 * time.Millisecond

// configureProcessGroup makes cmd's child process its own process-group
// leader (via SysProcAttr.Setpgid=true) and rewires Cmd.Cancel so that
// context cancellation SIGTERMs the whole group, with a follow-up
// SIGKILL after procGroupKillGrace. PLAN-2 / F1: closes the
// orphan-subagent gap where claude-spawned Task-tool grandchildren
// survived PLAN-1 / I2's confirmed-quit because the default Cancel hook
// only delivered SIGKILL to the direct child.
//
// We intentionally do not set Cmd.WaitDelay here. WaitDelay only
// observes Go-managed copying goroutines (e.g. when Stdout is set to
// an io.Writer); StdoutPipe / StderrPipe readers are user-managed and
// invisible to WaitDelay. Closing the pipes correctly requires SIGKILL
// to the group, which is exactly what the escalator goroutine does.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		// With Setpgid=true the child's pgid equals its pid, so a
		// negative-pid kill addresses the whole group.
		termErr := syscall.Kill(-pid, syscall.SIGTERM)
		// Escalate to SIGKILL after the grace period. Detached
		// goroutine — fire-and-forget — and the SIGKILL is benign
		// (ESRCH absorbed) if the group has already exited.
		go func() {
			time.Sleep(procGroupKillGrace)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}()
		return termErr
	}
}


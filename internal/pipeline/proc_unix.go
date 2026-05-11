//go:build linux || darwin

package pipeline

import (
	"os/exec"
	"syscall"
	"time"
)

// procGroupKillGrace is how long Go's exec package waits after firing
// the user-supplied Cancel hook (SIGTERM to the process group) before
// the framework escalates by calling cmd.Process.Kill() on the
// immediate child. The pipeline runner additionally SIGKILLs the whole
// group after Wait returns when ctx was cancelled, so this constant
// caps how long a well-behaved subagent has to shut down cleanly.
const procGroupKillGrace = 500 * time.Millisecond

// configureProcessGroup makes cmd's child process its own process-group
// leader (via SysProcAttr.Setpgid=true) and rewires Cmd.Cancel so that
// context cancellation SIGTERMs the whole group, not just the immediate
// child. PLAN-2 / F1: closes the orphan-subagent gap where claude-spawned
// Task tool grandchildren survived PLAN-1 / I2's confirmed-quit because
// the default Cancel hook only delivered SIGKILL to the direct child.
//
// Pairs with finalProcessGroupCleanup, which SIGKILLs the group after
// Wait returns to mop up grandchildren that ignored or outran the
// SIGTERM.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// With Setpgid=true the child's pgid equals its pid, so a
		// negative-pid kill addresses the whole group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = procGroupKillGrace
}

// finalProcessGroupCleanup SIGKILLs the process group rooted at cmd's
// immediate child. Called by runClaude after Wait returns when the
// surrounding context was cancelled — defends against grandchildren
// that trapped SIGTERM or were spawned between the Cancel hook firing
// and the immediate child exiting. Idempotent; safe when the group
// has already exited (ESRCH is absorbed).
func finalProcessGroupCleanup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

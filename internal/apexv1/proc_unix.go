//go:build !windows

package apexv1

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// checkWatchdog is the shell a check runs under: a watchdog in the check's
// process group that kills the group when ape's end of fd 3 closes without
// the word "ok" — ape ended mid-check, however it ended — and then the
// check itself, as `sh -c <cmd>` with fd 3 closed (exec: the same pid).
const checkWatchdog = `(read ok <&3; [ "$ok" = ok ] || kill -9 0) </dev/null >/dev/null 2>&1 &
exec 3<&-
exec sh -c "$1"`

// checkGroup is a running check's process group: its own (Setpgid), so a
// timeout kills everything it started, not only the shell.
type checkGroup struct {
	pid  int
	r, w *os.File
}

// prepareCheck makes c — `sh -c <cmd>` — run under the watchdog, in its
// own process group, with Pdeathsig on Linux.
func prepareCheck(c *exec.Cmd, cmd string) (*checkGroup, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.Args = []string{"sh", "-c", checkWatchdog, "ape-check", cmd}
	c.ExtraFiles = []*os.File{r}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setDeathSignal(c.SysProcAttr)
	return &checkGroup{r: r, w: w}, nil
}

// started records the check's pid (its process group's id) and drops this
// process's copy of the watchdog's read end.
func (g *checkGroup) started(c *exec.Cmd) {
	g.pid = c.Process.Pid
	_ = g.r.Close()
}

// done tells the watchdog the check ended on its own (finished), so it
// leaves the group alone, and closes ape's end.
func (g *checkGroup) done(finished bool) {
	_ = g.r.Close()
	if finished {
		_, _ = g.w.WriteString("ok\n")
	}
	_ = g.w.Close()
}

// kill kills the started check c's whole process group.
func (g *checkGroup) kill(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}

// cpu is the CPU time the check's process group has used, its reaped
// children's included; ok is false when it cannot be read.
func (g *checkGroup) cpu() (time.Duration, bool) {
	if g.pid <= 0 {
		return 0, false
	}
	return groupCPU(g.pid)
}

// processAlive reports whether a process with pid exists (signal 0).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

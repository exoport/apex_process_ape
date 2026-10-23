//go:build !windows

package apexv1

import (
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
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

// ProcessGroup is this process's group id.
func ProcessGroup() int { return syscall.Getpgrp() }

// groupMember is a live process of a check's process group.
type groupMember struct {
	pid  int
	argv []string
}

// isWatchdog reports whether m is the check's own watchdog (checkWatchdog's
// background subshell): ape's, not the check's. On Linux its argv holds
// the watchdog script whole; where ps joins the arguments, the line holds
// its start.
func (m groupMember) isWatchdog() bool {
	return slices.Contains(m.argv, checkWatchdog) ||
		(len(m.argv) == 1 && strings.Contains(m.argv[0], "(read ok <&3;"))
}

// leftoverGrace is how long what a check left running is given to end on
// SIGTERM before its group is SIGKILLed. A var so tests can shorten it.
var leftoverGrace = time.Second

// endLeftovers ends what a check that exited on its own left running in
// its process group (F15): the check's shell has exited, so anything still
// in the group — a server it started, a `sleep` a killed subshell
// orphaned — is a leftover. Each is named "<pid> <command>"; the group is
// sent SIGTERM, then SIGKILL after leftoverGrace. A process that left the
// group (setsid, a daemon) is not seen here.
func (g *checkGroup) endLeftovers() []string {
	if g.pid <= 0 {
		return nil
	}
	left := g.leftovers()
	if len(left) == 0 {
		return nil
	}
	names := make([]string, len(left))
	for i, m := range left {
		names[i] = strconv.Itoa(m.pid) + " " + clip(strings.Join(m.argv, " "), 120)
	}
	_ = syscall.Kill(-g.pid, syscall.SIGTERM)
	deadline := time.Now().Add(leftoverGrace)
	for time.Now().Before(deadline) {
		if len(g.leftovers()) == 0 {
			return names // an empty group's id may be reused: never signal it
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-g.pid, syscall.SIGKILL)
	return names
}

// leftovers are the group's live members other than the watchdog.
func (g *checkGroup) leftovers() []groupMember {
	var left []groupMember
	for _, m := range groupMembers(g.pid) {
		if !m.isWatchdog() {
			left = append(left, m)
		}
	}
	return left
}

// parseGroupMembers reads ps lines "<pid> <pgid> <stat> <command…>" for the
// processes of pgid. A line that does not parse — a command line's own
// newline continued — is skipped.
func parseGroupMembers(out string, pgid int) []groupMember {
	var members []groupMember
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		const psColumns = 4 // pid, pgid, stat, the command's first word
		if len(f) < psColumns {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		g, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || g != pgid || strings.HasPrefix(f[2], "Z") {
			continue
		}
		members = append(members, groupMember{pid: pid, argv: []string{strings.Join(f[3:], " ")}})
	}
	return members
}

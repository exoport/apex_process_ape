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

// scopeWatchdog is checkWatchdog for a check in a scope of its own (v1.4,
// scope_linux.go): ape gone, it kills the whole scope — what left the
// process group too — and then the group.
const scopeWatchdog = `(read ok <&3; [ "$ok" = ok ] || { cg=$(sed -n 's/^0:://p' /proc/self/cgroup); [ -n "$cg" ] && echo 1 > "/sys/fs/cgroup$cg/cgroup.kill"; kill -9 0; }) </dev/null >/dev/null 2>&1 &
exec 3<&-
exec sh -c "$1"`

// checkGroup is a running check's process group: its own (Setpgid), so a
// timeout kills everything it started, not only the shell — and on Linux,
// where a user manager answers, its own scope (unit, dir), which holds
// what left the group as well.
type checkGroup struct {
	pid       int
	r, w      *os.File
	unit, dir string
}

// scopeUnit is the check's scope unit, "" without one.
func (g *checkGroup) scopeUnit() string { return g.unit }

// reaping is the check's reaping.
func (g *checkGroup) reaping() string {
	if g.unit != "" {
		return ReapingScope
	}
	return ReapingGroup
}

// prepareCheck makes c — `sh -c <cmd>` — run under the watchdog, in its
// own process group (and scope, where one works), with Pdeathsig on Linux.
func prepareCheck(c *exec.Cmd, cmd string) (*checkGroup, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	g := &checkGroup{r: r, w: w}
	c.Args = []string{"sh", "-c", checkWatchdog, "ape-check", cmd}
	if unit, dir, runner := newScope(); unit != "" {
		g.unit, g.dir = unit, dir
		c.Path = runner
		c.Args = scopeArgv(runner, unit, []string{"sh", "-c", scopeWatchdog, "ape-check", cmd})
	}
	c.ExtraFiles = []*os.File{r}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setDeathSignal(c.SysProcAttr)
	return g, nil
}

// started records the check's pid (its process group's id) and drops this
// process's copy of the watchdog's read end.
func (g *checkGroup) started(c *exec.Cmd) {
	g.pid = c.Process.Pid
	_ = g.r.Close()
	// A scope exists once systemd-run has registered it and moved itself
	// in: wait for that (briefly), so the idle window's first CPU reading
	// comes after systemd-run's own work, which is not the check's.
	if g.unit != "" {
		for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if pids := scopePIDs(g.dir); len(pids) > 0 {
				// The first pid is the check's: systemd-run, until it execs.
				if argv := pidArgv(pids[0]); len(argv) == 0 || !strings.HasSuffix(argv[0], "systemd-run") {
					return
				}
			}
		}
	}
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

// kill kills the started check c's whole process group, and its scope.
func (g *checkGroup) kill(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	if g.unit != "" {
		scopeKill(g.dir)
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}

// cpu is the CPU time the check's process group has used, its reaped
// children's included — its scope's, where it has one; ok is false when it
// cannot be read.
func (g *checkGroup) cpu() (time.Duration, bool) {
	if g.pid <= 0 {
		return 0, false
	}
	if g.unit != "" {
		// The scope's alone, never the group's beside it (two sources
		// would read as progress), at the group reading's granularity: a
		// shell's millisecond of start-up is not work.
		return scopeCPU(g.dir)
	}
	return groupCPU(g.pid)
}

// scopeBusyMin is the CPU a scope must have used since the last sample to
// count as progress: its cpu.stat counts microseconds, so the few a
// sleeping check's processes use (a shell's start-up, the kernel's
// accounting) would otherwise read as work. A busy check uses hundreds of
// milliseconds a sample.
const scopeBusyMin = 20 * time.Millisecond

// busy reports whether CPU time went from last to now: any change for a
// process group (counted in clock ticks), at least scopeBusyMin for a scope.
func (g *checkGroup) busy(last, now time.Duration) bool {
	if g.unit != "" {
		return now-last >= scopeBusyMin
	}
	return now != last
}

// memoryPeak is the scope's memory.peak in bytes: read while the watchdog
// still holds the scope open, before done. 0 without a scope.
func (g *checkGroup) memoryPeak() int64 {
	if g.unit == "" {
		return 0
	}
	return scopeMemoryPeak(g.dir)
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
	return slices.Contains(m.argv, checkWatchdog) || slices.Contains(m.argv, scopeWatchdog) ||
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
	if g.unit != "" {
		// A scope: each leftover by pid, never the group, whose watchdog
		// holds the scope open until done (its memory.peak is read then).
		for _, m := range left {
			_ = syscall.Kill(m.pid, syscall.SIGTERM)
		}
		deadline := time.Now().Add(leftoverGrace)
		for time.Now().Before(deadline) && len(g.leftovers()) > 0 {
			time.Sleep(20 * time.Millisecond)
		}
		for _, m := range g.leftovers() {
			_ = syscall.Kill(m.pid, syscall.SIGKILL)
		}
		return names
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

// scopeMemberArgv is a scope member's command line. A process in the
// middle of an exec reads an empty one for an instant (a check's `sleep`
// just forked): it is read again briefly, and named "?" if it stays empty —
// never skipped, since only the watchdog is not a leftover.
func scopeMemberArgv(pid int) []string {
	for range 10 {
		if argv := pidArgv(pid); len(argv) > 0 && argv[0] != "" {
			return argv
		}
		time.Sleep(2 * time.Millisecond)
	}
	return []string{"?"}
}

// leftovers are the group's live members other than the watchdog — the
// scope's, where the check has one, so what left the group is among them.
func (g *checkGroup) leftovers() []groupMember {
	var left []groupMember
	seen := map[int]bool{}
	if g.unit != "" {
		for _, pid := range scopePIDs(g.dir) {
			m := groupMember{pid: pid, argv: scopeMemberArgv(pid)}
			if seen[pid] = true; !m.isWatchdog() {
				left = append(left, m)
			}
		}
	}
	// The group's too, in a scope: a process can leave the scope and keep
	// the group (a systemd-run --scope the check runs).
	for _, m := range groupMembers(g.pid) {
		if !seen[m.pid] && !m.isWatchdog() {
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

// contains reports whether pid is the check's: in its scope, or its
// process group.
func (g *checkGroup) contains(pid int) bool {
	if g.unit != "" {
		for _, p := range scopePIDs(g.dir) {
			if p == pid {
				return true
			}
		}
	}
	if g.pid <= 0 {
		return false
	}
	pgid, err := syscall.Getpgid(pid)
	return err == nil && pgid == g.pid
}

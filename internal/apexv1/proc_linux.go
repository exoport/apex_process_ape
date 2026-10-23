package apexv1

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// setDeathSignal kills the check's shell when the thread that started it
// — ape's — ends: SIGKILLed, the shell dies with it, even in its own
// process group (2.2). The watchdog takes the rest of the group.
func setDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }

// clockTick is the unit /proc/<pid>/stat counts CPU time in (USER_HZ,
// 100 on every Linux ape runs on). Only changes are compared, so an odd
// USER_HZ misstates the total, never the progress.
const clockTick = 10 * time.Millisecond

// groupCPU sums utime+stime+cutime+cstime over the processes of process
// group pgid, from /proc/<pid>/stat: what the group's live processes used,
// and what its reaped ones left in their parents.
func groupCPU(pgid int) (time.Duration, bool) {
	stats, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil || len(stats) == 0 {
		return 0, false
	}
	var ticks int64
	found := false
	for _, path := range stats {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // gone since the glob
		}
		group, t, ok := parseProcStat(string(data))
		if ok && group == pgid {
			ticks += t
			found = true
		}
	}
	return time.Duration(ticks) * clockTick, found
}

// parseProcStat reads a /proc/<pid>/stat line's process group and its
// utime+stime+cutime+cstime, in clock ticks. The command name, in
// parentheses, may hold spaces and parentheses: fields count from the last
// ')'.
func parseProcStat(line string) (pgid int, ticks int64, ok bool) {
	_, rest, found := strings.CutLast(line, ")")
	if !found {
		return 0, 0, false
	}
	f := strings.Fields(rest)
	// state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt
	// cmajflt utime stime cutime cstime …
	if len(f) < 15 {
		return 0, 0, false
	}
	pgid, err := strconv.Atoi(f[2])
	if err != nil {
		return 0, 0, false
	}
	for _, s := range f[11:15] {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		ticks += n
	}
	return pgid, ticks, true
}

// groupMembers lists the live processes of process group pgid, from
// /proc: each one's pid and command line. A zombie is not listed: it has
// ended, and holds nothing.
func groupMembers(pgid int) []groupMember {
	stats, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		return nil
	}
	var out []groupMember
	for _, path := range stats {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // gone since the glob
		}
		group, _, ok := parseProcStat(string(data))
		if !ok || group != pgid || procState(string(data)) == "Z" {
			continue
		}
		dir := filepath.Dir(path)
		pid, err := strconv.Atoi(filepath.Base(dir))
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		out = append(out, groupMember{pid: pid, argv: argv})
	}
	return out
}

// procState is a /proc/<pid>/stat line's state letter, the first field
// after the parenthesised command name.
func procState(line string) string {
	_, rest, found := strings.CutLast(line, ")")
	if !found {
		return ""
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is the proc filesystem the sampler reads. A constant so the joins
// below are one path expression rather than a separator embedded in each.
const procRoot = "/proc"

// pidStatMinFields is how many whitespace-separated fields must follow a
// /proc/<pid>/stat comm for utime (index 11) and stime (index 12) to be present.
const pidStatMinFields = 13

// Sampling the guest from inside it (PLAN-24 D6).
//
// Everything here reads /proc in the process's own namespace, which for the
// agent is the workspace's. It is Linux-only because a guest is; the pure
// arithmetic that turns two samples into a verdict lives in guestsample.go so it
// is testable everywhere, including on the Windows CI job that must still
// compile this command.

// SampleGuest reads /proc/stat and every /proc/<pid>/stat.
//
// Best-effort throughout: a process that exits mid-walk is skipped rather than
// failing the sample. A sample with no readable /proc/stat returns zeroed
// jiffies, which DiffSamples reports as an unknown utilisation — the agent then
// publishes no verdict rather than a fabricated one.
func SampleGuest() GuestSample {
	s := GuestSample{PerProcess: map[string]uint64{}}
	if data, err := os.ReadFile(filepath.Join(procRoot, "stat")); err == nil {
		s.TotalJiffies, s.IdleJiffies = parseProcStatCPU(string(data))
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return s
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, perr := strconv.Atoi(e.Name()); perr != nil {
			continue // not a pid
		}
		data, rerr := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if rerr != nil {
			continue // exited between the readdir and the read
		}
		name, jiffies, ok := parsePidStat(string(data))
		if !ok {
			continue
		}
		s.PerProcess[name] += jiffies
	}
	return s
}

// GuestLoad1 reads the guest's 1-minute load average; 0 when unreadable.
func GuestLoad1() float64 {
	data, err := os.ReadFile(filepath.Join(procRoot, "loadavg"))
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}

// parseProcStatCPU reads the aggregate "cpu" line of /proc/stat and returns its
// total and idle jiffies. Idle counts idle + iowait: a process blocked on disk
// is waiting, not working, and counting iowait as busy would keep a workspace
// alive on nothing but a slow mount.
func parseProcStatCPU(stat string) (total, idle uint64) {
	for line := range strings.SplitSeq(stat, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		for i, f := range fields[1:] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				continue
			}
			total += v
			// Fields after "cpu": user nice system IDLE IOWAIT irq …
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return total, idle
	}
	return 0, 0
}

// parsePidStat extracts a process's comm and its utime+stime from a
// /proc/<pid>/stat line.
//
// The comm field is parsed by finding the LAST ')', not the first: a process
// may legitimately be named "foo) (bar" and every naive split on whitespace or
// on the first paren gets it wrong, shifting every subsequent field and
// producing garbage jiffy counts.
func parsePidStat(line string) (name string, jiffies uint64, ok bool) {
	open := strings.IndexByte(line, '(')
	closeIdx := strings.LastIndexByte(line, ')')
	if open < 0 || closeIdx < open {
		return "", 0, false
	}
	name = line[open+1 : closeIdx]
	fields := strings.Fields(line[closeIdx+1:])
	// After the comm: state(0) ppid(1) pgrp(2) session(3) tty(4) tpgid(5)
	// flags(6) minflt(7) cminflt(8) majflt(9) cmajflt(10) utime(11) stime(12)
	if len(fields) < pidStatMinFields {
		return "", 0, false
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return "", 0, false
	}
	return name, utime + stime, true
}

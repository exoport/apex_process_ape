//go:build linux

package proctree

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procRoot is where the scan reads processes from.
const procRoot = "/proc"

// leftoverSettle is how long the scan lets the processes the session's
// teardown has just SIGKILLed finish exiting before it looks again: a
// killed process keeps its environment until it is gone, and must not be
// reported as one the session left behind.
var leftoverSettle = 200 * time.Millisecond

// findLeftovers scans /proc for the processes whose environment holds
// marker, and selects the leftovers among them (selectLeftovers).
func findLeftovers(marker string) []procInfo {
	pick := func() []procInfo {
		boot := bootTime()
		marked := markedProcs([]byte(marker), boot)
		if len(marked) == 0 {
			return nil
		}
		lookup := func(pid int) (procInfo, bool) { return readProc(pid, boot) }
		self := os.Getpid()
		return selectLeftovers(marked, self, Ancestors(self, Parent), lookup)
	}
	if len(pick()) == 0 {
		return nil
	}
	time.Sleep(leftoverSettle)
	return pick()
}

// markedProcs lists the processes whose environment holds the exact entry
// marker. An environment that cannot be read — another user's, a process
// gone since the listing — is skipped.
func markedProcs(marker []byte, boot time.Time) []procInfo {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []procInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		env, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "environ"))
		if err != nil || !bytes.Contains(env, marker) || !hasEntry(env, marker) {
			continue
		}
		if p, ok := readProc(pid, boot); ok {
			out = append(out, p)
		}
	}
	return out
}

// hasEntry reports whether the NUL-separated environment env holds entry
// exactly: APE_SESSION=task/r1 must not match APE_SESSION=task/r10.
func hasEntry(env, entry []byte) bool {
	for e := range bytes.SplitSeq(env, []byte{0}) {
		if bytes.Equal(e, entry) {
			return true
		}
	}
	return false
}

// readProc reads pid's stat and command line.
func readProc(pid int, boot time.Time) (procInfo, bool) {
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return procInfo{}, false
	}
	p, ok := parseFullStat(string(stat))
	if !ok {
		return procInfo{}, false
	}
	p.pid, p.boot = pid, boot
	if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		for a := range bytes.SplitSeq(bytes.TrimRight(cmdline, "\x00"), []byte{0}) {
			p.argv = append(p.argv, string(a))
		}
		if len(p.argv) == 1 && p.argv[0] == "" {
			p.argv = nil
		}
	}
	return p, true
}

// parseFullStat reads state, ppid, pgrp, session and starttime from a
// /proc/<pid>/stat line; fields count from the command name's LAST ')'.
func parseFullStat(stat string) (procInfo, bool) {
	_, rest, found := strings.CutLast(stat, ")")
	if !found {
		return procInfo{}, false
	}
	// state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt
	// cmajflt utime stime cutime cstime priority nice num_threads
	// itrealvalue starttime …
	f := strings.Fields(rest)
	if len(f) < 20 || len(f[0]) != 1 {
		return procInfo{}, false
	}
	var p procInfo
	var err error
	p.state = f[0][0]
	if p.ppid, err = strconv.Atoi(f[1]); err != nil {
		return procInfo{}, false
	}
	if p.pgid, err = strconv.Atoi(f[2]); err != nil {
		return procInfo{}, false
	}
	if p.sid, err = strconv.Atoi(f[3]); err != nil {
		return procInfo{}, false
	}
	if p.start, err = strconv.ParseUint(f[19], 10, 64); err != nil {
		return procInfo{}, false
	}
	return p, true
}

// bootTime is the system's boot time, from /proc/stat's btime; zero when
// it cannot be read (a leftover is then recorded without its start).
func bootTime() time.Time {
	data, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}
	}
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}

// running reports whether p is still the process the scan found — the same
// pid AND start time, so a reused pid is not it — and not a zombie.
func running(p procInfo) bool {
	q, ok := readProc(p.pid, p.boot)
	return ok && q.start == p.start && q.state != 'Z' && q.state != 'X'
}

// stopLeftover stops p's tree, unless p is gone or its pid was reused,
// and reports whether it is gone.
func stopLeftover(ctx context.Context, p procInfo, grace time.Duration) string {
	if !running(p) {
		return LeftoverKilled // gone on its own since the scan
	}
	KillTree(ctx, p.pid, grace)
	// KillTree returns once SIGKILL is sent; the process may take a moment
	// to exit (and its parent, init, to reap it: a zombie counts as gone).
	deadline := time.Now().Add(time.Second)
	for running(p) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if running(p) {
		return LeftoverKillFailed
	}
	return LeftoverKilled
}

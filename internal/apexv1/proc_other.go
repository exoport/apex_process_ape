//go:build !windows && !linux

package apexv1

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// setDeathSignal: no Pdeathsig outside Linux; the watchdog in the check's
// process group (checkWatchdog) is what ends it with ape.
func setDeathSignal(*syscall.SysProcAttr) {}

// groupCPU sums the CPU time `ps` reports for the processes of process
// group pgid (macOS, the BSDs): user+system, the reaped children's
// included where ps counts them.
func groupCPU(pgid int) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pgid=,time=").Output()
	if err != nil {
		return 0, false
	}
	return sumGroupTimes(string(out), pgid)
}

// sumGroupTimes adds the `time` column of ps lines "<pgid> <time>" whose
// pgid is the one asked for.
func sumGroupTimes(out string, pgid int) (time.Duration, bool) {
	var total time.Duration
	found := false
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if g, err := strconv.Atoi(f[0]); err != nil || g != pgid {
			continue
		}
		if d, ok := parsePSTime(f[1]); ok {
			total += d
			found = true
		}
	}
	return total, found
}

// groupMembers lists the live processes of process group pgid, from `ps`
// (macOS, the BSDs): each one's pid and command line, as one argument —
// ps prints the arguments joined. A zombie is not listed.
func groupMembers(pgid int) []groupMember {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,pgid=,stat=,command=").Output()
	if err != nil {
		return nil
	}
	return parseGroupMembers(string(out), pgid)
}

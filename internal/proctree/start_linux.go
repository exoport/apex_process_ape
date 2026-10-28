//go:build linux

package proctree

import (
	"math"
	"time"
)

// StartTime is pid's start time, from /proc/<pid>/stat and the boot time;
// false when either cannot be read.
func StartTime(pid int) (time.Time, bool) {
	boot := bootTime()
	if boot.IsZero() || pid <= 0 {
		return time.Time{}, false
	}
	p, ok := readProc(pid, boot)
	if !ok || p.start > uint64(math.MaxInt64/int64(clockTick)) {
		return time.Time{}, false
	}
	return boot.Add(time.Duration(p.start) * clockTick).UTC(), true
}

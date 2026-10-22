//go:build linux

package procstart

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// userHZ is the unit of /proc's tick counts. It is a fixed userspace ABI
// value (USER_HZ), 100 on every architecture Go runs Linux on, whatever
// the kernel's own HZ.
const userHZ = 100

// osStart is now minus the process's age: /proc/self/stat's starttime
// (field 22, ticks since boot) against /proc/uptime (seconds since boot,
// to the hundredth), both on the boot-time clock, so suspend does not
// skew them apart.
func osStart() (time.Time, bool) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return time.Time{}, false
	}
	now := time.Now()
	up, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return time.Time{}, false
	}
	ticks, ok := startTicks(string(stat))
	if !ok {
		return time.Time{}, false
	}
	upField, _, _ := strings.Cut(strings.TrimSpace(string(up)), " ")
	uptime, err := strconv.ParseFloat(upField, 64)
	if err != nil {
		return time.Time{}, false
	}
	age := time.Duration((uptime - float64(ticks)/userHZ) * float64(time.Second))
	if age < 0 {
		return time.Time{}, false
	}
	return now.Add(-age), true
}

// startTicks reads starttime from a /proc/<pid>/stat line. The command
// name is in parentheses and may hold spaces and parentheses, so the
// fields are read after its LAST closing parenthesis: state is the first
// of them (field 3), starttime the twentieth (field 22).
func startTicks(stat string) (uint64, bool) {
	_, rest, found := strings.CutLast(stat, ")")
	if !found {
		return 0, false
	}
	fields := strings.Fields(rest)
	if len(fields) < 20 {
		return 0, false
	}
	n, err := strconv.ParseUint(fields[19], 10, 64)
	return n, err == nil
}

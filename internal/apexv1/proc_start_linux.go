package apexv1

import (
	"os"
	"strconv"
	"strings"
)

// processStart is an opaque identity of when pid started (Q144): with the
// pid it tells a live owner from a later process that reuses its pid. On
// Linux, /proc/<pid>/stat's starttime (field 22, clock ticks since boot).
func processStart(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// The command name, field 2, is in parentheses and may hold spaces.
	_, rest, ok := strings.CutLast(string(data), ")")
	if !ok {
		return "", false
	}
	f := strings.Fields(rest)
	const starttime = 22 - 3 // f[0] is field 3
	if len(f) <= starttime {
		return "", false
	}
	return f[starttime], true
}

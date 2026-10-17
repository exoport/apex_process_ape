//go:build linux

package proctree

import (
	"os"
	"strconv"
	"strings"
)

// Parent reads pid's parent from /proc/<pid>/stat: the field after the
// state, which follows the command name. The name is in parentheses and may
// itself hold spaces and parentheses, so the fields are read after its LAST
// closing parenthesis.
func Parent(pid int) (int, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	return parseStat(string(data))
}

func parseStat(stat string) (int, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

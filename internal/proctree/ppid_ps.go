//go:build !linux && !windows

package proctree

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Parent asks ps for pid's parent (macOS and the BSDs have no /proc).
func Parent(pid int) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // ps, with a pid
	if err != nil {
		return 0, false
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return ppid, true
}

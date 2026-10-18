//go:build !windows

package proctree

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// KillTree stops the process group led by pid AND every process below
// pid, wherever it runs: a descendant that started its own session or
// group — claude in its PTY, or a command claude's Bash tool ran — is out
// of reach of a group kill.
//
// The descendants are snapshotted by parent pid BEFORE anything is
// signalled (once a parent dies its children are re-parented and the tree
// is lost). Each is sent SIGTERM, by pid as well as the group; after grace,
// or as soon as none is left, SIGKILL. The escalation is synchronous, so it
// lands even when the caller exits next: a goroutine that slept through
// the grace died with a caller that exited first.
func KillTree(ctx context.Context, pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	targets := append([]int{pid}, Descendants(ctx, pid)...)
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for _, p := range targets {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) && anyAlive(targets) {
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	for _, p := range targets {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

func anyAlive(pids []int) bool {
	for _, p := range pids {
		if Alive(p) {
			return true
		}
	}
	return false
}

// Descendants lists every process below pid, by parent pid, via `ps` —
// present on every Linux and macOS ape runs on. A failure returns nothing:
// a group kill still happens, so the worst case is a group kill alone.
//
// ctx is detached from cancellation: this runs when something is being
// stopped, which is usually because its context was just cancelled.
func Descendants(ctx context.Context, pid int) []int {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		p, err1 := strconv.Atoi(f[0])
		pp, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			children[pp] = append(children[pp], p)
		}
	}
	var below []int
	queue := []int{pid}
	seen := map[int]bool{pid: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if !seen[c] {
				seen[c] = true
				below = append(below, c)
				queue = append(queue, c)
			}
		}
	}
	return below
}

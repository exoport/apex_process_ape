//go:build linux || darwin

package repl

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// procGroupKillGrace is how long a SIGTERMed child group has to shut
// down cleanly before we escalate to SIGKILL. Mirrors the
// programmatic runner's `internal/pipeline/proc_unix.go` constant of
// the same name — 500ms is enough for a well-behaved REPL + bridge
// child to flush state, short enough that an unresponsive group
// doesn't hang the runner.
const procGroupKillGrace = 500 * time.Millisecond

// terminateGroup stops the claude child AND everything it started.
//
// go-pty's Unix backend sets SysProcAttr.Setsid on the child, so its pgid
// equals its pid, and a negative-pid kill addresses that group. That was
// once believed to be all of it. It is not: claude's Bash tool runs each
// command outside claude's process group, so a skill's `go test` or build
// kept running — and kept writing into the project — after ape stopped
// the run. Measured on claude 2.1.285: a `sleep 90` survived both SIGTERM
// and SIGKILL of the group.
//
// So the descendants are snapshotted by parent pid BEFORE anything is
// signalled (once claude dies they are re-parented and the tree is lost),
// then each is signalled by pid as well as the group. The SIGKILL
// escalation is synchronous: it used to run in a goroutine 500 ms later,
// and ape, which exits about 0.1 s after a SIGTERM, was gone before it
// fired.
func terminateGroup(ctx context.Context, pid int) {
	if pid <= 0 {
		return
	}
	targets := append([]int{pid}, descendants(ctx, pid)...)
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for _, p := range targets {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	deadline := time.Now().Add(procGroupKillGrace)
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
		if err := syscall.Kill(p, 0); err == nil || errors.Is(err, syscall.EPERM) {
			return true
		}
	}
	return false
}

// descendants lists every process below pid, by parent pid, via `ps` —
// present on every Linux and macOS ape runs on. A failure returns nothing:
// the group kill still happens, so the worst case is the old behaviour.
//
// ctx is detached from cancellation: this runs when a run is being stopped,
// which is usually because its context was just cancelled.
func descendants(ctx context.Context, pid int) []int {
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
	var out2 []int
	queue := []int{pid}
	seen := map[int]bool{pid: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if !seen[c] {
				seen[c] = true
				out2 = append(out2, c)
				queue = append(queue, c)
			}
		}
	}
	return out2
}

//go:build linux || darwin

package pipeline //nolint:testpackage // tests white-box test the runClaude / configureProcessGroup pair

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunClaude_KillsProcessGroupOnCancel verifies that PLAN-2 / F1's
// process-group teardown reaches grandchildren. The shim shell script
// forks a SIGTERM-trapping grandchild (sleep loop) then blocks itself,
// echoing both PIDs to stdout so the test can poll for liveness.
// runClaude is invoked with a cancellable context; after both PIDs
// surface in the observer's line buffer the test cancels the context
// and asserts both processes are reaped within the SIGKILL-escalation
// grace window.
func TestRunClaude_KillsProcessGroupOnCancel(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh required: " + err.Error())
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "shim.sh")
	// The grandchild traps SIGTERM, so only SIGKILL ends it — exactly
	// the scenario configureProcessGroup + finalProcessGroupCleanup
	// must close. Parent shim dies on SIGTERM.
	body := `#!/bin/sh
(trap '' TERM; while true; do sleep 1; done) &
echo "grandchild=$!"
echo "parent=$$"
sleep 30
`
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs := &captureObserver{}

	// Cancel the context once we've observed both PIDs.
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			obs.mu.Lock()
			haveParent := false
			haveGrandchild := false
			for _, l := range obs.lines {
				if strings.HasPrefix(l, "parent=") {
					haveParent = true
				}
				if strings.HasPrefix(l, "grandchild=") {
					haveGrandchild = true
				}
			}
			obs.mu.Unlock()
			if haveParent && haveGrandchild {
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel() // give up waiting; let the test report the missing PIDs
	}()

	_, _ = runClaude(ctx, []string{shim}, dir, obs, "stage", 0)
	<-cancelDone

	var parentPID, grandchildPID int
	obs.mu.Lock()
	for _, l := range obs.lines {
		if v, ok := strings.CutPrefix(l, "grandchild="); ok {
			grandchildPID, _ = strconv.Atoi(v)
		}
		if v, ok := strings.CutPrefix(l, "parent="); ok {
			parentPID, _ = strconv.Atoi(v)
		}
	}
	obs.mu.Unlock()
	if grandchildPID == 0 || parentPID == 0 {
		t.Fatalf("shim did not echo both PIDs; lines=%v", obs.lines)
	}

	// finalProcessGroupCleanup runs as soon as Wait returns, so by the
	// time runClaude returns the SIGKILL has been delivered. Allow up
	// to 1.5s for the kernel to deliver + reap.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !isProcessAlive(parentPID) && !isProcessAlive(grandchildPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if isProcessAlive(grandchildPID) {
		_ = syscall.Kill(grandchildPID, syscall.SIGKILL) // clean up the test artifact
		t.Fatalf("grandchild PID %d still alive after cancel — process-group teardown leaked", grandchildPID)
	}
	if isProcessAlive(parentPID) {
		_ = syscall.Kill(parentPID, syscall.SIGKILL)
		t.Fatalf("parent PID %d still alive after cancel", parentPID)
	}
}

// isProcessAlive returns true when pid still names a live process the
// current uid can signal. syscall.Kill with signal 0 performs the
// permission/existence check without delivering anything.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

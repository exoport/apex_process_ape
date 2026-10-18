//go:build !windows

package apexv1

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// helperRunCheckEnv makes TestHelperProcess_RunCheck run a check: the
// process a SIGKILL test kills.
const helperRunCheckEnv = "APEXV1_HELPER_RUN_CHECK"

func TestHelperProcess_RunCheck(t *testing.T) {
	cmd := os.Getenv(helperRunCheckEnv)
	if cmd == "" {
		t.Skip("a helper process for TestRunCheck_NoOrphanAfterSIGKILL")
	}
	runCheck(ctx(), os.Getenv("APEXV1_HELPER_DIR"), cmd, "", TierTimeout{Max: time.Hour}, nil)
}

// D58 2.2: a check never outlives the ape running it. ape SIGKILLed
// mid-check (nothing of ape's runs) still takes the check's whole process
// group with it — the shell, and what the shell started.
func TestRunCheck_NoOrphanAfterSIGKILL(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux: Pdeathsig and the watchdog; macOS has the watchdog alone")
	}
	needSh(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pgid")
	// The check's shell is its group's leader; it starts a child and waits.
	check := "echo $$ > " + pidFile + "; sleep 300 & sleep 300; wait"
	exe, err := os.Executable()
	require.NoError(t, err)
	helper := exec.Command(exe, "-test.run=^TestHelperProcess_RunCheck$")
	helper.Env = append(os.Environ(), helperRunCheckEnv+"="+check, "APEXV1_HELPER_DIR="+dir)
	require.NoError(t, helper.Start())
	var pgid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pgid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pgid > 0
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, syscall.Kill(-pgid, 0), "the check runs")

	require.NoError(t, helper.Process.Kill()) // SIGKILL: no ape code runs
	_ = helper.Wait()
	require.Eventually(t, func() bool {
		return syscall.Kill(-pgid, 0) == syscall.ESRCH
	}, 10*time.Second, 50*time.Millisecond, "the check's process group outlived ape")
}

// The idle kill lands at the window, not at the next progress sample (eval
// rc.3 probe L2: a 15 s idle window killed at 20.1 s, a 10 s sample late).
// Sampling is slow here, so only the deadline timer can kill in time.
func TestRunCheck_IdleKillAtTheDeadline(t *testing.T) {
	needSh(t)
	old := progressSample
	progressSample = 2 * time.Second
	t.Cleanup(func() { progressSample = old })
	idle := 2500 * time.Millisecond // samples at 2 s and 4 s: the old kill was at 4 s
	start := time.Now()
	r := runCheck(ctx(), t.TempDir(), "sleep 30", "", TierTimeout{Idle: idle, Max: time.Minute}, nil)
	took := time.Since(start)
	require.Equal(t, ExitTimeout, r.Exit, r.Tail)
	require.Equal(t, ReasonIdle, r.Reason)
	require.GreaterOrEqual(t, took, idle)
	require.Less(t, took, idle+time.Second, "the kill lands within a second of the idle window")
}

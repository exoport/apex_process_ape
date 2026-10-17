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

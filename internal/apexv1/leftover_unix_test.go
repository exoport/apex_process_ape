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

// F15 (eval bf :3 M2): a check's shell exited, but a process it started
// still held ape's output pipe — a `sleep` orphaned when the script killed
// the subshell around it. exec's copy waited WaitDelay (5 s) for it and
// returned "WaitDelay expired", recorded as exit 127, though the script
// printed green and exited 0. The check's exit is its shell's own, and
// what it left running is ended and named.

// leftoverCmd starts `sleep 300` that writes its pid to pidFile first,
// wrapped as the check needs; it waits until the pid is written.
func leftoverCmd(pidFile, start string) string {
	return start + "\nwhile [ ! -s " + pidFile + " ]; do sleep 0.02; done"
}

// sleeperPid is the leftover's pid, from pidFile.
func sleeperPid(t *testing.T, pidFile string) int {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	return pid
}

// requireNamed requires pid among the leftovers, named "<pid> <cmd…>".
// Among, not alone: what else the check's group holds at the instant ape
// lists it — a subshell the script killed that has not exited yet, a
// loop's short-lived child — is named too, and depends on timing (a CI
// runner listed two where a workstation always listed one).
func requireNamed(t *testing.T, leftovers []string, pid int, cmd string) {
	t.Helper()
	want := strconv.Itoa(pid) + " " + cmd
	for _, l := range leftovers {
		if strings.HasPrefix(l, want) {
			return
		}
	}
	require.Failf(t, "not named", "%q not among the leftovers %q", want, leftovers)
}

// requireGone waits (briefly) for pid to be gone: SIGTERMed, reaped.
func requireGone(t *testing.T, pid int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}, 3*time.Second, 20*time.Millisecond, "pid %d outlived its check", pid)
}

func TestRunCheck_LeftoverHoldingThePipe_KeepsTheExit(t *testing.T) {
	needSh(t)
	for _, exit := range []int{0, 3} {
		t.Run("exit "+strconv.Itoa(exit), func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			// bf's shape: a background subshell, killed; its child is
			// orphaned and keeps stdout and stderr.
			cmd := leftoverCmd(pidFile, "( sh -c 'echo $$ > "+pidFile+"; exec sleep 300'; echo late ) & w=$!") +
				"; kill $w; echo 'gate: done'; exit " + strconv.Itoa(exit)
			start := time.Now()
			r := runCheck(ctx(), dir, cmd, filepath.Join(dir, "check.log"), "", TierTimeout{Max: time.Minute}, nil)
			took := time.Since(start)
			pid := sleeperPid(t, pidFile)
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

			require.Equal(t, exit, r.Exit, "the script's own exit, not 127: %q", r.Tail)
			require.Less(t, took, 4*time.Second, "not held for the pipe")
			requireNamed(t, r.Leftovers, pid, "sleep 300")
			requireGone(t, pid)
			out := strings.Join(r.Tail, "\n")
			require.Contains(t, out, "gate: done")
			require.NotContains(t, out, "WaitDelay")
			require.Contains(t, out, "still running; ape ended them: ")
			require.Contains(t, out, strconv.Itoa(pid)+" sleep 300")
			logged, err := os.ReadFile(filepath.Join(dir, "check.log"))
			require.NoError(t, err)
			require.Contains(t, string(logged), "ape ended them")
		})
	}
}

// A leftover holding no pipe — a server started with its output sent
// elsewhere — is ended too: it would hold its port for the next check.
func TestRunCheck_QuietLeftoverIsEnded(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	cmd := leftoverCmd(pidFile, "sh -c 'echo $$ > "+pidFile+"; exec sleep 300' >/dev/null 2>&1 </dev/null &") + "; echo ok"
	r := runCheck(ctx(), dir, cmd, "", "", TierTimeout{Max: time.Minute}, nil)
	pid := sleeperPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	require.Equal(t, 0, r.Exit, r.Tail)
	requireNamed(t, r.Leftovers, pid, "sleep 300")
	requireGone(t, pid)
}

// A check that leaves nothing names nothing: ape's own watchdog, in the
// same process group, is never a leftover.
func TestRunCheck_NothingLeftNothingNamed(t *testing.T) {
	needSh(t)
	r := runCheck(ctx(), t.TempDir(), "echo ok; sleep 0.1 & wait", "", "", TierTimeout{Max: time.Minute}, nil)
	require.Equal(t, 0, r.Exit, r.Tail)
	require.Empty(t, r.Leftovers)
	require.NotContains(t, strings.Join(r.Tail, "\n"), "[ape]")
}

// A leftover that ignores SIGTERM is SIGKILLed at leftoverGrace.
func TestRunCheck_LeftoverIgnoringTERMIsKilled(t *testing.T) {
	needSh(t)
	old := leftoverGrace
	leftoverGrace = 300 * time.Millisecond
	t.Cleanup(func() { leftoverGrace = old })
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	cmd := leftoverCmd(pidFile, `sh -c 'trap "" TERM; echo $$ > `+pidFile+`; while :; do sleep 0.05; done' >/dev/null 2>&1 &`) + "; echo ok"
	start := time.Now()
	r := runCheck(ctx(), dir, cmd, "", "", TierTimeout{Max: time.Minute}, nil)
	took := time.Since(start)
	pid := sleeperPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	require.Equal(t, 0, r.Exit, r.Tail)
	requireNamed(t, r.Leftovers, pid, "sh -c trap")
	requireGone(t, pid)
	require.Less(t, took, 3*time.Second)
}

// A process that left the check's group (setsid) and holds the pipe is not
// ended — ape cannot tell it is the check's — but it holds the result only
// for outputDrain, and the check's exit stands.
func TestRunCheck_EscapedPipeHolderBoundsTheDrain(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("setsid(1) is util-linux")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	needSh(t)
	old := outputDrain
	outputDrain = 500 * time.Millisecond
	t.Cleanup(func() { outputDrain = old })
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	cmd := leftoverCmd(pidFile, "setsid sh -c 'echo $$ > "+pidFile+"; exec sleep 300' &") + "; echo ok"
	start := time.Now()
	r := runCheck(ctx(), dir, cmd, "", "", TierTimeout{Max: time.Minute}, nil)
	took := time.Since(start)
	pid := sleeperPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	require.Equal(t, 0, r.Exit, r.Tail)
	require.Less(t, took, 3*time.Second)
	if r.Reaping == ReapingScope {
		// v1.4 (B1): the scope holds what left the group — named and ended.
		requireNamed(t, r.Leftovers, pid, "sleep 300")
		requireGone(t, pid)
		return
	}
	// Group reaping (no user manager: CI runners, containers): it left the
	// group, but it inherited the check's TMPDIR, so B1's sweep of the
	// TMPDIR's holders ends and names it (v1.4, T1) — and the drain does
	// not wait for it.
	requireNamed(t, r.Leftovers, pid, "sleep 300")
	require.Contains(t, strings.Join(r.Leftovers, "\n"), "held the check's TMPDIR")
	requireGone(t, pid)
}

// Where ps lists the group (macOS, the BSDs): pid, pgid and state parse,
// a zombie and a continued command line are skipped, and the watchdog is
// known by its joined command line.
func TestParseGroupMembers(t *testing.T) {
	out := "  101   100 Ss   sh -c (read ok <&3; [ \"$ok\" = ok ] || kill -9 0) </dev/null >/dev/null 2>&1 &\n" +
		"exec 3<&-\n" +
		"  102   100 S    sleep 13\n" +
		"  103   100 Z    (sh)\n" +
		"  104   999 S    sleep 13\n"
	got := parseGroupMembers(out, 100)
	require.Len(t, got, 2)
	require.True(t, got[0].isWatchdog())
	require.Equal(t, groupMember{pid: 102, argv: []string{"sleep 13"}}, got[1])
	require.False(t, got[1].isWatchdog())
	require.True(t, groupMember{argv: []string{"sh", "-c", checkWatchdog, "ape-check", "true"}}.isWatchdog())
}

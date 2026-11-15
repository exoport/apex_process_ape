package natstest

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The helper process for TestClaimAcross_AnotherProcessesPortIsNotPicked:
// it claims a port in the test's claim directory, says so, and holds the
// claim until its stdin closes. Skipped unless run as that helper.
func TestHelperClaimHolder(t *testing.T) {
	dir, port := os.Getenv("NATSTEST_HELPER_CLAIM_DIR"), os.Getenv("NATSTEST_HELPER_PORT")
	if dir == "" {
		t.Skip("the helper process of a cross-process claim test")
	}
	claimDir = dir
	p, err := strconv.Atoi(port)
	require.NoError(t, err)
	require.True(t, claimAcross(p))
	_, _ = os.Stdout.WriteString("claimed\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// CI-5GREEN-4 on b4a54f8 (H1): claimPort's set was per process, so a
// parallel package's cluster could pick a stopped node's port. Now a port
// another live process claimed is not picked, and is picked again once
// that process is gone (a stale claim).
func TestClaimAcross_AnotherProcessesPortIsNotPicked(t *testing.T) {
	wasDir, wasPick := claimDir, pickPort
	t.Cleanup(func() { claimDir, pickPort = wasDir, wasPick })
	claimDir = t.TempDir()
	const p = 21017 // not handed out by this process: the dir is new, and claimed is checked below
	claimedMu.Lock()
	handed := claimed[p]
	claimedMu.Unlock()
	require.False(t, handed, "this process has not handed it out")

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperClaimHolder$")
	cmd.Env = append(os.Environ(), "NATSTEST_HELPER_CLAIM_DIR="+claimDir, "NATSTEST_HELPER_PORT="+strconv.Itoa(p))
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "claimed", strings.TrimSpace(line), "the other process holds a claim on %d", p)

	// The picker draws p first: another process holds it, so it takes a
	// draw of its own after it.
	first := true
	pickPort = func(lo, hi int) int {
		if first {
			first = false
			return p
		}
		return wasPick(lo, hi)
	}
	got := freePorts(t, 1)
	require.NotEqual(t, p, got[0], "a port another process claimed is not picked")
	require.False(t, claimAcross(p), "nor claimed directly")

	// That process gone, its claim is stale and the port is free again.
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait())
	require.True(t, claimAcross(p), "a dead process's claim is claimed again")
}

// deadPid is the pid of a process that has exited.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

// NT-1: two processes that read the same stale claim both removed it,
// the second removing the live claim the first had made in its place, and
// both held the port. Here the seam plays the first process: once this one
// has read the claim as stale, the other reclaims it and claims the port
// (a live pid, this test's parent). This one must then refuse the port and
// leave the other's claim where it is.
func TestClaimAcross_AStaleClaimIsReclaimedOnce(t *testing.T) {
	wasDir := claimDir
	t.Cleanup(func() { claimDir, testAfterStaleRead = wasDir, nil })
	claimDir = t.TempDir()
	const p = 21018
	path := filepath.Join(claimDir, strconv.Itoa(p))
	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(deadPid(t))), 0o600))
	other := strconv.Itoa(os.Getppid())
	testAfterStaleRead = func(path string) {
		testAfterStaleRead = nil
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.WriteFile(path, []byte(other), 0o600))
	}

	require.False(t, claimAcross(p), "the port is the other process's now")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, other, string(b), "the other process's live claim is not removed")
	_, err = os.Stat(path + ".reclaim")
	require.ErrorIs(t, err, fs.ErrNotExist, "the reclaim is let go")

	// A reclaim another process holds refuses the port, the stale claim
	// left to it.
	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(deadPid(t))), 0o600))
	require.NoError(t, os.WriteFile(path+".reclaim", nil, 0o600))
	require.False(t, claimAcross(p))
	require.FileExists(t, path)
	require.NoError(t, os.Remove(path+".reclaim"))
	require.True(t, claimAcross(p), "a stale claim with no reclaim held is claimed again")
}

// NT-2: one 0700 claim directory in a shared /tmp, made by whoever ran
// first, refused every port to every other user. Each user has their own.
func TestClaimDir_IsPerUser(t *testing.T) {
	require.NotEqual(t, claimDirName(1000), claimDirName(1001))
	if uid := os.Getuid(); uid >= 0 {
		require.Equal(t, claimDirName(uid), filepath.Base(claimDir))
	}
}

// CI-5GREEN-4 on b4a54f8 (H2): Restart failed at once on a port it could
// not bind. Now a port of its own held a moment after the node stopped is
// waited out before the node starts, and the node starts once.
func TestRestart_RetriesAPortHeldAMoment(t *testing.T) {
	c := RunCluster(t, 3)
	route := c.opts[0].Cluster.Port
	c.Stop(0)
	var lc net.ListenConfig
	held, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:"+strconv.Itoa(route))
	require.NoError(t, err, "n1's route port, held as a closing socket would hold it")
	const hold = 2 * time.Second
	go func() { time.Sleep(hold); _ = held.Close() }()
	start, starts := time.Now(), nodeStarts.Load()
	c.Restart(0)
	require.GreaterOrEqual(t, time.Since(start), hold-100*time.Millisecond, "it waited for the port, not around it")
	// One start: a start that loses its port has opened the store, which
	// Windows then keeps locked for the next (CI-5GREEN-3 on 5c98b25).
	require.Equal(t, int64(1), nodeStarts.Load()-starts, "the node started once, after its ports were free")
	require.True(t, c.Servers[0].Running())
	require.Equal(t, route, c.opts[0].Cluster.Port, "on its own route port")
	c.WaitMetaLeader(30 * time.Second)
}

// CI-5GREEN-2 on 9b13533: a restart whose ports were free still failed,
// on JetStream's rename of a store the stopped node itself still held
// (Windows). Now that refusal is waited out on any start, and the node
// comes back. The seam stands in for the Windows lock.
func TestRestart_WaitsOutAStoreStillInUse(t *testing.T) {
	c := RunCluster(t, 3)
	c.Stop(0)
	refused := 0
	f := func(name string) error {
		if name != "n1" || refused == 2 {
			return nil
		}
		refused++
		return fmt.Errorf("natstest: cluster server %s: Can't start JetStream: rename …\\msgs …\\__msgs__: Access is denied", name)
	}
	testStartErr.Store(&f)
	t.Cleanup(func() { testStartErr.Store(nil) })
	starts := nodeStarts.Load()
	c.Restart(0)
	require.Equal(t, 2, refused, "both refusals were waited out")
	require.Equal(t, int64(3), nodeStarts.Load()-starts, "two refused starts, then the one that came up")
	require.True(t, c.Servers[0].Running())
	c.WaitMetaLeader(30 * time.Second)
}

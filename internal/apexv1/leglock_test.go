package apexv1

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// useLegLock points the compose-leg lock at a test's own file.
func useLegLock(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legs.lock")
	oldPath, oldPoll := legLockPath, legLockPoll
	legLockPath, legLockPoll = path, 10*time.Millisecond
	t.Cleanup(func() { legLockPath, legLockPoll = oldPath, oldPoll })
	return path
}

// D705: what holds the lock — a full check, a container runtime named, a
// *_LIVE variable set on — and what does not: atomix's own check lines.
func TestComposeLeg_Classifies(t *testing.T) {
	legs := []string{
		`sh -c "ATOMIX_PERIODIC_DISPATCH_LIVE=1 go test -count=1 -v -timeout 20m -run '^TestPeriodicDispatchLive_X$' ./test/integration | grep -q -- '--- PASS'"`,
		`sh -c "ATOMIX_QUEUE_LEASE_LIVE=1 ATOMIX_QUEUE_LEASE_LIVE_EVIDENCE_PART=20-4-pg15 go test ./test/integration"`,
		"docker compose -f deploy/compose/pg15.yml up -d && make itest",
		"make test && docker-compose down",
		"podman run --rm img true",
		"FOO_LIVE=true go test ./...",
		"go test ./... -tags testcontainers",
	}
	for _, cmd := range legs {
		require.True(t, composeLeg(&PlannedCheck{Cmd: cmd}), cmd)
	}
	plain := []string{
		`sh -c 'env -u ATOMIX_NETWORK_PARTITION_LIVE go test -count=1 -v -run "^TestConformance_X$" ./test/conformance/injection | grep -q -- "--- SKIP"'`,
		`sh -c 'go test -count=1 -v -run "^TestPlanNetworkDisconnect_IssuesDockerNetworkDisconnectNotIptables$" ./test/harness/cluster'`,
		`sh -c 'grep -q "claimLogProvisionAndDrive" test/conformance/injection/claimlog_live_test.go'`,
		"ATOMIX_RELAY_LIVE=0 go test ./...",
		"go test ./internal/...",
		"grep -q dockerfile README.md",
	}
	for _, cmd := range plain {
		require.False(t, composeLeg(&PlannedCheck{Cmd: cmd}), cmd)
	}
	require.True(t, composeLeg(&PlannedCheck{Cmd: "go test ./...", Full: true}))
}

// A leg waits while another run holds the lock, names who holds it, and
// records the wait; a check that is no leg does not wait.
func TestWithLegLock_WaitsForTheHolder(t *testing.T) {
	path := useLegLock(t)
	unlock, ok, err := tryLockFile(path)
	require.NoError(t, err)
	require.True(t, ok)
	writeLegHolder(path, "/elsewhere/b", &PlannedCheck{Ref: "cap/R/s"})

	plain := withLegLock(context.Background(), "/p", &PlannedCheck{Ref: "T1", Cmd: "go test ./..."}, nil, func() CheckResult { return CheckResult{} })
	require.False(t, plain.LegLock)

	var progress bytes.Buffer
	var mu sync.Mutex
	done := make(chan CheckResult)
	go func() {
		done <- withLegLock(context.Background(), "/p", &PlannedCheck{Ref: "cap/R/live", Cmd: "X_LIVE=1 go test"}, &lockedBuf{b: &progress, mu: &mu}, func() CheckResult {
			return CheckResult{Exit: 0}
		})
	}()
	select {
	case <-done:
		t.Fatal("the leg ran while the lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	r := <-done
	require.True(t, r.LegLock)
	require.Greater(t, r.LegLockWaitSecs, 0.0)
	require.Contains(t, r.LegLockHolder, "cap/R/s in /elsewhere/b")
	mu.Lock()
	require.Contains(t, progress.String(), "waits for the host's compose-leg lock, held by pid")
	mu.Unlock()

	// Free now: it takes the lock without a wait.
	r = withLegLock(context.Background(), "/p", &PlannedCheck{Ref: "cap/R/live", Cmd: "X_LIVE=1 go test"}, nil, func() CheckResult { return CheckResult{} })
	require.True(t, r.LegLock)
	require.Zero(t, r.LegLockWaitSecs)
}

// Interrupted while waiting, the leg is not run.
func TestWithLegLock_InterruptedWhileWaiting(t *testing.T) {
	path := useLegLock(t)
	unlock, ok, err := tryLockFile(path)
	require.NoError(t, err)
	require.True(t, ok)
	defer unlock()
	c, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ran := false
	r := withLegLock(c, "/p", &PlannedCheck{Ref: "s", Cmd: "docker ps"}, nil, func() CheckResult { ran = true; return CheckResult{} })
	require.False(t, ran)
	require.Equal(t, exitInterrupted, r.Exit)
}

// T2 in small: two runs on one host whose legs share a fixed resource (a
// stand-in for a host port) both go green, the legs one at a time, and
// the gate's sum shows the wait.
func TestRunChecks_ConcurrentLegsTakeTurns(t *testing.T) {
	useLegLock(t)
	port := filepath.Join(t.TempDir(), "port-5615")
	leg := "X_LIVE=1 sh -c 'mkdir " + port + " || exit 1; sleep 0.5; rmdir " + port + "'"
	var wg sync.WaitGroup
	results := make([][]CheckResult, 2)
	for i := range results {
		p := fixture(t, baseFiles())
		wg.Go(func() {
			results[i] = p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{{Ref: "cap/R/live", Kind: KindScenario, Cmd: leg}}, RunOptions{})
		})
	}
	wg.Wait()
	waited := 0
	for _, rs := range results {
		require.Equal(t, 0, rs[0].Exit, rs[0].Tail)
		require.True(t, rs[0].LegLock)
		if rs[0].LegLockWaitSecs > 0 {
			waited++
			u := legLockUse(rs)
			require.Equal(t, 1, u.Waited)
			require.Contains(t, u.String(), "1 waited")
		}
	}
	require.Equal(t, 1, waited, "exactly one of the two legs waited for the other")
}

type lockedBuf struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

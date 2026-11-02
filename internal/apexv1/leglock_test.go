package apexv1

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
		require.True(t, composeLeg(&PlannedCheck{Cmd: cmd}, nil), cmd)
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
		require.False(t, composeLeg(&PlannedCheck{Cmd: cmd}, nil), cmd)
	}
	require.True(t, composeLeg(&PlannedCheck{Cmd: "go test ./...", Full: true}, nil))
}

// A leg waits while another run holds the lock, names who holds it, and
// records the wait; a check that is no leg does not wait.
func TestWithLegLock_WaitsForTheHolder(t *testing.T) {
	path := useLegLock(t)
	unlock, ok, err := tryLockFile(path)
	require.NoError(t, err)
	require.True(t, ok)
	writeLegHolder(path, "/elsewhere/b", &PlannedCheck{Ref: "cap/R/s"})

	plain := withLegLock(context.Background(), "/p", &PlannedCheck{Ref: "T1", Cmd: "go test ./..."}, nil, nil, func() CheckResult { return CheckResult{} })
	require.False(t, plain.LegLock)

	var progress bytes.Buffer
	var mu sync.Mutex
	done := make(chan CheckResult)
	go func() {
		done <- withLegLock(context.Background(), "/p", &PlannedCheck{Ref: "cap/R/live", Cmd: "X_LIVE=1 go test"}, nil, &lockedBuf{b: &progress, mu: &mu}, func() CheckResult {
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
	r = withLegLock(context.Background(), "/p", &PlannedCheck{Ref: "cap/R/live", Cmd: "X_LIVE=1 go test"}, nil, nil, func() CheckResult { return CheckResult{} })
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
	r := withLegLock(c, "/p", &PlannedCheck{Ref: "s", Cmd: "docker ps"}, nil, nil, func() CheckResult { ran = true; return CheckResult{} })
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
			require.Len(t, u.Waits, 1)
			require.Equal(t, "cap/R/live", u.Waits[0].Check)
			require.Contains(t, u.Waits[0].Holder, "cap/R/live in ")
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

// FINDING-V15-1A-12: the line rule missed the regression command's
// `make smoke` (pg15 on a fixed port), so a verify's regression and
// another gate's own port check collided in the 1a run. A project names
// the commands that start containers in checks.containers; a check whose
// command holds one — the regression command here — takes the lock for
// its own run. Two runs at once whose regression takes one fixed resource:
// declared, both green and one waited (the hold is reported); the
// control, undeclared, collides.
func TestRunChecks_DeclaredContainersTakeTurns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared bool
	}{{"declared", true}, {"control-undeclared", false}} {
		t.Run(tc.name, func(t *testing.T) {
			useLegLock(t)
			port := filepath.Join(t.TempDir(), "port-5615")
			// No container runtime named, no *_LIVE variable: the line
			// rule cannot tell, as with atomix's `make smoke`.
			regression := "./smoke.sh && echo regression green"
			smoke := "#!/bin/sh\nmkdir " + port + " || { echo 'port 5615 is already allocated'; exit 1; }\nsleep 0.5\nrmdir " + port + "\n"
			cfg := ""
			if tc.declared {
				cfg = "checks:\n  containers: [\"./smoke.sh\"]\n"
			}
			var wg sync.WaitGroup
			results := make([][]CheckResult, 2)
			for i := range results {
				files := baseFiles()
				files["_apex/config.yaml"] = cfg
				files["smoke.sh"] = smoke
				p := fixture(t, files)
				require.NoError(t, os.Chmod(filepath.Join(p.Root, "smoke.sh"), 0o755))
				wg.Go(func() {
					results[i] = p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{{Ref: regressionRef, Kind: KindRegression, Cmd: regression}}, RunOptions{})
				})
			}
			wg.Wait()
			if !tc.declared {
				reds := 0
				for _, rs := range results {
					require.False(t, rs[0].LegLock, "undeclared: no lock")
					if rs[0].Exit != 0 {
						reds++
					}
				}
				require.Equal(t, 1, reds, "the control collides: one regression red on the taken port")
				return
			}
			waited := 0
			for _, rs := range results {
				require.Equal(t, 0, rs[0].Exit, rs[0].Tail)
				require.True(t, rs[0].LegLock, "declared: the regression command is a leg")
				u := legLockUse(rs)
				require.Len(t, u.Holds, 1)
				require.Greater(t, u.HeldSecs, 0.4, "the hold lasts the regression's own run")
				require.Contains(t, u.String(), "for "+strconv.FormatFloat(u.HeldSecs, 'f', 1, 64)+"s in all")
				if rs[0].LegLockWaitSecs > 0 {
					waited++
				}
			}
			require.Equal(t, 1, waited, "queued: one waited for the other")
		})
	}
}

// checks.containers parses, and refuses an empty command.
func TestChecksConfig_Containers(t *testing.T) {
	var c Config
	require.NoError(t, yaml.Unmarshal([]byte("checks:\n  containers: [\"make smoke\", \" ./scripts/relay-live.sh \"]\n"), &c))
	require.Equal(t, []string{"make smoke", "./scripts/relay-live.sh"}, c.Checks.Containers)
	require.ErrorContains(t, yaml.Unmarshal([]byte("checks:\n  containers: [\"\"]\n"), &c), "empty command")
	require.True(t, composeLeg(&PlannedCheck{Cmd: "make golint && make smoke && go build ./..."}, c.Checks.Containers))
	require.False(t, composeLeg(&PlannedCheck{Cmd: "make golint"}, c.Checks.Containers))
}

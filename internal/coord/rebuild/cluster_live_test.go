package rebuild

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
)

// Drill 3, Tier 2: the local three-process cluster is wiped (every node's
// data deleted) and rebuilt from the apes' records. Opt-in: `make
// check-coord-cluster` runs it with NATSLOCAL_URLS / NATSLOCAL_SCRIPT set.
func TestLive_ClusterWipeAndRebuild(t *testing.T) {
	urls, script := os.Getenv("NATSLOCAL_URLS"), os.Getenv("NATSLOCAL_SCRIPT")
	if urls == "" || script == "" {
		t.Skip("NATSLOCAL_URLS / NATSLOCAL_SCRIPT unset: run `make check-coord-cluster`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	sup := EpochFile(filepath.Join(dir, "sup", "epoch"))
	bob := lease.OpenBook(filepath.Join(dir, "bob", "tokens.json"))
	key := layout.MilestoneKey("taskforge", "M2")

	connect := func() (jetstream.JetStream, *lease.Store) {
		nc, err := nats.Connect(urls, nats.MaxReconnects(-1))
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		js, err := jetstream.New(nc)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			c, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, err := js.AccountInfo(c)
			return err == nil
		}, time.Minute, 200*time.Millisecond, "no meta leader")
		// R3 placement needs all three peers, which join a little after the
		// meta leader: the rebuild's declare retries until they have.
		require.Eventually(t, func() bool {
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return Declare(c, js, 3) == nil
		}, time.Minute, 500*time.Millisecond, "the layout never declared at R3")
		ls, err := lease.Open(ctx, js)
		require.NoError(t, err)
		return js, ls
	}
	sh := func(args ...string) {
		out, err := exec.Command(script, args...).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}

	js1, ls1 := connect()
	_, err := NewEpoch(ctx, js1, ls1, sup, time.Minute)
	require.NoError(t, err)
	b, err := ls1.Claim(ctx, key, "bob", time.Minute)
	require.NoError(t, err)
	b, err = ls1.Renew(ctx, b)
	require.NoError(t, err)
	require.NoError(t, bob.Record(key, b, lease.Held))
	require.NoError(t, Replay(ctx, js1, records("ann", "c-ann", 0)))
	require.NoError(t, Replay(ctx, js1, records("bob", "c-bob", 0)))
	n1, d1 := snapshot(t, js1)

	sh("down")
	sh("up")
	js2, ls2 := connect()
	e2, err := NewEpoch(ctx, js2, ls2, sup, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, uint64(2), e2, "the epoch comes from the supervisor's own record")
	require.NoError(t, Replay(ctx, js2, records("ann", "c-ann", 0)))
	require.NoError(t, Replay(ctx, js2, records("bob", "c-bob", 0)))
	n2, d2 := snapshot(t, js2)
	require.Equal(t, n1, n2)
	require.Equal(t, d1, d2)
	l, err := Reclaim(ctx, js2, ls2, bob, key, "bob", time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(2), l.Token.Epoch)
}

package natstest

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// The Tier-1 cluster carries an R3 KV bucket and keeps it through the loss
// of one node, and the node rejoins with its data.
func TestRunCluster_R3SurvivesANode(t *testing.T) {
	c := RunCluster(t, 3)
	nc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1))
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ape-test", Replicas: 3})
	require.NoError(t, err)
	_, err = kv.Create(ctx, "k", []byte("v1"))
	require.NoError(t, err)

	// V8-CI-2-D1: which node leads the bucket's stream when one stops, and
	// how long the read with it down takes, are in the log, and a failed
	// wait names its last error (it said <nil> by construction before).
	if st, err := js.Stream(ctx, "KV_ape-test"); err == nil && st.CachedInfo().Cluster != nil {
		t.Logf("V8-CI-2: KV_ape-test's leader before the stop: %s (stopping n3)", st.CachedInfo().Cluster.Leader)
	}
	c.Stop(2)
	e, r := readUntil(ctx, 60*time.Second, func(c context.Context) (jetstream.KeyValueEntry, error) { return kv.Get(c, "k") })
	require.NoError(t, r.Err, "an R3 bucket reads with one node down: the last error, %v after the stop, of %d reads in %v", r.At, r.Tries, r.Elapsed)
	t.Logf("V8-CI-2: read with one node down %v after the stop (%d reads)", r.Elapsed.Round(10*time.Millisecond), r.Tries)
	require.Equal(t, "v1", string(e.Value()))
	_, err = kv.Update(ctx, "k", []byte("v2"), e.Revision())
	require.NoError(t, err, "and writes, with two of three")

	c.Restart(2)
	c.WaitMetaLeader(30 * time.Second)
	e, err = kv.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "v2", string(e.Value()))
}

// V15-RED-4: a node stopped for an outage comes back on its ports, so
// they are picked below the ephemeral range, where no outgoing connection
// is handed one while the node is down (a route port, 36457, was on a
// ci-local).
func TestRunCluster_PortsBelowTheEphemeralRange(t *testing.T) {
	c := RunCluster(t, 3)
	hi := ephemeralLow()
	for i, s := range c.Servers {
		a, ok := s.Addr().(*net.TCPAddr)
		require.True(t, ok)
		for what, p := range map[string]int{"client": a.Port, "route": c.opts[i].Cluster.Port} {
			require.GreaterOrEqual(t, p, portFloor, "n%d's %s port", i+1, what)
			require.Less(t, p, hi, "n%d's %s port below the ephemeral range", i+1, what)
		}
	}
	was := c.Servers[0].ClientURL()
	c.Stop(0)
	c.Restart(0)
	require.Equal(t, was, c.Servers[0].ClientURL(), "back on the same client port")
	c.WaitMetaLeader(30 * time.Second)
	require.Len(t, map[int]bool{c.opts[0].Port: true, c.opts[1].Port: true, c.opts[2].Port: true, c.opts[0].Cluster.Port: true, c.opts[1].Cluster.Port: true, c.opts[2].Cluster.Port: true}, 6, "six distinct ports")
}

// V8-FLAKE-4: freePorts releases its ports before the nodes bind them, so
// a parallel test can take one in between; the node then never became
// ready and RunCluster failed "not ready" after the whole wait. Now the
// node's own "Error listening on" line ends the wait at once and the
// cluster is started again on fresh ports.
func TestRunCluster_APortTakenMeanwhile(t *testing.T) {
	was, wasWait := reservePorts, ReadyWait
	t.Cleanup(func() { reservePorts, ReadyWait = was, wasWait })
	ReadyWait = 20 * time.Second
	var lc net.ListenConfig
	held, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = held.Close() }()
	addr, ok := held.Addr().(*net.TCPAddr)
	require.True(t, ok)
	calls := 0
	var second time.Time
	reservePorts = func(t *testing.T, n int) []int {
		t.Helper()
		calls++
		ports := freePorts(t, n)
		switch calls {
		case 1:
			ports[n-1] = addr.Port // n3's route port, taken by "another test"
		case 2:
			second = time.Now()
		}
		return ports
	}
	start := time.Now()
	c := RunCluster(t, 3)
	require.Equal(t, 2, calls, "the cluster was started again on fresh ports")
	// The first try alone: one that waited for its node would take
	// ReadyWait. Not the whole RunCluster, whose second, fresh start took
	// 21 s on a Windows runner (V8-FLAKE-4, CI-5GREEN-EC7-1: 20.998 s).
	require.Less(t, second.Sub(start), ReadyWait, "the taken port ended the first try at once (not the second try's start, 21 s once on Windows)")
	nc, err := nats.Connect(c.URLs())
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ape-taken", Replicas: 3})
	require.NoError(t, err, "and the second cluster carries an R3 bucket")
}

// readResult is a readUntil's outcome: Err is its last read's error (nil
// once one succeeded), At when that error came, after the start.
type readResult struct {
	Err         error
	At, Elapsed time.Duration
	Tries       int
}

// readUntil reads until a read succeeds or d has passed, each read under
// its own 2 s deadline, so one stuck read cannot spend the whole wait.
func readUntil[T any](ctx context.Context, d time.Duration, read func(context.Context) (T, error)) (T, readResult) {
	start := time.Now()
	var r readResult
	for {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		v, err := read(c)
		cancel()
		r.Tries++
		r.Elapsed = time.Since(start)
		if err == nil {
			r.Err = nil
			return v, r
		}
		r.Err, r.At = err, r.Elapsed
		if r.Elapsed >= d || ctx.Err() != nil {
			return v, r
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// V8-CI-2-D1: a wait that fails says what its last read said, not the
// error from before the wait.
func TestReadUntil_ReportsTheLastError(t *testing.T) {
	n := 0
	_, r := readUntil(context.Background(), 500*time.Millisecond, func(context.Context) (int, error) {
		n++
		return 0, fmt.Errorf("no responders (read %d)", n)
	})
	require.Error(t, r.Err)
	require.Equal(t, fmt.Sprintf("no responders (read %d)", n), r.Err.Error(), "the last read's error")
	require.Equal(t, n, r.Tries)
	require.Greater(t, n, 1)
	v, r := readUntil(context.Background(), time.Second, func(context.Context) (int, error) { return 7, nil })
	require.NoError(t, r.Err)
	require.Equal(t, 7, v)
	require.Equal(t, 1, r.Tries)
}

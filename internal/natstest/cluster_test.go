package natstest

import (
	"context"
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ape-test", Replicas: 3})
	require.NoError(t, err)
	_, err = kv.Create(ctx, "k", []byte("v1"))
	require.NoError(t, err)

	c.Stop(2)
	var e jetstream.KeyValueEntry
	require.Eventually(t, func() bool {
		e, err = kv.Get(ctx, "k")
		return err == nil
	}, 20*time.Second, 200*time.Millisecond, "an R3 bucket reads with one node down: %v", err)
	require.Equal(t, "v1", string(e.Value()))
	_, err = kv.Update(ctx, "k", []byte("v2"), e.Revision())
	require.NoError(t, err, "and writes, with two of three")

	c.Restart(2)
	c.WaitMetaLeader(30 * time.Second)
	e, err = kv.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "v2", string(e.Value()))
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
	reservePorts = func(t *testing.T, n int) []int {
		t.Helper()
		calls++
		ports := freePorts(t, n)
		if calls == 1 {
			ports[n-1] = addr.Port // taken by "another test"
		}
		return ports
	}
	start := time.Now()
	c := RunCluster(t, 3)
	require.Equal(t, 2, calls, "the cluster was started again on fresh ports")
	require.Less(t, time.Since(start), ReadyWait, "the taken port ended the first try at once")
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

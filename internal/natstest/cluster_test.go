package natstest

import (
	"context"
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

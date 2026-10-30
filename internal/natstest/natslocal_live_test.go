package natstest

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// Tier 2 (v1.5 stage 0): the local three-process cluster from
// deploy/natslocal carries an R3 bucket through a node's stop and start.
// Opt-in: `make check-natslocal` brings the cluster up, sets NATSLOCAL_URLS
// and NATSLOCAL_SCRIPT, and takes it down after.
func TestLive_NatsLocalCluster(t *testing.T) {
	urls, script := os.Getenv("NATSLOCAL_URLS"), os.Getenv("NATSLOCAL_SCRIPT")
	if urls == "" || script == "" {
		t.Skip("NATSLOCAL_URLS / NATSLOCAL_SCRIPT unset: run `make check-natslocal`")
	}
	nc, err := nats.Connect(urls, nats.MaxReconnects(-1))
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	require.Eventually(t, func() bool {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, err := js.AccountInfo(c)
		return err == nil
	}, 30*time.Second, 200*time.Millisecond, "no meta leader")
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ape-natslocal-smoke", Replicas: 3})
	require.NoError(t, err)
	defer func() { _ = js.DeleteKeyValue(context.Background(), "ape-natslocal-smoke") }()
	_, err = kv.Put(ctx, "k", []byte("v1"))
	require.NoError(t, err)

	node := func(verb string) {
		out, err := exec.Command(script, verb, "3").CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	node("stop")
	require.Eventually(t, func() bool { _, err := kv.Put(ctx, "k", []byte("v2")); return err == nil }, 30*time.Second, 200*time.Millisecond,
		"two of three nodes take a write")
	node("start")
	require.Eventually(t, func() bool {
		e, err := kv.Get(ctx, "k")
		return err == nil && string(e.Value()) == "v2"
	}, 30*time.Second, 200*time.Millisecond)
}

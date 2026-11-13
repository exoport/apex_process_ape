package natstest

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// CI on c9f4854 (ubuntu): a rig's first read of a just-declared bucket met
// "no responders". WaitKV waits until the bucket answers a get; here the
// bucket does not answer at all for a second (it is created then), and
// WaitKV returns once it does, ErrKeyNotFound counting as an answer.
func TestWaitKV_WaitsUntilTheBucketAnswers(t *testing.T) {
	nc, err := nats.Connect(RunJetStreamServer(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	const late = time.Second
	go func() {
		time.Sleep(late)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "late"})
	}()
	start := time.Now()
	WaitKV(t, js, "late")
	require.GreaterOrEqual(t, time.Since(start), late-100*time.Millisecond, "it waited for the bucket")
}

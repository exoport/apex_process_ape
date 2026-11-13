package natstest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// kvWait bounds WaitKV, as the rigs' 60 s setup deadline (744f355);
// kvPoll spaces its tries.
const (
	kvWait = 60 * time.Second
	kvPoll = 100 * time.Millisecond
)

// WaitKV waits, at most kvWait, until bucket answers a get: a found key
// or jetstream.ErrKeyNotFound both count. A bucket just declared on a
// cluster can exist before its stream's leader serves, and a rig's first
// read then met "nats: no responders available for request" (lease: read
// epoch, CI on c9f4854, ubuntu). A rig calls it between declaring the
// lease buckets and its first SetEpoch, with lease.MetaBucket.
func WaitKV(t *testing.T, js jetstream.JetStream, bucket string) {
	t.Helper()
	deadline := time.Now().Add(kvWait)
	for {
		err := getOnce(js, bucket)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("natstest: %s answered no get in %v: %v", bucket, kvWait, err)
		}
		time.Sleep(kvPoll)
	}
}

// getOnce is one try: a get of a key no rig writes, so a serving bucket
// answers ErrKeyNotFound.
func getOnce(js jetstream.JetStream, bucket string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	kv, err := js.KeyValue(ctx, bucket)
	if err != nil {
		return err
	}
	_, err = kv.Get(ctx, "natstest-probe")
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil
	}
	return err
}

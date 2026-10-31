package member

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// Plan step 4: a member project's logged event is published once (its
// message id dedups a second publish), and a watcher wakes on it; during a
// recorded outage nothing is published (replay writes it later).
func TestPublishAndWatch(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))

	p := project(t, strings.Replace(block, "nats://127.0.0.1:4222", url, 1), false)
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))

	wake, stop, live := Watch(ctx, p)
	defer stop()
	require.True(t, live)
	ev := apexv1.Event{Seq: 1, At: "2026-10-31T00:00:00Z", Event: apexv1.EventGateGreen, Milestone: "M1"}
	Publish(ctx, p, ev)
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not wake on the published event")
	}
	Publish(ctx, p, ev)
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	info, err := st.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), info.State.Msgs, "the same event twice is stored once")

	_, err = Down(p.Root, time.Now())
	require.NoError(t, err)
	Publish(ctx, p, apexv1.Event{Seq: 2, Event: apexv1.EventMilestoneDone, Milestone: "M1"})
	info, err = st.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), info.State.Msgs, "nothing published during a recorded outage")

	_, _, live = Watch(ctx, project(t, "project_name: x\n", false))
	require.False(t, live, "a project in no ensemble polls as before")
}

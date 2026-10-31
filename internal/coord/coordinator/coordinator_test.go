package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// §4.5: the coordinator's link drops (its node stops) and comes back on
// another node; it then publishes nats-paused, carrying since when, and
// nats-resumed. Nothing is published while the link is down.
func TestCoordinator_PublishesTheOutageOnReconnect(t *testing.T) {
	c := natstest.RunCluster(t, 3)
	first := c.Servers[0].ClientURL()
	nc, err := nats.Connect(c.URLs(), nats.DontRandomize(), nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	require.Equal(t, first, nc.ConnectedUrl(), "the coordinator's link is to the node the test stops")
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, lease.Declare(ctx, js, 3))
	require.NoError(t, slots.Declare(ctx, js, 3))
	require.NoError(t, events.Declare(ctx, js, 3))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	rctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = Run(rctx, nc, Config{Ensemble: "ens", ID: "c1"}) }()
	require.Eventually(t, func() bool {
		h, _, err := ls.Holder(ctx, "coord.ens")
		return err == nil && h == "c1"
	}, 10*time.Second, 50*time.Millisecond, "the coordinator holds coord.ens")
	stored := func(event string) (uint64, string) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		st, err := js.Stream(ctx, events.EventsStream)
		if err != nil {
			return 0, ""
		}
		m, err := st.GetLastMsgForSubject(ctx, "ape.evt.coord.ens.coord.c1."+event)
		if err != nil {
			return 0, ""
		}
		return m.Sequence, string(m.Data)
	}
	time.Sleep(500 * time.Millisecond)
	seq, _ := stored("nats-paused")
	require.Zero(t, seq, "nats-paused before any outage")

	c.Stop(0)
	require.Eventually(t, func() bool { return nc.IsConnected() && nc.ConnectedUrl() != first }, 20*time.Second, 50*time.Millisecond)
	var pSeq, rSeq uint64
	var pData, rData string
	require.Eventually(t, func() bool {
		pSeq, pData = stored("nats-paused")
		rSeq, rData = stored("nats-resumed")
		return pSeq > 0 && rSeq > 0
	}, 30*time.Second, 200*time.Millisecond, "after the reconnect, both events are stored")
	require.Less(t, pSeq, rSeq, "nats-paused first")
	require.Contains(t, pData, `"since"`)
	require.Contains(t, rData, `"at"`)
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	info, err := st.Info(ctx, jetstream.WithSubjectFilter("ape.evt.coord.ens.coord.c1.nats-*"))
	require.NoError(t, err)
	require.Len(t, info.State.Subjects, 2)
	for subj, n := range info.State.Subjects {
		require.Equal(t, uint64(1), n, "%s stored once, retries deduplicated", subj)
	}
}

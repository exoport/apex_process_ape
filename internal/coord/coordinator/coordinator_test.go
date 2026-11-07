package coordinator

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

// V8n: the coordinator exited on the first renewal an outage failed ("lost
// coord.<ens>: lease: read epoch: context deadline exceeded"), so every
// outage cost a restart by the supervisor (2a, and twice in 2b). Now it
// rides one out: every node down past the lease's TTL, then back, and the
// same coordinator runs on, holds coord.<ens> again and answers. The
// control: another coordinator claimed the lapsed lease before this one
// was back, and this one ends (lost).
func TestCoordinator_RidesOutANATSOutage(t *testing.T) {
	for _, taken := range []bool{false, true} {
		t.Run(map[bool]string{false: "rides-it-out", true: "control-taken-over"}[taken], func(t *testing.T) {
			c := natstest.RunCluster(t, 3)
			nc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond))
			require.NoError(t, err)
			t.Cleanup(nc.Close)
			js, err := jetstream.New(nc)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			require.NoError(t, lease.Declare(ctx, js, 3))
			require.NoError(t, slots.Declare(ctx, js, 3))
			require.NoError(t, events.Declare(ctx, js, 3))
			ls, err := lease.Open(ctx, js)
			require.NoError(t, err)
			require.NoError(t, ls.SetEpoch(ctx, 1))

			var logMu sync.Mutex
			var logged []string
			cfg := Config{Ensemble: "ens", ID: "c1", TTL: 2 * time.Second, Renew: 500 * time.Millisecond,
				Log: func(f string, a ...any) { logMu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); logMu.Unlock() }}
			var als *lease.Store
			if taken {
				// c2's store, bound before the outage; c1 renews slowly, so
				// c2's claim after it comes first.
				cfg.Renew = 5 * time.Second
				anc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond))
				require.NoError(t, err)
				t.Cleanup(anc.Close)
				ajs, err := jetstream.New(anc)
				require.NoError(t, err)
				als, err = lease.Open(ctx, ajs)
				require.NoError(t, err)
			}
			said := func(s string) bool {
				logMu.Lock()
				defer logMu.Unlock()
				for _, l := range logged {
					if strings.Contains(l, s) {
						return true
					}
				}
				return false
			}
			rctx, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan error, 1)
			go func() { done <- Run(rctx, nc, cfg) }()
			holder := func() string {
				hctx, hcancel := context.WithTimeout(ctx, 2*time.Second)
				defer hcancel()
				h, _, err := ls.Holder(hctx, "coord.ens")
				if err != nil {
					return "?"
				}
				return h
			}
			require.Eventually(t, func() bool { return holder() == "c1" }, 10*time.Second, 50*time.Millisecond)

			for i := range 3 {
				c.Stop(i)
			}
			time.Sleep(3 * cfg.TTL)
			if taken {
				time.Sleep(cfg.Renew) // c1's tick met the outage: it is mid-wait when the nodes return
			}
			select {
			case err := <-done:
				t.Fatalf("the coordinator ended during the outage: %v", err)
			default:
			}
			for i := range 3 {
				c.Restart(i)
			}
			if taken {
				// Another coordinator gets the lapsed lease first.
				require.Eventually(t, func() bool {
					cctx, ccancel := context.WithTimeout(ctx, 2*time.Second)
					defer ccancel()
					_, err := als.Claim(cctx, "coord.ens", "c2", time.Minute)
					return err == nil
				}, 30*time.Second, 50*time.Millisecond, "c2 claims the lapsed lease")
				select {
				case err := <-done:
					require.ErrorContains(t, err, "c2 holds it now", "control: lost to another coordinator, it ends")
				case <-time.After(30 * time.Second):
					t.Fatal("c1 runs on beside c2")
				}
				return
			}
			require.Eventually(t, func() bool { return holder() == "c1" }, 30*time.Second, 100*time.Millisecond, "the same coordinator holds coord.ens again")
			require.Eventually(t, func() bool {
				m, err := nc.Request("ape.coord.ens.status", nil, time.Second)
				return err == nil && strings.Contains(string(m.Data), `"id":"c1"`)
			}, 20*time.Second, 100*time.Millisecond, "and answers")
			select {
			case err := <-done:
				t.Fatalf("the coordinator ended after the outage: %v", err)
			default:
			}
			require.True(t, said("riding it out"), "it said it rides the outage out")
			require.Eventually(t, func() bool { return said("claimed again") || said("renewed again") }, 5*time.Second, 50*time.Millisecond, "and that it holds the lease again")
		})
	}
}

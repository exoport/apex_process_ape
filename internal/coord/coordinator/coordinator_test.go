package coordinator

import (
	"context"
	"errors"
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

	// No deadline, as ape coordinator: under the rig's, a call whose reply
	// the stopped node lost would hold c.mu for the rest of it.
	rctx, stop := context.WithCancel(context.Background())
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
			// V8-WIN-5: the restart of three nodes on their stores, and the
			// waits after it, outlasted 90 s and their 30/20/5 s on GitHub's
			// Windows runners (~3 runs in 5); each wait logs how long it took.
			ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
			defer cancel()
			require.NoError(t, lease.Declare(ctx, js, 3))
			require.NoError(t, slots.Declare(ctx, js, 3))
			require.NoError(t, events.Declare(ctx, js, 3))
			ls, err := lease.Open(ctx, js)
			require.NoError(t, err)
			require.NoError(t, ls.SetEpoch(ctx, 1))

			var logMu sync.Mutex
			var logged []string
			cfg := Config{
				Ensemble: "ens", ID: "c1", TTL: 2 * time.Second, Renew: 500 * time.Millisecond,
				Log: func(f string, a ...any) { logMu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); logMu.Unlock() },
			}
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
			t.Cleanup(func() {
				if t.Failed() {
					logMu.Lock()
					t.Log("the coordinator's log:\n" + strings.Join(logged, ""))
					logMu.Unlock()
				}
			})
			// The coordinator runs with no deadline, as `ape coordinator`
			// does, so each of its JetStream calls gets nats.go's own 5 s
			// timeout. Under the rig's 240 s one, a renewal whose ack the
			// restart lost waited out the rest of it holding c.mu, and status
			// never answered (CI-5GREEN-2 on 4d597e0, Windows).
			rctx, stop := context.WithCancel(context.Background())
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
			back := time.Now()
			waitedFor := func(what string) {
				t.Logf("V8-WIN-5: %s %v after the nodes were back", what, time.Since(back).Round(100*time.Millisecond))
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
			require.Eventually(t, func() bool { return holder() == "c1" }, 60*time.Second, 100*time.Millisecond, "the same coordinator holds coord.ens again")
			waitedFor("holds coord.ens again")
			require.Eventually(t, func() bool {
				m, err := nc.Request("ape.coord.ens.status", nil, time.Second)
				return err == nil && strings.Contains(string(m.Data), `"id":"c1"`)
			}, 60*time.Second, 100*time.Millisecond, "and answers")
			waitedFor("answers")
			select {
			case err := <-done:
				t.Fatalf("the coordinator ended after the outage: %v", err)
			default:
			}
			require.True(t, said("riding it out"), "it said it rides the outage out")
			require.Eventually(t, func() bool { return said("claimed again") || said("renewed again") }, 20*time.Second, 50*time.Millisecond, "and that it holds the lease again")
			waitedFor("says it holds the lease again")
		})
	}
}

// V8-WIN-5: a request made while NATS is down waits in the connection's
// buffer and is answered once a node is back, so a coordinator can ride
// an outage out with no call of its ticks failing, and said nothing of it
// (the full-outage drill above, Windows run 42844724276). Here no call
// can fail: the one server is never stopped, so no leader moves, and the
// connection is made to reconnect to it (ForceReconnect). The
// connection's word is then the only thing that can say the outage, as
// the conductor's does (V8d). Stopping a cluster node instead left a call
// failing on Windows while its stream's leader moved (CI-5GREEN-2), which
// is the other branch, not this one.
func TestCoordinator_SaysAReconnectThatNoCallFailed(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url, nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, slots.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	var logMu sync.Mutex
	var logged []string
	said := func(s string) bool {
		logMu.Lock()
		defer logMu.Unlock()
		return strings.Contains(strings.Join(logged, ""), s)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log("the coordinator's log:\n" + strings.Join(logged, ""))
			logMu.Unlock()
		}
	})
	cfg := Config{
		Ensemble: "ens", ID: "c1", TTL: 2 * time.Second, Renew: 500 * time.Millisecond,
		Log: func(f string, a ...any) { logMu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); logMu.Unlock() },
	}
	// No deadline, as ape coordinator (the outage drill's reason, above).
	rctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- Run(rctx, nc, cfg) }()
	require.Eventually(t, func() bool {
		h, _, err := ls.Holder(ctx, "coord.ens")
		return err == nil && h == "c1"
	}, 10*time.Second, 50*time.Millisecond, "the coordinator holds coord.ens")
	time.Sleep(2 * cfg.Renew) // a few good renewals first: nothing to say yet
	require.False(t, said("riding it out"), "nothing said before the reconnect")

	was := nc.Stats().Reconnects
	require.NoError(t, nc.ForceReconnect())
	require.Eventually(t, func() bool { return nc.IsConnected() && nc.Stats().Reconnects > was }, 20*time.Second, 50*time.Millisecond, "reconnected")
	// Said once per outage, so the line names what said it first: the
	// reconnect, not a call's error.
	require.Eventually(t, func() bool { return said("could not be renewed (NATS reconnected); riding it out") }, 10*time.Second, 50*time.Millisecond, "the reconnect is said, though no call failed")
	require.Eventually(t, func() bool { return said("is renewed again") }, 10*time.Second, 50*time.Millisecond, "and the next good renewal ends it")
	select {
	case err := <-done:
		t.Fatalf("the coordinator ended: %v", err)
	default:
	}
	h, _, err := ls.Holder(ctx, "coord.ens")
	require.NoError(t, err)
	require.Equal(t, "c1", h, "it held coord.ens throughout")
}

// observe, row by row: the connection's word on an outage, said once, on
// either branch of a ride-out — no call failed (only the reconnect says
// it) or one did (keep said it first, and the reconnect adds nothing).
func TestObserve_TheConnectionSaysAnOutage(t *testing.T) {
	var logged []string
	fresh := func() *Coordinator {
		logged = nil
		return &Coordinator{cfg: Config{Ensemble: "ens", Renew: time.Second, Log: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}, reconnects: 3}
	}
	c := fresh()
	require.True(t, c.observe(true, 3), "the control: connected, no reconnect")
	require.Empty(t, logged)
	require.False(t, c.down)

	c = fresh()
	require.False(t, c.observe(false, 3), "disconnected: the tick does not go on")
	require.True(t, c.down)
	require.Len(t, logged, 1)
	require.Contains(t, logged[0], "riding it out")
	require.Contains(t, logged[0], "NATS disconnected")
	require.False(t, c.observe(false, 3))
	require.Len(t, logged, 1, "said once per outage")

	c = fresh() // no call failed: the buffered renewal landed after the reconnect
	require.True(t, c.observe(true, 4), "reconnected: the tick goes on")
	require.True(t, c.down)
	require.Len(t, logged, 1)
	require.Contains(t, logged[0], "NATS reconnected")
	c.reachable() // keep's next good renewal
	require.Contains(t, logged[1], "is renewed again")
	require.True(t, c.observe(true, 4))
	require.Len(t, logged, 2, "the same count again is no new outage")

	c = fresh() // a call failed first: keep said it
	c.unreachable(errors.New("lease: read epoch: nats: no responders available for request"))
	require.True(t, c.observe(true, 4))
	require.Len(t, logged, 1, "already said: the reconnect adds nothing")
	c.reachable()
	require.Contains(t, logged[1], "is renewed again")
}

// V8n's ride-out has an end: a connection closed for good (its reconnects
// spent, or closed by its owner) brings nothing back, so the coordinator
// ends rather than running on holding nothing, and its supervisor's
// restart is not left facing a process that will never answer.
func TestCoordinator_EndsOnAClosedConnection(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := t.Context()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, slots.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	cnc, err := nats.Connect(url)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cnc, Config{Ensemble: "ens", ID: "c1", TTL: 2 * time.Second, Renew: 200 * time.Millisecond})
	}()
	require.Eventually(t, func() bool { h, _, err := ls.Holder(ctx, "coord.ens"); return err == nil && h == "c1" }, 10*time.Second, 50*time.Millisecond)
	cnc.Close()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "its NATS connection is closed")
	case <-time.After(5 * time.Second):
		t.Fatal("the coordinator runs on over a closed connection")
	}
}

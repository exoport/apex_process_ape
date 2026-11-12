package conductor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
)

// V15-RED-1-B (CI-5GREEN-3 on defa954, Windows): one tick saw the link up,
// then its sweep waited out the whole outage, and keep judged silence on
// an event the outage itself had kept unacknowledged; the next tick, 1 ms
// later, said the reconnect. Here the seam holds a sweep across an outage
// no tick sees (and deliver's next fetch, which would start the window
// again), and no silence may be judged when it returns: the connection is
// read again before keep.
func TestConductor_AnOutageInsideOneTickIsNotJudgedSilent(t *testing.T) {
	c, js := outageRig(t)
	ctx := t.Context()
	anc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(anc.Close)
	a := &fakeSession{nc: anc, answers: 99, acked: map[string]bool{}}
	var logMu sync.Mutex
	var logged []string
	cfg := scaled("ann", "cond-a", a)
	cfg.Log = func(f string, args ...any) {
		logMu.Lock()
		logged = append(logged, time.Now().Format("15:04:05.000 ")+fmt.Sprintf(f, args...))
		logMu.Unlock()
	}
	has := func(x string) bool {
		logMu.Lock()
		defer logMu.Unlock()
		return strings.Contains(strings.Join(logged, ""), x)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(strings.Join(logged, ""))
			logMu.Unlock()
		}
	})
	errA := make(chan error, 1)
	go func() { errA <- Run(ctx, anc, cfg) }()
	bound(t, js)
	a.hold.Store(true) // the event stays pending: only the outage's own silence is in play
	_, err = events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 1), []byte(`{"q":"held across"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.typedWith("held across") }, 5*time.Second, 50*time.Millisecond)

	entered, released := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func() {
		first := false
		once.Do(func() { first = true })
		if !first {
			return
		}
		close(entered)
		<-released
	}
	testSweepHook.Store(&hook)
	t.Cleanup(func() { testSweepHook.Store(nil) })
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no tick reached sweep")
	}
	// deliver's next fetch is held too, as a slow cluster holds it (the
	// gated drill's seam): its first good fetch would start the window
	// again, and on the runner none had come back yet (10.5 s, no fetch).
	g := make(chan struct{})
	testFetchGate.Store(&g)
	lifted := false
	lift := func() {
		if !lifted {
			lifted = true
			testFetchGate.Store(nil)
			close(g)
		}
	}
	t.Cleanup(lift)
	// The whole outage inside that one tick: past the lease's TTL, so the
	// event's last acknowledgement is old when the tick goes on.
	was := anc.Stats().Reconnects
	outageWith(t, c, 3*cfg.LeaseTTL, nil)
	require.Eventually(t, func() bool { return anc.IsConnected() && anc.Stats().Reconnects > was }, afterOutage, 100*time.Millisecond, "the conductor's connection is back")
	// The durable answers again, so sweep's own read succeeds and nothing
	// else holds the judgment off (infoDown).
	require.Eventually(t, func() bool {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		cons, err := js.Consumer(cctx, events.EventsStream, layout.ConductorConsumer("ann", "atomix"))
		if err != nil {
			return false
		}
		_, err = cons.Info(cctx)
		return err == nil
	}, afterOutage, 250*time.Millisecond, "the durable answers")
	close(released)

	require.Eventually(t, func() bool { return has("riding it out") }, 10*time.Second, 50*time.Millisecond, "the tick says the outage it did not see")
	time.Sleep(2 * cfg.Renew) // the tick that came back, and the one after it
	require.False(t, has("judged silent"), "no silence judged for the outage's own unacknowledged event")
	lift()
	a.hold.Store(false)
	require.Eventually(t, func() bool { return a.ackedWith("held across") }, afterOutage, 100*time.Millisecond, "typed again after the outage, acknowledged")
	require.False(t, has("judged silent"))
	select {
	case err := <-errA:
		t.Fatalf("attach ended: %v", err)
	default:
	}
}

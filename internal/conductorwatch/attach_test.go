package conductorwatch

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/conductor"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// lockedPane is fakePane safe to change while conductor.Run reads it.
type lockedPane struct {
	mu sync.Mutex
	fakePane
}

func (l *lockedPane) set(screen, session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.screen, l.session = screen, session
}

func (l *lockedPane) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.typed...)
}

func (l *lockedPane) Capture(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakePane.Capture(ctx)
}

func (l *lockedPane) Type(ctx context.Context, text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakePane.Type(ctx, text)
}

func (l *lockedPane) Session(ctx context.Context) (id, why string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakePane.Session(ctx)
}

// v1.5 stage 1a, `ape conductor attach`: an ensemble's events reach the
// conductor's pane through the same gate as the 1.4.2 watcher's
// deliveries. A busy pane gets nothing (the control); an idle one gets the
// event, marked as ape's with its ack command; another session in the pane
// pauses delivery, and the event waiting is typed after a --repin from that
// session.
func TestAttach_DeliversThroughTheGate(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := t.Context()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	pane := &lockedPane{screen: busyScreen, conductor: true, session: "conductor-1"}
	state := t.TempDir()
	gate := &Gate{Pane: pane, StateDir: state}
	go func() {
		_ = conductor.Run(ctx, nc, conductor.Config{
			User: "dana", Ensemble: "shop", Projects: []string{"notes"}, Holder: "h1", Nonce: "n1",
			LeaseTTL: time.Minute, Renew: 10 * time.Second, AckWait: time.Minute,
			Session: &PaneSession{Ctx: ctx, Gate: gate},
		})
	}()
	time.Sleep(500 * time.Millisecond) // the consumer bound: DeliverNew
	publish := func(seq int64, event string) {
		_, err := events.Publish(ctx, js, events.Subject("dana", "notes", events.KindProject, "M1", event), events.MessageID("notes", "c1", seq), []byte(`{"reason":"x"}`))
		require.NoError(t, err)
	}

	publish(1, "block")
	time.Sleep(time.Second)
	require.Empty(t, pane.seen(), "the control: a busy pane gets nothing typed")

	pane.set(idleScreen, "conductor-1")
	require.Eventually(t, func() bool { return len(pane.seen()) == 1 }, 5*time.Second, 50*time.Millisecond)
	typed := pane.seen()[0]
	require.True(t, strings.HasPrefix(typed, apexv1.DeliveryTag), typed)
	require.Contains(t, typed, "ape conductor ack $JS.ACK.")

	pane.set(idleScreen, "after-clear")
	publish(2, "question")
	time.Sleep(1500 * time.Millisecond)
	require.Len(t, pane.seen(), 1, "another session in the pane: paused, nothing typed")
	require.NoError(t, RequestRepin(state, "after-clear", time.Now()))
	require.Eventually(t, func() bool { return len(pane.seen()) == 2 }, 5*time.Second, 50*time.Millisecond, "typed after the --repin")
	require.Contains(t, pane.seen()[1], "question")
}

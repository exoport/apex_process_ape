package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

func js(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	nc, err := nats.Connect(natstest.RunJetStreamServer(t), nats.MaxReconnects(-1), nats.ReconnectWait(50*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	j, err := jetstream.New(nc)
	require.NoError(t, err)
	require.NoError(t, Declare(context.Background(), j, 1))
	require.NoError(t, Declare(context.Background(), j, 1), "declaring again is a no-op")
	return nc, j
}

func TestSubject(t *testing.T) {
	require.Equal(t, "ape.evt.ann.taskforge.project.M2.gate-green", Subject("Ann", "taskforge", KindProject, "M2", "gate-green"))
	require.Equal(t, "ape.evt.coord.atomix.coord.-.nats-paused", Subject("coord", "atomix", KindCoord, "", "nats-paused"))
	require.Equal(t, "ape.evt.ann.my-proj.change.take-in-2.merged", Subject("ann", "my.proj", KindChange, "take-in-2", "merged"))
}

// OQ14: a checkout's id is made once and kept; two checkouts differ.
func TestCheckoutID(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	ida, err := CheckoutID(a)
	require.NoError(t, err)
	require.Len(t, ida, 12)
	again, err := CheckoutID(a)
	require.NoError(t, err)
	require.Equal(t, ida, again)
	idb, err := CheckoutID(b)
	require.NoError(t, err)
	require.NotEqual(t, ida, idb)
	require.Equal(t, "taskforge/"+ida+"/42", MessageID("taskforge", ida, 42))
}

// §4.4 step 3: the same message id stores once, so a replay racing a live
// publish is harmless; another checkout's same seq is another event.
func TestPublish_Dedup(t *testing.T) {
	_, j := js(t)
	ctx := context.Background()
	subj := Subject("ann", "taskforge", KindProject, "M2", "gate-green")
	dup, err := Publish(ctx, j, subj, MessageID("taskforge", "c1", 7), []byte(`{"seq":7}`))
	require.NoError(t, err)
	require.False(t, dup)
	dup, err = Publish(ctx, j, subj, MessageID("taskforge", "c1", 7), []byte(`{"seq":7}`))
	require.NoError(t, err)
	require.True(t, dup, "the replay of a stored event")
	dup, err = Publish(ctx, j, subj, MessageID("taskforge", "c2", 7), []byte(`{"seq":7}`))
	require.NoError(t, err)
	require.False(t, dup, "a second checkout's seq 7 is its own event")
	s, err := j.Stream(ctx, EventsStream)
	require.NoError(t, err)
	info, err := s.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), info.State.Msgs)
}

// A waiter wakes on events, not on a timer, and re-reads once after a
// reconnect.
func TestWait(t *testing.T) {
	nc, j := js(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var reads atomic.Int32
	var green atomic.Bool
	done := func() (bool, error) { //nolint:unparam // Wait's contract; a read here never fails
		reads.Add(1)
		return green.Load(), nil
	}
	var wg sync.WaitGroup
	var werr error
	wg.Go(func() { werr = Wait(ctx, nc, "ape.evt.ann.taskforge.project.M2.>", done) })

	require.Eventually(t, func() bool { return reads.Load() == 1 }, 5*time.Second, 10*time.Millisecond, "one read at the start")
	time.Sleep(700 * time.Millisecond)
	require.Equal(t, int32(1), reads.Load(), "no timer: an idle waiter does not re-read")

	_, err := Publish(ctx, j, "ape.evt.ann.taskforge.project.M2.block", "taskforge/c1/1", nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return reads.Load() == 2 }, 5*time.Second, 10*time.Millisecond, "an event wakes it")

	require.NoError(t, nc.ForceReconnect())
	require.Eventually(t, func() bool { return reads.Load() == 3 }, 10*time.Second, 10*time.Millisecond, "a reconnect re-reads once")

	green.Store(true)
	_, err = Publish(ctx, j, "ape.evt.ann.taskforge.project.M2.gate-green", "taskforge/c1/2", nil)
	require.NoError(t, err)
	wg.Wait()
	require.NoError(t, werr)
	require.Equal(t, int32(4), reads.Load())
}

// events.jsonl lines go out as kind project; publishing the log twice
// stores each line once.
func TestPublishProject_ReplayStoresOnce(t *testing.T) {
	_, j := js(t)
	ctx := context.Background()
	exit := 0
	log := []apexv1.Event{
		{Seq: 1, Event: apexv1.EventGateGreen, Milestone: "M2"},
		{Seq: 2, Event: apexv1.EventRunEnd, Run: "r1", Exit: &exit},
		{Seq: 3, Event: apexv1.EventMilestoneDone, Milestone: "M2"},
	}
	for range 2 {
		for _, e := range log {
			_, err := PublishProject(ctx, j, "ann", "taskforge", "c1", e)
			require.NoError(t, err)
		}
	}
	s, err := j.Stream(ctx, EventsStream)
	require.NoError(t, err)
	info, err := s.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(3), info.State.Msgs)
	m, err := s.GetLastMsgForSubject(ctx, "ape.evt.ann.taskforge.project.-.run-end")
	require.NoError(t, err, "an event with no milestone has id -")
	require.Equal(t, "taskforge/c1/2", m.Header.Get(jetstream.MsgIDHeader))
}

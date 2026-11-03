package coordinator

import (
	"context"
	"fmt"
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

func trig(id string) TriggerEvent {
	return TriggerEvent{MsgID: id, Subject: "ape.evt.dana.notes.project.M1.change-archived"}
}

// PLAN-V15-ASSESSOR step 2 (Q2: a 60 s quiet window, a 5 min ceiling): a
// burst of triggers is one batch; triggers further apart than the quiet
// window are a batch each (the control); a steady stream closes at the
// ceiling; an id already batched is dropped (a replay).
func TestBatcher_DebounceCeilingAndReplays(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	b := newBatcher(time.Minute, 5*time.Minute)
	for i := range 3 {
		require.True(t, b.add(trig(fmt.Sprint("a", i)), t0.Add(time.Duration(i)*3*time.Second)))
	}
	require.Nil(t, b.due(t0.Add(50*time.Second)), "still inside the quiet window")
	burst := b.due(t0.Add(66 * time.Second))
	require.NotNil(t, burst)
	require.Len(t, burst.Events, 3, "a burst is one batch")
	require.Nil(t, b.due(t0.Add(70*time.Second)), "nothing open")

	require.False(t, b.add(trig("a1"), t0.Add(80*time.Second)), "a replayed id is dropped")
	require.Nil(t, b.due(t0.Add(200*time.Second)))

	spaced := 0
	for i := range 3 {
		at := t0.Add(time.Hour + time.Duration(i)*90*time.Second)
		require.True(t, b.add(trig(fmt.Sprint("s", i)), at))
		if got := b.due(at.Add(61 * time.Second)); got != nil {
			spaced++
			require.Len(t, got.Events, 1)
		}
	}
	require.Equal(t, 3, spaced, "control: further apart than the quiet window, a batch each")

	t2 := t0.Add(2 * time.Hour)
	var closed []*Batch
	for i := range 40 { // one every 20 s: never quiet for a minute
		at := t2.Add(time.Duration(i) * 20 * time.Second)
		if got := b.due(at); got != nil {
			closed = append(closed, got)
		}
		b.add(trig(fmt.Sprint("c", i)), at)
	}
	require.NotEmpty(t, closed, "a steady stream closes at the ceiling")
	require.Equal(t, 5*time.Minute, closed[0].Closed.Sub(closed[0].Opened))
}

// Wired into Run: the coordinator subscribes to its ensemble's projects'
// triggers and hands each closed batch to OnBatch. The T1: three triggers
// in quick succession are one batch, with their message ids. The control:
// a trigger of a project outside the ensemble makes no batch.
func TestCoordinator_BatchesItsProjectsTriggers(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, slots.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	got := make(chan Batch, 8)
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = Run(rctx, nc, Config{Ensemble: "ens", ID: "c1", Projects: []string{"notes"}, Assessor: &AssessorConfig{
			Quiet: 300 * time.Millisecond, Ceiling: 3 * time.Second,
			OnBatch: func(_ context.Context, b Batch) { got <- b },
		}})
	}()
	require.Eventually(t, func() bool {
		h, _, err := ls.Holder(ctx, "coord.ens")
		return err == nil && h == "c1"
	}, 10*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // the consumer is bound

	pub := func(subject, id string) {
		_, err := js.Publish(ctx, subject, []byte(`{"key":"`+id+`"}`), jetstream.WithMsgID(id))
		require.NoError(t, err)
	}
	pub("ape.evt.dana.other.project.M1.change-archived", "other/c/change-archived/M1/x")
	pub("ape.evt.dana.notes.project.M1.plan-approved", "notes/c/plan-approved/M1/t1")
	pub("ape.evt.dana.notes.project.M1.change-archived", "notes/c/change-archived/M1/add-wave")
	pub("ape.evt.dana.notes.project.M2.gate-red", "notes/c/gate-red/M2/sha@t")
	pub("ape.evt.dana.notes.project.M1.gate-green", "notes/c/gate-green/M1/1") // not a trigger
	select {
	case b := <-got:
		ids := make([]string, 0, len(b.Events))
		for _, e := range b.Events {
			ids = append(ids, e.MsgID)
		}
		require.Equal(t, []string{"notes/c/plan-approved/M1/t1", "notes/c/change-archived/M1/add-wave", "notes/c/gate-red/M2/sha@t"}, ids, "one batch, the ensemble's triggers only")
		require.NotEmpty(t, b.ID)
	case <-time.After(10 * time.Second):
		t.Fatal("no batch")
	}
	select {
	case b := <-got:
		t.Fatalf("control: a second batch %+v", b)
	case <-time.After(time.Second):
	}
}

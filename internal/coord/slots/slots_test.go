package slots

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

func open(t *testing.T) *Store {
	t.Helper()
	nc, err := nats.Connect(natstest.RunJetStreamServer(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, Declare(ctx, js, 1))
	require.NoError(t, Declare(ctx, js, 1))
	s, err := Open(ctx, js)
	require.NoError(t, err)
	return s
}

// A counted resource: units are handed out up to its capacity, a release
// frees one, and a stale release frees nothing.
func TestAcquireRelease(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	v := Value{Holder: "ann", Coordinator: "coord-1"}
	held := make([]Slot, 0, 3)
	for range 3 {
		sl, err := s.Acquire(ctx, "mmq4", Heavy, 3, v, time.Minute)
		require.NoError(t, err)
		held = append(held, sl)
	}
	require.Equal(t, []string{"slot.mmq4.heavy.0", "slot.mmq4.heavy.1", "slot.mmq4.heavy.2"}, []string{held[0].Key, held[1].Key, held[2].Key})
	_, err := s.Acquire(ctx, "mmq4", Heavy, 3, v, time.Minute)
	require.ErrorIs(t, err, ErrFull)

	renewed, err := s.Renew(ctx, held[1])
	require.NoError(t, err)
	require.ErrorIs(t, s.Release(ctx, held[1]), ErrLost, "a release at the old revision frees nothing")
	require.NoError(t, s.Release(ctx, renewed))
	again, err := s.Acquire(ctx, "mmq4", Heavy, 3, v, time.Minute)
	require.NoError(t, err)
	require.Equal(t, "slot.mmq4.heavy.1", again.Key)

	_, err = s.Acquire(ctx, "other-host", Heavy, 3, v, time.Minute)
	require.NoError(t, err, "another host has its own units")
}

// R7: a slot held past its TTL is released by the server, and the
// coordinator that granted it — only that one — records it; a release is
// not a reclaim.
func TestWatchExpiries(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	var mu sync.Mutex
	var got []Reclaimed
	go func() {
		_ = s.WatchExpiries(ctx, "coord-1", func(r Reclaimed) { mu.Lock(); got = append(got, r); mu.Unlock() })
	}()
	time.Sleep(200 * time.Millisecond)
	mine, err := s.Acquire(ctx, "mmq4", "port-8080", 1, Value{Holder: "ann", Coordinator: "coord-1", Check: "T3"}, time.Second)
	require.NoError(t, err)
	_, err = s.Acquire(ctx, "mmq4", "port-9090", 1, Value{Holder: "bob", Coordinator: "coord-2"}, time.Second)
	require.NoError(t, err)
	released, err := s.Acquire(ctx, "mmq4", "port-7070", 1, Value{Holder: "ann", Coordinator: "coord-1"}, time.Minute)
	require.NoError(t, err)
	require.NoError(t, s.Release(ctx, released))

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 }, 10*time.Second, 50*time.Millisecond)
	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 1, "only coord-1's expired slot; not coord-2's, not a released one")
	require.Equal(t, mine.Key, got[0].Key)
	require.Equal(t, "T3", got[0].Value.Check)
}

// OQ12: what v1.4 saw a check do proposes its slots.
func TestPropose(t *testing.T) {
	require.Equal(t, []Proposal{
		{Resource: "port-8080", Why: "a service listened on :8080 (4242 postgres)"},
		{Resource: "containers", Why: "it started 2 container(s)"},
	}, Propose(apexv1.Observed{Services: []string{"4242 postgres :8080", "4243 postgres :8080"}, Containers: 2}))
	require.Empty(t, Propose(apexv1.Observed{InProcessListeners: 3, Runner: "go"}), "OS-assigned ports in the test runner need no slot")
}

// OQ13: heavy units are bounded by capacity, memory and pressure.
func TestHeavyGuard(t *testing.T) {
	g := DefaultHeavyGuard
	const gib = int64(1 << 30)
	require.Equal(t, 3, g.Room(0, 16*gib, 0.5))
	require.Equal(t, 1, g.Room(2, 16*gib, 0.5))
	require.Equal(t, 0, g.Room(3, 16*gib, 0.5))
	require.Equal(t, 2, g.Room(0, 2*gib+gib/2, 0.5), "memory for two runs")
	require.Equal(t, 0, g.Room(0, gib/2, 0.5), "not one run's memory")
	require.Equal(t, 0, g.Room(0, 16*gib, 25), "memory pressure")
	require.Equal(t, 3, g.Room(0, -1, -1), "unknown memory and pressure do not block")
	require.InDelta(t, 1.25, parsePSI("some avg10=1.25 avg60=0.50 avg300=0.10 total=123\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"), 1e-9)
	require.InDelta(t, -1.0, parsePSI(""), 1e-9)
}

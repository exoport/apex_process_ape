package sessiondriver

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/stretchr/testify/require"
)

func stopEvent(session string) orchestrator.HookEvent {
	payload := json.RawMessage(`{"session_id":"` + session + `","hook_event_name":"Stop","background_tasks":[]}`)
	return orchestrator.HookEvent{Event: ipc.HookStop, Payload: payload}
}

// A project stop hook that BLOCKED the Stop keeps the step alive: Claude
// Code is continuing the turn, so tearing the session down would end it
// mid-work (v1 trial decision D1). The next Stop, allowed, completes it.
func TestDriver_StopGateBlockKeepsTheStepAlive(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var sessions []string
	d := newTestDriver(time.Minute)
	d.SetStopGate(func(sessionID string, stopAt time.Time) (bool, bool) {
		sessions = append(sessions, sessionID)
		require.False(t, stopAt.IsZero())
		return calls.Add(1) == 1, true // block the first, allow the second
	})

	d.FeedHook(stopEvent("sess-1"))
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, d.WaitStepDone(ctx), context.DeadlineExceeded, "a blocked Stop must not complete the step")
	n, _ := d.DeferredStops()
	require.Equal(t, 1, n, "the deferral is counted")

	d.FeedHook(stopEvent("sess-1"))
	require.NoError(t, d.WaitStepDone(t.Context()))
	require.Equal(t, []string{"sess-1", "sess-1"}, sessions)
}

// No verdict (no gate applies, or the hook never ran) completes as before.
func TestDriver_StopGateWithoutVerdictCompletes(t *testing.T) {
	t.Parallel()
	d := newTestDriver(time.Minute)
	d.SetStopGate(func(string, time.Time) (bool, bool) { return true, false })
	d.FeedHook(stopEvent("sess-1"))
	require.NoError(t, d.WaitStepDone(t.Context()))
	n, _ := d.DeferredStops()
	require.Zero(t, n)
}

// A Stop the background-task gate deferred is still put to the stop gate
// (D88: the project's hook decided it too, and its verdict must be taken so
// the next Stop meets its own), but the answer changes nothing: even an
// allow does not complete the step.
func TestDriver_StopGateTakesDeferredStopsVerdictAndIgnoresIt(t *testing.T) {
	t.Parallel()
	var asked atomic.Int32
	d := newTestDriver(80 * time.Millisecond)
	d.SetStopGate(func(string, time.Time) (bool, bool) { asked.Add(1); return false, true })
	payload := json.RawMessage(`{"session_id":"s","background_tasks":[{"id":"a1","type":"subagent","status":"running","description":"x"}]}`)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop, Payload: payload})
	require.Error(t, d.WaitStepDone(t.Context()))
	require.Eventually(t, func() bool { return asked.Load() == 1 }, time.Second, 10*time.Millisecond)
}

// Stops are put to the gate in the order they arrived, one at a time,
// whatever the goroutines' scheduling: a slow first answer does not let
// the second Stop take the first verdict.
func TestDriver_StopGateDecidesStopsInArrivalOrder(t *testing.T) {
	t.Parallel()
	var (
		mu    sync.Mutex
		order []time.Time
	)
	var calls atomic.Int32
	d := newTestDriver(time.Minute)
	d.SetStopGate(func(_ string, stopAt time.Time) (bool, bool) {
		n := calls.Add(1)
		if n == 1 {
			time.Sleep(150 * time.Millisecond)
		}
		mu.Lock()
		order = append(order, stopAt)
		mu.Unlock()
		return n < 3, true // calls 1 and 2 block, call 3 allows
	})
	deferred := json.RawMessage(`{"session_id":"s","background_tasks":[{"id":"a1","type":"subagent","status":"running"}]}`)
	// 30 ms apart: Windows' clock can advance once per ~15.6 ms tick, so
	// Stops fed closer together could carry the same arrival time.
	d.FeedHook(stopEvent("s"))
	time.Sleep(30 * time.Millisecond)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop, Payload: deferred})
	time.Sleep(30 * time.Millisecond)
	d.FeedHook(stopEvent("s"))
	require.NoError(t, d.WaitStepDone(t.Context()))
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, order, 3)
	require.True(t, order[0].Before(order[1]) && order[1].Before(order[2]), "decided in arrival order: %v", order)
}

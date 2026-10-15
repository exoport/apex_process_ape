package sessiondriver

import (
	"context"
	"encoding/json"
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

// The gate is never consulted for a Stop the background-task gate already
// deferred: it decides only Stops that would otherwise complete.
func TestDriver_StopGateNotAskedForDeferredStops(t *testing.T) {
	t.Parallel()
	var asked atomic.Bool
	d := newTestDriver(80 * time.Millisecond)
	d.SetStopGate(func(string, time.Time) (bool, bool) { asked.Store(true); return false, true })
	payload := json.RawMessage(`{"session_id":"s","background_tasks":[{"id":"a1","type":"subagent","status":"running","description":"x"}]}`)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop, Payload: payload})
	require.Error(t, d.WaitStepDone(t.Context()))
	require.False(t, asked.Load())
}

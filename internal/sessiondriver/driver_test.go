package sessiondriver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	bs, err := json.Marshal(v)
	require.NoError(t, err)
	return bs
}

// TestDriver_StopSignalsDone: a Stop hook unblocks WaitStepDone.
func TestDriver_StopSignalsDone(t *testing.T) {
	d := NewDriver(func() *runlog.Writer { return nil }, time.Minute)
	d.Begin()

	go func() {
		time.Sleep(20 * time.Millisecond)
		d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, d.WaitStepDone(ctx))
}

// TestDriver_IdleTimeout: with no hook events, WaitStepDone trips the
// idle backstop and returns an error.
func TestDriver_IdleTimeout(t *testing.T) {
	d := NewDriver(func() *runlog.Writer { return nil }, 50*time.Millisecond)
	d.Begin()
	err := d.WaitStepDone(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "idle for")
}

// TestDriver_BeginDrainsStaleStop: a Stop that arrived before Begin must
// not satisfy the next WaitStepDone.
func TestDriver_BeginDrainsStaleStop(t *testing.T) {
	d := NewDriver(func() *runlog.Writer { return nil }, 60*time.Millisecond)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop}) // stale
	d.Begin()                                               // drains it
	err := d.WaitStepDone(t.Context())
	require.Error(t, err, "drained stale Stop must not complete the wait")
	require.Contains(t, err.Error(), "idle for")
}

// TestDriver_CapturesTranscriptAndScans: a UPS hook binds the main
// transcript; Telemetry scans it.
func TestDriver_CapturesTranscriptAndScans(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	require.NoError(t, os.WriteFile(src, []byte(turnLine(100, 200)+"\n"), 0o600))

	d := NewDriver(func() *runlog.Writer { return nil }, time.Minute)
	d.SetFlushGrace(time.Millisecond)
	d.Begin()
	d.FeedHook(orchestrator.HookEvent{
		Event:   ipc.HookUserPromptSubmit,
		Payload: mustJSON(t, map[string]string{"session_id": "sess-1", "transcript_path": src}),
	})
	require.Equal(t, "sess-1", d.SessionID())

	tele := d.Telemetry()
	require.Equal(t, 1, tele.Totals.NumTurns)
	require.Equal(t, 100, tele.Totals.InputTokens)
	require.Equal(t, "sess-1", tele.Sessions[0].SessionID)
}

// TestDriver_RunlogFanout: FeedHook / FeedCall / FeedReply write to the
// runlog streams.
func TestDriver_RunlogFanout(t *testing.T) {
	dir := t.TempDir()
	rl, err := runlog.New(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rl.Close() })

	d := NewDriver(func() *runlog.Writer { return rl }, time.Minute)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookPreToolUse, SessionID: "s1", At: time.Now()})
	d.FeedCall(orchestrator.ToolCall{Tool: "Read", SessionID: "s1", At: time.Now()})
	d.FeedReply("done")
	require.NoError(t, rl.Close())

	hooks, err := os.ReadFile(filepath.Join(dir, "hook-events.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(hooks), ipc.HookPreToolUse)
	calls, err := os.ReadFile(filepath.Join(dir, "bridge-calls.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(calls), "Read")
}

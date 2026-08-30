package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// TestSessionBoundaryHooks_DeliveredAndInert drives SessionStart and
// PreCompact through the REAL delivery path — runNotify → TCP → IPC
// framing → BridgeRuntime dispatch → FeedHook — and asserts the two halves
// of what registering them is supposed to mean:
//
//	DELIVERED  both land in hook-events.jsonl, because nothing between
//	           `ape notify --event` and the runlog writer allowlists event
//	           names. That is the whole mechanism, and it is worth a test
//	           precisely because it is an ABSENCE — a filter added later in
//	           any of those four hops would silently drop the events and
//	           the only symptom would be a metric that reads zero.
//
//	INERT      neither completes a step. Both are async, neither carries a
//	           field a completion gate reads, and NoteHook switches on
//	           PostToolUse and Stop alone. A step still ends when, and only
//	           when, Stop says it does.
func TestSessionBoundaryHooks_DeliveredAndInert(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	rl, err := runlog.New(runDir)
	require.NoError(t, err)
	defer rl.Close()

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return rl })

	var delivered atomic.Int64
	rt := orchestrator.NewBridgeRuntime(orchestrator.BridgeRuntimeOptions{
		OnHook: func(h orchestrator.HookEvent) {
			core.FeedHook(h)
			delivered.Add(1)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, rt.Listen(ctx))
	serveDone := make(chan struct{})
	go func() { _ = rt.Serve(ctx); close(serveDone) }()
	defer func() { cancel(); <-serveDone }()
	port := strconv.Itoa(rt.IPCPort())

	send := func(event string, envelope map[string]any) {
		t.Helper()
		before := delivered.Load()
		runNotify(event, bytes.NewReader(mustJSON(t, envelope)), port)
		require.Eventually(t, func() bool { return delivered.Load() > before },
			5*time.Second, 2*time.Millisecond, "hook %s not delivered through the bridge", event)
	}

	core.OnStepStart(pipeline.InteractiveStepInfo{
		Stage: "build", StepIdx: 0, Skill: "apex-story-batch-create",
	})
	source := filepath.Join(dir, "sess-1.jsonl")

	// `source: startup` — the spawn. The runner's inter-step `/clear`
	// produces `source: clear` against the same claude process, which is
	// why a count of these is a count of session ROTATIONS, not spawns.
	send(ipc.HookSessionStart, map[string]any{
		"session_id": "sess-1", "transcript_path": source, "source": "startup",
	})
	send(ipc.HookUserPromptSubmit, map[string]any{
		"session_id": "sess-1", "transcript_path": source,
		"prompt": "/apex-story-batch-create --autonomous --no-commit",
	})
	send(ipc.HookPreCompact, map[string]any{
		"session_id": "sess-1", "transcript_path": source, "trigger": "auto",
	})

	// INERT: three hooks in, and the step is not done. Without this the
	// test would pass just as well against a build that completed the step
	// on any hook at all.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer waitCancel()
	require.ErrorIs(t, core.WaitStepDone(waitCtx, "build", 0), context.DeadlineExceeded,
		"a session boundary must not complete a step")

	// Stop still does, and is still the only thing that does.
	send(ipc.HookStop, map[string]any{"session_id": "sess-1", "transcript_path": source})
	doneCtx, doneCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer doneCancel()
	require.NoError(t, core.WaitStepDone(doneCtx, "build", 0))

	// DELIVERED: both rows are in the durable stream, unfiltered.
	rows := readHookEvents(t, runDir)
	require.Equal(t, "startup", rows[ipc.HookSessionStart]["source"],
		"the payload arrives whole — `source` is what separates a spawn from a /clear")
	require.Equal(t, "auto", rows[ipc.HookPreCompact]["trigger"])
	require.Contains(t, rows, ipc.HookStop, "the pre-existing events are undisturbed")
	require.Contains(t, rows, ipc.HookUserPromptSubmit)
}

// readHookEvents indexes hook-events.jsonl by event name, returning each
// event's payload.
func readHookEvents(t *testing.T, runDir string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "hook-events.jsonl"))
	require.NoError(t, err)
	out := map[string]map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var row struct {
			Event   string         `json:"event"`
			Payload map[string]any `json:"payload"`
		}
		require.NoError(t, dec.Decode(&row))
		out[row.Event] = row.Payload
	}
	return out
}

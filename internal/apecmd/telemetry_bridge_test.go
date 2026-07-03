package apecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// TestStepTelemetry_BridgeDeliveredLifecycle is THE integration guard
// the three green-but-blind suites lacked: it drives the REAL delivery
// pipeline — `ape notify` (runNotify) → TCP → IPC framing →
// BridgeRuntime dispatch → FeedHook — under the NESTED context
// (CLAUDECODE=1 on the test process, the environment every
// zero-telemetry run had) with the post-v0.0.33 lifecycle: the
// transcript persists at its normal path. Asserts non-zero per-step
// telemetry + model_usage + no telemetry_note + the durable run-dir
// transcript copy.
func TestStepTelemetry_BridgeDeliveredLifecycle(t *testing.T) {
	shortFlushGrace(t)
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "parent")

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

	// send delivers one hook envelope through the real ape-notify →
	// IPC → dispatch path and waits until FeedHook has processed it.
	send := func(event string, envelope map[string]any) {
		t.Helper()
		before := delivered.Load()
		runNotify(event, bytes.NewReader(mustJSON(t, envelope)), port)
		require.Eventually(t, func() bool { return delivered.Load() > before },
			5*time.Second, 2*time.Millisecond, "hook %s not delivered through the bridge", event)
	}

	core.OnStepStart(pipeline.InteractiveStepInfo{
		Stage: "design", StepIdx: 0, Skill: "apex-create-prd", Agent: "apex-agent-pm",
	})

	source := filepath.Join(dir, "sess-1.jsonl") // ~/.claude/projects/… stand-in

	// UPS fires before the assistant writes the transcript.
	send("UserPromptSubmit", map[string]any{
		"session_id":      "sess-1",
		"transcript_path": source,
		"prompt":          "/apex-agent-pm --autonomous -- apex-create-prd --autonomous",
	})
	// The turn progresses; tool hooks flow through (observability
	// only — no snapshot machinery in the single-path design).
	writeTranscript(t, source, "sess-1", "claude-opus-4-8", 2)
	send("PreToolUse", map[string]any{"session_id": "sess-1", "transcript_path": source, "tool_name": "Read"})
	writeTranscript(t, source, "sess-1", "claude-opus-4-8", 4)
	send("PostToolUse", map[string]any{"session_id": "sess-1", "transcript_path": source, "tool_name": "Read"})
	// Stop: turn complete; the transcript PERSISTS (top-level session
	// post-env-scrub).
	send("Stop", map[string]any{"session_id": "sess-1", "transcript_path": source})

	tele := core.StepTelemetry("design", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note, "expected clean telemetry, got note: %s", tele.Note)
	require.Equal(t, 4, tele.NumTurns)
	require.Greater(t, tele.CostUSD, 0.0)
	require.Len(t, tele.ModelUsage, 1)
	require.Equal(t, 4, tele.ModelUsage["claude-opus-4-8"].NumTurns)

	// Durable run-dir copy exists.
	copyPath := filepath.Join(runDir, "transcripts", "sess-1.jsonl")
	info, statErr := os.Stat(copyPath)
	require.NoError(t, statErr, "durable transcript copy missing")
	require.Positive(t, info.Size())
}

// TestStepTelemetry_StopEnvelopeSeedsPath: when UPS carried no
// transcript_path, the Stop envelope alone seeds the source for the
// scan.
func TestStepTelemetry_StopEnvelopeSeedsPath(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return nil })
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "st", StepIdx: 0, Skill: "apex-x"})

	source := filepath.Join(dir, "sess-9.jsonl")
	writeTranscript(t, source, "sess-9", "claude-opus-4-8", 2)

	core.FeedHook(orchestrator.HookEvent{
		Event:   "Stop",
		Payload: mustJSON(t, map[string]string{"session_id": "sess-9", "transcript_path": source}),
	})

	tele := core.StepTelemetry("st", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note)
	require.Equal(t, 2, tele.NumTurns)
}

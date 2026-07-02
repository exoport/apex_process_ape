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

// TestStepTelemetry_BridgeDeliveredLifecycle is the v0.0.31 Phase-3
// integration guard: it drives the REAL delivery pipeline — `ape
// notify` (runNotify) → TCP → IPC framing → BridgeRuntime dispatch →
// FeedHook — with the observed live lifecycle:
//
//	UPS (source absent) → Pre/PostToolUse (source present + growing)
//	→ Stop (source present) → source deleted before the deferred scan.
//
// The prior unit tests called FeedHook directly and passed while real
// runs failed with snapAttempts=0; this test exercises the layer the
// unit tests bypassed. Asserts: snapshots landed (snapAttempts > 0), a
// transcripts/ copy exists, telemetry is non-zero with model_usage,
// and no telemetry_note.
func TestStepTelemetry_BridgeDeliveredLifecycle(t *testing.T) {
	shortFlushGrace(t)
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

	source := filepath.Join(dir, "session.jsonl") // ~/.claude/projects/… stand-in

	// UPS fires BEFORE the assistant writes the transcript (real
	// lifecycle): the file does not exist yet.
	send("UserPromptSubmit", map[string]any{
		"session_id":      "sess-1",
		"transcript_path": source,
		"prompt":          "/apex-agent-pm --autonomous -- apex-create-prd --autonomous",
	})

	// The turn progresses: the file appears and grows across tool
	// hooks — the only reliable snapshot window.
	toolEnv := func(tool string) map[string]any {
		return map[string]any{
			"session_id":      "sess-1",
			"transcript_path": source,
			"hook_event_name": tool,
			"tool_name":       "Read",
			"tool_input":      map[string]any{"file_path": "/tmp/x"},
		}
	}
	writeTranscript(t, source, "sess-1", "claude-opus-4-8", 1)
	send("PreToolUse", toolEnv("PreToolUse"))
	writeTranscript(t, source, "sess-1", "claude-opus-4-8", 3)
	send("PostToolUse", toolEnv("PostToolUse"))
	writeTranscript(t, source, "sess-1", "claude-opus-4-8", 4)

	// Stop fires while the source is still present (probe-verified),
	// then the source is deleted BEFORE the deferred scan.
	send("Stop", map[string]any{"session_id": "sess-1", "transcript_path": source})
	require.NoError(t, os.Remove(source))

	core.transcriptMu.Lock()
	attempts := core.snapAttempts
	diag := core.diag
	core.transcriptMu.Unlock()
	require.Positive(t, attempts, "no snapshot landed; diagnostics: %s", diag.summary())

	snap := filepath.Join(runDir, "transcripts", "design-1-apex-create-prd.jsonl")
	info, statErr := os.Stat(snap)
	require.NoError(t, statErr, "transcripts/ snapshot missing; diagnostics: %s", diag.summary())
	require.True(t, info.Mode().IsRegular())
	require.Positive(t, info.Size())

	tele := core.StepTelemetry("design", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note, "expected clean telemetry, got note: %s", tele.Note)
	require.Equal(t, 4, tele.NumTurns, "all turns captured; diagnostics: %s", diag.summary())
	require.Greater(t, tele.CostUSD, 0.0)
	require.Len(t, tele.ModelUsage, 1)
	require.Equal(t, 4, tele.ModelUsage["claude-opus-4-8"].NumTurns)
}

// TestSyncStopCopy_StopEnvelopeSeedsPath: the guaranteed Stop-time
// copy must land even when UPS and every tool hook failed to deliver a
// parseable transcript_path — the Stop envelope alone seeds it.
func TestSyncStopCopy_StopEnvelopeSeedsPath(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	rl, err := runlog.New(runDir)
	require.NoError(t, err)
	defer rl.Close()

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return rl })
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "st", StepIdx: 0, Skill: "apex-x"})

	source := filepath.Join(dir, "session.jsonl")
	writeTranscript(t, source, "sess-1", "claude-opus-4-8", 2)

	// ONLY the Stop hook arrives with a usable envelope.
	core.FeedHook(orchestrator.HookEvent{
		Event:   "Stop",
		Payload: mustJSON(t, map[string]string{"session_id": "sess-1", "transcript_path": source}),
	})
	require.NoError(t, os.Remove(source))

	tele := core.StepTelemetry("st", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note)
	require.Equal(t, 2, tele.NumTurns)
}

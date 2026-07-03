package apecmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// mustJSON marshals v, failing the test on error.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	bs, err := json.Marshal(v)
	require.NoError(t, err)
	return bs
}

// shortFlushGrace shrinks the Stop→scan flush window for tests.
func shortFlushGrace(t *testing.T) {
	t.Helper()
	old := transcriptFlushGrace
	transcriptFlushGrace = 10 * time.Millisecond
	t.Cleanup(func() { transcriptFlushGrace = old })
}

// writeTranscript writes n assistant turns in the live claude-code
// line shape (nested cache_creation, requestId) to path.
func writeTranscript(t *testing.T, path, sessionID, model string, turns int) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	for i := range turns {
		line := fmt.Sprintf(`{"type":"assistant","uuid":"u-%s-%d","sessionId":%q,"requestId":"req_%s_%d","timestamp":"2026-07-02T12:00:0%d.000Z","message":{"id":"msg_%s_%d","type":"message","role":"assistant","model":%q,"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_1h_input_tokens":20,"ephemeral_5m_input_tokens":0}}}}`,
			sessionID, i, sessionID, sessionID, i, i, sessionID, i, model)
		_, err := fmt.Fprintln(f, line)
		require.NoError(t, err)
	}
}

// TestStepTelemetry_ScansPersistentSource pins the single-source
// design: the spawned claude is a top-level session (v0.0.33 env
// scrub), its transcript persists, and StepTelemetry scans it
// directly — non-zero aggregate + model_usage + per-session record,
// no telemetry_note — and copies it into the run dir as the durable
// artifact.
func TestStepTelemetry_ScansPersistentSource(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	rl, err := runlog.New(runDir)
	require.NoError(t, err)
	defer rl.Close()

	source := filepath.Join(dir, "sess-1.jsonl")
	writeTranscript(t, source, "sess-1", "claude-opus-4-8", 3)

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return rl })
	core.transcriptMu.Lock()
	core.activeTranscript = source
	core.activeSessionID = "sess-1"
	core.transcriptMu.Unlock()

	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note)
	require.Equal(t, 3, tele.NumTurns)
	require.Equal(t, 300, tele.TokensInput)
	require.Greater(t, tele.CostUSD, 0.0)
	require.Len(t, tele.ModelUsage, 1)
	require.Equal(t, 3, tele.ModelUsage["claude-opus-4-8"].NumTurns)
	require.Len(t, tele.Sessions, 1)
	require.Equal(t, "sess-1", tele.Sessions[0].SessionID)

	// Durable copy in the run dir (survives ~/.claude rotation).
	copyPath := filepath.Join(runDir, "transcripts", "sess-1.jsonl")
	info, statErr := os.Stat(copyPath)
	require.NoError(t, statErr, "durable transcript copy missing")
	require.True(t, info.Mode().IsRegular())
	require.Positive(t, info.Size())
}

// TestStepTelemetry_SubagentSessions: sub-agent transcripts tracked
// via SubagentStart/Stop hooks are scanned and emitted as per-session
// records, folding into the step's aggregate + per-model breakdown.
// Aggregate == sum(sessions).
func TestStepTelemetry_SubagentSessions(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()

	mainPath := filepath.Join(dir, "main.jsonl")
	writeTranscript(t, mainPath, "sess-main", "claude-opus-4-8", 2)
	subPath := filepath.Join(dir, "sub.jsonl")
	writeTranscript(t, subPath, "sess-sub", "claude-haiku-4-5", 4)

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return nil })
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "stage", StepIdx: 0, Skill: "apex-x"})

	upsPayload := mustJSON(t, map[string]string{"session_id": "sess-main", "transcript_path": mainPath, "prompt": "/apex-x --autonomous --no-commit"})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookUserPromptSubmit, Payload: upsPayload})
	subPayload := mustJSON(t, map[string]string{"session_id": "sess-sub", "transcript_path": subPath})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookSubagentStart, Payload: subPayload})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookSubagentStop, Payload: subPayload})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})

	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note)
	require.Equal(t, 6, tele.NumTurns, "2 main + 4 sub turns")
	require.Equal(t, 600, tele.TokensInput)

	require.Len(t, tele.Sessions, 2)
	require.Equal(t, "sess-main", tele.Sessions[0].SessionID)
	require.Empty(t, tele.Sessions[0].ParentSessionID)
	require.Equal(t, "sess-sub", tele.Sessions[1].SessionID)
	require.Equal(t, "sess-main", tele.Sessions[1].ParentSessionID)
	require.Equal(t, 4, tele.Sessions[1].Usage.NumTurns)

	var sumTurns, sumIn int
	for _, s := range tele.Sessions {
		sumTurns += s.Usage.NumTurns
		sumIn += s.Usage.TokensInput
	}
	require.Equal(t, tele.NumTurns, sumTurns, "aggregate equals session sum")
	require.Equal(t, tele.TokensInput, sumIn)

	require.Len(t, tele.ModelUsage, 2, "both models attributed")
	require.Equal(t, 2, tele.ModelUsage["claude-opus-4-8"].NumTurns)
	require.Equal(t, 4, tele.ModelUsage["claude-haiku-4-5"].NumTurns)
}

// TestStepTelemetry_ZeroTurnsNote: a readable transcript with zero
// assistant turns must produce the diagnosability note, not a silent
// zero.
func TestStepTelemetry_ZeroTurnsNote(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(empty, []byte(`{"type":"user","message":{}}`+"\n"), 0o600))

	core := &interactiveCore{activeTranscript: empty, activeSessionID: "sess-e"}
	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Zero(t, tele.NumTurns)
	require.Contains(t, tele.Note, "zero assistant turns")
}

// TestStepTelemetry_MissingSourceNote: a transcript that vanished
// before the scan names itself in the note — the diagnostic that
// would catch any genuine future persistence regression.
func TestStepTelemetry_MissingSourceNote(t *testing.T) {
	shortFlushGrace(t)
	core := &interactiveCore{
		activeTranscript: filepath.Join(t.TempDir(), "gone.jsonl"),
		activeSessionID:  "sess-g",
	}
	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Zero(t, tele.NumTurns)
	require.Contains(t, tele.Note, "transcript missing at scan time")
}

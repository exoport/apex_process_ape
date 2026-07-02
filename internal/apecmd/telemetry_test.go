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

// TestStepTelemetry_SnapshotFallback is the P0a regression guard for
// the live failure mode: the source session file under
// ~/.claude/projects/ is GONE by scan time, but the snapshot copied
// into the run dir survives — telemetry must be non-zero from the
// snapshot instead of silently zero.
func TestStepTelemetry_SnapshotFallback(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()

	snapshot := filepath.Join(dir, "step.jsonl")
	writeTranscript(t, snapshot, "sess-1", "claude-opus-4-8", 3)

	core := &interactiveCore{
		activeTranscript: filepath.Join(dir, "removed-source.jsonl"), // never existed
		activeSnapshot:   snapshot,
		activeSessionID:  "sess-1",
	}
	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note, "healthy fallback must not stamp a note")
	require.Equal(t, 3, tele.NumTurns)
	require.Equal(t, 300, tele.TokensInput)
	require.Greater(t, tele.CostUSD, 0.0)
	require.Len(t, tele.ModelUsage, 1)
	require.Equal(t, 3, tele.ModelUsage["claude-opus-4-8"].NumTurns)
	require.Len(t, tele.Sessions, 1)
	require.Equal(t, "sess-1", tele.Sessions[0].SessionID)
}

// TestStepTelemetry_SubagentSessions is the Imp2 guard: sub-agent
// transcripts captured via SubagentStart/Stop hooks are scanned and
// emitted as per-session records, and their usage folds into the
// step's aggregate + per-model breakdown. Aggregate == sum(sessions).
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
// zero (P0a no-silent-zero contract).
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

// TestFeedHookSnapshotsTranscript: UPS snapshots the transcript into
// the run dir as a REAL FILE (not a symlink), and the Stop hook
// refreshes it — the P0a capture contract that survives source
// rotation.
func TestFeedHookSnapshotsTranscript(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	rl, err := runlog.New(runDir)
	require.NoError(t, err)
	defer rl.Close()

	source := filepath.Join(dir, "source.jsonl")
	writeTranscript(t, source, "sess-1", "claude-opus-4-8", 1)

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return rl })
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "st", StepIdx: 0, Skill: "apex-x"})
	payload := mustJSON(t, map[string]string{"session_id": "sess-1", "transcript_path": source, "prompt": "/apex-x --autonomous --no-commit"})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookUserPromptSubmit, Payload: payload})

	snap := filepath.Join(runDir, "transcripts", "st-1-apex-x.jsonl")
	info, err := os.Lstat(snap)
	require.NoError(t, err, "snapshot must exist after UPS")
	require.True(t, info.Mode().IsRegular(), "snapshot must be a copy, not a symlink")

	// Append a turn, fire Stop → the snapshot must be refreshed.
	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"assistant","uuid":"u2","sessionId":"sess-1","message":{"id":"msg_late","model":"claude-opus-4-8","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})

	srcInfo, err := os.Stat(source)
	require.NoError(t, err)
	snapInfo, err := os.Stat(snap)
	require.NoError(t, err)
	require.Equal(t, srcInfo.Size(), snapInfo.Size(), "Stop must refresh the snapshot to the full source")

	// Delete the source: telemetry still derives from the snapshot.
	require.NoError(t, os.Remove(source))
	shortFlushGrace(t)
	tele := core.StepTelemetry("st", 0)
	require.NotNil(t, tele)
	require.Equal(t, 2, tele.NumTurns)
}

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

// subagentStopPayload builds a SubagentStop hook envelope in the REAL
// claude-code shape: transcript_path is the PARENT session (the field
// the v0.0.34 bug wrongly folded), agent_transcript_path is the sub's
// own agent-<id>.jsonl, agent_id is the only distinct per-sub id.
func subagentStopPayload(t *testing.T, parentSID, mainPath, agentID, agentPath string) []byte {
	t.Helper()
	return mustJSON(t, map[string]string{
		"session_id":            parentSID,
		"transcript_path":       mainPath,
		"agent_id":              agentID,
		"agent_transcript_path": agentPath,
	})
}

// TestStepTelemetry_SubagentSessions: sub-agent transcripts captured
// via SubagentStop's agent_transcript_path are scanned and emitted as
// per-session records, folding into the step's aggregate + per-model
// breakdown. Aggregate == sum(sessions); the sub carries the agent_id
// as its distinct session id and the parent's id as ParentSessionID.
func TestStepTelemetry_SubagentSessions(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()

	mainPath := filepath.Join(dir, "main.jsonl")
	writeTranscript(t, mainPath, "sess-main", "claude-opus-4-8", 2)
	subPath := filepath.Join(dir, "agent-a1.jsonl")
	writeTranscript(t, subPath, "sess-main", "claude-haiku-4-5", 4) // sub's internal sessionId IS the parent's

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return nil })
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "stage", StepIdx: 0, Skill: "apex-x"})

	upsPayload := mustJSON(t, map[string]string{"session_id": "sess-main", "transcript_path": mainPath, "prompt": "/apex-x --autonomous --no-commit"})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookUserPromptSubmit, Payload: upsPayload})
	// SubagentStart carries no agent_transcript_path — presence only.
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookSubagentStart, AgentID: "a1", Payload: mustJSON(t, map[string]string{"session_id": "sess-main", "agent_id": "a1"})})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookSubagentStop, AgentID: "a1", Payload: subagentStopPayload(t, "sess-main", mainPath, "a1", subPath)})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})

	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note)
	require.Equal(t, 6, tele.NumTurns, "2 main + 4 sub turns")
	require.Equal(t, 600, tele.TokensInput)

	require.Len(t, tele.Sessions, 2)
	require.Equal(t, "sess-main", tele.Sessions[0].SessionID)
	require.Empty(t, tele.Sessions[0].ParentSessionID)
	require.Equal(t, 2, tele.Sessions[0].Usage.NumTurns, "main session record is main-only, not the folded total")
	require.Equal(t, "a1", tele.Sessions[1].SessionID, "sub id is agent_id, not the parent session id")
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

// TestStepTelemetry_MultiSubagentNoDoubleCount is the regression lock
// for the v0.0.34 bug: N sub-agents whose SubagentStop envelopes all
// carry transcript_path == the MAIN transcript (real claude shape) must
// fold as main + Σ(distinct subs), NOT 2×main. Each sub is keyed by its
// distinct agent_id even though its internal sessionId equals the
// parent's.
func TestStepTelemetry_MultiSubagentNoDoubleCount(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()

	const parentSID = "323a7efe"
	mainPath := filepath.Join(dir, parentSID+".jsonl")
	const mainTurns = 5
	writeTranscript(t, mainPath, parentSID, "claude-opus-4-8", mainTurns)

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return nil })
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "stage", StepIdx: 0, Skill: "apex-story-batch-dev"})
	core.FeedHook(orchestrator.HookEvent{
		Event:   ipc.HookUserPromptSubmit,
		Payload: mustJSON(t, map[string]string{"session_id": parentSID, "transcript_path": mainPath, "prompt": "/apex-story-batch-dev --autonomous --no-commit"}),
	})

	// Six sub-agents with distinct turn counts, every SubagentStop
	// pointing transcript_path at MAIN (the bug's trigger).
	subTurns := []int{7, 3, 4, 6, 2, 8}
	wantSub := 0
	for i, n := range subTurns {
		wantSub += n
		agentID := fmt.Sprintf("a%02d", i)
		agentPath := filepath.Join(dir, "agent-"+agentID+".jsonl")
		writeTranscript(t, agentPath, parentSID, "claude-haiku-4-5", n) // internal sessionId == parent's
		core.FeedHook(orchestrator.HookEvent{
			Event:   ipc.HookSubagentStop,
			AgentID: agentID,
			Payload: subagentStopPayload(t, parentSID, mainPath, agentID, agentPath),
		})
	}
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})

	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Empty(t, tele.Note)

	wantTotal := mainTurns + wantSub
	require.Equal(t, wantTotal, tele.NumTurns, "step total == main + Σ subs")
	require.NotEqual(t, 2*mainTurns, tele.NumTurns, "the precise 2×-main bug signature must not recur")

	// sessions[] = main + one per distinct sub, each with its own id.
	require.Len(t, tele.Sessions, 1+len(subTurns))
	require.Equal(t, parentSID, tele.Sessions[0].SessionID)
	require.Equal(t, mainTurns, tele.Sessions[0].Usage.NumTurns, "main record is main-only")
	seen := map[string]bool{}
	for _, s := range tele.Sessions[1:] {
		require.NotEqual(t, parentSID, s.SessionID, "sub must not reuse the parent id")
		require.Equal(t, parentSID, s.ParentSessionID)
		require.False(t, seen[s.SessionID], "each sub folded exactly once")
		seen[s.SessionID] = true
	}

	// Aggregate equals the session sum.
	var sum int
	for _, s := range tele.Sessions {
		sum += s.Usage.NumTurns
	}
	require.Equal(t, tele.NumTurns, sum)
}

// TestStepTelemetry_DoubleCountGuard: a SubagentStop whose
// agent_transcript_path resolves to the main transcript (hook-shape
// drift) must NOT be folded — belt-and-suspenders against any future
// regression to the 2×-main signature.
func TestStepTelemetry_DoubleCountGuard(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "sess-main.jsonl")
	writeTranscript(t, mainPath, "sess-main", "claude-opus-4-8", 3)

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return nil })
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "stage", StepIdx: 0, Skill: "apex-x"})
	core.FeedHook(orchestrator.HookEvent{
		Event:   ipc.HookUserPromptSubmit,
		Payload: mustJSON(t, map[string]string{"session_id": "sess-main", "transcript_path": mainPath, "prompt": "/apex-x --autonomous --no-commit"}),
	})
	// agent_transcript_path == main (the drift case the guard defends).
	core.FeedHook(orchestrator.HookEvent{
		Event:   ipc.HookSubagentStop,
		AgentID: "a1",
		Payload: subagentStopPayload(t, "sess-main", mainPath, "a1", mainPath),
	})
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})

	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Equal(t, 3, tele.NumTurns, "main only; the phantom sub pointing at main is not folded")
	require.Len(t, tele.Sessions, 1, "no sub session recorded for the main-pointing phantom")
}

// TestStepTelemetry_SubagentSweepRecoversDroppedStop: when a
// SubagentStop is lost, the robustness sweep of the main session's
// subagents/ dir still folds the sub, so no usage is silently dropped.
func TestStepTelemetry_SubagentSweepRecoversDroppedStop(t *testing.T) {
	shortFlushGrace(t)
	dir := t.TempDir()
	const parentSID = "sess-main"
	mainPath := filepath.Join(dir, parentSID+".jsonl")
	writeTranscript(t, mainPath, parentSID, "claude-opus-4-8", 2)
	// The sub file lives at <main-without-.jsonl>/subagents/agent-*.jsonl.
	subDir := filepath.Join(dir, parentSID, "subagents")
	require.NoError(t, os.MkdirAll(subDir, 0o755))
	subPath := filepath.Join(subDir, "agent-lost.jsonl")
	writeTranscript(t, subPath, parentSID, "claude-haiku-4-5", 5)

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return nil })
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "stage", StepIdx: 0, Skill: "apex-x"})
	core.FeedHook(orchestrator.HookEvent{
		Event:   ipc.HookUserPromptSubmit,
		Payload: mustJSON(t, map[string]string{"session_id": parentSID, "transcript_path": mainPath, "prompt": "/apex-x --autonomous --no-commit"}),
	})
	// NOTE: no SubagentStop delivered — the sweep must find it.
	core.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})

	tele := core.StepTelemetry("stage", 0)
	require.NotNil(t, tele)
	require.Equal(t, 7, tele.NumTurns, "2 main + 5 swept sub turns")
	require.Len(t, tele.Sessions, 2)
	require.Equal(t, "lost", tele.Sessions[1].SessionID, "swept sub id derived from agent-<id>.jsonl")
	require.Equal(t, parentSID, tele.Sessions[1].ParentSessionID)
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

package apecmd //nolint:testpackage // exercising the unexported plainObserver for PLAN-2 / F5 verification

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// TestPlainObserver_QuietSuppressesEventStream asserts the F5 contract:
// with quiet=true, OnStepLine prints nothing; start/end markers from
// the surrounding lifecycle methods still print so failures stay
// visible in CI logs.
func TestPlainObserver_QuietSuppressesEventStream(t *testing.T) {
	var buf bytes.Buffer
	obs := newPlainObserver(&buf, "", true)
	obs.OnStageStart("alpha")
	obs.OnStepStart("alpha", 0, pipeline.Step{Skill: "apex-shard-doc"})
	// stream-json event: should be suppressed.
	obs.OnStepLine("alpha", 0, `{"type":"assistant","message":{"content":[{"type":"text","text":"Drafting"}]}}`)
	obs.OnStepLine("alpha", 0, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/abs/foo.md"}}]}}`)
	obs.OnStepEnd("alpha", 0, pipeline.Step{Skill: "apex-shard-doc"}, time.Second, "", nil)
	obs.OnStageEnd("alpha", time.Second, nil)

	out := buf.String()
	require.Contains(t, out, "stage start: alpha", "stage markers must still print under --quiet")
	require.Contains(t, out, "stage done: alpha", "stage end markers must still print under --quiet")
	require.NotContains(t, out, "Drafting", "OnStepLine text events must be suppressed under --quiet")
	require.NotContains(t, out, "🔧", "OnStepLine tool events must be suppressed under --quiet")
}

// TestPlainObserver_VerboseEmitsEvents is the control case — without
// --quiet, OnStepLine renders displayable events through the same
// event renderer that powers the TUI.
func TestPlainObserver_VerboseEmitsEvents(t *testing.T) {
	var buf bytes.Buffer
	obs := newPlainObserver(&buf, "", false)
	obs.OnStageStart("alpha")
	obs.OnStepStart("alpha", 0, pipeline.Step{Skill: "apex-shard-doc"})
	obs.OnStepLine("alpha", 0, `{"type":"assistant","message":{"content":[{"type":"text","text":"Drafting"}]}}`)
	obs.OnStepEnd("alpha", 0, pipeline.Step{Skill: "apex-shard-doc"}, time.Second, "", nil)

	out := buf.String()
	require.Contains(t, out, "Drafting", "OnStepLine must render events when quiet=false")
	require.Contains(t, out, "✎", "text-event glyph must appear when quiet=false")
}

// TestTranscriptLinkName converts the `<stage>/<idx>-<skill>` step
// label into a filesystem-safe symlink basename under transcripts/.
func TestTranscriptLinkName(t *testing.T) {
	cases := map[string]string{
		"create-prd/1-apex-create-prd":          "create-prd-1-apex-create-prd.jsonl",
		"adr-governance/3-apex-adr-adoption":    "adr-governance-3-apex-adr-adoption.jsonl",
		"a/b/c":                                 "a-b-c.jsonl", // multi-slash tolerated
	}
	for input, want := range cases {
		got := transcriptLinkName(input)
		require.Equal(t, want, got, "transcriptLinkName(%q)", input)
	}
}

// TestExtractTranscriptPath pulls transcript_path out of a Claude Code
// hook payload. Empty/malformed payloads return "".
func TestExtractTranscriptPath(t *testing.T) {
	ok := []byte(`{"session_id":"s1","transcript_path":"/home/u/.claude/projects/foo/sess.jsonl","prompt":"/x"}`)
	require.Equal(t, "/home/u/.claude/projects/foo/sess.jsonl", extractTranscriptPath(ok))

	missing := []byte(`{"session_id":"s1","prompt":"/x"}`)
	require.Equal(t, "", extractTranscriptPath(missing))

	require.Equal(t, "", extractTranscriptPath(nil))
	require.Equal(t, "", extractTranscriptPath([]byte(`{not json`)))
}

// TestInteractiveCore_StepTelemetry_DeltaFromTranscript writes a
// minimal claude session JSONL (two assistant turns), points the
// interactiveCore at it, calls StepTelemetry twice — the first call
// captures the cumulative absolute totals (delta from zero), and the
// second call after appending one more assistant turn returns the
// incremental delta.
func TestInteractiveCore_StepTelemetry_DeltaFromTranscript(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "sess.jsonl")
	turn := func(in, out int) string {
		return fmt.Sprintf(
			`{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":%d,"output_tokens":%d}}}`,
			in, out)
	}
	require.NoError(t, os.WriteFile(transcript, []byte(turn(100, 200)+"\n"+turn(50, 60)+"\n"), 0o600))

	core := &interactiveCore{}
	core.activeTranscript = transcript
	// transcriptFlushGrace (500ms) runs inline; acceptable for one test.
	t1 := core.StepTelemetry("create-prd", 0)
	require.NotNil(t, t1, "first call returns telemetry")
	require.Equal(t, 2, t1.NumTurns, "first call covers two assistant turns")
	require.Equal(t, 150, t1.TokensInput)
	require.Equal(t, 260, t1.TokensOutput)
	require.Greater(t, t1.CostUSD, 0.0, "cost positive given known model")

	// Append one more turn; second call should return the delta only.
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(turn(10, 20) + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	t2 := core.StepTelemetry("create-prd", 1)
	require.NotNil(t, t2)
	require.Equal(t, 1, t2.NumTurns, "second call delta is one new turn")
	require.Equal(t, 10, t2.TokensInput)
	require.Equal(t, 20, t2.TokensOutput)
}

// TestInteractiveCore_StepTelemetry_NoTranscript returns nil when no
// UPS has set activeTranscript yet (very first interactive step,
// pre-first-UPS edge).
func TestInteractiveCore_StepTelemetry_NoTranscript(t *testing.T) {
	core := &interactiveCore{}
	require.Nil(t, core.StepTelemetry("create-prd", 0))
}

// TestInteractiveCore_StepTelemetry_ResetsBaselineOnPathChange
// guards against the bug that produced negative per-step costs in
// multi-step interactive stages. `/clear` rotates the claude
// session_id, so each step in a multi-step stage gets its own
// transcript file. The previous step's cumulative is computed
// against a different file and is meaningless as a baseline —
// subtracting it produced negative deltas. The fix resets the
// baseline to zero when activeTranscript moves to a new path.
func TestInteractiveCore_StepTelemetry_ResetsBaselineOnPathChange(t *testing.T) {
	dir := t.TempDir()
	sess1 := filepath.Join(dir, "sess1.jsonl")
	sess2 := filepath.Join(dir, "sess2.jsonl")
	turn := func(id string, in, out int) string {
		return fmt.Sprintf(
			`{"type":"assistant","message":{"id":"%s","model":"claude-opus-4-7","usage":{"input_tokens":%d,"output_tokens":%d}}}`,
			id, in, out)
	}
	require.NoError(t, os.WriteFile(sess1, []byte(turn("m1", 100, 200)+"\n"+turn("m2", 50, 60)+"\n"), 0o600))
	require.NoError(t, os.WriteFile(sess2, []byte(turn("n1", 30, 40)+"\n"), 0o600))

	core := &interactiveCore{}

	// Step 1: scan sess1, baseline is zero. Expect 2 turns / 260 out / etc.
	core.activeTranscript = sess1
	t1 := core.StepTelemetry("pattern-governance", 0)
	require.NotNil(t, t1)
	require.Equal(t, 2, t1.NumTurns, "step 1 covers two turns")
	require.Equal(t, 260, t1.TokensOutput)

	// `/clear` rotates: step 2 sees a different transcript_path on
	// its UPS, so activeTranscript moves. The cumulative for sess1
	// must NOT be used as a baseline against sess2.
	core.activeTranscript = sess2
	t2 := core.StepTelemetry("pattern-governance", 1)
	require.NotNil(t, t2)
	require.Equal(t, 1, t2.NumTurns, "step 2 reports its own absolute totals (one turn), not sess2-sess1 (would be -1)")
	require.Equal(t, 40, t2.TokensOutput, "step 2 output = sess2's only turn (40), not sess2-sess1 (-220)")
	require.GreaterOrEqual(t, t2.CostUSD, 0.0, "no negative costs from path-change reset")
}

// TestStepTaggingObserver_TracksCurrentStep verifies the
// programmatic-web step tagger: tracker starts empty, OnStepStart
// records `<stage>/<idx>-<skill>`, OnStepEnd clears it. The child
// observer must still see every lifecycle call.
func TestStepTaggingObserver_TracksCurrentStep(t *testing.T) {
	tracker := &webHookStepTracker{}
	var buf bytes.Buffer
	child := newPlainObserver(&buf, "", true)
	obs := &stepTaggingObserver{child: child, tracker: tracker}

	require.Equal(t, "", tracker.get(), "tracker starts empty")

	obs.OnStageStart("alpha")
	obs.OnStepStart("alpha", 0, pipeline.Step{Skill: "apex-create-prd"})
	require.Equal(t, "alpha/1-apex-create-prd", tracker.get(), "OnStepStart sets the label (1-based idx)")

	obs.OnStepEnd("alpha", 0, pipeline.Step{Skill: "apex-create-prd"}, time.Second, "", nil)
	require.Equal(t, "", tracker.get(), "OnStepEnd clears the label")

	obs.OnStepStart("alpha", 1, pipeline.Step{Skill: "apex-shard-doc"})
	require.Equal(t, "alpha/2-apex-shard-doc", tracker.get(), "OnStepStart on next step relabels (1-based idx)")

	obs.OnStepEnd("alpha", 1, pipeline.Step{Skill: "apex-shard-doc"}, time.Second, "", nil)
	obs.OnStageEnd("alpha", time.Second, nil)
	require.Equal(t, "", tracker.get(), "tracker remains cleared after stage end")

	out := buf.String()
	require.Contains(t, out, "stage start: alpha")
	require.Contains(t, out, "stage done: alpha")
}

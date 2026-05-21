package apecmd //nolint:testpackage // exercising the unexported plainObserver for PLAN-2 / F5 verification

import (
	"bytes"
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

// TestStepTaggingObserver_TracksCurrentStep verifies the
// programmatic-web step tagger: tracker starts empty, OnStepStart
// records `<stage>/<skill>`, OnStepEnd clears it. The child observer
// must still see every lifecycle call.
func TestStepTaggingObserver_TracksCurrentStep(t *testing.T) {
	tracker := &webHookStepTracker{}
	var buf bytes.Buffer
	child := newPlainObserver(&buf, "", true)
	obs := &stepTaggingObserver{child: child, tracker: tracker}

	require.Equal(t, "", tracker.get(), "tracker starts empty")

	obs.OnStageStart("alpha")
	obs.OnStepStart("alpha", 0, pipeline.Step{Skill: "apex-create-prd"})
	require.Equal(t, "alpha/apex-create-prd", tracker.get(), "OnStepStart sets the label")

	obs.OnStepEnd("alpha", 0, pipeline.Step{Skill: "apex-create-prd"}, time.Second, "", nil)
	require.Equal(t, "", tracker.get(), "OnStepEnd clears the label")

	obs.OnStepStart("alpha", 1, pipeline.Step{Skill: "apex-shard-doc"})
	require.Equal(t, "alpha/apex-shard-doc", tracker.get(), "OnStepStart on next step relabels")

	obs.OnStepEnd("alpha", 1, pipeline.Step{Skill: "apex-shard-doc"}, time.Second, "", nil)
	obs.OnStageEnd("alpha", time.Second, nil)
	require.Equal(t, "", tracker.get(), "tracker remains cleared after stage end")

	out := buf.String()
	require.Contains(t, out, "stage start: alpha")
	require.Contains(t, out, "stage done: alpha")
}

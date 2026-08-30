package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestManifestModelUsageRoundTrip: telemetry with per-model + per-
// session records flows through stepTelemetryToResultEvent →
// recordStep → manifest.yaml and survives a YAML round-trip with
// non-zero values — the eval reads exactly this artifact (its PLAN-9
// C5 guard requires ≥1 step with cost_usd or num_turns > 0).
func TestManifestModelUsageRoundTrip(t *testing.T) {
	base := t.TempDir()
	mw, err := newManifestWriter(base, "task-x", "/tmp/p", "/nonexistent.yaml", "test", time.Now())
	if err != nil {
		t.Fatalf("newManifestWriter: %v", err)
	}
	stageIdx := mw.BeginStage("task-x", time.Now())

	tele := &StepTelemetry{
		CostUSD:               1.25,
		TokensInput:           100,
		TokensOutput:          200,
		TokensCacheCreation:   90,
		TokensCacheCreation5m: 40,
		TokensCacheCreation1h: 50,
		NumTurns:              6,
		ModelUsage: map[string]ModelUsage{
			"claude-opus-4-8":  {CostUSD: 1.00, TokensInput: 80, TokensOutput: 150, TokensCacheCreation: 90, TokensCacheCreation5m: 40, TokensCacheCreation1h: 50, NumTurns: 2},
			"claude-haiku-4-5": {CostUSD: 0.25, TokensInput: 20, TokensOutput: 50, NumTurns: 4},
		},
		Sessions: []SessionUsage{
			{SessionID: "sess-main", Usage: ModelUsage{CostUSD: 1.00, NumTurns: 2}},
			{SessionID: "sess-sub", ParentSessionID: "sess-main", Usage: ModelUsage{CostUSD: 0.25, NumTurns: 4}},
		},
	}
	ev := stepTelemetryToResultEvent(tele)
	recordStep(mw, stageIdx, 1, Step{Skill: "apex-x"}, "", time.Now(), time.Now(), StatusCompleted, 0, "", ev)
	if _, err := mw.Finalize(StatusCompleted, time.Now()); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(mw.runDir, "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	// Eval C5 guard shape: non-zero cost + turns on the step.
	step := m.Stages[0].Steps[0]
	if step.CostUSD <= 0 || step.NumTurns == 0 {
		t.Fatalf("step telemetry zeroed: cost=%v turns=%d", step.CostUSD, step.NumTurns)
	}
	if len(step.ModelUsage) != 2 {
		t.Fatalf("step model_usage entries = %d, want 2: %+v", len(step.ModelUsage), step.ModelUsage)
	}
	if step.ModelUsage["claude-opus-4-8"].NumTurns != 2 {
		t.Fatalf("opus turns = %d, want 2", step.ModelUsage["claude-opus-4-8"].NumTurns)
	}
	if len(step.Sessions) != 2 {
		t.Fatalf("step sessions = %d, want 2: %+v", len(step.Sessions), step.Sessions)
	}
	if step.Sessions[1].ParentSessionID != "sess-main" {
		t.Fatalf("sub session parent = %q", step.Sessions[1].ParentSessionID)
	}
	// Run-level totals fold the per-model map AND num_turns (v0.0.34
	// fix: per-step turns previously never summed into totals).
	if len(m.Totals.ModelUsage) != 2 {
		t.Fatalf("totals model_usage entries = %d, want 2", len(m.Totals.ModelUsage))
	}
	if m.Totals.ModelUsage["claude-haiku-4-5"].NumTurns != 4 {
		t.Fatalf("totals haiku turns = %d, want 4", m.Totals.ModelUsage["claude-haiku-4-5"].NumTurns)
	}
	if m.Totals.NumTurns != 6 {
		t.Fatalf("totals num_turns = %d, want 6", m.Totals.NumTurns)
	}
	// PLAN-10 D1: the ephemeral cache split flows through to both the
	// step record and the run totals, additively, with the summed
	// tokens_cache_creation staying equal to 5m + 1h.
	if step.TokensCacheCreation5m != 40 || step.TokensCacheCreation1h != 50 {
		t.Fatalf("step cache split = 5m %d / 1h %d, want 40 / 50",
			step.TokensCacheCreation5m, step.TokensCacheCreation1h)
	}
	if m.Totals.TokensCacheCreation5m != 40 || m.Totals.TokensCacheCreation1h != 50 {
		t.Fatalf("totals cache split = 5m %d / 1h %d, want 40 / 50",
			m.Totals.TokensCacheCreation5m, m.Totals.TokensCacheCreation1h)
	}
	if m.Totals.TokensCacheCreation5m+m.Totals.TokensCacheCreation1h != m.Totals.TokensCacheCreation {
		t.Fatalf("totals 5m+1h (%d) != tokens_cache_creation (%d)",
			m.Totals.TokensCacheCreation5m+m.Totals.TokensCacheCreation1h, m.Totals.TokensCacheCreation)
	}
	if opus := m.Totals.ModelUsage["claude-opus-4-8"]; opus.TokensCacheCreation5m != 40 || opus.TokensCacheCreation1h != 50 {
		t.Fatalf("totals opus cache split = 5m %d / 1h %d, want 40 / 50",
			opus.TokensCacheCreation5m, opus.TokensCacheCreation1h)
	}
}

// TestManifestTelemetryNoteRoundTrip: the no-silent-zero breadcrumb
// must land on the manifest so a zeroed step is diagnosable from the
// artifact alone.
func TestManifestTelemetryNoteRoundTrip(t *testing.T) {
	base := t.TempDir()
	mw, err := newManifestWriter(base, "task-y", "/tmp/p", "/nonexistent.yaml", "test", time.Now())
	if err != nil {
		t.Fatalf("newManifestWriter: %v", err)
	}
	stageIdx := mw.BeginStage("task-y", time.Now())
	ev := stepTelemetryToResultEvent(&StepTelemetry{Note: "transcript unavailable at scan time"})
	recordStep(mw, stageIdx, 1, Step{Skill: "apex-y"}, "", time.Now(), time.Now(), StatusCompleted, 0, "", ev)
	if _, err := mw.Finalize(StatusCompleted, time.Now()); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(mw.runDir, "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if got := m.Stages[0].Steps[0].TelemetryNote; got != "transcript unavailable at scan time" {
		t.Fatalf("telemetry_note = %q", got)
	}
}

// TestManifestContractRoundTrip: the Gate C verdict reaches the manifest,
// and only when there is one. The omitted case is the load-bearing half —
// see StepRecord.Contract for why "not-enrolled" is not written.
func TestManifestContractRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contract string
		wantKey  bool
	}{
		{"enrolled and emitted", "present", true},
		{"enrolled, no closing message", "no-transcript", true},
		{"not enrolled", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			mw, err := newManifestWriter(base, "task-c", "/tmp/p", "/nonexistent.yaml", "test", time.Now())
			if err != nil {
				t.Fatalf("newManifestWriter: %v", err)
			}
			stageIdx := mw.BeginStage("task-c", time.Now())
			ev := stepTelemetryToResultEvent(&StepTelemetry{Contract: tc.contract})
			recordStep(mw, stageIdx, 1, Step{Skill: "apex-c"}, "", time.Now(), time.Now(),
				StatusCompleted, 0, "", ev)
			if _, err := mw.Finalize(StatusCompleted, time.Now()); err != nil {
				t.Fatalf("Finalize: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(mw.runDir, "manifest.yaml"))
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			if got := strings.Contains(string(data), "contract:"); got != tc.wantKey {
				t.Fatalf("manifest carries a `contract:` key = %v, want %v\n%s", got, tc.wantKey, data)
			}
			var m Manifest
			if err := yaml.Unmarshal(data, &m); err != nil {
				t.Fatalf("parse manifest: %v", err)
			}
			if got := m.Stages[0].Steps[0].Contract; got != tc.contract {
				t.Fatalf("contract = %q, want %q", got, tc.contract)
			}
			// The addition stays additive under v2: a version bump is what
			// would break the eval's [1,2] reader range, not a new field.
			if m.SchemaVersion != 2 {
				t.Fatalf("schema_version = %d, want 2 — the contract field is ADDITIVE", m.SchemaVersion)
			}
		})
	}
}

// A manifest written before the field existed still parses, and reports
// the absence as an absence rather than as a verdict.
func TestManifestContract_PreV0060ManifestStillParses(t *testing.T) {
	const old = `schema_version: 2
ape_version: v0.0.59
status: completed
stages:
  - index: 0
    name: build
    status: completed
    steps:
      - index: 1
        skill: apex-story-batch-review
        status: completed
        exit_code: 0
`
	var m Manifest
	if err := yaml.Unmarshal([]byte(old), &m); err != nil {
		t.Fatalf("a pre-v0.0.60 manifest must still parse: %v", err)
	}
	if got := m.Stages[0].Steps[0].Contract; got != "" {
		t.Fatalf("contract = %q, want empty", got)
	}
}

// TestManifestContextWindowRoundTrip: the occupancy denominator reaches the
// manifest, and an unknown window is ABSENT rather than zero-valued.
//
// The two fields are asserted together because they legitimately disagree.
// StepRecord.ContextWindow is resolved from the effective `--model` string
// and honours a `[1m]` suffix; the model_usage entries are keyed by the
// attribution id, which has the suffix stripped by construction. On a step
// spawned with `opus[1m]` the per-model entry reports the base 200k while
// the step reports 1M — a 5x gap that is correct, and the reason the
// manifest documents which one to divide by.
func TestManifestContextWindowRoundTrip(t *testing.T) {
	base := t.TempDir()
	mw, err := newManifestWriter(base, "task-w", "/tmp/p", "/nonexistent.yaml", "test", time.Now())
	if err != nil {
		t.Fatalf("newManifestWriter: %v", err)
	}
	stageIdx := mw.BeginStage("task-w", time.Now())
	ev := stepTelemetryToResultEvent(&StepTelemetry{
		ContextWindow: 1_000_000, // spawned as opus[1m]
		ModelUsage: map[string]ModelUsage{
			"claude-opus-5":  {NumTurns: 3, ContextWindow: 200_000},
			"claude-fable-5": {NumTurns: 1}, // no known window
		},
	})
	recordStep(mw, stageIdx, 1, Step{Skill: "apex-w"}, "", time.Now(), time.Now(),
		StatusCompleted, 0, "", ev)
	if _, err := mw.Finalize(StatusCompleted, time.Now()); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(mw.runDir, "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	step := m.Stages[0].Steps[0]
	if step.ContextWindow != 1_000_000 {
		t.Fatalf("step context_window = %d, want 1000000 (the [1m] step, not the base model)",
			step.ContextWindow)
	}
	if got := step.ModelUsage["claude-opus-5"].ContextWindow; got != 200_000 {
		t.Fatalf("model_usage context_window = %d, want 200000 (attribution key drops the suffix)", got)
	}
	if got := step.ModelUsage["claude-fable-5"].ContextWindow; got != 0 {
		t.Fatalf("unknown window = %d, want 0", got)
	}
	// Absent, not `context_window: 0` — a consumer must be able to tell
	// "could not look" from a real value.
	if strings.Contains(string(data), "context_window: 0") {
		t.Fatalf("an unknown window was serialized as zero:\n%s", data)
	}
	if m.SchemaVersion != 2 {
		t.Fatalf("schema_version = %d, want 2 — the window field is ADDITIVE", m.SchemaVersion)
	}
}

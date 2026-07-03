package pipeline

import (
	"os"
	"path/filepath"
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
		CostUSD:      1.25,
		TokensInput:  100,
		TokensOutput: 200,
		NumTurns:     6,
		ModelUsage: map[string]ModelUsage{
			"claude-opus-4-8":  {CostUSD: 1.00, TokensInput: 80, TokensOutput: 150, NumTurns: 2},
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

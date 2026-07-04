//go:build !windows

package pipeline //nolint:testpackage // white-box reads internal manifestWriter side effects

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Whole file is //go:build !windows: every TestRun_* drives the passive
// bash REPL shim (see runner_commit_test.go header). PTY-only since
// v0.0.36 — telemetry comes from StepTelemetryFn (transcript scan), not
// a stream-json result event, so these tests inject canned telemetry.

// TestRun_EmitsManifest exercises the full Run -> manifestWriter ->
// Finalize path in interactive mode. Telemetry is injected via
// StepTelemetryFn (the apecmd layer's transcript-scan hook); asserts
// manifest.yaml + the per-step NDJSON event log exist with the expected
// metrics.
func TestRun_EmitsManifest(t *testing.T) {
	root := t.TempDir()
	pipelinesDir := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		t.Fatalf("mkdir pipelines: %v", err)
	}
	specBody := []byte("name: smoke\nstages:\n  only:\n    chain:\n      - skill: apex-fake\n")
	if err := os.WriteFile(filepath.Join(pipelinesDir, "smoke.yaml"), specBody, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	spec, err := LoadSpec("smoke", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	stubSpecSkills(t, root, spec)

	tele := &StepTelemetry{
		CostUSD:             0.05,
		TokensInput:         100,
		TokensOutput:        50,
		TokensCacheRead:     25,
		TokensCacheCreation: 10,
		NumTurns:            3,
	}
	err = Run(context.Background(), spec, RunOptions{
		ProjectRoot:     root,
		ClaudeBin:       claudeREPLShim(t),
		ApeVersion:      "0.0.9-test",
		NoCommit:        true, // PLAN-3 contract: manifest only, no git
		WaitStepDone:    fastStepDone,
		StepTelemetryFn: func(_ string, _ int) *StepTelemetry { return tele },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	pipelineDir := filepath.Join(root, "_output", "pipelines", "smoke")
	entries, err := os.ReadDir(pipelineDir)
	if err != nil {
		t.Fatalf("read pipeline dir: %v", err)
	}
	var runID string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "20") {
			runID = e.Name()
			break
		}
	}
	if runID == "" {
		t.Fatalf("no run_id dir found under %s", pipelineDir)
	}

	manifestPath := filepath.Join(pipelineDir, runID, "manifest.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Status != StatusCompleted {
		t.Errorf("status %q, want completed", m.Status)
	}
	if m.ApeVersion != "0.0.9-test" {
		t.Errorf("ape_version %q, want 0.0.9-test", m.ApeVersion)
	}
	if m.Totals.CostUSD != 0.05 {
		t.Errorf("totals.cost_usd %v, want 0.05", m.Totals.CostUSD)
	}
	if m.Totals.TokensInput != 100 || m.Totals.TokensOutput != 50 {
		t.Errorf("totals.tokens mismatch: %+v", m.Totals)
	}
	if m.Totals.StepsRun != 1 || m.Totals.StepsFailed != 0 {
		t.Errorf("totals.steps mismatch: %+v", m.Totals)
	}
	if len(m.Stages) != 1 || len(m.Stages[0].Steps) != 1 {
		t.Fatalf("unexpected stage/step shape: %+v", m.Stages)
	}
	step := m.Stages[0].Steps[0]
	if step.Skill != "apex-fake" || step.NumTurns != 3 {
		t.Errorf("step metrics wrong: %+v", step)
	}
	if step.EventsPath == "" {
		t.Errorf("step events_path is empty")
	}

	// Interactive mode writes step-start / step-end lifecycle events to
	// the per-step NDJSON (not a stream-json result event).
	ndjson, err := os.ReadFile(filepath.Join(pipelineDir, runID, step.EventsPath))
	if err != nil {
		t.Fatalf("read ndjson: %v", err)
	}
	if !strings.Contains(string(ndjson), "step-start") {
		t.Errorf("ndjson missing step-start event: %q", string(ndjson))
	}

	reportPath := filepath.Join(pipelineDir, runID, "pipeline-report.md")
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(report), "apex-fake") {
		t.Errorf("report missing skill name: %q", string(report))
	}

	link := filepath.Join(pipelineDir, "latest")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != runID {
		t.Errorf("latest -> %q, want %q", target, runID)
	}
	if got := ReportPathFor(root, "smoke", ""); got != reportPath {
		t.Errorf("ReportPathFor mismatch:\n  got:  %s\n  want: %s", got, reportPath)
	}
}

// TestRun_FailedStepCaptured asserts the manifest still finalizes (with
// status: failed) when a step fails. In interactive mode a step fails
// when WaitStepDone returns an error; the stage loop breaks before the
// step record is written, so the failure surfaces as a failed STAGE and
// a failed overall run (StepsFailed stays 0 — no per-step record for the
// aborted step, unlike the removed programmatic path).
func TestRun_FailedStepCaptured(t *testing.T) {
	root := t.TempDir()
	pipelinesDir := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		t.Fatalf("mkdir pipelines: %v", err)
	}
	specBody := []byte("name: bad\nstages:\n  only:\n    chain:\n      - skill: apex-fake\n")
	_ = os.WriteFile(filepath.Join(pipelinesDir, "bad.yaml"), specBody, 0o644)

	spec, _ := LoadSpec("bad", root)
	stubSpecSkills(t, root, spec)
	err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   claudeREPLShim(t),
		ApeVersion:  "0.0.9-test",
		NoCommit:    true,
		WaitStepDone: func(_ context.Context, _ string, _ int) error {
			return errors.New("simulated step failure")
		},
	})
	if err == nil {
		t.Fatalf("expected non-nil error from failing step")
	}

	entries, _ := os.ReadDir(filepath.Join(root, "_output", "pipelines", "bad"))
	var runID string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "20") {
			runID = e.Name()
			break
		}
	}
	if runID == "" {
		t.Fatalf("no run_id dir found")
	}
	data, _ := os.ReadFile(filepath.Join(root, "_output", "pipelines", "bad", runID, "manifest.yaml"))
	var m Manifest
	_ = yaml.Unmarshal(data, &m)
	if m.Status != StatusFailed {
		t.Errorf("status %q, want failed", m.Status)
	}
	if len(m.Stages) != 1 || m.Stages[0].Status != StatusFailed {
		t.Errorf("stage status not propagated: %+v", m.Stages)
	}
}

// TestRun_DisableManifestSkipsTree verifies that DisableManifest leaves
// the project tree clean.
func TestRun_DisableManifestSkipsTree(t *testing.T) {
	root := t.TempDir()
	pipelinesDir := filepath.Join(root, "_apex", "pipelines")
	_ = os.MkdirAll(pipelinesDir, 0o755)
	_ = os.WriteFile(filepath.Join(pipelinesDir, "skip.yaml"),
		[]byte("name: skip\nstages:\n  only:\n    chain:\n      - skill: apex-fake\n"), 0o644)

	spec, _ := LoadSpec("skip", root)
	stubSpecSkills(t, root, spec)
	err := Run(context.Background(), spec, RunOptions{
		ProjectRoot:     root,
		ClaudeBin:       claudeREPLShim(t),
		DisableManifest: true,
		ApeVersion:      "0.0.9-test",
		NoCommit:        true,
		WaitStepDone:    fastStepDone,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "_output")); !os.IsNotExist(err) {
		t.Errorf("expected _output to be absent, got err=%v", err)
	}
}

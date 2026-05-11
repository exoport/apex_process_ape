package pipeline //nolint:testpackage // white-box reads internal manifestWriter side effects

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRun_EmitsManifest exercises the full Run -> manifestWriter -> Finalize
// path against a claude shim that prints a canned terminal `result`
// event. Asserts manifest.yaml + the per-step NDJSON file exist with
// the expected metrics.
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

	shim := filepath.Join(t.TempDir(), "claude-shim.sh")
	shimBody := "#!/bin/sh\n" +
		"echo '{\"type\":\"system\",\"subtype\":\"init\"}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"duration_ms\":1500,\"num_turns\":3,\"total_cost_usd\":0.05,\"usage\":{\"input_tokens\":100,\"output_tokens\":50,\"cache_read_input_tokens\":25,\"cache_creation_input_tokens\":10}}'\n" +
		"exit 0\n"
	if err := os.WriteFile(shim, []byte(shimBody), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	spec, err := LoadSpec("smoke", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}

	err = Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.0.9-test",
		NoCommit:    true, // PLAN-3 contract: manifest only, no git
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Manifest tree exists.
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

	ndjson, err := os.ReadFile(filepath.Join(pipelineDir, runID, step.EventsPath))
	if err != nil {
		t.Fatalf("read ndjson: %v", err)
	}
	if !strings.Contains(string(ndjson), `"type":"result"`) {
		t.Errorf("ndjson missing result event: %q", string(ndjson))
	}

	reportPath := filepath.Join(pipelineDir, runID, "pipeline-report.md")
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(report), "apex-fake") {
		t.Errorf("report missing skill name: %q", string(report))
	}

	// latest symlink resolves to runID.
	link := filepath.Join(pipelineDir, "latest")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != runID {
		t.Errorf("latest -> %q, want %q", target, runID)
	}

	// ReportPathFor returns the same report file.
	if got := ReportPathFor(root, "smoke", ""); got != reportPath {
		t.Errorf("ReportPathFor mismatch:\n  got:  %s\n  want: %s", got, reportPath)
	}
}

// TestRun_FailedStepCaptured asserts the manifest still finalizes (with
// status: failed) when the underlying step exits non-zero.
func TestRun_FailedStepCaptured(t *testing.T) {
	root := t.TempDir()
	pipelinesDir := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		t.Fatalf("mkdir pipelines: %v", err)
	}
	specBody := []byte("name: bad\nstages:\n  only:\n    chain:\n      - skill: apex-fake\n")
	_ = os.WriteFile(filepath.Join(pipelinesDir, "bad.yaml"), specBody, 0o644)

	shim := filepath.Join(t.TempDir(), "claude-shim.sh")
	_ = os.WriteFile(shim, []byte("#!/bin/sh\necho '{\"type\":\"error\"}'\nexit 2\n"), 0o755)

	spec, _ := LoadSpec("bad", root)
	err := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   shim,
		ApeVersion:  "0.0.9-test",
		NoCommit:    true, // PLAN-3 contract
	})
	if err == nil {
		t.Fatalf("expected non-nil error from failing step")
	}

	// Find the run_id dir.
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
	if m.Totals.StepsFailed != 1 {
		t.Errorf("expected 1 failed step, got %d", m.Totals.StepsFailed)
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

	shim := filepath.Join(t.TempDir(), "claude-shim.sh")
	_ = os.WriteFile(shim, []byte("#!/bin/sh\necho done\nexit 0\n"), 0o755)

	spec, _ := LoadSpec("skip", root)
	err := Run(context.Background(), spec, RunOptions{
		ProjectRoot:     root,
		ClaudeBin:       shim,
		DisableManifest: true,
		ApeVersion:      "0.0.9-test",
		NoCommit:        true, // PLAN-3 contract
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "_output")); !os.IsNotExist(err) {
		t.Errorf("expected _output to be absent, got err=%v", err)
	}
}

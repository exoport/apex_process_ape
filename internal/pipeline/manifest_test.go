package pipeline //nolint:testpackage // white-box tests touch unexported writer + parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseResultEvent_HappyPath(t *testing.T) {
	output := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":"hello"}`,
		`{"type":"result","subtype":"success","duration_ms":760498,"num_turns":47,"total_cost_usd":1.42,"usage":{"input_tokens":84012,"output_tokens":8910,"cache_read_input_tokens":41208,"cache_creation_input_tokens":2811}}`,
		``,
	}, "\n")
	ev := parseResultEvent(output)
	if ev == nil {
		t.Fatalf("expected event, got nil")
	}
	if ev.Type != "result" || ev.Subtype != "success" {
		t.Errorf("type/subtype mismatch: %+v", ev)
	}
	if ev.TotalCostUSD != 1.42 || ev.NumTurns != 47 {
		t.Errorf("cost/turns mismatch: %+v", ev)
	}
	if ev.Usage.InputTokens != 84012 || ev.Usage.OutputTokens != 8910 {
		t.Errorf("usage mismatch: %+v", ev.Usage)
	}
	if ev.Usage.CacheReadInputTokens != 41208 || ev.Usage.CacheCreationInputTokens != 2811 {
		t.Errorf("cache mismatch: %+v", ev.Usage)
	}
}

func TestParseResultEvent_AbsentReturnsNil(t *testing.T) {
	output := `{"type":"system"}` + "\n" + `{"type":"assistant"}` + "\n"
	if ev := parseResultEvent(output); ev != nil {
		t.Errorf("expected nil, got %+v", ev)
	}
}

func TestParseResultEvent_Empty(t *testing.T) {
	if ev := parseResultEvent(""); ev != nil {
		t.Errorf("expected nil for empty input")
	}
}

func TestParseResultEvent_MalformedLineSkipped(t *testing.T) {
	// First line claims type:result but is malformed JSON; the second
	// line (the real result) wins because we scan back-to-front and
	// continue past JSON failures.
	output := strings.Join([]string{
		`{not json but mentions "result"}`,
		`{"type":"result","subtype":"success","total_cost_usd":0.5}`,
		``,
	}, "\n")
	ev := parseResultEvent(output)
	if ev == nil || ev.TotalCostUSD != 0.5 {
		t.Fatalf("expected real result, got %+v", ev)
	}
}

func TestManifestWriter_FullLifecycle(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "_apex", "pipelines", "design.yaml")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(source, []byte("name: design\nstages: {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	baseDir := filepath.Join(root, "_output", "pipelines")
	mw, err := newManifestWriter(baseDir, "design", root, source, "0.0.9-test", time.Unix(1715420730, 0))
	if err != nil {
		t.Fatalf("newManifestWriter: %v", err)
	}

	// Pre-finalize: partial manifest exists with status running.
	partial := readManifest(t, filepath.Join(mw.runDir, "manifest.yaml"))
	if partial.Status != StatusRunning {
		t.Errorf("partial status %q, want running", partial.Status)
	}
	if partial.SchemaVersion != ManifestSchemaVersion {
		t.Errorf("schema_version %d, want %d", partial.SchemaVersion, ManifestSchemaVersion)
	}
	if !strings.HasPrefix(partial.Pipeline.Digest, "sha256:") {
		t.Errorf("digest empty/wrong: %q", partial.Pipeline.Digest)
	}

	// Stage 1, step 1 — full happy-path lifecycle.
	stageStart := time.Unix(1715420731, 0)
	idx := mw.BeginStage("prd", stageStart)
	if idx != 1 {
		t.Fatalf("first stage idx %d, want 1", idx)
	}
	log, rel, err := mw.OpenStepLog(idx, 1, "prd", "apex-create-prd")
	if err != nil {
		t.Fatalf("open step log: %v", err)
	}
	if _, err := log.Write([]byte("{\"type\":\"result\"}\n")); err != nil {
		t.Fatalf("write log: %v", err)
	}
	_ = log.Close()
	if !strings.Contains(rel, "stages/01-prd/step-01-apex-create-prd.ndjson") {
		t.Errorf("rel path unexpected: %s", rel)
	}

	stepEnd := stageStart.Add(60 * time.Second)
	rec := StepRecord{
		Skill: "apex-create-prd", Agent: "apex-agent-pm",
		StartedAt: stageStart.UTC(), EndedAt: stepEnd.UTC(),
		DurationSecs: 60, Status: StatusCompleted, ExitCode: 0,
		CostUSD: 1.42, TokensInput: 84012, TokensOutput: 8910,
		EventsPath: rel,
	}
	if err := mw.RecordStep(idx, rec); err != nil {
		t.Fatalf("record step: %v", err)
	}
	if err := mw.EndStage(idx, StatusCompleted, stepEnd); err != nil {
		t.Fatalf("end stage: %v", err)
	}

	// Finalize.
	reportPath, err := mw.Finalize(StatusCompleted, stepEnd.Add(time.Second))
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Errorf("report missing: %v", err)
	}

	final := readManifest(t, filepath.Join(mw.runDir, "manifest.yaml"))
	if final.Status != StatusCompleted {
		t.Errorf("final status %q, want completed", final.Status)
	}
	if final.Totals.StepsRun != 1 || final.Totals.StepsFailed != 0 {
		t.Errorf("totals.steps mismatch: %+v", final.Totals)
	}
	if final.Totals.CostUSD != 1.42 || final.Totals.TokensInput != 84012 {
		t.Errorf("totals.metrics mismatch: %+v", final.Totals)
	}
	if len(final.Stages) != 1 || len(final.Stages[0].Steps) != 1 {
		t.Fatalf("stage/step counts wrong: %+v", final.Stages)
	}
	if final.Stages[0].Steps[0].EventsPath != rel {
		t.Errorf("events_path mismatch: got %q, want %q", final.Stages[0].Steps[0].EventsPath, rel)
	}

	// latest symlink resolves to runDir.
	link := filepath.Join(baseDir, "design", "latest")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != mw.runID {
		t.Errorf("symlink target %q, want %q", target, mw.runID)
	}
}

func TestManifestWriter_FailedStepCountsInTotals(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "_apex", "pipelines", "p.yaml")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_ = os.WriteFile(source, []byte("name: p"), 0o644)
	mw, err := newManifestWriter(filepath.Join(root, "_output", "pipelines"), "p", root, source, "0.0.9-test", time.Now())
	if err != nil {
		t.Fatalf("newManifestWriter: %v", err)
	}
	idx := mw.BeginStage("only", time.Now())
	_ = mw.RecordStep(idx, StepRecord{Skill: "s", Status: StatusFailed, ExitCode: 1})
	_, _ = mw.Finalize(StatusFailed, time.Now())
	final := readManifest(t, filepath.Join(mw.runDir, "manifest.yaml"))
	if final.Totals.StepsFailed != 1 || final.Totals.StepsRun != 1 {
		t.Errorf("expected 1 run / 1 failed, got %+v", final.Totals)
	}
}

func TestComputeRunID_StableFormat(t *testing.T) {
	at := time.Date(2026, 5, 11, 9, 45, 30, 0, time.UTC)
	id := computeRunID(at, "design", "/tmp/foo")
	if !strings.HasPrefix(id, "20260511-094530-") {
		t.Errorf("run_id prefix wrong: %s", id)
	}
	if len(strings.Split(id, "-")) != 3 {
		t.Errorf("expected three dash-segments, got %s", id)
	}
}

func TestSanitizeFsName(t *testing.T) {
	cases := map[string]string{
		"apex-create-prd": "apex-create-prd",
		"foo/bar":         "foo_bar",
		"":                "unnamed",
		"weird name!":     "weird_name_",
	}
	for in, want := range cases {
		if got := sanitizeFsName(in); got != want {
			t.Errorf("sanitize %q: got %q, want %q", in, got, want)
		}
	}
}

func readManifest(t *testing.T, path string) Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return m
}

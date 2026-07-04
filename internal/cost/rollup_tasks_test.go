package cost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRebuildRollupIncludesTasks verifies the PLAN-11 task tree
// (_output/tasks/<skill>/<run-id>/manifest.yaml) folds into the
// rollup's Tasks bucket alongside pipelines.
func TestRebuildRollupIncludesTasks(t *testing.T) {
	root := t.TempDir()

	writeManifest := func(base, name, runID, yaml string) {
		dir := filepath.Join(root, "_output", base, name, runID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeManifest("tasks", "apex-shard-doc", "20260702-120000-abc1234", `
run_id: 20260702-120000-abc1234
started_at: 2026-07-02T12:00:00Z
totals:
  cost_usd: 0.42
  tokens_input: 100
  tokens_output: 200
  tokens_cache_read: 300
  tokens_cache_creation: 400
`)
	writeManifest("pipelines", "design", "20260702-130000-def5678", `
run_id: 20260702-130000-def5678
started_at: 2026-07-02T13:00:00Z
totals:
  cost_usd: 1.00
  tokens_input: 10
  tokens_output: 20
  tokens_cache_read: 30
  tokens_cache_creation: 40
`)

	r, err := RebuildRollup(root)
	if err != nil {
		t.Fatalf("RebuildRollup: %v", err)
	}

	tb, ok := r.Tasks["apex-shard-doc"]
	if !ok {
		t.Fatalf("expected Tasks bucket for apex-shard-doc, got %+v", r.Tasks)
	}
	if tb.Totals.CostUSD != 0.42 || tb.Totals.InputTokens != 100 {
		t.Fatalf("task totals wrong: %+v", tb.Totals)
	}
	if _, ok := tb.Runs["20260702-120000-abc1234"]; !ok {
		t.Fatalf("task run id missing: %+v", tb.Runs)
	}
	if _, ok := r.Pipelines["design"]; !ok {
		t.Fatalf("pipeline bucket must still fold: %+v", r.Pipelines)
	}
	day := r.ByDay["2026-07-02"]
	if day.CostUSD != 1.42 {
		t.Fatalf("by-day must sum pipeline + task: got %v", day.CostUSD)
	}

	// Round-trip through LoadRollup preserves the Tasks bucket.
	loaded, err := LoadRollup(root)
	if err != nil {
		t.Fatalf("LoadRollup: %v", err)
	}
	if _, ok := loaded.Tasks["apex-shard-doc"]; !ok {
		t.Fatalf("Tasks bucket lost on round-trip")
	}
}

// TestFoldTaskRun pins the incremental fold used by future callers.
func TestFoldTaskRun(t *testing.T) {
	r := &Rollup{}
	day := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	r.FoldTaskRun("apex-create-prd", "run-1", day,
		Totals{CostUSD: 0.5, InputTokens: 1, NumTurns: 2},
		map[string]Totals{"claude-opus-4-8": {CostUSD: 0.5, InputTokens: 1, NumTurns: 2}})
	r.FoldTaskRun("apex-create-prd", "run-2", day,
		Totals{CostUSD: 0.25, InputTokens: 2, NumTurns: 1},
		map[string]Totals{"claude-opus-4-8": {CostUSD: 0.25, InputTokens: 2, NumTurns: 1}})

	b := r.Tasks["apex-create-prd"]
	if len(b.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(b.Runs))
	}
	if b.Totals.CostUSD != 0.75 || b.Totals.InputTokens != 3 {
		t.Fatalf("fold totals wrong: %+v", b.Totals)
	}
	if b.Totals.NumTurns != 3 {
		t.Fatalf("fold totals num_turns wrong: got %d, want 3", b.Totals.NumTurns)
	}
	if r.ByDay["2026-07-02"].CostUSD != 0.75 {
		t.Fatalf("by-day fold wrong: %+v", r.ByDay)
	}
	// PLAN-10 D5: per-model breakdown accumulates on the bucket and
	// project-wide.
	if got := b.PerModel["claude-opus-4-8"]; got.CostUSD != 0.75 || got.NumTurns != 3 {
		t.Fatalf("bucket per-model wrong: %+v", got)
	}
	if got := r.PerModel["claude-opus-4-8"]; got.CostUSD != 0.75 || got.NumTurns != 3 {
		t.Fatalf("rollup per-model wrong: %+v", got)
	}
}

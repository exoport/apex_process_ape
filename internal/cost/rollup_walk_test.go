package cost

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile mkdirs and writes content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildRollup_WalksPipelinesAndChats(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, "_output/pipelines/design/20260517-101010-aaa1111/manifest.yaml"),
		`schema_version: 2
run_id: 20260517-101010-aaa1111
started_at: 2026-05-17T10:10:10Z
totals:
  cost_usd: 1.25
  tokens_input: 10000
  tokens_output: 2000
`)
	writeFile(t, filepath.Join(root, "_output/pipelines/design/20260517-110000-bbb2222/manifest.yaml"),
		`schema_version: 2
run_id: 20260517-110000-bbb2222
started_at: 2026-05-17T11:00:00Z
totals:
  cost_usd: 0.75
  tokens_input: 5000
  tokens_output: 1000
`)
	writeFile(t, filepath.Join(root, "_output/ape/chats/20260517-120000-ccc3333/session.yaml"),
		`chat_id: 20260517-120000-ccc3333
started_at: 2026-05-17T12:00:00Z
cost_usd: 0.50
tokens_input: 1000
tokens_output: 500
`)

	r, err := RebuildRollup(root)
	if err != nil {
		t.Fatalf("RebuildRollup: %v", err)
	}

	if got := r.Pipelines["design"].Totals.CostUSD; got < 1.99 || got > 2.01 {
		t.Errorf("design totals = $%.2f, want ~$2.00 (1.25 + 0.75)", got)
	}
	if got := len(r.Pipelines["design"].Runs); got != 2 {
		t.Errorf("design runs = %d, want 2", got)
	}
	if got := r.Chats.Totals.CostUSD; got < 0.49 || got > 0.51 {
		t.Errorf("chats totals = $%.2f, want ~$0.50", got)
	}
	if got := len(r.ByDay); got != 1 {
		t.Errorf("byDay = %d, want 1 (all three on 2026-05-17)", got)
	}
	if got := r.ByDay["2026-05-17"].CostUSD; got < 2.49 || got > 2.51 {
		t.Errorf("byDay totals = $%.2f, want ~$2.50", got)
	}
}

func TestRebuildRollup_EmptyProjectIsNoError(t *testing.T) {
	root := t.TempDir()
	r, err := RebuildRollup(root)
	if err != nil {
		t.Fatalf("RebuildRollup on empty dir: %v", err)
	}
	if r.Pipelines == nil || r.Chats.Runs == nil {
		t.Errorf("rollup maps should be non-nil even when empty: %+v", r)
	}
}

func TestRebuildRollup_TolerantOfMalformedManifest(t *testing.T) {
	root := t.TempDir()
	// Garbage manifest — must skip, not abort.
	writeFile(t, filepath.Join(root, "_output/pipelines/design/bad-run/manifest.yaml"),
		"this is not yaml at all: { broken")
	// Valid manifest alongside.
	writeFile(t, filepath.Join(root, "_output/pipelines/design/20260517-101010-aaa1111/manifest.yaml"),
		`schema_version: 2
run_id: 20260517-101010-aaa1111
started_at: 2026-05-17T10:10:10Z
totals:
  cost_usd: 1.00
`)
	r, err := RebuildRollup(root)
	if err != nil {
		t.Fatalf("RebuildRollup: %v", err)
	}
	if got := len(r.Pipelines["design"].Runs); got != 1 {
		t.Errorf("expected 1 run (malformed skipped), got %d", got)
	}
}

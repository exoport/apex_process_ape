package cost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanSessionJSONL_Aggregates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := `{"type":"user","message":{"content":"hi"}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1000000,"output_tokens":0}}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":0,"output_tokens":500000}}}
{"type":"system","message":{"content":"ignored"}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	totals, model, err := ScanSessionJSONL(path)
	if err != nil {
		t.Fatalf("ScanSessionJSONL: %v", err)
	}
	if model != "claude-opus-4-7" {
		t.Errorf("model = %q, want claude-opus-4-7", model)
	}
	// 1M input × $5 + 0.5M output × $25 = 5 + 12.50 = $17.50
	if totals.CostUSD < 17.4 || totals.CostUSD > 17.6 {
		t.Errorf("cost = $%.2f, want ~$17.50", totals.CostUSD)
	}
	if totals.InputTokens != 1_000_000 {
		t.Errorf("input tokens = %d, want 1M", totals.InputTokens)
	}
	if totals.OutputTokens != 500_000 {
		t.Errorf("output tokens = %d, want 500k", totals.OutputTokens)
	}
}

func TestFindSessionJSONL_PicksNewestAfterSince(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(filepath.Join(projects, "-tmp-foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projects, "-tmp-bar"), 0o755); err != nil {
		t.Fatal(err)
	}

	old := filepath.Join(projects, "-tmp-foo", "old.jsonl")
	if err := os.WriteFile(old, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate the "old" file by 1 hour.
	pastMtime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(old, pastMtime, pastMtime); err != nil {
		t.Fatal(err)
	}

	newer := filepath.Join(projects, "-tmp-bar", "newer.jsonl")
	if err := os.WriteFile(newer, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// since=now-30m: only "newer" should qualify.
	since := time.Now().Add(-30 * time.Minute)
	got, err := FindSessionJSONL(home, since)
	if err != nil {
		t.Fatalf("FindSessionJSONL: %v", err)
	}
	if got != newer {
		t.Errorf("got %q, want %q", got, newer)
	}

	// since=2h-ago: both qualify; newest wins ("newer").
	got2, err := FindSessionJSONL(home, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("FindSessionJSONL: %v", err)
	}
	if got2 != newer {
		t.Errorf("got %q, want %q (newest of two)", got2, newer)
	}
}

func TestFindSessionJSONL_NoMatchReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	got, err := FindSessionJSONL(home, time.Now())
	if err != nil {
		t.Fatalf("FindSessionJSONL: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

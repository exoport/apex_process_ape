package cost

import (
	"os"
	"path/filepath"
	"testing"
)

// testScanModel is a priced model, so a scan that finds turns also finds a
// non-zero cost — which is what distinguishes "scanned nothing" from "scanned
// something the price table has no rate for".
const testScanModel = "claude-sonnet-4-5-20250929"

// writeTranscript writes a one-turn assistant transcript at path.
func writeTranscript(t *testing.T, path string, out int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"2026-08-01T10:00:00Z","sessionId":"s","message":` +
		`{"id":"` + filepath.Base(path) + `","model":"` + testScanModel + `","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":100,"output_tokens":` + itoa(out) + `}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestScanHomeMatchesTheSandboxHomeLayout is the D3 verification, pinned as a
// test: a sandbox workspace's staging dir IS the guest $HOME, so the transcripts
// it accumulates sit at <home>/.claude/projects/<slug>/<sid>.jsonl — the same
// layout a local run produces. If Claude Code ever moved them, this fails here
// rather than silently reporting every workspace as costing nothing.
func TestScanHomeMatchesTheSandboxHomeLayout(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "-workspace-app")
	writeTranscript(t, filepath.Join(proj, "sess-a.jsonl"), 200)
	writeTranscript(t, filepath.Join(proj, "sess-b.jsonl"), 300)
	// A sub-agent transcript of the first session.
	writeTranscript(t, filepath.Join(proj, "sess-a", "subagents", "agent-x1.jsonl"), 50)

	got := ScanHome(home)
	if got.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2 main transcripts", got.Sessions)
	}
	if got.Files != 3 {
		t.Errorf("Files = %d, want 3 (2 main + 1 sub-agent)", got.Files)
	}
	if got.Totals.OutputTokens != 550 {
		t.Errorf("OutputTokens = %d, want 550 (200+300+50)", got.Totals.OutputTokens)
	}
	if got.Totals.NumTurns != 3 {
		t.Errorf("NumTurns = %d, want 3", got.Totals.NumTurns)
	}
	if got.Totals.CostUSD <= 0 {
		t.Error("CostUSD = 0 for a priced model — the rollup would report free work")
	}
}

func TestScanHomeEmptyHomeIsNotAnError(t *testing.T) {
	// A workspace nobody has run Claude in costs nothing. That is a fact, not a
	// failure, and it must not make the whole node's report fail.
	got := ScanHome(t.TempDir())
	if got.Sessions != 0 || got.Files != 0 || got.Totals.CostUSD != 0 {
		t.Errorf("empty home = %+v, want a zero rollup", got)
	}
}

func TestHomeSessionsDedupesAndOrders(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "-workspace-app")
	writeTranscript(t, filepath.Join(proj, "b.jsonl"), 1)
	writeTranscript(t, filepath.Join(proj, "a.jsonl"), 1)

	files := HomeSessions(home)
	if len(files) != 2 {
		t.Fatalf("HomeSessions = %d files, want 2", len(files))
	}
	if filepath.Base(files[0].Path) != "a.jsonl" {
		t.Errorf("first = %s, want the path-sorted a.jsonl", files[0].Path)
	}
	for _, f := range files {
		if f.Kind != SessionMain {
			t.Errorf("%s classified %v, want main", f.Path, f.Kind)
		}
	}
}

func TestHomeSessionsNoHome(t *testing.T) {
	if got := HomeSessions(""); got != nil {
		t.Errorf("HomeSessions(\"\") = %v, want nil", got)
	}
}

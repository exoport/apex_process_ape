package cost_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"assistant"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionFiles(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sess-main.jsonl")
	subDir := filepath.Join(dir, "sess-main", "subagents")
	subA := filepath.Join(subDir, "agent-aaa.jsonl")
	subB := filepath.Join(subDir, "agent-bbb.jsonl")
	writeFile(t, main)
	writeFile(t, subA)
	writeFile(t, subB)

	got := cost.SessionFiles(main, time.Time{})
	if len(got) != 3 {
		t.Fatalf("want 3 files (main + 2 subs), got %d: %+v", len(got), got)
	}
	if got[0].Kind != cost.SessionMain || got[0].SessionID != "sess-main" {
		t.Errorf("first entry should be the main session, got %+v", got[0])
	}
	// Subagents are path-sorted after the main; ids come from the filenames.
	if got[1].Kind != cost.SessionSubagent || got[1].SessionID != "aaa" {
		t.Errorf("second entry = %+v, want subagent aaa", got[1])
	}
	if got[2].SessionID != "bbb" {
		t.Errorf("third entry = %+v, want subagent bbb", got[2])
	}
}

func TestSessionFiles_MtimeFloorExcludesOldSubs(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	old := filepath.Join(dir, "s", "subagents", "agent-old.jsonl")
	writeFile(t, main)
	writeFile(t, old)
	// Backdate the sub well before the floor.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	got := cost.SessionFiles(main, time.Now())
	if len(got) != 1 || got[0].Kind != cost.SessionMain {
		t.Fatalf("mtime floor should exclude the old sub, got %+v", got)
	}
}

func TestSessionFiles_EmptyMain(t *testing.T) {
	if got := cost.SessionFiles("", time.Time{}); got != nil {
		t.Fatalf("empty main should yield nil, got %+v", got)
	}
}

func TestAgentIDFromTranscript(t *testing.T) {
	if got := cost.AgentIDFromTranscript("/x/y/agent-1234.jsonl"); got != "1234" {
		t.Errorf("AgentIDFromTranscript = %q, want 1234", got)
	}
}

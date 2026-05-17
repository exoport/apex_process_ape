package runlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriter_AllThreeStreamsAndTranscript(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	ts := time.Date(2026, 5, 17, 10, 23, 0, 0, time.UTC)

	if err := w.Hook(HookEntry{
		Timestamp: ts,
		Event:     "PreToolUse",
		SessionID: "sess-1",
		Payload:   json.RawMessage(`{"tool":"Bash"}`),
	}); err != nil {
		t.Fatalf("Hook: %v", err)
	}
	if err := w.Call(CallEntry{
		Timestamp: ts,
		Method:    "tools/call",
		Tool:      "reply",
		Params:    json.RawMessage(`{"content":"hi"}`),
		Result:    json.RawMessage(`{"sent":true}`),
	}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if err := w.Checkpoint(CheckpointEntry{
		Timestamp: ts,
		Kind:      "stage-start",
		Step:      "design/architecture",
	}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// Link a transcript.
	target := filepath.Join(t.TempDir(), "fake-claude.jsonl")
	if err := os.WriteFile(target, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.LinkTranscript("step-01-architecture.jsonl", target); err != nil {
		t.Fatalf("LinkTranscript: %v", err)
	}

	w.Close()

	// hook-events.jsonl
	hookLine := readFirstLine(t, filepath.Join(dir, "hook-events.jsonl"))
	if !strings.Contains(hookLine, `"event":"PreToolUse"`) {
		t.Errorf("hook line missing event: %s", hookLine)
	}
	if !strings.Contains(hookLine, `"step":null`) {
		t.Errorf("empty Step should serialise as null: %s", hookLine)
	}

	// bridge-calls.jsonl
	callLine := readFirstLine(t, filepath.Join(dir, "bridge-calls.jsonl"))
	if !strings.Contains(callLine, `"tool":"reply"`) {
		t.Errorf("call line missing tool: %s", callLine)
	}

	// checkpoints.jsonl
	chkLine := readFirstLine(t, filepath.Join(dir, "checkpoints.jsonl"))
	if !strings.Contains(chkLine, `"kind":"stage-start"`) {
		t.Errorf("checkpoint line missing kind: %s", chkLine)
	}

	// Transcript symlink points to target.
	link, err := os.Readlink(filepath.Join(dir, "transcripts", "step-01-architecture.jsonl"))
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if link != target {
		t.Errorf("symlink target = %q, want %q", link, target)
	}
}

func TestEnsureNoCollision_FailsLoud(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureNoCollision(dir); err == nil {
		t.Fatal("expected collision error for existing dir")
	}
	if err := EnsureNoCollision(filepath.Join(dir, "nope")); err != nil {
		t.Errorf("non-existent path should not error: %v", err)
	}
}

func TestNewChatID_Shape(t *testing.T) {
	id := NewChatID(time.Date(2026, 5, 17, 10, 23, 0, 0, time.UTC), "/home/diegos/foo", 12345)
	if !strings.HasPrefix(id, "20260517-102300-") {
		t.Errorf("chat id prefix wrong: %s", id)
	}
	if len(id) != len("20060102-150405-")+7 {
		t.Errorf("chat id length = %d, want %d", len(id), len("20060102-150405-")+7)
	}
	// Different cwd → different id.
	id2 := NewChatID(time.Date(2026, 5, 17, 10, 23, 0, 0, time.UTC), "/home/diegos/bar", 12345)
	if id == id2 {
		t.Error("different cwd should produce different chat ids")
	}
}

func TestEnsureGitignore_AlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	gitignore := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignore, []byte("_output/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := EnsureGitignore(dir, nil, nil)
	if err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	if !ok {
		t.Error("expected ok=true when _output/ already present")
	}
}

func TestEnsureGitignore_NonTTYWarns(t *testing.T) {
	dir := t.TempDir()
	var warned strings.Builder
	ok, err := EnsureGitignore(dir, nil, &warned)
	if err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	if ok {
		t.Error("non-TTY path should not modify .gitignore")
	}
	if !strings.Contains(warned.String(), "_output/") {
		t.Errorf("expected warning mentioning _output/, got %q", warned.String())
	}
}

func TestEnsureGitignore_TTYAccept(t *testing.T) {
	dir := t.TempDir()
	ask := func(_ string) bool { return true }
	ok, err := EnsureGitignore(dir, ask, nil)
	if err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	if !ok {
		t.Error("expected ok=true after accept")
	}
	bs, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), "_output/") {
		t.Errorf(".gitignore did not get _output/ appended: %s", string(bs))
	}
}

func readFirstLine(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatalf("%s is empty", path)
	}
	return sc.Text()
}

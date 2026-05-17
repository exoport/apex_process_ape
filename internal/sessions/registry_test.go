package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tmpRegistry(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "registry.json")
}

func TestRegister_AppendsRow(t *testing.T) {
	path := tmpRegistry(t)
	s := Session{
		PID:       os.Getpid(),
		CWD:       "/home/diegos/foo",
		Command:   "ape pipeline design",
		Port:      47291,
		URL:       "http://127.0.0.1:47291/",
		StartedAt: time.Now().UTC(),
	}
	if err := Register(path, s); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rows, err := List(path)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len=%d, want 1", len(rows))
	}
	if rows[0].URL != s.URL {
		t.Errorf("URL = %q, want %q", rows[0].URL, s.URL)
	}
}

func TestDeregister_RemovesByPID(t *testing.T) {
	path := tmpRegistry(t)
	for _, pid := range []int{100, 200, 300} {
		_ = Register(path, Session{PID: pid, URL: "http://127.0.0.1:0/"})
	}
	if err := Deregister(path, 200); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	rows, _ := List(path)
	if len(rows) != 2 {
		t.Fatalf("len=%d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.PID == 200 {
			t.Error("PID=200 should have been removed")
		}
	}
}

func TestPrune_DropsDeadPIDs(t *testing.T) {
	path := tmpRegistry(t)
	// Live PID: this process.
	if err := Register(path, Session{PID: os.Getpid(), URL: "live"}); err != nil {
		t.Fatal(err)
	}
	// Dead PID: an extremely-unlikely-to-be-allocated value.
	if err := Register(path, Session{PID: 0x7FFFFFFE, URL: "dead"}); err != nil {
		t.Fatal(err)
	}
	alive, err := Prune(path)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(alive) != 1 {
		t.Fatalf("after prune len=%d, want 1: %+v", len(alive), alive)
	}
	if alive[0].URL != "live" {
		t.Errorf("survivor URL = %q, want 'live'", alive[0].URL)
	}
}

func TestList_MissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	rows, err := List(path)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected empty, got %+v", rows)
	}
}

func TestRegister_CorruptFileStartsFresh(t *testing.T) {
	path := tmpRegistry(t)
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Register(path, Session{PID: os.Getpid(), URL: "x"}); err != nil {
		t.Fatalf("Register against corrupt file: %v", err)
	}
	rows, _ := List(path)
	if len(rows) != 1 {
		t.Fatalf("len=%d, want 1 (corrupt file should have been replaced)", len(rows))
	}
}

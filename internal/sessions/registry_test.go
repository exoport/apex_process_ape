package sessions

import (
	"os"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS == "windows" {
		// Windows process IDs are recycled aggressively, and our
		// pidAlive (lock_windows.go) uses Process.Signal(nil) which
		// can return false-positive "alive" for a recycled PID slot.
		// Reliable Windows process-existence checks require
		// OpenProcess + GetExitCodeProcess via golang.org/x/sys/windows;
		// deferred until someone actually relies on prune on Windows.
		t.Skip("Windows pidAlive cannot reliably detect dead PIDs; skipping")
	}
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

// D620 (FINDING-C3-6): a row whose pid now belongs to a process that
// started after the row was written is a reused pid: Alive and Prune drop
// it. A row recorded with its process's start keeps only that process.
func TestAliveAndPrune_DropAReusedPid(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("start times are read from /proc")
	}
	path := tmpRegistry(t)
	self := os.Getpid()
	old := time.Date(2026, 10, 20, 19, 25, 27, 0, time.UTC)
	rows := []Session{
		{PID: self, RunID: "now", StartedAt: time.Now().UTC()},                 // this process wrote it: kept
		{PID: self, RunID: "stale-v13", StartedAt: old, PIDStart: time.Time{}}, // an older ape's row, its pid reused since
		{PID: self, RunID: "stale-v14", StartedAt: old, PIDStart: old},         // a v1.4 row of the process that had this pid
	}
	for _, r := range rows {
		if err := Register(path, r); err != nil {
			t.Fatal(err)
		}
	}
	// Register filled PIDStart for the rows that left it zero; undo it for
	// the older ape's row, which recorded none.
	all, _ := List(path)
	all[1].PIDStart = time.Time{}
	if err := withLockedFile(path, func(f *os.File) error { return writeBack(f, registryShape{Sessions: all}) }); err != nil {
		t.Fatal(err)
	}

	runs := func(ss []Session) []string {
		out := make([]string, 0, len(ss))
		for _, s := range ss {
			out = append(out, s.RunID)
		}
		return out
	}
	alive, err := Alive(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := runs(alive); len(got) != 1 || got[0] != "now" {
		t.Fatalf("Alive = %v, want [now]", got)
	}
	pruned, err := Prune(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := runs(pruned); len(got) != 1 || got[0] != "now" {
		t.Fatalf("Prune kept %v, want [now]", got)
	}
	if !pruned[0].PIDStart.IsZero() && !pruned[0].Running() {
		t.Fatal("the kept row is this process's")
	}
}

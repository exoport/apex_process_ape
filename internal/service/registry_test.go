package service

import (
	"testing"
	"time"
)

func TestRegistryLifecycle(t *testing.T) {
	r := NewRegistry()
	r.Add(JobInfo{JobID: "j1", Kind: KindTask, ExclusivityKey: "", Exclusive: true})
	r.SetProcess("j1", 4242, "/p/_output/ape/service/j1.log")

	got, ok := r.Get("j1")
	if !ok {
		t.Fatal("j1 not found")
	}
	if got.State != StateRunning || got.PID != 4242 || got.LogPath == "" {
		t.Fatalf("got %+v, want running pid 4242 with log path", got)
	}
	if r.RunningCount() != 1 {
		t.Fatalf("RunningCount = %d, want 1", r.RunningCount())
	}

	// Add forces state to running even if the caller passed something else.
	r.Add(JobInfo{JobID: "j2", Kind: KindPipeline, State: StateDone})
	if g, _ := r.Get("j2"); g.State != StateRunning {
		t.Fatalf("Add must force running, got %q", g.State)
	}
	if r.RunningCount() != 2 {
		t.Fatalf("RunningCount = %d, want 2", r.RunningCount())
	}
}

func TestRegistryFinishStates(t *testing.T) {
	tests := []struct {
		name          string
		stopRequested bool
		exitCode      int
		wantState     string
	}{
		{"clean exit → done", false, 0, StateDone},
		{"nonzero exit → failed", false, 1, StateFailed},
		{"operator stop → stopped", true, 143, StateStopped},
		{"operator stop even on clean exit → stopped", true, 0, StateStopped},
	}
	started := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			r.Add(JobInfo{JobID: "j", Kind: KindTask, PID: 100, StartedAt: started})
			if tc.stopRequested {
				pid, ok := r.RequestStop("j")
				if !ok || pid != 100 {
					t.Fatalf("RequestStop = (%d,%v), want (100,true)", pid, ok)
				}
			}
			r.Finish("j", tc.exitCode, finished)
			got, _ := r.Get("j")
			if got.State != tc.wantState {
				t.Fatalf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.ExitCode == nil || *got.ExitCode != tc.exitCode {
				t.Fatalf("exit code = %v, want %d", got.ExitCode, tc.exitCode)
			}
			// Finish stamps LastEventAt atomically with the terminal state and
			// leaves StartedAt untouched.
			if !got.LastEventAt.Equal(finished) {
				t.Fatalf("LastEventAt = %v, want %v (job-end time)", got.LastEventAt, finished)
			}
			if !got.StartedAt.Equal(started) {
				t.Fatalf("Finish changed StartedAt to %v, want %v", got.StartedAt, started)
			}
			if r.RunningCount() != 0 {
				t.Fatalf("RunningCount = %d, want 0", r.RunningCount())
			}
		})
	}
}

func TestRegistryRequestStopGuards(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.RequestStop("missing"); ok {
		t.Fatal("RequestStop on unknown job should be false")
	}
	r.Add(JobInfo{JobID: "j", PID: 5})
	r.Finish("j", 0, time.Now().UTC()) // now terminal
	if _, ok := r.RequestStop("j"); ok {
		t.Fatal("RequestStop on a terminal job should be false")
	}
}

func TestRegistryLastEventAt(t *testing.T) {
	r := NewRegistry()
	started := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	r.Add(JobInfo{JobID: "j1", Kind: KindTask, StartedAt: started})

	// Add seeds LastEventAt to StartedAt: a just-accepted job's last
	// lifecycle event is its acceptance.
	if got, _ := r.Get("j1"); !got.LastEventAt.Equal(started) {
		t.Fatalf("Add: LastEventAt = %v, want %v (== StartedAt)", got.LastEventAt, started)
	}

	// Finish on an unknown id is a no-op (no panic, no phantom entry).
	r.Finish("missing", 0, started.Add(30*time.Second))
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Finish must not create an entry for an unknown id")
	}
}

func TestRegistryListSorted(t *testing.T) {
	r := NewRegistry()
	r.Add(JobInfo{JobID: "20260709-090000-bbbbbbb"})
	r.Add(JobInfo{JobID: "20260709-080000-aaaaaaa"})
	list := r.List()
	if len(list) != 2 || list[0].JobID > list[1].JobID {
		t.Fatalf("List not sorted ascending: %+v", list)
	}
}

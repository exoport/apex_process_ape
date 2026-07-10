package service

import "testing"

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
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			r.Add(JobInfo{JobID: "j", Kind: KindTask, PID: 100})
			if tc.stopRequested {
				pid, ok := r.RequestStop("j")
				if !ok || pid != 100 {
					t.Fatalf("RequestStop = (%d,%v), want (100,true)", pid, ok)
				}
			}
			r.Finish("j", tc.exitCode)
			got, _ := r.Get("j")
			if got.State != tc.wantState {
				t.Fatalf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.ExitCode == nil || *got.ExitCode != tc.exitCode {
				t.Fatalf("exit code = %v, want %d", got.ExitCode, tc.exitCode)
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
	r.Finish("j", 0) // now terminal
	if _, ok := r.RequestStop("j"); ok {
		t.Fatal("RequestStop on a terminal job should be false")
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

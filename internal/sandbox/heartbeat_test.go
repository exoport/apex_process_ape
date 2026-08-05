package sandbox

import (
	"strings"
	"testing"
)

func TestHeartbeatSubjectIsInsideTheVMGrant(t *testing.T) {
	// The per-VM credential's publish grant is ape.metrics.<tok>.>, so the
	// heartbeat subject must sit under it — the agent gets no new authority.
	got := HeartbeatSubject("vm-alpha")
	if got != "ape.metrics.vm-alpha.heartbeat" {
		t.Errorf("HeartbeatSubject = %q", got)
	}
	if !strings.HasPrefix(got, "ape.metrics.vm-alpha.") {
		t.Error("the heartbeat subject falls outside the per-VM publish grant")
	}
}

func TestWorkspaceFromHeartbeatSubject(t *testing.T) {
	for subject, want := range map[string]string{
		"ape.metrics.vm-alpha.heartbeat": "vm-alpha",
		"ape.metrics.vm-b.heartbeat":     "vm-b",
	} {
		got, ok := WorkspaceFromHeartbeatSubject(subject)
		if !ok || got != want {
			t.Errorf("WorkspaceFromHeartbeatSubject(%q) = %q, %v; want %q, true", subject, got, ok, want)
		}
	}
	// Anything that is not a heartbeat subject must be rejected: the reaper reads
	// the token from here precisely because the subject is what the server
	// authorized, so a loose parse would let one workspace's payload be attributed
	// to another.
	for _, subject := range []string{
		"ape.metrics.vm-alpha.cost",
		"ape.metrics.vm-alpha.heartbeat.extra",
		"ape.evt.vm-alpha.heartbeat",
		"ape.metrics..heartbeat",
		"heartbeat",
		"",
	} {
		if got, ok := WorkspaceFromHeartbeatSubject(subject); ok {
			t.Errorf("WorkspaceFromHeartbeatSubject(%q) accepted, returning %q", subject, got)
		}
	}
}

func TestCPUPercentBetween(t *testing.T) {
	for name, tc := range map[string]struct {
		prev, cur GuestSample
		want      float64
		wantOK    bool
	}{
		"fully busy": {
			prev:   GuestSample{TotalJiffies: 100, IdleJiffies: 50},
			cur:    GuestSample{TotalJiffies: 200, IdleJiffies: 50},
			want:   100,
			wantOK: true,
		},
		"fully idle": {
			prev:   GuestSample{TotalJiffies: 100, IdleJiffies: 50},
			cur:    GuestSample{TotalJiffies: 200, IdleJiffies: 150},
			want:   0,
			wantOK: true,
		},
		"half busy": {
			prev:   GuestSample{TotalJiffies: 0, IdleJiffies: 0},
			cur:    GuestSample{TotalJiffies: 100, IdleJiffies: 50},
			want:   50,
			wantOK: true,
		},
		// No time passed, or /proc was unreadable: NO answer. This is the case
		// that must not degrade into a fabricated 0%.
		"no elapsed jiffies": {
			prev:   GuestSample{TotalJiffies: 100, IdleJiffies: 50},
			cur:    GuestSample{TotalJiffies: 100, IdleJiffies: 50},
			wantOK: false,
		},
		"unreadable": {
			wantOK: false,
		},
		// A counter that appears to go backwards must clamp, never produce a
		// negative or an over-100 percentage.
		"idle counter regressed": {
			prev:   GuestSample{TotalJiffies: 100, IdleJiffies: 90},
			cur:    GuestSample{TotalJiffies: 200, IdleJiffies: 80},
			want:   100,
			wantOK: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := CPUPercentBetween(tc.prev, tc.cur)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (got < tc.want-0.01 || got > tc.want+0.01) {
				t.Errorf("percent = %.2f, want %.2f", got, tc.want)
			}
		})
	}
}

func TestTopProcessesRanksByDelta(t *testing.T) {
	prev := GuestSample{PerProcess: map[string]uint64{"go": 100, "sshd": 5, "idle-daemon": 42}}
	cur := GuestSample{PerProcess: map[string]uint64{"go": 400, "sshd": 15, "idle-daemon": 42, "compile": 50}}

	got := TopProcesses(prev, cur, 3)
	want := []string{"go", "compile", "sshd"}
	if len(got) != len(want) {
		t.Fatalf("TopProcesses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TopProcesses = %v, want %v", got, want)
		}
	}
	// A process that burned nothing is not evidence of work and must not appear.
	for _, name := range got {
		if name == "idle-daemon" {
			t.Error("a process with no CPU delta was reported as busy")
		}
	}
}

func TestTopProcessesCap(t *testing.T) {
	prev := GuestSample{PerProcess: map[string]uint64{}}
	cur := GuestSample{PerProcess: map[string]uint64{"a": 1, "b": 2, "c": 3, "d": 4}}
	if got := TopProcesses(prev, cur, 2); len(got) != 2 {
		t.Errorf("TopProcesses(_, _, 2) returned %d entries: %v", len(got), got)
	}
	if got := TopProcesses(prev, cur, 0); got != nil {
		t.Errorf("TopProcesses(_, _, 0) = %v, want nil", got)
	}
}

func TestHeartbeatSummaryCarriesTheEvidence(t *testing.T) {
	// The summary is what a reaper logs when it stops a workspace. "idle since
	// 14:02" is not evidence; what was and was not running is.
	h := Heartbeat{Busy: true, CPUPercent: 87.5, Load1: 3.2, Top: []string{"go", "compile"}}
	got := h.Summary()
	for _, want := range []string{"busy", "87.5", "3.2", "go", "compile"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary() = %q, missing %q", got, want)
		}
	}
	idle := Heartbeat{Busy: false, CPUPercent: 0.4}
	if !strings.Contains(idle.Summary(), "idle") {
		t.Errorf("Summary() = %q, want it to say idle", idle.Summary())
	}
}

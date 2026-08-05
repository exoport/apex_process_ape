package sandbox

import (
	"testing"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

func TestPlanCapacityDividesAvailableNotTotal(t *testing.T) {
	// A box with 30 GiB total but only 8 GiB free fits 4 more at 2 GiB, not 15:
	// the memory a running workspace already holds is not headroom.
	got := PlanCapacity(8, workspace.MemInfo{
		TotalBytes:     30 << 30,
		AvailableBytes: 8 << 30,
	}, 5, 3, DefaultWorkspaceMemBytes)

	if got.Fits != 4 {
		t.Errorf("Fits = %d, want 4 (8 GiB available / 2 GiB each)", got.Fits)
	}
	if got.Cores != 8 || got.Workspaces != 5 || got.Running != 3 {
		t.Errorf("passthrough fields = %+v, want cores 8, workspaces 5, running 3", got)
	}
	if got.WorkspaceMemBytes != DefaultWorkspaceMemBytes {
		t.Errorf("WorkspaceMemBytes = %d, want the divisor reported alongside Fits", got.WorkspaceMemBytes)
	}
}

func TestPlanCapacityUnknownMemoryFitsNothing(t *testing.T) {
	// An unreadable /proc/meminfo must not read as unbounded room. Zero is the
	// fail-safe direction for a number someone provisions against.
	got := PlanCapacity(8, workspace.MemInfo{}, 2, 2, DefaultWorkspaceMemBytes)
	if got.Fits != 0 {
		t.Errorf("Fits = %d with no memory reading, want 0", got.Fits)
	}
}

func TestPlanCapacityRoundsDown(t *testing.T) {
	// 3 GiB free does not fit two 2 GiB workspaces, and half a workspace is not a
	// thing you can start.
	got := PlanCapacity(4, workspace.MemInfo{TotalBytes: 8 << 30, AvailableBytes: 3 << 30}, 1, 1, DefaultWorkspaceMemBytes)
	if got.Fits != 1 {
		t.Errorf("Fits = %d, want 1 (3 GiB / 2 GiB rounds down)", got.Fits)
	}
}

func TestPlanCapacityZeroDivisorFallsBackToDefault(t *testing.T) {
	got := PlanCapacity(4, workspace.MemInfo{AvailableBytes: 4 << 30}, 0, 0, 0)
	if got.WorkspaceMemBytes != DefaultWorkspaceMemBytes {
		t.Errorf("WorkspaceMemBytes = %d, want the default when the caller passes 0", got.WorkspaceMemBytes)
	}
	if got.Fits != 2 {
		t.Errorf("Fits = %d, want 2", got.Fits)
	}
}

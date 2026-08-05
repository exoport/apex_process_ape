package sandbox

import "github.com/exoport/apex_process_ape/internal/workspace"

// Node capacity reporting (PLAN-24 D4).
//
// `workspace.Capabilities` has always declared KVM, Mem and Factory and the
// containerd driver has always left them empty, so `ape sandbox` could report
// which runtimes a node offers but not whether the node had room to run
// anything. This file is the pure half of filling them: the probes that read
// /proc and /dev are Linux-only (capacity_linux.go), the arithmetic is not, so
// the interesting part is testable everywhere.
//
// The question being answered is the one an operator actually asks — "does
// workspace #6 fit on this box?" — not "which node should this land on". That
// second question is the fleet's (PLAN-24 F5) and needs a shared-storage
// decision first, because a host-fs mount pins a workspace to the disk holding
// its repo.

// DefaultWorkspaceMemBytes is the guest memory a workspace is ASSUMED to take
// when estimating how many more fit: 2 GiB, Kata's own default_memory.
//
// It is an assumption and not a measurement, deliberately. Guest memory is set
// by the Kata configuration on the node, which aped does not own and cannot read
// through the containerd client — so the honest options were to report no
// estimate at all or to report one next to the raw numbers it was derived from.
// The second is more useful, and CapacityInfo carries the divisor so the reader
// can redo the division with a number they trust more.
const DefaultWorkspaceMemBytes int64 = 2 << 30

// PlanCapacity computes the node headroom report from already-probed inputs.
//
// Fits divides AVAILABLE memory (not total) by the per-workspace assumption:
// the memory a running workspace already holds is by definition not available,
// so counting it again would double-book the box. A node reporting no available
// memory — an unreadable /proc/meminfo, or a genuinely full box — fits zero more,
// which is the fail-safe direction for a number an operator provisions against.
func PlanCapacity(cores int, mem workspace.MemInfo, registered, running int, perWorkspace int64) workspace.CapacityInfo {
	if perWorkspace <= 0 {
		perWorkspace = DefaultWorkspaceMemBytes
	}
	fits := 0
	if mem.AvailableBytes > 0 {
		fits = int(mem.AvailableBytes / perWorkspace)
	}
	return workspace.CapacityInfo{
		Cores:             max(cores, 0),
		Workspaces:        max(registered, 0),
		Running:           max(running, 0),
		WorkspaceMemBytes: perWorkspace,
		Fits:              fits,
	}
}

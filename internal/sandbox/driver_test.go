package sandbox

import (
	"context"
	"testing"

	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShellDriverUnsupported locks the D7/Phase-1 unsupported surface: VMM
// save/restore and the containerd-only streams report ErrUnsupported (which
// maps to the vmm UNSUPPORTED code). None of these touch the runner, so the
// test is cross-platform.
func TestShellDriverUnsupported(t *testing.T) {
	d := NewShellDriver(nil, nil, nil)
	ctx := context.Background()

	require.ErrorIs(t, d.Suspend(ctx, "dev"), workspace.ErrUnsupported)
	require.ErrorIs(t, d.Resume(ctx, "dev"), workspace.ErrUnsupported)

	_, err := d.Snapshot(ctx, "dev", workspace.SnapshotRequest{})
	require.ErrorIs(t, err, workspace.ErrUnsupported)

	_, err = d.Logs(ctx, "dev", workspace.LogsRequest{})
	require.ErrorIs(t, err, workspace.ErrUnsupported)

	_, err = d.Events(ctx)
	require.ErrorIs(t, err, workspace.ErrUnsupported)

	// Every unsupported error classifies to the wire code.
	assert.Equal(t, workspace.CodeUnsupported, workspace.Code(d.Suspend(ctx, "dev")))
}

// TestShellDriverCreateNeedsResolver: Create without a SpecResolver is a
// validation error (maps to VALIDATION), never a nil-deref.
func TestShellDriverCreateNeedsResolver(t *testing.T) {
	d := NewShellDriver(nil, nil, nil)
	_, err := d.Create(context.Background(), workspace.CreateRequest{Name: "dev"})
	require.ErrorIs(t, err, workspace.ErrValidation)
}

// TestShellDriverCapabilities reports the known Kata runtimes and host-fs
// support (device probing is Phase 3).
func TestShellDriverCapabilities(t *testing.T) {
	caps, err := NewShellDriver(nil, nil, nil).Capabilities(context.Background())
	require.NoError(t, err)
	assert.True(t, caps.HostFS)
	require.Len(t, caps.Runtimes, 2)
	assert.Equal(t, "io.containerd.kata-clh.v2", caps.Runtimes[0].Name)
	assert.True(t, caps.Runtimes[0].Default)
	assert.Equal(t, "io.containerd.kata-qemu.v2", caps.Runtimes[1].Name)
}

// TestShellDriverListInspectRegistry: List is served from the on-disk
// Registry and maps records to wire types; Inspect distinguishes absent
// (NOT_FOUND) from present-but-unqueryable-state (UNSUPPORTED).
func TestShellDriverListInspectRegistry(t *testing.T) {
	reg := OpenRegistry(t.TempDir())
	require.NoError(t, reg.Put(Workspace{
		Name: "dev", Container: "ape-ws-dev", Profile: "dev", VMM: "clh",
		Image: "img", Mount: "host-fs", CreatedAt: "2026-07-10T00:00:00Z",
	}))
	require.NoError(t, reg.Put(Workspace{Name: "app", Container: "ape-ws-app", VMM: "qemu", Mount: "volume"}))

	d := NewShellDriver(nil, reg, nil)
	ctx := context.Background()

	list, err := d.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Sorted by name (app before dev), mapped to wire records.
	assert.Equal(t, workspace.Workspace{ID: "app", Name: "app", Runtime: "kata-qemu", Mount: "volume"}, list[0])
	assert.Equal(t, workspace.Workspace{
		ID: "dev", Name: "dev", Image: "img", Runtime: "kata-clh", Mount: "host-fs",
		Profile: "dev", CreatedAt: "2026-07-10T00:00:00Z",
	}, list[1])

	// Inspect: absent → NOT_FOUND; present → UNSUPPORTED (live state needs containerd).
	_, err = d.Inspect(ctx, "nope")
	require.ErrorIs(t, err, workspace.ErrNotFound)
	_, err = d.Inspect(ctx, "dev")
	require.ErrorIs(t, err, workspace.ErrUnsupported)
}

// TestShellDriverListInspectNoRegistry: without a Registry, List/Inspect
// report ErrUnsupported rather than panicking.
func TestShellDriverListInspectNoRegistry(t *testing.T) {
	d := NewShellDriver(nil, nil, nil)
	_, err := d.List(context.Background())
	require.ErrorIs(t, err, workspace.ErrUnsupported)
	_, err = d.Inspect(context.Background(), "dev")
	require.ErrorIs(t, err, workspace.ErrUnsupported)
}

func TestRuntimeName(t *testing.T) {
	assert.Equal(t, "kata-clh", runtimeName(VMMCloudHypervisor))
	assert.Equal(t, "kata-qemu", runtimeName(VMMQemu))
	assert.Empty(t, runtimeName(VMM("")))
}

// TestWorkspaceFromSpec maps a resolved spec + request into the wire record.
func TestWorkspaceFromSpec(t *testing.T) {
	req := workspace.CreateRequest{Name: "dev", Profile: "dev", Devices: []workspace.Device{{PCI: "0000:01:00.0"}}}
	spec := WorkspaceSpec{Name: "dev", Image: "img", VMM: VMMQemu, Mount: MountHostFS}
	assert.Equal(t, workspace.Workspace{
		ID: "dev", Name: "dev", Image: "img", Runtime: "kata-qemu", Mount: "host-fs",
		Profile: "dev", Devices: []workspace.Device{{PCI: "0000:01:00.0"}},
	}, workspaceFromSpec(req, spec))
}

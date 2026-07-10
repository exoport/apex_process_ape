package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodeMapsSentinels locks the sentinel→wire-code mapping frozen in
// docs/reference/events.md (the ape.vmm req.Error set). Wrapped errors must
// classify via errors.Is; nil and unrecognized errors return "".
func TestCodeMapsSentinels(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrUnsupported, CodeUnsupported},
		{ErrNotFound, CodeNotFound},
		{ErrBusy, CodeBusy},
		{ErrValidation, CodeValidation},
		{ErrDeviceUnavailable, CodeDeviceUnavailable},
		{ErrPolicyDenied, CodeDenied},
		// Wrapped errors still classify.
		{fmt.Errorf("create: %w", ErrValidation), CodeValidation},
		{fmt.Errorf("bind %s: %w", "0000:01:00.0", ErrDeviceUnavailable), CodeDeviceUnavailable},
		// nil and unrecognized → "".
		{nil, ""},
		{errors.New("some other failure"), ""},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, Code(c.err), "Code(%v)", c.err)
	}
}

// TestWireCodesAreFrozen guards the exact code strings against accidental
// rename (they are an external contract).
func TestWireCodesAreFrozen(t *testing.T) {
	assert.Equal(t, "UNSUPPORTED", CodeUnsupported)
	assert.Equal(t, "NOT_FOUND", CodeNotFound)
	assert.Equal(t, "BUSY", CodeBusy)
	assert.Equal(t, "VALIDATION", CodeValidation)
	assert.Equal(t, "DEVICE_UNAVAILABLE", CodeDeviceUnavailable)
	assert.Equal(t, "DENIED", CodeDenied)
	assert.Equal(t, 1, WireVersion)
}

// TestWireTypesJSONRoundTrip proves every wire type survives a
// marshal→unmarshal cycle unchanged — the property the NATS contract relies on.
func TestWireTypesJSONRoundTrip(t *testing.T) {
	code := 137
	values := []any{
		&CreateRequest{
			V: WireVersion, Name: "dev", Image: "img@sha256:abc", Runtime: "kata-qemu",
			Mount: "host-fs", Profile: "dev",
			Devices: []Device{{PCI: "0000:01:00.0"}, {USB: "303a:1001"}},
			From:    "tmpl",
		},
		&Workspace{
			ID: "dev", Name: "dev", Image: "img", Runtime: "kata-clh", Mount: "volume",
			Profile: "dev", Devices: []Device{{PCI: "0000:01:00.0"}}, CreatedAt: "2026-07-10T00:00:00Z",
		},
		&Status{ID: "dev", Name: "dev", State: StateFrozen, ExitCode: &code},
		&ExitStatus{Code: 0},
		&DestroyRequest{Force: true, RemoveVolume: true},
		&ExecRequest{Cmd: []string{"ls", "-la"}, TTY: true, Env: []string{"FOO=bar"}},
		&AttachRequest{Shell: "/bin/bash", TTY: true},
		&LogsRequest{Follow: true, Tail: 100},
		&SnapshotRequest{Name: "snap1"},
		&SnapshotRef{ID: "s1", Name: "snap1"},
		&Event{Type: EventTaskExit, WorkspaceID: "dev", Time: "2026-07-10T00:00:00Z", ExitCode: &code},
		&Capabilities{
			KVM:      true,
			Runtimes: []RuntimeInfo{{Name: "io.containerd.kata-clh.v2", VMM: "clh", Default: true}},
			HostFS:   true,
			GPUs: []GPU{{
				BDF: "0000:01:00.0", VendorID: "10de", DeviceID: "2204", Model: "RTX 3090",
				Driver: "vfio-pci", IOMMUGroup: 15, GroupIsolated: true,
				GroupMembers: []string{"0000:01:00.0", "0000:01:00.1"},
			}},
			USB:     []USBDevice{{VendorID: "303a", ProductID: "1001", Description: "ESP32"}},
			IOMMU:   IOMMUState{Enabled: true, Mode: "pt", VfioReady: true},
			Mem:     MemInfo{TotalBytes: 1 << 34, AvailableBytes: 1 << 33},
			Factory: FactoryState{Templating: true, VMCache: false},
		},
	}
	for _, v := range values {
		data, err := json.Marshal(v)
		require.NoErrorf(t, err, "marshal %T", v)

		// Round-trip into a fresh zero value of the same concrete type.
		out := newLike(v)
		require.NoErrorf(t, json.Unmarshal(data, out), "unmarshal %T", v)
		assert.Equalf(t, v, out, "round-trip %T", v)
	}
}

// newLike returns a new pointer to the zero value of the same concrete type
// that ptr points at, so the round-trip test can unmarshal into a matching
// container without a type switch per case.
func newLike(ptr any) any {
	switch ptr.(type) {
	case *CreateRequest:
		return &CreateRequest{}
	case *Workspace:
		return &Workspace{}
	case *Status:
		return &Status{}
	case *ExitStatus:
		return &ExitStatus{}
	case *DestroyRequest:
		return &DestroyRequest{}
	case *ExecRequest:
		return &ExecRequest{}
	case *AttachRequest:
		return &AttachRequest{}
	case *LogsRequest:
		return &LogsRequest{}
	case *SnapshotRequest:
		return &SnapshotRequest{}
	case *SnapshotRef:
		return &SnapshotRef{}
	case *Event:
		return &Event{}
	case *Capabilities:
		return &Capabilities{}
	default:
		panic(fmt.Sprintf("newLike: unhandled %T", ptr))
	}
}

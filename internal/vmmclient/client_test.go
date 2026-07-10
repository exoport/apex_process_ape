//go:build linux || darwin

package vmmclient_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/exoport/apex_process_ape/internal/vmmclient"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// fakeBackend is a minimal in-memory workspace.Backend for the client test.
type fakeBackend struct {
	ws map[string]workspace.State
}

var _ workspace.Backend = (*fakeBackend)(nil)

func (f *fakeBackend) Capabilities(context.Context) (workspace.Capabilities, error) {
	return workspace.Capabilities{KVM: true, HostFS: true}, nil
}

func (f *fakeBackend) Create(_ context.Context, req workspace.CreateRequest) (workspace.Workspace, error) {
	f.ws[req.Name] = workspace.StateCreated
	return workspace.Workspace{ID: req.Name, Name: req.Name, Image: req.Image}, nil
}

func (f *fakeBackend) Start(_ context.Context, id string) error {
	return f.set(id, workspace.StateRunning)
}

func (f *fakeBackend) Stop(_ context.Context, id string) error {
	return f.set(id, workspace.StateStopped)
}

func (f *fakeBackend) Freeze(_ context.Context, id string) error {
	return f.set(id, workspace.StateFrozen)
}

func (f *fakeBackend) Unfreeze(_ context.Context, id string) error {
	return f.set(id, workspace.StateRunning)
}

func (f *fakeBackend) set(id string, st workspace.State) error {
	if _, ok := f.ws[id]; !ok {
		return workspace.ErrNotFound
	}
	f.ws[id] = st
	return nil
}

func (f *fakeBackend) Destroy(_ context.Context, id string, _ workspace.DestroyRequest) error {
	if _, ok := f.ws[id]; !ok {
		return workspace.ErrNotFound
	}
	delete(f.ws, id)
	return nil
}

func (f *fakeBackend) Exec(_ context.Context, id string, _ workspace.ExecRequest) (workspace.ExitStatus, error) {
	if _, ok := f.ws[id]; !ok {
		return workspace.ExitStatus{}, workspace.ErrNotFound
	}
	return workspace.ExitStatus{Code: 0}, nil
}

func (f *fakeBackend) Attach(context.Context, string, workspace.AttachRequest, workspace.Stream) (workspace.ExitStatus, error) {
	return workspace.ExitStatus{}, workspace.ErrUnsupported
}
func (f *fakeBackend) Suspend(context.Context, string) error { return workspace.ErrUnsupported }
func (f *fakeBackend) Resume(context.Context, string) error  { return workspace.ErrUnsupported }
func (f *fakeBackend) Snapshot(context.Context, string, workspace.SnapshotRequest) (workspace.SnapshotRef, error) {
	return workspace.SnapshotRef{}, workspace.ErrUnsupported
}

func (f *fakeBackend) Logs(context.Context, string, workspace.LogsRequest) (io.ReadCloser, error) {
	return nil, workspace.ErrUnsupported
}

func (f *fakeBackend) Events(context.Context) (<-chan workspace.Event, error) {
	return nil, workspace.ErrUnsupported
}

func (f *fakeBackend) List(context.Context) ([]workspace.Workspace, error) {
	out := make([]workspace.Workspace, 0, len(f.ws))
	for name := range f.ws {
		out = append(out, workspace.Workspace{ID: name, Name: name})
	}
	return out, nil
}

func (f *fakeBackend) Inspect(_ context.Context, id string) (workspace.Status, error) {
	st, ok := f.ws[id]
	if !ok {
		return workspace.Status{}, workspace.ErrNotFound
	}
	return workspace.Status{ID: id, Name: id, State: st}, nil
}

// TestClientDrivesVMMService proves the ape-side NATS client speaks the vmm
// contract end-to-end against the real aped vmm service (fake backend): the same
// workspace.Backend interface, over NATS, with sentinel errors preserved.
func TestClientDrivesVMMService(t *testing.T) {
	url := natstest.RunServer(t)

	svcConn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("service connect: %v", err)
	}
	t.Cleanup(func() { _ = svcConn.Drain() })
	svc, err := micro.AddService(svcConn, micro.Config{Name: "ape-vmm", Version: "0.0.0"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	if err := aped.NewVMM(aped.VMMConfig{Node: "node1", Backend: &fakeBackend{ws: map[string]workspace.State{}}}).Register(svc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_ = svcConn.Flush()

	cliConn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cliConn.Drain() })
	c := vmmclient.New(cliConn, "node1", 3*time.Second)
	ctx := context.Background()

	// A local Backend and the remote client are interchangeable.
	var _ workspace.Backend = c

	caps, err := c.Capabilities(ctx)
	if err != nil || !caps.KVM {
		t.Fatalf("capabilities: %+v err=%v", caps, err)
	}

	ws, err := c.Create(ctx, workspace.CreateRequest{Name: "dev", Image: "img@sha256:abc"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ws.ID != "dev" || ws.Image != "img@sha256:abc" {
		t.Fatalf("create reply = %+v", ws)
	}

	if err := c.Start(ctx, "dev"); err != nil {
		t.Fatalf("start: %v", err)
	}
	st, err := c.Inspect(ctx, "dev")
	if err != nil || st.State != workspace.StateRunning {
		t.Fatalf("inspect = %+v err=%v", st, err)
	}
	list, err := c.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}

	// Sentinel errors survive the round-trip.
	if _, err := c.Inspect(ctx, "ghost"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("inspect ghost: got %v, want ErrNotFound", err)
	}
	if err := c.Suspend(ctx, "dev"); !errors.Is(err, workspace.ErrUnsupported) {
		t.Fatalf("suspend: got %v, want ErrUnsupported", err)
	}

	if err := c.Destroy(ctx, "dev", workspace.DestroyRequest{}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := c.Inspect(ctx, "dev"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("inspect after destroy: got %v, want ErrNotFound", err)
	}
}

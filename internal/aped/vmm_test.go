//go:build linux || darwin

package aped

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// fakeBackend is an in-memory workspace.Backend for the vmm endpoint tests. It
// records lifecycle calls and supports error injection so the code mapping and
// dispatch can be asserted without KVM/containerd.
type fakeBackend struct {
	mu        sync.Mutex
	ws        map[string]workspace.State
	createErr error
}

func newFakeBackend() *fakeBackend { return &fakeBackend{ws: map[string]workspace.State{}} }

var _ workspace.Backend = (*fakeBackend)(nil)

func (f *fakeBackend) Capabilities(context.Context) (workspace.Capabilities, error) {
	return workspace.Capabilities{KVM: true, HostFS: true}, nil
}

func (f *fakeBackend) Create(_ context.Context, req workspace.CreateRequest) (workspace.Workspace, error) {
	if f.createErr != nil {
		return workspace.Workspace{}, f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ws[req.Name] = workspace.StateCreated
	return workspace.Workspace{ID: req.Name, Name: req.Name, Image: req.Image, Runtime: req.Runtime}, nil
}

func (f *fakeBackend) transition(id string, to workspace.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ws[id]; !ok {
		return workspace.ErrNotFound
	}
	f.ws[id] = to
	return nil
}

func (f *fakeBackend) Start(_ context.Context, id string) error {
	return f.transition(id, workspace.StateRunning)
}

func (f *fakeBackend) Stop(_ context.Context, id string) error {
	return f.transition(id, workspace.StateStopped)
}

func (f *fakeBackend) Freeze(_ context.Context, id string) error {
	return f.transition(id, workspace.StateFrozen)
}

func (f *fakeBackend) Unfreeze(_ context.Context, id string) error {
	return f.transition(id, workspace.StateRunning)
}

func (f *fakeBackend) Destroy(_ context.Context, id string, _ workspace.DestroyRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ws[id]; !ok {
		return workspace.ErrNotFound
	}
	delete(f.ws, id)
	return nil
}

func (f *fakeBackend) Exec(_ context.Context, id string, _ workspace.ExecRequest) (workspace.ExitStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ws[id]; !ok {
		return workspace.ExitStatus{}, workspace.ErrNotFound
	}
	return workspace.ExitStatus{Code: 0}, nil
}

func (f *fakeBackend) Attach(context.Context, string, workspace.AttachRequest, workspace.Stream) (workspace.ExitStatus, error) {
	return workspace.ExitStatus{}, workspace.ErrUnsupported
}

// The fake is also an InteractiveBackend (like the containerd driver) so the
// executor's OpAttach handler can be exercised end-to-end without containerd:
// OpenExec/OpenAttach return the echo Process the stream tests reuse.
var _ sandbox.InteractiveBackend = (*fakeBackend)(nil)

func (f *fakeBackend) OpenExec(_ context.Context, id string, _ workspace.ExecRequest) (workspace.Process, error) {
	return f.openInteractive(id)
}

func (f *fakeBackend) OpenAttach(_ context.Context, id string, _ workspace.AttachRequest) (workspace.Process, error) {
	return f.openInteractive(id)
}

func (f *fakeBackend) openInteractive(id string) (workspace.Process, error) {
	f.mu.Lock()
	_, ok := f.ws[id]
	f.mu.Unlock()
	if !ok {
		return nil, workspace.ErrNotFound
	}
	return newFakeStreamProcess(), nil
}

// Suspend/Resume/Snapshot mirror the real Kata tier: unsupported.
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
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]workspace.Workspace, 0, len(f.ws))
	for name := range f.ws {
		out = append(out, workspace.Workspace{ID: name, Name: name})
	}
	return out, nil
}

func (f *fakeBackend) Inspect(_ context.Context, id string) (workspace.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.ws[id]
	if !ok {
		return workspace.Status{}, workspace.ErrNotFound
	}
	return workspace.Status{ID: id, Name: id, State: st}, nil
}

// vmmRig is an embedded two-account server + a registered vmm service + a
// HOST_OPS operator client.
type vmmRig struct {
	s    *Server
	be   *fakeBackend
	cli  *nats.Conn
	base string // ape.vmm.node1
}

func startVMMRig(t *testing.T) *vmmRig {
	t.Helper()
	s := startTestServer(t)
	be := newFakeBackend()

	// aped-front runs the service under a HOST_OPS service cred.
	svcCreds, _, err := s.HostOps().MintUser("aped", serviceGrant(), 0)
	if err != nil {
		t.Fatalf("mint service cred: %v", err)
	}
	svcConn, _ := connectCreds(t, s.ClientURL(), svcCreds, "")
	svc, err := micro.AddService(svcConn, micro.Config{Name: "ape-vmm", Version: "0.0.0"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	if err := NewVMM(VMMConfig{Node: "node1", Backend: be}).Register(svc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_ = svcConn.Flush()

	// host `ape` connects under a scoped HOST_OPS operator cred.
	opCreds, _, err := s.HostOps().MintUser("ape-cli", OperatorGrant("node1"), 0)
	if err != nil {
		t.Fatalf("mint operator cred: %v", err)
	}
	cli, _ := connectCreds(t, s.ClientURL(), opCreds, "")

	return &vmmRig{s: s, be: be, cli: cli, base: "ape.vmm.node1"}
}

func (r *vmmRig) req(t *testing.T, endpoint string, payload any) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	msg, err := r.cli.Request(r.base+"."+endpoint, data, 3*time.Second)
	if err != nil {
		t.Fatalf("request %s: %v", endpoint, err)
	}
	return msg
}

func vmmErrCode(m *nats.Msg) string { return m.Header.Get(micro.ErrorCodeHeader) }

func TestVMMDiscovery(t *testing.T) {
	r := startVMMRig(t)
	msg, err := r.cli.Request("$SRV.INFO", nil, 3*time.Second)
	if err != nil {
		t.Fatalf("$SRV.INFO: %v", err)
	}
	var info micro.Info
	if err := json.Unmarshal(msg.Data, &info); err != nil {
		t.Fatalf("unmarshal info: %v", err)
	}
	if len(info.Endpoints) != 14 {
		t.Fatalf("endpoint count = %d, want 14", len(info.Endpoints))
	}
	subjects := map[string]bool{}
	for _, e := range info.Endpoints {
		subjects[e.Subject] = true
	}
	for _, want := range []string{"capabilities", "create", "start", "stop", "exec", "attach.open", "freeze", "unfreeze", "suspend", "resume", "snapshot", "list", "inspect", "destroy"} {
		if !subjects[r.base+"."+want] {
			t.Errorf("INFO missing endpoint subject %s.%s", r.base, want)
		}
	}
}

func TestVMMCreateAndLifecycle(t *testing.T) {
	r := startVMMRig(t)

	// create
	m := r.req(t, "create", workspace.CreateRequest{Name: testWS, Image: testImage, Runtime: "kata-qemu"})
	if c := vmmErrCode(m); c != "" {
		t.Fatalf("create rejected: %s / %s", c, m.Header.Get(micro.ErrorHeader))
	}
	var cr workspace.CreateReply
	if err := json.Unmarshal(m.Data, &cr); err != nil {
		t.Fatalf("unmarshal create reply: %v", err)
	}
	if cr.ID != testWS || cr.Image != testImage {
		t.Fatalf("create reply = %+v", cr)
	}

	// start → running (via inspect)
	if c := vmmErrCode(r.req(t, "start", workspace.IDRequest{ID: testWS})); c != "" {
		t.Fatalf("start rejected: %s", c)
	}
	var ir workspace.InspectReply
	if err := json.Unmarshal(r.req(t, "inspect", workspace.IDRequest{ID: testWS}).Data, &ir); err != nil {
		t.Fatalf("inspect unmarshal: %v", err)
	}
	if ir.State != workspace.StateRunning {
		t.Fatalf("state = %q, want running", ir.State)
	}

	// freeze → frozen, unfreeze → running
	_ = r.req(t, "freeze", workspace.IDRequest{ID: testWS})
	if err := json.Unmarshal(r.req(t, "inspect", workspace.IDRequest{ID: testWS}).Data, &ir); err != nil {
		t.Fatal(err)
	}
	if ir.State != workspace.StateFrozen {
		t.Fatalf("state after freeze = %q, want frozen", ir.State)
	}
	_ = r.req(t, "unfreeze", workspace.IDRequest{ID: testWS})

	// exec
	var er workspace.ExecReply
	if err := json.Unmarshal(r.req(t, "exec", workspace.ExecReq{ID: testWS, ExecRequest: workspace.ExecRequest{Cmd: []string{"true"}}}).Data, &er); err != nil {
		t.Fatal(err)
	}
	if er.Code != 0 {
		t.Fatalf("exec code = %d, want 0", er.Code)
	}

	// list contains dev
	var lr workspace.ListReply
	if err := json.Unmarshal(r.req(t, "list", struct{}{}).Data, &lr); err != nil {
		t.Fatal(err)
	}
	if len(lr.Workspaces) != 1 || lr.Workspaces[0].ID != testWS {
		t.Fatalf("list = %+v", lr.Workspaces)
	}

	// destroy → ok, then inspect → NOT_FOUND
	if c := vmmErrCode(r.req(t, "destroy", workspace.DestroyReq{ID: testWS})); c != "" {
		t.Fatalf("destroy rejected: %s", c)
	}
	if c := vmmErrCode(r.req(t, "inspect", workspace.IDRequest{ID: testWS})); c != workspace.CodeNotFound {
		t.Fatalf("inspect after destroy: code = %q, want NOT_FOUND", c)
	}
}

func TestVMMErrorCodes(t *testing.T) {
	r := startVMMRig(t)

	// malformed JSON → VALIDATION
	if m, err := r.cli.Request(r.base+".create", []byte("{not json"), 3*time.Second); err != nil {
		t.Fatal(err)
	} else if vmmErrCode(m) != workspace.CodeValidation {
		t.Errorf("malformed: code = %q, want VALIDATION", vmmErrCode(m))
	}

	// unknown id → NOT_FOUND
	if c := vmmErrCode(r.req(t, "start", workspace.IDRequest{ID: "nope"})); c != workspace.CodeNotFound {
		t.Errorf("start unknown: code = %q, want NOT_FOUND", c)
	}

	// suspend/resume/snapshot → UNSUPPORTED (Kata tier)
	_ = r.req(t, "create", workspace.CreateRequest{Name: "w"})
	for _, ep := range []string{"suspend", "resume"} {
		if c := vmmErrCode(r.req(t, ep, workspace.IDRequest{ID: "w"})); c != workspace.CodeUnsupported {
			t.Errorf("%s: code = %q, want UNSUPPORTED", ep, c)
		}
	}
	if c := vmmErrCode(r.req(t, "snapshot", workspace.SnapshotReq{ID: "w"})); c != workspace.CodeUnsupported {
		t.Errorf("snapshot: code = %q, want UNSUPPORTED", c)
	}

	// missing id / cmd → VALIDATION
	if c := vmmErrCode(r.req(t, "exec", workspace.ExecReq{ID: "w"})); c != workspace.CodeValidation {
		t.Errorf("exec missing cmd: code = %q, want VALIDATION", c)
	}

	// injected policy denial → DENIED
	r.be.createErr = workspace.ErrPolicyDenied
	if c := vmmErrCode(r.req(t, "create", workspace.CreateRequest{Name: "x"})); c != workspace.CodeDenied {
		t.Errorf("denied create: code = %q, want DENIED", c)
	}
}

func TestVMMAttachOpen(t *testing.T) {
	// The lightweight rig dispatches to the fake backend directly (no priv-socket
	// executor), so it configures no streaming bridge — attach.open fails closed
	// with UNSUPPORTED rather than handing out a session that nothing can serve.
	// The full streamed attach (bridge → executor → process → back) is covered
	// end-to-end by TestFullStackAttachStream.
	r := startVMMRig(t)
	_ = r.req(t, "create", workspace.CreateRequest{Name: testWS})
	msg := r.req(t, "attach.open", workspace.AttachOpenReq{ID: testWS})
	if code := vmmErrCode(msg); code != workspace.CodeUnsupported {
		t.Fatalf("attach.open error code = %q, want %s (no streaming bridge configured)", code, workspace.CodeUnsupported)
	}
}

// TestVMMGuestCannotInvoke ties the guest→host barrier to the live service: a
// per-VM TELEMETRY credential cannot even publish a management request, so a
// vmm request from a guest never reaches the service (server-rejected).
func TestVMMGuestCannotInvoke(t *testing.T) {
	r := startVMMRig(t)
	vmCreds, _, err := r.s.Telemetry().MintUser("vm-evil", VMGrant("evil"), 0)
	if err != nil {
		t.Fatalf("mint vm cred: %v", err)
	}
	guest, _ := connectCreds(t, r.s.ClientURL(), vmCreds, VMInbox("evil"))

	// The publish is denied by the guest's own account/permissions, so the
	// request cannot be delivered — it errors rather than reaching the service.
	if _, err := guest.Request(r.base+".create", []byte("{}"), 1*time.Second); err == nil {
		t.Fatal("a guest (TELEMETRY) cred must not be able to invoke the vmm service")
	}
}

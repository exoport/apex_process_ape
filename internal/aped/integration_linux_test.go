//go:build linux

package aped

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/vmmclient"
	"github.com/exoport/apex_process_ape/internal/vmmstream"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// fullStack wires the whole Phase-2 control plane in one process: the embedded
// two-account server, the vmm micro service dispatching to a privClient, the
// AF_UNIX priv boundary, and the root Executor driving a Backend — then a
// vmmclient dialing it as `ape` would. It is the end-to-end integration rig.
type fullStack struct {
	client     *vmmclient.Client
	clientConn *nats.Conn // the operator conn, for driving a vmmstream session
	reg        *sandbox.Registry
	srv        *Server // exposed so a test can mint an audit-consumer cred
}

// stackOpts selects the backend the stack's executor drives.
type stackOpts struct {
	// fake replaces the real shellDriver + shell provisioner with an in-memory
	// backend so create/mutate verbs work without containerd — for the pure
	// control-plane tests (audit forwarding, error mapping) that run in CI.
	fake bool
	// driver selects a real driver for the gated Tier-2 acceptance: "" → the
	// nerdctl shellDriver; DriverContainerd → the opt-in containerd client.
	driver string
}

func startFullStack(t *testing.T) *fullStack {
	t.Helper()
	return newStack(t, stackOpts{})
}

func newStack(t *testing.T, opts stackOpts) *fullStack {
	t.Helper()
	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "priv.sock")

	// Root executor over a real priv socket.
	l, err := listenPriv(sock)
	if err != nil {
		t.Fatalf("listenPriv: %v", err)
	}
	reg := sandbox.OpenRegistry(stateDir)
	var (
		backend   workspace.Backend
		provision Provisioner
		policy    *Policy
	)
	switch {
	case opts.fake:
		fake := newFakeBackend()
		backend = fake
		provision = func(ctx context.Context, spec sandbox.WorkspaceSpec) (workspace.Workspace, error) {
			_, _ = fake.Create(ctx, workspace.CreateRequest{Name: spec.Name, Image: spec.Image})
			return workspace.Workspace{ID: spec.Name, Name: spec.Name, Image: spec.Image}, nil
		}
		policy = &Policy{Images: []string{testImage}}
	case opts.driver == DriverContainerd:
		// The opt-in containerd driver, driven in-process for live Tier-2
		// validation (needs a real containerd socket; the gated test skips
		// without one). Backend + Provisioner are the same driver instance.
		cd, err := sandbox.NewContainerdDriver(sandbox.ContainerdConfig{Registry: reg})
		if err != nil {
			t.Fatalf("containerd driver: %v", err)
		}
		t.Cleanup(func() { _ = cd.Close() })
		backend = cd
		provision = cd.Provision
		policy = &Policy{Images: []string{testImage, liveImage()}}
	default:
		runner := &sandbox.Runner{}
		backend = sandbox.NewShellDriver(runner, reg, nil)
		provision = NewShellProvisioner(runner, reg)
		// Allow both the unit-test literal and the live Tier-2 image (below);
		// ephemeral mounts need no mount_roots. checkImage is an exact string
		// match, so the allow-list carries the same string the request sends.
		policy = &Policy{Images: []string{testImage, liveImage()}}
	}
	ex := NewExecutor(ExecutorConfig{
		Backend:     backend,
		Provision:   provision,
		Policy:      policy,
		Auditor:     NewAuditor(nil, nil, "node1"),
		AllowedUIDs: []uint32{selfUID()},
		Node:        "node1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = ex.Serve(ctx, l) }()
	t.Cleanup(func() { cancel(); _ = l.Close() })

	// Embedded server + vmm service on a HOST_OPS service cred, dispatching to
	// the privClient (front → executor). The privClient forwards the executor's
	// audit records on ape.audit.<node>.> over this same service conn.
	srv := startTestServer(t)
	resolver := NewResolver(ResolverConfig{StateDir: stateDir, HostHome: t.TempDir(), Telemetry: srv.Telemetry()})

	svcCreds, _, err := srv.HostOps().MintUser("aped", serviceGrant(), 0)
	if err != nil {
		t.Fatalf("mint service cred: %v", err)
	}
	svcConn, _ := connectCreds(t, srv.ClientURL(), svcCreds, "")
	priv := NewPrivClient(PrivClientConfig{
		Socket:  sock,
		Resolve: resolver.Resolve,
		Publish: func(subject string, data []byte) { _ = svcConn.Publish(subject, data) },
		Node:    "node1",
	})

	svc, err := micro.AddService(svcConn, micro.Config{Name: "ape-vmm", Version: "0.0.0"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	// NatsConn/Socket/Publish arm the interactive attach bridge exactly as
	// RunFront does: attach.open dials the same priv socket for a streaming exec
	// and bridges it to the session subjects on svcConn.
	if err := NewVMM(VMMConfig{
		Node:     "node1",
		Backend:  priv,
		NatsConn: svcConn,
		Socket:   sock,
		Publish:  func(subject string, data []byte) { _ = svcConn.Publish(subject, data) },
	}).Register(svc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_ = svcConn.Flush()

	// The `ape` operator client.
	opCreds, _, err := srv.HostOps().MintUser("ape-cli", OperatorGrant("node1"), 0)
	if err != nil {
		t.Fatalf("mint operator cred: %v", err)
	}
	cliConn, _ := connectCreds(t, srv.ClientURL(), opCreds, "")
	return &fullStack{client: vmmclient.New(cliConn, "node1", 5*time.Second), clientConn: cliConn, reg: reg, srv: srv}
}

// TestFullStackControlPlane exercises the entire real stack (NATS → vmm →
// privClient → AF_UNIX + SO_PEERCRED → executor → shellDriver) for the verbs
// that need no containerd: capabilities, list, and inspect. It is the permanent
// CI proof that the two-process split wires up end-to-end.
func TestFullStackControlPlane(t *testing.T) {
	fs := startFullStack(t)
	ctx := context.Background()

	caps, err := fs.client.Capabilities(ctx)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if len(caps.Runtimes) == 0 || !caps.HostFS {
		t.Fatalf("capabilities = %+v, want runtimes + host_fs", caps)
	}

	list, err := fs.client.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %+v, want empty", list)
	}

	// inspect of an unknown workspace round-trips NOT_FOUND across the whole
	// stack (executor's shellDriver → priv → vmm → NATS → client sentinel).
	if _, err := fs.client.Inspect(ctx, "ghost"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("inspect ghost: got %v, want ErrNotFound", err)
	}
}

// TestFullStackAuditForwarding proves the network-less executor's audit records
// reach ape.audit.<node>.> over NATS: the executor returns them in the priv
// Response, the front (privClient) forwards them on its HOST_OPS conn, and a
// HOST_OPS audit consumer receives them with the resolved args + policy decision
// + outcome. It runs in CI (fake backend → no containerd) and is the permanent
// proof of the PLAN-18 D9 forwarding leg.
func TestFullStackAuditForwarding(t *testing.T) {
	fs := newStack(t, stackOpts{fake: true})
	ctx := context.Background()

	// An audit consumer in the HOST_OPS account (serviceGrant → may sub ape.audit.>).
	subCreds, _, err := fs.srv.HostOps().MintUser("audit-sub", serviceGrant(), 0)
	if err != nil {
		t.Fatalf("mint audit-sub cred: %v", err)
	}
	subConn, _ := connectCreds(t, fs.srv.ClientURL(), subCreds, "")
	recs := make(chan AuditRecord, 16)
	sub, err := subConn.Subscribe("ape.audit.node1.>", func(m *nats.Msg) {
		var rec AuditRecord
		if json.Unmarshal(m.Data, &rec) == nil {
			recs <- rec
		}
	})
	if err != nil {
		t.Fatalf("subscribe audit: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	_ = subConn.Flush()

	// Drive create (allowed) + a mutate (start) + a policy-denied create.
	if _, err := fs.client.Create(ctx, workspace.CreateRequest{Name: testWS, Image: testImage, Mount: "ephemeral"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fs.client.Start(ctx, testWS); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := fs.client.Create(ctx, workspace.CreateRequest{Name: "evil", Image: "evil:latest", Mount: "ephemeral"}); !errors.Is(err, workspace.ErrPolicyDenied) {
		t.Fatalf("denied create: got %v, want ErrPolicyDenied", err)
	}

	got := collectAudit(t, recs, 3)

	createOK := findAudit(got, func(r AuditRecord) bool {
		return r.Op == "CreateVM" && r.Policy.Decision == DecisionAllow
	})
	if createOK == nil {
		t.Fatalf("no allowed CreateVM audit record in %+v", got)
	}
	if createOK.Resolved.Image != testImage {
		t.Errorf("CreateVM resolved image = %q, want %q", createOK.Resolved.Image, testImage)
	}
	if !createOK.Outcome.OK || createOK.Outcome.VMID != testWS {
		t.Errorf("CreateVM outcome = %+v, want OK vm_id=%s", createOK.Outcome, testWS)
	}
	if createOK.BoundaryPeer == nil || createOK.BoundaryPeer.UID != selfUID() {
		t.Errorf("CreateVM boundary peer = %+v, want uid %d", createOK.BoundaryPeer, selfUID())
	}

	if start := findAudit(got, func(r AuditRecord) bool { return r.Op == "StartVM" }); start == nil {
		t.Errorf("no StartVM audit record in %+v", got)
	} else if !start.Outcome.OK || start.Policy.Decision != DecisionAllow {
		t.Errorf("StartVM record = %+v, want allow + OK", start)
	}

	deny := findAudit(got, func(r AuditRecord) bool {
		return r.Op == "CreateVM" && r.Policy.Decision == DecisionDeny
	})
	if deny == nil {
		t.Fatalf("no denied CreateVM audit record in %+v", got)
	}
	if deny.Outcome.OK || deny.Outcome.Error == "" {
		t.Errorf("denied CreateVM outcome = %+v, want !OK with an error", deny.Outcome)
	}
}

// collectAudit gathers n audit records off recs or fails on timeout.
func collectAudit(t *testing.T, recs <-chan AuditRecord, n int) []AuditRecord {
	t.Helper()
	got := make([]AuditRecord, 0, n)
	deadline := time.After(5 * time.Second)
	for len(got) < n {
		select {
		case rec := <-recs:
			got = append(got, rec)
		case <-deadline:
			t.Fatalf("got %d audit records, want %d: %+v", len(got), n, got)
		}
	}
	return got
}

// findAudit returns the first record matching pred, or nil.
func findAudit(recs []AuditRecord, pred func(AuditRecord) bool) *AuditRecord {
	for i := range recs {
		if pred(recs[i]) {
			return &recs[i]
		}
	}
	return nil
}

// liveImage resolves the image the live Tier-2 acceptance provisions. It must be
// PULLABLE and LONG-LIVED (a detached `nerdctl run` whose command exits — e.g. a
// bare `alpine`, which defaults to a one-shot shell — stops immediately, so the
// subsequent exec/freeze would fail on a dead container) and must carry a shell
// for `exec true`. The default is the production ape-sandbox image
// (CMD ["sleep","infinity"]); override with APE_APED_IT_IMAGE to point at a
// locally-built long-running stand-in when that image isn't published/pulled
// (deploy/tier2-setup.sh builds one and prints the exact command).
func liveImage() string {
	if img := os.Getenv("APE_APED_IT_IMAGE"); img != "" {
		return img
	}
	return sandbox.DefaultImage
}

// TestTier2Provision is the live Tier-2 acceptance: aped provisions a non-device
// Kata-QEMU workspace over the vmm contract and drives its lifecycle. Gated on
// APE_APED_IT=1 + /dev/kvm + nerdctl (mirrors internal/sandbox's gated tier).
func TestTier2Provision(t *testing.T) {
	if os.Getenv("APE_APED_IT") != "1" {
		t.Skip("set APE_APED_IT=1 (needs /dev/kvm + containerd + Kata + nerdctl) to run the Tier-2 acceptance")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm")
	}
	if _, err := exec.LookPath("nerdctl"); err != nil {
		t.Skip("nerdctl not on PATH")
	}

	driveTier2Lifecycle(t, startFullStack(t))
}

// TestTier2ProvisionContainerd is the same live acceptance driven through the
// OPT-IN containerd driver (PLAN-18 D3 barrier-3 fix) instead of the nerdctl
// shellDriver — the in-process validation of `aped run --driver containerd`.
// Gated on APE_APED_IT=1 + /dev/kvm + a containerd socket + Kata.
func TestTier2ProvisionContainerd(t *testing.T) {
	if os.Getenv("APE_APED_IT") != "1" {
		t.Skip("set APE_APED_IT=1 (needs /dev/kvm + containerd + Kata) to run the containerd Tier-2 acceptance")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm")
	}
	if _, err := os.Stat(sandbox.DefaultContainerdAddress); err != nil {
		t.Skip("no containerd socket at " + sandbox.DefaultContainerdAddress)
	}
	driveTier2Lifecycle(t, newStack(t, stackOpts{driver: DriverContainerd}))
}

// driveTier2Lifecycle runs the create → exec → freeze → unfreeze → destroy
// acceptance against a live stack (shared by the shell + containerd Tier-2 tests).
func driveTier2Lifecycle(t *testing.T, fs *fullStack) {
	t.Helper()
	ctx := context.Background()
	const name = "aped-it"
	img := liveImage()
	t.Logf("Tier-2 acceptance: image=%s runtime=kata-qemu mount=ephemeral", img)

	ws, err := fs.client.Create(ctx, workspace.CreateRequest{Name: name, Image: img, Mount: "ephemeral", Runtime: "kata-qemu"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = fs.client.Destroy(context.Background(), name, workspace.DestroyRequest{Force: true}) })
	if ws.ID != name {
		t.Fatalf("create reply = %+v", ws)
	}

	if _, err := fs.client.Exec(ctx, name, workspace.ExecRequest{Cmd: []string{"true"}}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if err := fs.client.Freeze(ctx, name); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if err := fs.client.Unfreeze(ctx, name); err != nil {
		t.Fatalf("unfreeze: %v", err)
	}
	if err := fs.client.Destroy(ctx, name, workspace.DestroyRequest{Force: true}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

// TestFullStackAttachStream proves interactive exec/attach end-to-end through the
// whole real control plane WITHOUT containerd: client → attach.open (NATS) →
// front dials a streaming priv conn → executor OpAttach → InteractiveBackend
// (fake echo process) → relay over the priv socket → front bridges to the session
// subjects → client's vmmstream.Attach. It drives stdin and asserts the echo on
// stdout, the stderr banner, and the propagated exit code. This is the permanent
// CI proof of the exec-stream bridge (the containerd PTY itself is Tier-2). It
// also exercises the race-free startup: attach.open returns only after the server
// session has subscribed, and output is credit-gated until the client primes.
func TestFullStackAttachStream(t *testing.T) {
	fs := newStack(t, stackOpts{fake: true})
	ctx := context.Background()

	if _, err := fs.client.Create(ctx, workspace.CreateRequest{Name: testWS, Image: testImage, Mount: "ephemeral"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	open, err := fs.client.AttachOpen(ctx, testWS, workspace.AttachRequest{TTY: true})
	if err != nil {
		t.Fatalf("attach.open: %v", err)
	}
	if open.SubjectPrefix == "" || open.SessionID == "" {
		t.Fatalf("attach.open reply = %+v, want a session id + prefix", open)
	}

	stdinR, stdinW := io.Pipe()
	var stdout, stderr bytes.Buffer
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, aerr := vmmstream.Attach(ctx, fs.clientConn, open.SubjectPrefix, vmmstream.ClientStreams{
			Stdin: stdinR, Stdout: &stdout, Stderr: &stderr,
		}, 4)
		done <- result{code, aerr}
	}()

	if _, err := io.WriteString(stdinW, "hello"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = stdinW.Close() // half-close → echo drains → process exits

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("attach: %v", r.err)
		}
		if r.code != 7 {
			t.Fatalf("attach exit code = %d, want 7", r.code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("attach session did not complete (bridge deadlock?)")
	}
	if stdout.String() != "hello" {
		t.Errorf("stdout = %q, want %q (echoed through the full bridge)", stdout.String(), "hello")
	}
	if stderr.String() != "READY\n" {
		t.Errorf("stderr = %q, want %q (banner through the full bridge)", stderr.String(), "READY\n")
	}
}

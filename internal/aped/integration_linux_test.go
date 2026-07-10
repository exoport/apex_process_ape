//go:build linux

package aped

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/vmmclient"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go/micro"
)

// fullStack wires the whole Phase-2 control plane in one process: the embedded
// two-account server, the vmm micro service dispatching to a privClient, the
// AF_UNIX priv boundary, and the root Executor driving a real shellDriver — then
// a vmmclient dialing it as `ape` would. It is the end-to-end integration rig.
type fullStack struct {
	client *vmmclient.Client
	reg    *sandbox.Registry
}

func startFullStack(t *testing.T) *fullStack {
	t.Helper()
	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "priv.sock")

	// Root executor over a real priv socket, driving the real shellDriver.
	l, err := listenPriv(sock)
	if err != nil {
		t.Fatalf("listenPriv: %v", err)
	}
	runner := &sandbox.Runner{}
	reg := sandbox.OpenRegistry(stateDir)
	shell := sandbox.NewShellDriver(runner, reg, nil)
	policy := &Policy{Images: []string{testImage}} // ephemeral mounts need no mount_roots
	ex := NewExecutor(ExecutorConfig{
		Backend:     shell,
		Provision:   NewShellProvisioner(runner, reg),
		Policy:      policy,
		Auditor:     NewAuditor(nil, nil, "node1"),
		AllowedUIDs: []uint32{selfUID()},
		Node:        "node1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = ex.Serve(ctx, l) }()
	t.Cleanup(func() { cancel(); _ = l.Close() })

	// Embedded server + vmm service on a HOST_OPS service cred, dispatching to
	// the privClient (front → executor).
	srv := startTestServer(t)
	resolver := NewResolver(ResolverConfig{StateDir: stateDir, HostHome: t.TempDir(), Telemetry: srv.Telemetry()})
	backend := NewPrivClient(sock, resolver.Resolve)

	svcCreds, _, err := srv.HostOps().MintUser("aped", serviceGrant(), 0)
	if err != nil {
		t.Fatalf("mint service cred: %v", err)
	}
	svcConn, _ := connectCreds(t, srv.ClientURL(), svcCreds, "")
	svc, err := micro.AddService(svcConn, micro.Config{Name: "ape-vmm", Version: "0.0.0"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	if err := NewVMM(VMMConfig{Node: "node1", Backend: backend}).Register(svc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_ = svcConn.Flush()

	// The `ape` operator client.
	opCreds, _, err := srv.HostOps().MintUser("ape-cli", OperatorGrant("node1"), 0)
	if err != nil {
		t.Fatalf("mint operator cred: %v", err)
	}
	cliConn, _ := connectCreds(t, srv.ClientURL(), opCreds, "")
	return &fullStack{client: vmmclient.New(cliConn, "node1", 5*time.Second), reg: reg}
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

	fs := startFullStack(t)
	ctx := context.Background()
	const name = "aped-it"

	ws, err := fs.client.Create(ctx, workspace.CreateRequest{Name: name, Image: testImage, Mount: "ephemeral", Runtime: "kata-qemu"})
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

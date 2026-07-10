//go:build linux

package aped

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// selfUID is the caller's uid — the privClient dials from this process, so
// SO_PEERCRED reports it to the executor.
func selfUID() uint32 { return uint32(os.Getuid()) }

// syncBuffer is a mutex-guarded buffer so the test can read the audit log the
// executor goroutine writes without a data race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// execRig is an executor serving a real priv socket + a privClient dialing it.
type execRig struct {
	client workspace.Backend
	be     *fakeBackend
	audit  *syncBuffer
}

func startExecRig(t *testing.T, policy *Policy, allowedUIDs []uint32) *execRig {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "priv.sock")
	l, err := listenPriv(sock)
	if err != nil {
		t.Fatalf("listenPriv: %v", err)
	}
	be := newFakeBackend()
	audit := &syncBuffer{}
	provision := func(_ context.Context, spec sandbox.WorkspaceSpec) (workspace.Workspace, error) {
		be.mu.Lock()
		be.ws[spec.Name] = workspace.StateCreated
		be.mu.Unlock()
		return workspace.Workspace{ID: spec.Name, Name: spec.Name, Image: spec.Image}, nil
	}
	ex := NewExecutor(ExecutorConfig{
		Backend:     be,
		Provision:   provision,
		Policy:      policy,
		Auditor:     NewAuditor(audit, nil, "node1"),
		AllowedUIDs: allowedUIDs,
		Node:        "node1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = ex.Serve(ctx, l) }()
	t.Cleanup(func() { cancel(); _ = l.Close() })

	// The front-side resolver: a trivial ephemeral spec (no compose/mount) so
	// the test exercises the boundary + policy, not composition.
	resolve := func(_ context.Context, req workspace.CreateRequest) (sandbox.WorkspaceSpec, error) {
		return sandbox.WorkspaceSpec{Name: req.Name, Image: req.Image, Mount: sandbox.MountEphemeral, Comp: &sandbox.Composition{}}, nil
	}
	return &execRig{client: NewPrivClient(sock, resolve), be: be, audit: audit}
}

func TestExecutorCreateAllowedAndAudited(t *testing.T) {
	r := startExecRig(t, &Policy{Images: []string{testImage}}, []uint32{selfUID()})

	ws, err := r.client.Create(context.Background(), workspace.CreateRequest{Name: testWS, Image: testImage})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ws.ID != testWS || ws.Image != testImage {
		t.Fatalf("create reply = %+v", ws)
	}
	log := r.audit.String()
	if !strings.Contains(log, `"op":"CreateVM"`) || !strings.Contains(log, `"decision":"allow"`) {
		t.Fatalf("audit missing an allowed CreateVM record:\n%s", log)
	}
	if !strings.Contains(log, `"image":"`+testImage+`"`) {
		t.Fatalf("audit missing the resolved image:\n%s", log)
	}
}

func TestExecutorCreateDeniedByPolicy(t *testing.T) {
	// Policy allows a different image, so this create is denied at the executor's
	// re-validation even though the front forwarded it.
	r := startExecRig(t, &Policy{Images: []string{"only-this@sha256:def"}}, []uint32{selfUID()})

	_, err := r.client.Create(context.Background(), workspace.CreateRequest{Name: testWS, Image: "evil:latest"})
	if !errors.Is(err, workspace.ErrPolicyDenied) {
		t.Fatalf("create: got %v, want ErrPolicyDenied", err)
	}
	log := r.audit.String()
	if !strings.Contains(log, `"op":"CreateVM"`) || !strings.Contains(log, `"decision":"deny"`) {
		t.Fatalf("audit missing a denied CreateVM record:\n%s", log)
	}
}

func TestExecutorForwardsVerbs(t *testing.T) {
	r := startExecRig(t, &Policy{Images: []string{"img"}}, []uint32{selfUID()})
	ctx := context.Background()

	if _, err := r.client.Create(ctx, workspace.CreateRequest{Name: testWS, Image: "img"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.client.Start(ctx, testWS); err != nil {
		t.Fatalf("start: %v", err)
	}
	st, err := r.client.Inspect(ctx, testWS)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if st.State != workspace.StateRunning {
		t.Fatalf("state = %q, want running", st.State)
	}
	if _, err := r.client.Exec(ctx, testWS, workspace.ExecRequest{Cmd: []string{"true"}}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	// Suspend is unsupported on the Kata tier and must round-trip that code.
	if err := r.client.Suspend(ctx, testWS); !errors.Is(err, workspace.ErrUnsupported) {
		t.Fatalf("suspend: got %v, want ErrUnsupported", err)
	}
	// Unknown id → NOT_FOUND across the boundary.
	if err := r.client.Start(ctx, "ghost"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("start unknown: got %v, want ErrNotFound", err)
	}
}

// TestExecutorRejectsUnauthorizedPeer is the SO_PEERCRED gate: an executor
// configured for a different uid than the caller rejects every command. This
// simulates a boundary peer that is not the legitimate aped-front.
func TestExecutorRejectsUnauthorizedPeer(t *testing.T) {
	r := startExecRig(t, &Policy{Images: []string{"img"}}, []uint32{selfUID() + 1}) // NOT our uid

	err := r.client.Start(context.Background(), testWS)
	if !errors.Is(err, workspace.ErrPolicyDenied) {
		t.Fatalf("unauthorized peer: got %v, want a DENIED response", err)
	}
	if log := r.audit.String(); !strings.Contains(log, "RejectPeer") {
		t.Fatalf("audit missing the rejected-peer record:\n%s", log)
	}
}

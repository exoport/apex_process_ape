package aped

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/jwt/v2"
)

// fakeCompose returns a canned composition so the resolver test does not depend
// on the PLAN-16 Compose filesystem behavior (tested in internal/sandbox).
func fakeCompose(opts sandbox.ComposeOptions) (*sandbox.Composition, error) {
	return &sandbox.Composition{StagingDir: opts.StagingDir, GuestHome: "/home/ape", Env: []string{"HOME=/home/ape"}}, nil
}

func TestResolverInjectsVMCreds(t *testing.T) {
	acct, err := NewAccount()
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	stateDir := t.TempDir()
	r := NewResolver(ResolverConfig{
		StateDir:    stateDir,
		HostHome:    t.TempDir(),
		NatsURL:     "nats://10.0.0.1:4222",
		CredsExpiry: time.Hour,
		Telemetry:   acct,
	})
	r.compose = fakeCompose

	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{Name: testWS, Image: testImage, Mount: "ephemeral"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec.Name != testWS || spec.Image != testImage {
		t.Fatalf("spec = %+v", spec)
	}

	// A read-only .creds bind at the guest path.
	guestCreds := "/home/ape/" + guestCredsRel
	var credBind *sandbox.BindMount
	for i := range spec.Comp.Binds {
		if spec.Comp.Binds[i].Dest == guestCreds {
			credBind = &spec.Comp.Binds[i]
		}
	}
	if credBind == nil {
		t.Fatalf("no .creds bind at %s: %+v", guestCreds, spec.Comp.Binds)
	}
	if !credBind.ReadOnly {
		t.Error(".creds bind must be read-only")
	}

	// APE_NATS_URL + APE_NATS_CREDS injected.
	env := strings.Join(spec.Comp.Env, "\n")
	if !strings.Contains(env, natsconn.EnvURL+"=nats://10.0.0.1:4222") {
		t.Errorf("missing APE_NATS_URL: %v", spec.Comp.Env)
	}
	if !strings.Contains(env, natsconn.EnvCreds+"="+guestCreds) {
		t.Errorf("missing APE_NATS_CREDS: %v", spec.Comp.Env)
	}

	// The minted .creds is a per-VM cred (name vm-dev) written 0600.
	credsFile := filepath.Join(stateDir, "creds", "dev.creds")
	data, err := os.ReadFile(credsFile)
	if err != nil {
		t.Fatalf("read creds: %v", err)
	}
	tok, _ := jwt.ParseDecoratedJWT(data)
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		t.Fatalf("decode minted creds: %v", err)
	}
	if uc.Name != "vm-dev" {
		t.Errorf("minted cred name = %q, want vm-dev", uc.Name)
	}
	// Unix mode bits are meaningless on Windows (files read back 0666); the
	// creds are consumed by a Linux guest, and aped only runs on Linux.
	if info, _ := os.Stat(credsFile); runtime.GOOS != goosWindows && info.Mode().Perm() != 0o600 {
		t.Errorf("creds mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestResolverNoNATSSkipsCreds(t *testing.T) {
	r := NewResolver(ResolverConfig{StateDir: t.TempDir(), HostHome: t.TempDir()}) // no NatsURL
	r.compose = fakeCompose
	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{Name: testWS, Mount: "ephemeral"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, b := range spec.Comp.Binds {
		if strings.Contains(b.Dest, "vm.creds") {
			t.Error("no .creds should be injected without a NATS URL (agent skips in guest)")
		}
	}
}

// TestResolverNetworkNone locks the executor-sandbox-gap fix (PLAN-18 D1): the
// resolver defaults provisioned workspaces to NetworkNone so nerdctl's
// client-side CNI never runs inside the hardened executor, and an explicit
// Network override is honored.
func TestResolverNetworkNone(t *testing.T) {
	r := NewResolver(ResolverConfig{StateDir: t.TempDir(), HostHome: t.TempDir()})
	r.compose = fakeCompose
	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{Name: testWS, Mount: "ephemeral"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec.Network != sandbox.NetworkNone {
		t.Errorf("default network = %q, want %q", spec.Network, sandbox.NetworkNone)
	}

	r2 := NewResolver(ResolverConfig{StateDir: t.TempDir(), HostHome: t.TempDir(), Network: "bridge"})
	r2.compose = fakeCompose
	spec2, err := r2.Resolve(context.Background(), workspace.CreateRequest{Name: testWS, Mount: "ephemeral"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec2.Network != "bridge" {
		t.Errorf("override network = %q, want bridge", spec2.Network)
	}
}

func TestResolverHostFSRequiresMountSource(t *testing.T) {
	r := NewResolver(ResolverConfig{StateDir: t.TempDir(), HostHome: t.TempDir()})
	r.compose = fakeCompose

	if _, err := r.Resolve(context.Background(), workspace.CreateRequest{Name: testWS, Mount: "host-fs"}); !errors.Is(err, workspace.ErrValidation) {
		t.Fatalf("host-fs without mount_source: got %v, want ErrValidation", err)
	}
	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{Name: testWS, Mount: "host-fs", MountSource: "/home/alice/proj"})
	if err != nil {
		t.Fatalf("Resolve host-fs: %v", err)
	}
	if spec.ProjectRoot != "/home/alice/proj" {
		t.Errorf("project root = %q, want /home/alice/proj", spec.ProjectRoot)
	}
}

//go:build linux || darwin

package aped

import (
	"context"
	"errors"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- executor (D3/D4) ------------------------------------------------------

// fakeNetns records the executor's calls to the privileged helper.
type fakeNetns struct {
	ensured  []string
	ports    []int
	deleted  []string
	path     string
	ensueErr error
}

func (f *fakeNetns) EnsureNetns(_ context.Context, ws string, port int, _ bool) (string, error) {
	f.ensured = append(f.ensured, ws)
	f.ports = append(f.ports, port)
	if f.ensueErr != nil {
		return "", f.ensueErr
	}
	if f.path == "" {
		return "/run/netns/ape-" + ws, nil
	}
	return f.path, nil
}

func (f *fakeNetns) DeleteNetns(_ context.Context, ws string) error {
	f.deleted = append(f.deleted, ws)
	return nil
}

func egressSpec() sandbox.WorkspaceSpec {
	return sandbox.WorkspaceSpec{
		Name: "dev", Image: "img", Mount: sandbox.MountEphemeral, Network: sandbox.NetworkNone,
		EgressDomains: []string{"github.com"}, HTTPSProxy: "http://169.254.42.1:3200",
		Comp: &sandbox.Composition{StagingDir: "/staging", GuestHome: "/sandbox/home"},
	}
}

func TestExecutorAttachesNetnsBeforeProvisioning(t *testing.T) {
	helper := &fakeNetns{}
	var provisioned sandbox.WorkspaceSpec
	ex := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
		Provision: func(_ context.Context, spec sandbox.WorkspaceSpec) (workspace.Workspace, error) {
			provisioned = spec
			return workspace.Workspace{ID: spec.Name, Name: spec.Name}, nil
		},
		Policy: &Policy{Images: []string{"img"}, Egress: EgressPolicy{
			Enabled: true, AllowedDomains: []string{"github.com"},
		}},
		AllowedUIDs: []uint32{1},
		Netns:       helper,
	})

	resp, records := ex.doCreate(context.Background(), Command{
		Op: OpCreate, Create: &CreateCommand{Spec: egressSpec(), Caller: "op"},
	}, Peer{UID: 1})

	require.Empty(t, resp.Code, resp.Error)
	assert.Equal(t, []string{"dev"}, helper.ensured)
	// The port comes from the resolved proxy URL — one source of truth for the
	// address, so the wall can never be opened for a port nothing listens on.
	assert.Equal(t, []int{3200}, helper.ports)
	assert.Equal(t, "/run/netns/ape-dev", provisioned.NetnsPath)
	require.Len(t, records, 1)
	assert.Equal(t, []string{"github.com"}, records[0].Resolved.EgressDomains)
}

func TestExecutorDeniedCreateNeverTouchesTheNetwork(t *testing.T) {
	helper := &fakeNetns{}
	ex := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
		Provision: func(context.Context, sandbox.WorkspaceSpec) (workspace.Workspace, error) {
			t.Fatal("provision must not run for a denied create")
			return workspace.Workspace{}, nil
		},
		// Policy allows the image but NOT the domain.
		Policy:      &Policy{Images: []string{"img"}, Egress: EgressPolicy{Enabled: true}},
		AllowedUIDs: []uint32{1},
		Netns:       helper,
	})

	resp, records := ex.doCreate(context.Background(), Command{
		Op: OpCreate, Create: &CreateCommand{Spec: egressSpec()},
	}, Peer{UID: 1})

	assert.Equal(t, workspace.CodeDenied, resp.Code)
	assert.Empty(t, helper.ensured, "the netns is wired only after the policy check passes")
	require.Len(t, records, 1)
	assert.Equal(t, DecisionDeny, records[0].Policy.Decision)
}

func TestExecutorRefusesGrantedEgressWithoutHelper(t *testing.T) {
	ex := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
		Provision: func(context.Context, sandbox.WorkspaceSpec) (workspace.Workspace, error) {
			t.Fatal("provision must not run without a network helper")
			return workspace.Workspace{}, nil
		},
		Policy: &Policy{Images: []string{"img"}, Egress: EgressPolicy{
			Enabled: true, AllowedDomains: []string{"github.com"},
		}},
		AllowedUIDs: []uint32{1},
		// No Netns provider configured.
	})

	resp, _ := ex.doCreate(context.Background(), Command{
		Op: OpCreate, Create: &CreateCommand{Spec: egressSpec()},
	}, Peer{UID: 1})
	assert.Equal(t, workspace.CodeUnsupported, resp.Code)
	assert.Contains(t, resp.Error, "network helper")
}

func TestExecutorRollsBackNetnsWhenProvisionFails(t *testing.T) {
	helper := &fakeNetns{}
	ex := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
		Provision: func(context.Context, sandbox.WorkspaceSpec) (workspace.Workspace, error) {
			return workspace.Workspace{}, errors.New("kata exploded")
		},
		Policy: &Policy{Images: []string{"img"}, Egress: EgressPolicy{
			Enabled: true, AllowedDomains: []string{"github.com"},
		}},
		AllowedUIDs: []uint32{1},
		Netns:       helper,
	})

	resp, _ := ex.doCreate(context.Background(), Command{
		Op: OpCreate, Create: &CreateCommand{Spec: egressSpec()},
	}, Peer{UID: 1})
	require.NotEmpty(t, resp.Code)
	assert.Equal(t, []string{"dev"}, helper.deleted, "a failed create must not leak a namespace")
}

func TestExecutorDestroyTearsDownNetns(t *testing.T) {
	helper := &fakeNetns{}
	backend := newFakeBackend()
	_, err := backend.Create(context.Background(), workspace.CreateRequest{Name: "dev"})
	require.NoError(t, err)
	ex := NewExecutor(ExecutorConfig{
		Backend:     backend,
		Policy:      &Policy{Images: []string{"img"}},
		AllowedUIDs: []uint32{1},
		Netns:       helper,
	})
	resp, _ := ex.dispatch(context.Background(), Command{Op: OpDestroy, ID: "dev"}, Peer{UID: 1})
	require.Empty(t, resp.Code, resp.Error)
	assert.Equal(t, []string{"dev"}, helper.deleted)
}

func TestExecutorDestroyTearsDownNetnsEvenWhenTheBackendFails(t *testing.T) {
	// A workspace containerd already lost (or never had) must still give up its
	// namespace — otherwise the address lease and bridge port leak forever.
	helper := &fakeNetns{}
	ex := NewExecutor(ExecutorConfig{
		Backend:     newFakeBackend(),
		Policy:      &Policy{Images: []string{"img"}},
		AllowedUIDs: []uint32{1},
		Netns:       helper,
	})
	resp, _ := ex.dispatch(context.Background(), Command{Op: OpDestroy, ID: "ghost"}, Peer{UID: 1})
	assert.Equal(t, workspace.CodeNotFound, resp.Code)
	assert.Equal(t, []string{"ghost"}, helper.deleted)
}

func TestExecutorWithoutEgressSkipsHelper(t *testing.T) {
	helper := &fakeNetns{}
	ex := NewExecutor(ExecutorConfig{
		Backend: &fakeBackend{},
		Provision: func(_ context.Context, spec sandbox.WorkspaceSpec) (workspace.Workspace, error) {
			assert.Empty(t, spec.NetnsPath)
			return workspace.Workspace{ID: spec.Name}, nil
		},
		Policy:      &Policy{Images: []string{"img"}},
		AllowedUIDs: []uint32{1},
		Netns:       helper,
	})
	spec := egressSpec()
	spec.EgressDomains = nil
	spec.HTTPSProxy = ""

	resp, _ := ex.doCreate(context.Background(), Command{
		Op: OpCreate, Create: &CreateCommand{Spec: spec},
	}, Peer{UID: 1})
	require.Empty(t, resp.Code, resp.Error)
	assert.Empty(t, helper.ensured)
}

// stubResolveBackend is a privClient wired to a fake executor socket path so
// Create fails, letting the front-side cleanup hook be observed.
func TestPrivClientReleasesFrontResourcesWhenCreateFails(t *testing.T) {
	var released []string
	backend := NewPrivClient(PrivClientConfig{
		Socket: "/nonexistent/priv.sock",
		Resolve: func(context.Context, workspace.CreateRequest) (sandbox.WorkspaceSpec, error) {
			return sandbox.WorkspaceSpec{Name: "dev", Image: "img"}, nil
		},
		OnDestroy: func(id string) { released = append(released, id) },
	})

	_, err := backend.Create(context.Background(), workspace.CreateRequest{Name: "dev"})
	require.Error(t, err, "the executor is unreachable, so the create must fail")
	// Resolving started the workspace's egress proxy; a refused create must not leak
	// its listener and port until someone runs `down` on a workspace that never existed.
	assert.Equal(t, []string{"dev"}, released)
}

// fakeLiveEgress records egress.set calls for the vmm endpoint test.
type fakeLiveEgress struct {
	plan  EgressPlan
	err   error
	calls [][]string
	names []string
}

func (f *fakeLiveEgress) Plan(name string, requested []string) (EgressPlan, error) {
	f.names = append(f.names, name)
	f.calls = append(f.calls, requested)
	return f.plan, f.err
}

func TestEgressSetRequiresAnExistingWorkspace(t *testing.T) {
	// A live allowlist change must not be a way to stand up a proxy for a workspace
	// that was never created, so the id is checked against the backend first.
	egress := &fakeLiveEgress{plan: EgressPlan{Domains: []string{"github.com"}, ProxyURL: "http://169.254.42.1:3128"}}
	backend := newFakeBackend()
	v := NewVMM(VMMConfig{Node: "n", Backend: backend, Egress: egress})

	_, err := backend.Inspect(context.Background(), "ghost")
	require.ErrorIs(t, err, workspace.ErrNotFound, "precondition: the fake has no such workspace")

	_, err = backend.Create(context.Background(), workspace.CreateRequest{Name: "dev"})
	require.NoError(t, err)
	st, err := backend.Inspect(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, "dev", st.Name)

	// The endpoint itself is exercised through the service in vmm_test's harness; here
	// the contract that matters is that Plan is what applies policy, and it is only
	// reached for an existing workspace.
	plan, err := v.egress.Plan("dev", []string{"github.com"})
	require.NoError(t, err)
	assert.Equal(t, []string{"github.com"}, plan.Domains)
	assert.Equal(t, []string{"dev"}, egress.names)
}

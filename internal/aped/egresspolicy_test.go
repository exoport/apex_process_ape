package aped

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- policy (D1) -----------------------------------------------------------

func TestPolicyCheckEgress(t *testing.T) {
	base := ResolvedCreate{Image: "img", MountPath: ""}

	t.Run("no egress requested is always allowed", func(t *testing.T) {
		p := &Policy{Images: []string{"img"}}
		require.NoError(t, p.CheckCreate(base, 0))
	})

	t.Run("egress disabled denies any allowlist", func(t *testing.T) {
		p := &Policy{Images: []string{"img"}, Egress: EgressPolicy{Enabled: false, AllowedDomains: []string{"github.com"}}}
		rc := base
		rc.EgressDomains = []string{"github.com"}
		require.ErrorIs(t, p.CheckCreate(rc, 0), workspace.ErrPolicyDenied)
	})

	t.Run("enabled with an empty allow-list still denies", func(t *testing.T) {
		p := &Policy{Images: []string{"img"}, Egress: EgressPolicy{Enabled: true}}
		rc := base
		rc.EgressDomains = []string{"github.com"}
		require.ErrorIs(t, p.CheckCreate(rc, 0), workspace.ErrPolicyDenied)
	})

	t.Run("domain outside the allow-list is denied", func(t *testing.T) {
		p := &Policy{Images: []string{"img"}, Egress: EgressPolicy{Enabled: true, AllowedDomains: []string{"github.com"}}}
		rc := base
		rc.EgressDomains = []string{"github.com", "evil.example.com"}
		err := p.CheckCreate(rc, 0)
		require.ErrorIs(t, err, workspace.ErrPolicyDenied)
		assert.Contains(t, err.Error(), "evil.example.com")
	})

	t.Run("max_domains caps the grant", func(t *testing.T) {
		p := &Policy{Images: []string{"img"}, Egress: EgressPolicy{
			Enabled: true, AllowedDomains: []string{"*.example.com"}, MaxDomains: 1,
		}}
		rc := base
		rc.EgressDomains = []string{"a.example.com", "b.example.com"}
		require.ErrorIs(t, p.CheckCreate(rc, 0), workspace.ErrPolicyDenied)
	})

	t.Run("wildcard policy covers a concrete domain", func(t *testing.T) {
		p := &Policy{Images: []string{"img"}, Egress: EgressPolicy{
			Enabled: true, AllowedDomains: []string{"*.githubusercontent.com"},
		}}
		rc := base
		rc.EgressDomains = []string{"raw.githubusercontent.com"}
		require.NoError(t, p.CheckCreate(rc, 0))
	})
}

func TestLoadPolicyRejectsMalformedEgress(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(dir, "policy.yaml")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return p
	}

	// A bad wildcard must fail at LOAD: a policy that looks permissive but matches
	// nothing (or matches more than intended) is worse than a startup failure.
	_, err := LoadPolicy(write(t, "images: [img]\negress:\n  enabled: true\n  allowed_domains: [\"ev*l.com\"]\n"))
	require.Error(t, err)

	_, err = LoadPolicy(write(t, "images: [img]\negress:\n  enabled: true\n  max_domains: -1\n"))
	require.Error(t, err)

	// The valid shape loads, and an absent egress key keeps egress off.
	p, err := LoadPolicy(write(t, "images: [img]\negress:\n  enabled: true\n  allowed_domains: [\"github.com\", \"*.githubusercontent.com\"]\n  max_domains: 8\n"))
	require.NoError(t, err)
	assert.True(t, p.Egress.Enabled)
	assert.Equal(t, 8, p.Egress.MaxDomains)

	p, err = LoadPolicy(write(t, "images: [img]\n"))
	require.NoError(t, err)
	assert.False(t, p.Egress.Enabled, "no egress key → egress stays off (fail-closed)")
}

// ---- resolver (D1/D4) ------------------------------------------------------

// fakePlanner stands in for the supervisor so the resolver is tested without
// binding a listener.
type fakePlanner struct {
	plan     EgressPlan
	err      error
	requests [][]string
	names    []string
}

func (f *fakePlanner) Plan(name string, requested []string) (EgressPlan, error) {
	f.names = append(f.names, name)
	f.requests = append(f.requests, requested)
	return f.plan, f.err
}

func TestResolveEgressFoldsGrantedPlanIntoSpec(t *testing.T) {
	planner := &fakePlanner{plan: EgressPlan{
		Domains:  []string{"github.com"},
		ProxyURL: "http://169.254.42.1:3200",
	}}
	r := newTestResolver(t, planner)

	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", Mount: string(sandbox.MountEphemeral),
		Egress: &workspace.EgressRequest{AuthorizedDomains: []string{"github.com"}},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"github.com"}, spec.EgressDomains)
	assert.Equal(t, "http://169.254.42.1:3200", spec.HTTPSProxy)
	assert.True(t, spec.HasEgress())
	// Fail-safe: the spec still says "networkless". Only the netns path the executor
	// attaches gives the guest a network, so a failed wire-up cannot silently fall
	// back to an open CNI bridge.
	assert.Equal(t, sandbox.NetworkNone, spec.Network)
	assert.Empty(t, spec.NetnsPath, "the netns is attached executor-side, not here")
	assert.Equal(t, []string{"dev"}, planner.names)
}

func TestResolveEgressUnionsProfileAndRequest(t *testing.T) {
	planner := &fakePlanner{plan: EgressPlan{Domains: []string{"x"}, ProxyURL: "http://169.254.42.1:3128"}}
	r := newTestResolver(t, planner)
	r.loadProfile = func(string) (*sandbox.Profile, error) {
		p := &sandbox.Profile{
			Name: "p", Credentials: sandbox.CredentialNone, Mount: sandbox.MountEphemeral,
			Network: sandbox.NetworkPolicy{AuthorizedDomains: []string{"proxy.golang.org"}},
		}
		err := p.Validate()
		return p, err
	}

	_, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", Profile: "p",
		Egress: &workspace.EgressRequest{AuthorizedDomains: []string{"github.com"}},
	})
	require.NoError(t, err)
	require.Len(t, planner.requests, 1)
	// Both sources are merged, normalised and sorted before policy sees them.
	assert.Equal(t, []string{"github.com", "proxy.golang.org"}, planner.requests[0])
}

func TestResolveEgressWithoutPlannerIsDenied(t *testing.T) {
	r := newTestResolver(t, nil)
	_, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", Mount: string(sandbox.MountEphemeral),
		Egress: &workspace.EgressRequest{AuthorizedDomains: []string{"github.com"}},
	})
	require.ErrorIs(t, err, workspace.ErrPolicyDenied)
}

func TestResolveNoEgressRequestLeavesSpecNetworkless(t *testing.T) {
	planner := &fakePlanner{}
	r := newTestResolver(t, planner)
	spec, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", Mount: string(sandbox.MountEphemeral),
	})
	require.NoError(t, err)
	assert.False(t, spec.HasEgress())
	assert.Empty(t, spec.HTTPSProxy)
	assert.Empty(t, planner.names, "the planner is not consulted when nothing is requested")
}

func TestResolveEgressPropagatesPlannerDenial(t *testing.T) {
	planner := &fakePlanner{err: workspace.ErrPolicyDenied}
	r := newTestResolver(t, planner)
	_, err := r.Resolve(context.Background(), workspace.CreateRequest{
		Name: "dev", Mount: string(sandbox.MountEphemeral),
		Egress: &workspace.EgressRequest{AuthorizedDomains: []string{"nope.example.com"}},
	})
	require.ErrorIs(t, err, workspace.ErrPolicyDenied)
}

// newTestResolver builds a resolver with the compose/profile seams stubbed, so
// only the egress path is under test.
func newTestResolver(t *testing.T, planner EgressPlanner) *Resolver {
	t.Helper()
	r := NewResolver(ResolverConfig{StateDir: t.TempDir(), Egress: planner})
	r.compose = func(sandbox.ComposeOptions) (*sandbox.Composition, error) {
		return &sandbox.Composition{StagingDir: t.TempDir(), GuestHome: "/sandbox/home"}, nil
	}
	return r
}

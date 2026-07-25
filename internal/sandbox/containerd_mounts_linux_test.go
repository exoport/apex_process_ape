//go:build linux

package sandbox

import (
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

// TestContainerdMountsSkipsLegacyProjectBind covers the driver path aped actually
// runs (--driver containerd), where the duplicate mount was observed.
func TestContainerdMountsSkipsLegacyProjectBind(t *testing.T) {
	comp := &Composition{StagingDir: "/staging", GuestHome: "/sandbox/home"}
	spec := WorkspaceSpec{
		Name: "dev", Mount: MountHostFS, ProjectRoot: "/srv/workspaces/demo", Comp: comp,
		Mounts: []workspace.MountSpec{{Source: "/srv/workspaces/demo", Dest: "/workspace/demo"}},
	}
	dests := map[string]bool{}
	for _, m := range containerdMounts(spec) {
		dests[m.Destination] = true
	}
	assert.True(t, dests["/workspace/demo"], "the repo mount is applied")
	assert.False(t, dests[DefaultProjectDest],
		"the legacy /workspace bind would duplicate the repo and shadow the repo root")

	// A pre-PLAN-20 spec (no resolved mount list) keeps the legacy project bind.
	legacy := spec
	legacy.Mounts = nil
	dests = map[string]bool{}
	for _, m := range containerdMounts(legacy) {
		dests[m.Destination] = true
	}
	assert.True(t, dests[DefaultProjectDest])
}

func TestNetnsPathFromSpecAndProxyPortFromEnv(t *testing.T) {
	// The two facts a cold start must recover, both read from the container rather than
	// recomputed so the driver and the wiring can never disagree.
	spec := &specs.Spec{Linux: &specs.Linux{Namespaces: []specs.LinuxNamespace{
		{Type: specs.PIDNamespace},
		{Type: specs.NetworkNamespace, Path: "/run/netns/ape-dev"},
	}}}
	assert.Equal(t, "/run/netns/ape-dev", NetnsPathFromSpec(spec))

	// A path-less netns (networkless) and no netns at all both mean "nothing to rewire".
	assert.Empty(t, NetnsPathFromSpec(&specs.Spec{Linux: &specs.Linux{
		Namespaces: []specs.LinuxNamespace{{Type: specs.NetworkNamespace}},
	}}))
	assert.Empty(t, NetnsPathFromSpec(&specs.Spec{Linux: &specs.Linux{}}))
	assert.Empty(t, NetnsPathFromSpec(nil))

	port, err := ProxyPortFromEnv([]string{"HOME=/sandbox/home", "HTTPS_PROXY=http://169.254.42.1:3128"})
	require.NoError(t, err)
	assert.Equal(t, 3128, port)

	// Case-insensitive, because env casing is not guaranteed.
	port, err = ProxyPortFromEnv([]string{"https_proxy=http://169.254.42.1:3200"})
	require.NoError(t, err)
	assert.Equal(t, 3200, port)

	// A workspace with a netns but no proxy is a contradiction the driver must report
	// rather than guess a port for.
	_, err = ProxyPortFromEnv([]string{"HOME=/sandbox/home"})
	require.Error(t, err)
	_, err = ProxyPortFromEnv([]string{"HTTPS_PROXY=not-a-url"})
	require.Error(t, err)
}

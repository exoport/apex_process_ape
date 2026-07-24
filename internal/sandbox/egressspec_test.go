package sandbox

import (
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// netnsOf returns the spec's network namespace entry, if any.
func netnsOf(t *testing.T, spec *specs.Spec) (specs.LinuxNamespace, bool) {
	t.Helper()
	require.NotNil(t, spec.Linux)
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			return ns, true
		}
	}
	return specs.LinuxNamespace{}, false
}

func TestApplyImageConfigJoinsPreWiredNetns(t *testing.T) {
	spec := baseSpec()
	require.NoError(t, applyImageConfig(spec, ContainerdSpecOptions{
		Config:      ocispec.ImageConfig{Cmd: []string{"/bin/sh"}},
		Networkless: true, // a netns PATH wins over networkless
		NetnsPath:   "/run/netns/ape-dev",
	}))

	ns, ok := netnsOf(t, spec)
	require.True(t, ok, "the spec must keep a network namespace")
	assert.Equal(t, "/run/netns/ape-dev", ns.Path,
		"referencing the helper-created netns by path is what keeps the executor out of the network")
	// Exactly one netns entry — a duplicate would make runc reject the spec.
	count := 0
	for _, n := range spec.Linux.Namespaces {
		if n.Type == specs.NetworkNamespace {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestApplyImageConfigAddsNetnsPathWhenSpecHasNone(t *testing.T) {
	spec := baseSpec()
	spec.Linux.Namespaces = []specs.LinuxNamespace{{Type: specs.PIDNamespace}}
	require.NoError(t, applyImageConfig(spec, ContainerdSpecOptions{
		Config: ocispec.ImageConfig{}, NetnsPath: "/run/netns/ape-web",
	}))
	ns, ok := netnsOf(t, spec)
	require.True(t, ok)
	assert.Equal(t, "/run/netns/ape-web", ns.Path)
}

func TestApplyImageConfigNetworklessUnchangedWithoutNetnsPath(t *testing.T) {
	spec := baseSpec()
	require.NoError(t, applyImageConfig(spec, ContainerdSpecOptions{
		Config: ocispec.ImageConfig{}, Networkless: true,
	}))
	ns, ok := netnsOf(t, spec)
	require.True(t, ok)
	assert.Empty(t, ns.Path, "networkless keeps a private, path-less namespace")
}

func TestRunArgsJoinsPreWiredNetns(t *testing.T) {
	spec := WorkspaceSpec{
		Name: "dev", Image: "img", Mount: MountEphemeral,
		Network:   NetworkNone, // still "none": the netns path is what grants network
		NetnsPath: "/run/netns/ape-dev",
		Comp:      &Composition{StagingDir: "/staging", GuestHome: "/sandbox/home"},
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "--network ns:/run/netns/ape-dev")
	assert.NotContains(t, joined, "--network none",
		"the pre-wired namespace replaces the networkless flag rather than being added alongside it")
}

func TestRunArgsInjectsProxyEnvForEgress(t *testing.T) {
	spec := WorkspaceSpec{
		Name: "dev", Image: "img", Mount: MountEphemeral, Network: NetworkNone,
		NetnsPath: "/run/netns/ape-dev", HTTPSProxy: "http://169.254.42.1:3200",
		EgressDomains: []string{"github.com"},
		Comp:          &Composition{StagingDir: "/staging", GuestHome: "/sandbox/home"},
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "HTTPS_PROXY=http://169.254.42.1:3200")
	assert.Contains(t, joined, "HTTP_PROXY=http://169.254.42.1:3200")
	assert.Contains(t, joined, "NO_PROXY=localhost,127.0.0.1")
}

func TestRunArgsStaysNetworklessWithoutNetnsPath(t *testing.T) {
	spec := WorkspaceSpec{
		Name: "dev", Image: "img", Mount: MountEphemeral, Network: NetworkNone,
		Comp: &Composition{StagingDir: "/staging", GuestHome: "/sandbox/home"},
	}
	args, err := spec.RunArgs()
	require.NoError(t, err)
	assert.Contains(t, strings.Join(args, " "), "--network none")
}

func TestProxyCloseIsDeterministic(t *testing.T) {
	// Close must leave the port immediately rebindable: the egress supervisor stops a
	// proxy and re-binds the same port when a workspace's allowlist changes.
	p := NewProxy(ProxyConfig{Matcher: NewMatcher([]string{"example.com"})})
	require.NoError(t, p.Start("127.0.0.1:0"))
	addr := p.Addr()
	require.NoError(t, p.Close())

	p2 := NewProxy(ProxyConfig{Matcher: NewMatcher([]string{"example.com"})})
	require.NoError(t, p2.Start(addr), "the port must be free the moment Close returns")
	require.NoError(t, p2.Close())
	require.NoError(t, p2.Close(), "Close is idempotent")
}

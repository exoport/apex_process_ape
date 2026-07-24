package sandbox

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanEgressLinkResolvesNamesAndAddresses(t *testing.T) {
	link, err := PlanEgressLink(EgressLinkOptions{
		Workspace: "dev", GuestIP: "169.254.42.7", ProxyPort: 3200,
	})
	require.NoError(t, err)

	assert.Equal(t, "ape-dev", link.Netns)
	assert.Equal(t, "/run/netns/ape-dev", link.NetnsPath)
	assert.Equal(t, DefaultEgressBridge, link.Bridge)
	assert.Equal(t, "169.254.42.7/24", link.GuestCIDR)
	assert.Equal(t, "169.254.42.1", link.ProxyIP)
	assert.Equal(t, "http://169.254.42.1:3200", link.ProxyURL())
	// Interface names must fit IFNAMSIZ-1 (15) — they are digest-derived, not
	// name-derived, precisely so a long workspace name cannot overflow them.
	assert.LessOrEqual(t, len(link.HostVeth), 15)
	assert.LessOrEqual(t, len(link.GuestVeth), 15)
	assert.NotEqual(t, link.HostVeth, link.GuestVeth)
}

func TestPlanEgressLinkVethNamesFitLongWorkspaceNames(t *testing.T) {
	long := strings.Repeat("a", 62)
	link, err := PlanEgressLink(EgressLinkOptions{Workspace: long, GuestIP: "169.254.42.9", ProxyPort: 3128})
	require.NoError(t, err)
	assert.Len(t, link.HostVeth, 12)  // "apeh" + 8 hex digest chars
	assert.Len(t, link.GuestVeth, 12) // "apeg" + the same digest
}

func TestPlanEgressLinkRejectsBadInput(t *testing.T) {
	cases := map[string]EgressLinkOptions{
		"bad workspace name":  {Workspace: "Dev Box", GuestIP: "169.254.42.7", ProxyPort: 3200},
		"guest ip off subnet": {Workspace: "dev", GuestIP: "10.0.0.5", ProxyPort: 3200},
		"guest ip is bridge":  {Workspace: "dev", GuestIP: "169.254.42.1", ProxyPort: 3200},
		"guest ip not an ip":  {Workspace: "dev", GuestIP: "nope", ProxyPort: 3200},
		"port below range":    {Workspace: "dev", GuestIP: "169.254.42.7", ProxyPort: 80},
		"port above range":    {Workspace: "dev", GuestIP: "169.254.42.7", ProxyPort: 40000},
		"bad bridge name":     {Workspace: "dev", Bridge: "br;rm -rf", GuestIP: "169.254.42.7", ProxyPort: 3200},
		"bad host cidr":       {Workspace: "dev", HostCIDR: "not-a-cidr", GuestIP: "169.254.42.7", ProxyPort: 3200},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := PlanEgressLink(opts)
			require.Error(t, err)
		})
	}
}

func TestEgressLinkSetupCommandsWireBridgeAndIsolation(t *testing.T) {
	link, err := PlanEgressLink(EgressLinkOptions{Workspace: "dev", GuestIP: "169.254.42.7", ProxyPort: 3200})
	require.NoError(t, err)

	cmds := link.SetupCommands()
	joined := make([]string, 0, len(cmds))
	for _, c := range cmds {
		joined = append(joined, strings.Join(c, " "))
	}
	all := strings.Join(joined, "\n")

	assert.Contains(t, all, "ip netns add ape-dev")
	assert.Contains(t, all, "ip link add "+link.HostVeth+" type veth peer name "+link.GuestVeth)
	assert.Contains(t, all, "ip link set "+link.GuestVeth+" netns ape-dev")
	assert.Contains(t, all, "ip link set "+link.HostVeth+" master "+DefaultEgressBridge)
	// Port isolation is the L2 half of the wall — without it two workspaces on the
	// same bridge could reach each other directly.
	assert.Contains(t, all, "bridge link set dev "+link.HostVeth+" isolated on")
	assert.Contains(t, all, "ip -n ape-dev addr add 169.254.42.7/24 dev "+link.GuestVeth)
	// No default route and no DNS: the guest's only peer is the proxy, on-link.
	assert.NotContains(t, all, "default via")
	assert.NotContains(t, all, "resolv")
}

func TestEgressLinkTeardownRemovesVethAndNetns(t *testing.T) {
	link, err := PlanEgressLink(EgressLinkOptions{Workspace: "dev", GuestIP: "169.254.42.7", ProxyPort: 3200})
	require.NoError(t, err)
	cmds := link.TeardownCommands()
	require.Len(t, cmds, 2)
	assert.Equal(t, []string{"ip", "link", "del", link.HostVeth}, cmds[0])
	assert.Equal(t, []string{"ip", "netns", "del", "ape-dev"}, cmds[1])
}

func TestEgressLinkNftRulesetIsDenyByDefault(t *testing.T) {
	link, err := PlanEgressLink(EgressLinkOptions{Workspace: "dev", GuestIP: "169.254.42.7", ProxyPort: 3200})
	require.NoError(t, err)

	argv, ruleset := link.NftCommand()
	assert.Equal(t, []string{"ip", "netns", "exec", "ape-dev", "nft", "-f", "-"}, argv)
	// Replaceable in one shot (add/delete/add) so a re-Ensure is idempotent.
	assert.True(t, strings.HasPrefix(ruleset, "table inet ape_ws\ndelete table inet ape_ws\n"))
	assert.Contains(t, ruleset, "hook output priority 0; policy drop;")
	assert.Contains(t, ruleset, "hook input priority 0; policy drop;")
	assert.Contains(t, ruleset, "hook forward priority 0; policy drop;")
	assert.Contains(t, ruleset, "ip daddr 169.254.42.1 tcp dport 3200 accept")
}

func TestAllocateGuestIPSkipsTakenAndBridgeAddress(t *testing.T) {
	// .1 is the bridge, .2/.3 are taken → .4 is next.
	ip, err := AllocateGuestIP(DefaultEgressHostCIDR, map[string]string{
		"a": "169.254.42.2", "b": "169.254.42.3",
	}, "c")
	require.NoError(t, err)
	assert.Equal(t, "169.254.42.4", ip)

	// An existing lease is returned unchanged: a workspace keeps its address, so a
	// re-Ensure cannot renumber a running guest.
	ip, err = AllocateGuestIP(DefaultEgressHostCIDR, map[string]string{"a": "169.254.42.2"}, "a")
	require.NoError(t, err)
	assert.Equal(t, "169.254.42.2", ip)
}

func TestAllocateGuestIPExhaustion(t *testing.T) {
	taken := map[string]string{}
	// /29 → .0 network, .1 bridge, .2-.6 usable, .7 broadcast.
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		ip, err := AllocateGuestIP("10.9.9.1/29", taken, name)
		require.NoError(t, err, "allocation %d", i)
		taken[name] = ip
	}
	_, err := AllocateGuestIP("10.9.9.1/29", taken, "f")
	require.ErrorIs(t, err, ErrNoEgressAddress)
}

func TestAllocatePort(t *testing.T) {
	p, err := AllocatePort(0, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, DefaultEgressPortLow, p)

	p, err = AllocatePort(0, 0, map[string]int{"a": DefaultEgressPortLow})
	require.NoError(t, err)
	assert.Equal(t, DefaultEgressPortLow+1, p)

	_, err = AllocatePort(3128, 3128, map[string]int{"a": 3128})
	require.ErrorIs(t, err, ErrNoEgressAddress)
}

func TestIntersectDomainsNarrowsNeverWidens(t *testing.T) {
	policy := []string{"github.com", "*.githubusercontent.com", "proxy.golang.org"}

	granted, refused := IntersectDomains([]string{"github.com", "raw.githubusercontent.com"}, policy)
	assert.Equal(t, []string{"github.com", "raw.githubusercontent.com"}, granted)
	assert.Empty(t, refused)

	// Anything outside the policy set is refused, including a broader wildcard than
	// policy allows — a project can narrow, never widen.
	granted, refused = IntersectDomains([]string{"evil.example.com", "*.com", "github.com"}, policy)
	assert.Equal(t, []string{"github.com"}, granted)
	assert.Equal(t, []string{"*.com", "evil.example.com"}, refused)

	// A narrower wildcard inside a policy wildcard is granted.
	granted, _ = IntersectDomains([]string{"*.assets.githubusercontent.com"}, policy)
	assert.Equal(t, []string{"*.assets.githubusercontent.com"}, granted)

	// Empty policy denies everything (default-deny).
	granted, refused = IntersectDomains([]string{"github.com"}, nil)
	assert.Empty(t, granted)
	assert.Equal(t, []string{"github.com"}, refused)
}

func TestSortedDomainsNormalises(t *testing.T) {
	assert.Equal(t,
		[]string{"api.anthropic.com", "github.com"},
		SortedDomains([]string{"GitHub.com", " api.anthropic.com ", "github.com", ""}),
	)
	assert.Nil(t, SortedDomains(nil))
}

func TestParseProxyHostPort(t *testing.T) {
	host, port, err := ParseProxyHostPort("http://169.254.42.1:3200")
	require.NoError(t, err)
	assert.Equal(t, "169.254.42.1", host)
	assert.Equal(t, 3200, port)

	// Bare host:port (no scheme) is accepted — the spec field is a URL, but the
	// helper must not choke on the shorter form.
	host, port, err = ParseProxyHostPort("169.254.42.1:3128")
	require.NoError(t, err)
	assert.Equal(t, "169.254.42.1", host)
	assert.Equal(t, 3128, port)

	for _, bad := range []string{"", "http://169.254.42.1", "http://169.254.42.1:0", "http://169.254.42.1:abc"} {
		_, _, err := ParseProxyHostPort(bad)
		require.Error(t, err, "input %q", bad)
	}
}

func TestValidateWorkspaceName(t *testing.T) {
	for _, ok := range []string{"dev", "a", "web-1", "my_ws2"} {
		require.NoError(t, ValidateWorkspaceName(ok), ok)
	}
	for _, bad := range []string{"", "Dev", "-dev", "dev box", "dev/../etc", strings.Repeat("a", 64)} {
		require.Error(t, ValidateWorkspaceName(bad), bad)
	}
}

func TestWorkspaceSpecHasEgress(t *testing.T) {
	assert.False(t, WorkspaceSpec{}.HasEgress())
	assert.False(t, WorkspaceSpec{EgressDomains: []string{"github.com"}}.HasEgress())
	assert.False(t, WorkspaceSpec{HTTPSProxy: "http://1.2.3.4:3128"}.HasEgress())
	assert.True(t, WorkspaceSpec{
		EgressDomains: []string{"github.com"}, HTTPSProxy: "http://1.2.3.4:3128",
	}.HasEgress())
}

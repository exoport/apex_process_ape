package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The egress link (PLAN-21). A workspace reaches the outside world through ONE
// socket: the CONNECT proxy the de-privileged aped front binds on a host↔guest
// bridge. This file is the pure plan for that link — names, addresses, and the
// exact `ip`/`nft` argument vectors a privileged helper (internal/netd) executes.
// Nothing here touches the network or the filesystem, so it unit-tests on every
// platform while the helper that runs it stays Linux-only.
//
// Why a pre-wired netns at all: Kata's shim ENTERS the pod netns and taps
// whatever interfaces it finds — it does not create the netns, veth, bridge, or
// routes. aped's root executor cannot create them either (empty capability set,
// AF_UNIX only, @mount denied — PLAN-18 "barrier 2"), so a separate narrow
// privileged helper wires the netns first and the executor only references its
// path.
//
// Where enforcement actually lives (honest boundary — PLAN-16:138):
//
//  1. HOST nft input chain (deploy/dev-host.sh writes `table inet ape_egress`):
//     traffic from the bridge may reach ONLY the proxy port range on the host.
//     This hook is in the host netns and always applies.
//  2. BRIDGE PORT ISOLATION on every workspace veth (`bridge link set …
//     isolated on`): workspaces cannot talk to each other at L2, only to
//     non-isolated ports (the bridge itself).
//  3. The proxy's own deny-by-default domain allowlist + audit trail
//     (proxy.go / match.go) — the only layer that can reason about DOMAINS.
//  4. The in-netns ruleset below is DEFENCE IN DEPTH, not the load-bearing
//     wall: Kata's default `internetworking_model=tcfilter` redirects packets
//     between the veth and the guest tap at the tc layer, which BYPASSES
//     netfilter hooks inside that netns. It is still applied because it does
//     enforce under macvtap/none, and it costs nothing.
//
// The guest gets NO default route and NO DNS: the proxy IP is on-link in the
// same /24 and the proxy resolves each CONNECT hostname itself.

const (
	// DefaultEgressBridge is the host↔guest bridge aped-netbr.service creates.
	DefaultEgressBridge = "apebr0"
	// DefaultEgressHostCIDR is the bridge's host address. A link-local /24 keeps
	// the proxy inside the aped front's existing IPAddressAllow=169.254.0.0/16,
	// so no inbound unit change is needed to reach it.
	DefaultEgressHostCIDR = "169.254.42.1/24"
	// DefaultEgressPortLow/High bound the per-workspace proxy port. The host nft
	// input chain accepts exactly this range from the bridge, so a proxy MUST bind
	// inside it (an ephemeral port would be dropped).
	DefaultEgressPortLow  = 3128
	DefaultEgressPortHigh = 3999
	// NetnsRunDir is the iproute2 convention for named network namespaces. A
	// netns there is referenceable by path from the OCI spec, which is how the
	// executor hands it to containerd/Kata without touching the network itself.
	NetnsRunDir = "/run/netns"
	// netnsPrefix namespaces our netns names so they never collide with CNI's.
	netnsPrefix = "ape-"
)

// workspaceNameRe bounds a workspace name to what is safe as an interface/netns
// name component and as an argv token: lowercase alphanumerics, dash,
// underscore. Everything that reaches `ip`/`nft` is built from this or from
// parsed net.IP values — never from free-form caller input.
var workspaceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidateWorkspaceName reports whether name is safe to embed in netns and
// interface names.
func ValidateWorkspaceName(name string) error {
	if !workspaceNameRe.MatchString(name) {
		return fmt.Errorf("sandbox: invalid workspace name %q (want %s)", name, workspaceNameRe.String())
	}
	return nil
}

// NetnsName returns the named-netns name for a workspace.
func NetnsName(workspace string) string { return netnsPrefix + workspace }

// NetnsPath returns the /run/netns path for a workspace's netns.
func NetnsPath(workspace string) string { return NetnsRunDir + "/" + NetnsName(workspace) }

// vethNames derives the host/guest veth interface names for a workspace. Linux
// caps an interface name at 15 bytes (IFNAMSIZ-1), which a workspace name can
// blow past, so the pair is derived from a truncated digest of the name: a 4-byte
// prefix + 8 hex digest chars = 12 bytes, always valid, stable across restarts,
// and collision-resistant.
func vethNames(workspace string) (host, guest string) {
	sum := sha256.Sum256([]byte(workspace))
	tag := hex.EncodeToString(sum[:4]) // 8 hex chars
	return "apeh" + tag, "apeg" + tag
}

// EgressLink is the fully-resolved plan for one workspace's egress link.
type EgressLink struct {
	Workspace string // logical workspace name
	Netns     string // named netns (ape-<workspace>)
	NetnsPath string // /run/netns/<netns> — what the OCI spec references
	Bridge    string // host bridge the host-side veth is enslaved to
	HostVeth  string // host-side veth (bridge port, isolated)
	GuestVeth string // netns-side veth Kata taps into the VM
	GuestCIDR string // address configured in the netns (Kata replicates it into the guest)
	ProxyIP   string // the bridge host address the proxy listens on
	ProxyPort int    // the proxy port (inside the nft-allowed range)
}

// EgressLinkOptions drives PlanEgressLink.
type EgressLinkOptions struct {
	Workspace string // required
	Bridge    string // "" → DefaultEgressBridge
	HostCIDR  string // "" → DefaultEgressHostCIDR; supplies the proxy IP + prefix
	GuestIP   string // required: the address to configure in the netns
	ProxyPort int    // required: must fall in the allowed port range
	PortLow   int    // "" (0) → DefaultEgressPortLow
	PortHigh  int    // 0 → DefaultEgressPortHigh
}

// PlanEgressLink validates the inputs and returns the link plan. Every value the
// helper later passes to `ip`/`nft` is produced here, so validation happens once,
// in pure code, under test.
func PlanEgressLink(o EgressLinkOptions) (EgressLink, error) {
	if err := ValidateWorkspaceName(o.Workspace); err != nil {
		return EgressLink{}, err
	}
	bridge := o.Bridge
	if bridge == "" {
		bridge = DefaultEgressBridge
	}
	if err := validateIfaceName(bridge); err != nil {
		return EgressLink{}, fmt.Errorf("sandbox: egress bridge: %w", err)
	}
	hostCIDR := o.HostCIDR
	if hostCIDR == "" {
		hostCIDR = DefaultEgressHostCIDR
	}
	hostIP, subnet, err := net.ParseCIDR(hostCIDR)
	if err != nil {
		return EgressLink{}, fmt.Errorf("sandbox: egress host CIDR %q: %w", hostCIDR, err)
	}
	guestIP := net.ParseIP(strings.TrimSpace(o.GuestIP))
	if guestIP == nil {
		return EgressLink{}, fmt.Errorf("sandbox: egress guest IP %q is not an IP", o.GuestIP)
	}
	if !subnet.Contains(guestIP) {
		return EgressLink{}, fmt.Errorf("sandbox: egress guest IP %s is outside the bridge subnet %s", guestIP, subnet)
	}
	if guestIP.Equal(hostIP) {
		return EgressLink{}, fmt.Errorf("sandbox: egress guest IP %s collides with the bridge host address", guestIP)
	}
	low, high := o.PortLow, o.PortHigh
	if low == 0 {
		low = DefaultEgressPortLow
	}
	if high == 0 {
		high = DefaultEgressPortHigh
	}
	if o.ProxyPort < low || o.ProxyPort > high {
		return EgressLink{}, fmt.Errorf("sandbox: proxy port %d is outside the host-allowed range %d-%d "+
			"(the host nft input chain would drop it)", o.ProxyPort, low, high)
	}
	prefix, _ := subnet.Mask.Size()
	hostVeth, guestVeth := vethNames(o.Workspace)
	return EgressLink{
		Workspace: o.Workspace,
		Netns:     NetnsName(o.Workspace),
		NetnsPath: NetnsPath(o.Workspace),
		Bridge:    bridge,
		HostVeth:  hostVeth,
		GuestVeth: guestVeth,
		GuestCIDR: guestIP.String() + "/" + strconv.Itoa(prefix),
		ProxyIP:   hostIP.String(),
		ProxyPort: o.ProxyPort,
	}, nil
}

// ProxyURL is the HTTPS_PROXY value for this link.
func (l EgressLink) ProxyURL() string {
	return "http://" + net.JoinHostPort(l.ProxyIP, strconv.Itoa(l.ProxyPort))
}

// SetupCommands returns the argv vectors (binary first) that wire the link, in
// order. Each is executed directly — no shell, so no quoting/injection surface.
// `ip netns add` is idempotent-by-caller: the helper tolerates "File exists".
func (l EgressLink) SetupCommands() [][]string {
	return [][]string{
		{"ip", "netns", "add", l.Netns},
		{"ip", "link", "add", l.HostVeth, "type", "veth", "peer", "name", l.GuestVeth},
		{"ip", "link", "set", l.GuestVeth, "netns", l.Netns},
		{"ip", "link", "set", l.HostVeth, "master", l.Bridge},
		{"ip", "link", "set", l.HostVeth, "up"},
		// Port isolation is the L2 half of the wall: an isolated bridge port can
		// reach non-isolated ports (the bridge/host) but never another isolated
		// port — so workspace↔workspace traffic is dropped by the kernel.
		{"bridge", "link", "set", "dev", l.HostVeth, "isolated", "on"},
		{"ip", "-n", l.Netns, "link", "set", "lo", "up"},
		{"ip", "-n", l.Netns, "addr", "add", l.GuestCIDR, "dev", l.GuestVeth},
		{"ip", "-n", l.Netns, "link", "set", l.GuestVeth, "up"},
	}
}

// TeardownCommands returns the argv vectors that remove the link. Deleting the
// host veth takes its peer with it; both are best-effort (an already-gone link
// must not fail a Destroy).
func (l EgressLink) TeardownCommands() [][]string {
	return [][]string{
		{"ip", "link", "del", l.HostVeth},
		{"ip", "netns", "del", l.Netns},
	}
}

// NftCommand returns the argv that loads the in-netns ruleset (stdin-fed), and
// the ruleset text itself.
func (l EgressLink) NftCommand() (argv []string, ruleset string) {
	return []string{"ip", "netns", "exec", l.Netns, "nft", "-f", "-"}, l.NftRuleset()
}

// NftRuleset is the in-netns "only reach the proxy" wall (defence in depth — see
// the file header on tcfilter). Scoped to its own table so it can be replaced
// wholesale on a re-Ensure without touching anything else in the namespace.
func (l EgressLink) NftRuleset() string {
	var b strings.Builder
	b.WriteString("table inet ape_ws\n")
	b.WriteString("delete table inet ape_ws\n")
	b.WriteString("table inet ape_ws {\n")
	b.WriteString("  chain output {\n")
	b.WriteString("    type filter hook output priority 0; policy drop;\n")
	b.WriteString("    oifname \"lo\" accept\n")
	b.WriteString("    ct state established,related accept\n")
	fmt.Fprintf(&b, "    ip daddr %s tcp dport %d accept\n", l.ProxyIP, l.ProxyPort)
	b.WriteString("  }\n")
	b.WriteString("  chain input {\n")
	b.WriteString("    type filter hook input priority 0; policy drop;\n")
	b.WriteString("    iifname \"lo\" accept\n")
	b.WriteString("    ct state established,related accept\n")
	b.WriteString("  }\n")
	b.WriteString("  chain forward {\n")
	b.WriteString("    type filter hook forward priority 0; policy drop;\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// validateIfaceName bounds a caller-supplied interface name (the bridge) to the
// kernel's IFNAMSIZ-1 and a conservative character set.
func validateIfaceName(name string) error {
	if name == "" || len(name) > 15 {
		return fmt.Errorf("interface name %q must be 1..15 chars", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("interface name %q has an invalid character %q", name, r)
		}
	}
	return nil
}

// ---- address + port allocation --------------------------------------------

// ErrNoEgressAddress is returned when the bridge subnet has no free address (or
// the port range no free port) left for a new workspace.
var ErrNoEgressAddress = errors.New("sandbox: no free egress address in the bridge subnet")

// AllocateGuestIP picks the lowest free host address in hostCIDR's subnet,
// skipping the network address, the broadcast address, the bridge's own address,
// and every address in taken. An existing lease for workspace is returned
// unchanged so a re-Ensure is idempotent (the guest keeps its address across a
// stop/start).
//
// It is pure so the allocator is fully tested without a network.
func AllocateGuestIP(hostCIDR string, taken map[string]string, workspace string) (string, error) {
	if ip, ok := taken[workspace]; ok {
		return ip, nil
	}
	hostIP, subnet, err := net.ParseCIDR(hostCIDR)
	if err != nil {
		return "", fmt.Errorf("sandbox: egress host CIDR %q: %w", hostCIDR, err)
	}
	used := map[string]bool{hostIP.String(): true}
	for _, ip := range taken {
		used[ip] = true
	}
	ones, bits := subnet.Mask.Size()
	if bits-ones > 16 {
		// Refuse to walk a huge space; the egress subnet is a small /24 by design.
		return "", fmt.Errorf("sandbox: egress subnet %s is too large to allocate from", subnet)
	}
	network := subnet.IP.Mask(subnet.Mask).To4()
	if network == nil {
		return "", fmt.Errorf("sandbox: egress subnet %s is not IPv4", subnet)
	}
	total := 1 << (bits - ones)
	for i := 1; i < total-1; i++ { // skip network (.0) and broadcast (last)
		cand := make(net.IP, len(network))
		copy(cand, network)
		addInt(cand, i)
		s := cand.String()
		if !used[s] {
			return s, nil
		}
	}
	return "", ErrNoEgressAddress
}

// addInt adds n to a 4-byte IP in place (big-endian).
func addInt(ip net.IP, n int) {
	v := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
	v += uint32(n) //nolint:gosec // n is bounded by the subnet size (≤65534)
	ip[0], ip[1], ip[2], ip[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

// AllocatePort picks the lowest free port in [low, high] not present in taken.
// Callers still bind(2) to confirm — this only keeps our own assignments apart.
func AllocatePort(low, high int, taken map[string]int) (int, error) {
	if low == 0 {
		low = DefaultEgressPortLow
	}
	if high == 0 {
		high = DefaultEgressPortHigh
	}
	used := make(map[int]bool, len(taken))
	for _, p := range taken {
		used[p] = true
	}
	for p := low; p <= high; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("%w: port range %d-%d is exhausted", ErrNoEgressAddress, low, high)
}

// ParseProxyHostPort splits a proxy URL ("http://169.254.42.1:3128") into its host
// and port. The privileged helper needs the PORT to open the wall, and the spec
// carries only the URL — keeping one source of truth for the address instead of
// shipping the same port twice across the boundary.
func ParseProxyHostPort(proxyURL string) (host string, port int, err error) {
	raw := strings.TrimSpace(proxyURL)
	if raw == "" {
		return "", 0, errors.New("sandbox: empty proxy URL")
	}
	if !strings.Contains(raw, "//") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, fmt.Errorf("sandbox: parse proxy URL %q: %w", proxyURL, err)
	}
	h, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", 0, fmt.Errorf("sandbox: proxy URL %q must carry host:port: %w", proxyURL, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return "", 0, fmt.Errorf("sandbox: proxy URL %q has an invalid port %q", proxyURL, p)
	}
	return h, n, nil
}

// SortedDomains returns a de-duplicated, sorted copy of domains — a stable
// allowlist for the proxy, the audit trail, and the policy check.
func SortedDomains(domains []string) []string {
	if len(domains) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(domains))
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// ValidateDomainPattern checks one allowlist entry: an exact hostname or a single
// leading-wildcard label. Exported so aped's policy loader rejects a malformed
// pattern at startup with the same rule the profile schema applies.
func ValidateDomainPattern(d string) error { return validateDomainPattern(d) }

// IntersectDomains returns the requested domains that policy permits, plus the
// ones it refused. A policy pattern authorizes a request when it is the same
// pattern OR a wildcard that covers it ("*.example.com" covers "api.example.com"
// and "*.api.example.com") — so a project can NARROW what policy allows, never
// widen it (PLAN-21 D1). Both lists come back sorted and de-duplicated.
func IntersectDomains(requested, allowed []string) (granted, refused []string) {
	req := SortedDomains(requested)
	pol := SortedDomains(allowed)
	for _, r := range req {
		ok := false
		for _, a := range pol {
			if domainCovers(a, r) {
				ok = true
				break
			}
		}
		if ok {
			granted = append(granted, r)
		} else {
			refused = append(refused, r)
		}
	}
	return granted, refused
}

// domainCovers reports whether policy pattern p authorizes requested pattern r.
// Exact match always does. A leading-wildcard policy pattern covers any request
// inside its suffix — including a narrower wildcard.
func domainCovers(p, r string) bool {
	if p == r {
		return true
	}
	if !strings.HasPrefix(p, "*.") {
		return false
	}
	suffix := p[1:] // ".example.com"
	if strings.HasPrefix(r, "*.") {
		r = r[1:] // ".api.example.com" — must still sit inside the policy suffix
		return strings.HasSuffix(r, suffix)
	}
	return strings.HasSuffix(r, suffix)
}

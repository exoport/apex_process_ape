package netd

import "io"

// ServerConfig configures Serve — the helper's own process. It lives in a
// cross-platform file so the `aped netd` command wiring compiles on the Windows
// CI leg, where Serve itself is an ErrUnsupported stub (netns/veth/nft are Linux).
type ServerConfig struct {
	// Socket is the AF_UNIX path to bind; "" → DefaultSocket.
	Socket string
	// LeaseFile persists workspace→address allocations; "" → DefaultLeaseFile.
	LeaseFile string
	// Bridge / HostCIDR describe the host↔guest bridge aped-netbr.service created.
	// Empty → the sandbox defaults.
	Bridge   string
	HostCIDR string
	// PortLow/PortHigh bound the proxy ports the in-netns wall may be opened for.
	// They must match the host nft chain's accepted range, or a workspace's traffic
	// is dropped before it reaches the proxy. 0 → the sandbox defaults.
	PortLow, PortHigh int
	// AllowedUIDs are the peer uids permitted to command the helper. Empty → {0}
	// (root, i.e. aped's executor). Default-deny for everyone else.
	AllowedUIDs []uint32
	// IPBin/BridgeBin/NftBin override the tool paths (a test injects a recorder
	// instead of touching the host network).
	IPBin, BridgeBin, NftBin string
	Stderr                   io.Writer
}

// Package netd is the narrow privileged network helper for `ape sandbox`
// workspace egress (PLAN-21 D3) and its client.
//
// It exists because of a deliberate gap in the ape/aped split: Kata's shim taps
// the interfaces it FINDS in a pod netns but creates nothing, and aped's root
// executor cannot create them either — empty CapabilityBoundingSet,
// RestrictAddressFamilies=AF_UNIX, RestrictNamespaces=yes, @mount denied
// (deploy/systemd/aped.service). Widening that unit to run CNI is forbidden by
// the split's charter (PLAN-18 "barrier 2"), so the netns/veth/nft wiring lives
// here instead: a separate, single-purpose unit (aped-netd.service) holding only
// CAP_NET_ADMIN + CAP_SYS_ADMIN, speaking one typed AF_UNIX protocol with two
// verbs, reachable only by root (the executor).
//
// It never talks to containerd, never reads a policy, and never sees a domain
// allowlist: it wires a link and reports its path. Authorization happened before
// the call (executor policy check); domain enforcement happens after it (the
// CONNECT proxy in the aped front).
package netd

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/sandbox"
)

// DefaultSocket is where aped-netd.service binds its AF_UNIX socket. It lives
// under /run/aped (0710 root:ape, created by tmpfiles) with mode 0600: the only
// legitimate peer is the root executor.
const DefaultSocket = "/run/aped/netd.sock"

// DefaultLeaseFile is where the helper persists workspace→address leases so a
// re-Ensure is idempotent and two workspaces never share an address. It is under
// /run on purpose: leases are exactly as durable as the netns they describe (both
// vanish on reboot).
const DefaultLeaseFile = "/run/aped/netd-leases.json"

// MaxFrame bounds one request/response frame (both are small JSON objects).
const MaxFrame = 16 << 10

// Op is the closed verb set. Anything else is refused without being decoded
// further.
type Op string

const (
	// OpEnsure wires (or re-wires) a workspace's egress link and returns its netns
	// path. OpDelete removes it. OpPing is a liveness probe for `ape doctor`.
	OpEnsure Op = "ensure"
	OpDelete Op = "delete"
	OpPing   Op = "ping"
)

// Request is one typed command. Field names are the on-wire contract between the
// executor and the helper on the same host; they are additive-only.
//
//nolint:tagliatelle // snake_case matches the repo's socket/wire conventions
type Request struct {
	Op        Op     `json:"op"`
	Workspace string `json:"workspace,omitempty"`
	// ProxyPort is the port the aped front's CONNECT proxy for this workspace is
	// listening on, on the bridge address. It is written into the in-netns wall.
	ProxyPort int `json:"proxy_port,omitempty"`
	// Bridge/HostCIDR override the defaults (aped-netbr.service's bridge). Empty
	// takes sandbox.DefaultEgressBridge / DefaultEgressHostCIDR.
	Bridge   string `json:"bridge,omitempty"`
	HostCIDR string `json:"host_cidr,omitempty"`
	// Reuse asks for an existing link to be returned as-is instead of rebuilt.
	// Create sends Reuse=false (wire it fresh); Start sends Reuse=true (a stopped
	// workspace's container spec already references the netns path — recreate it
	// only if it went away, e.g. after a reboot).
	Reuse bool `json:"reuse,omitempty"`
}

// Response is the helper's reply. Code is empty on success.
//
//nolint:tagliatelle // snake_case matches the repo's socket/wire conventions
type Response struct {
	Error     string `json:"error,omitempty"`
	NetnsPath string `json:"netns_path,omitempty"`
	GuestIP   string `json:"guest_ip,omitempty"`
	ProxyURL  string `json:"proxy_url,omitempty"`
}

// Err returns the response's error, if any.
func (r Response) Err() error {
	if strings.TrimSpace(r.Error) == "" {
		return nil
	}
	return fmt.Errorf("netd: %s", r.Error)
}

// Validate checks a request is well-formed BEFORE anything is executed. The
// workspace name is bounded by sandbox.ValidateWorkspaceName because it becomes
// part of a netns and interface name; the port is bounded to the range the host
// nft chain accepts. Nothing else from the request reaches a command line.
func (r Request) Validate() error {
	switch r.Op {
	case OpPing:
		return nil
	case OpEnsure, OpDelete:
	default:
		return fmt.Errorf("netd: unknown op %q", r.Op)
	}
	if err := sandbox.ValidateWorkspaceName(r.Workspace); err != nil {
		return err
	}
	if r.Op == OpEnsure && r.ProxyPort <= 0 {
		return errors.New("netd: ensure requires a proxy port")
	}
	return nil
}

// EncodeRequest / DecodeRequest / EncodeResponse / DecodeResponse are the framing
// helpers: one JSON object terminated by a newline, bounded by MaxFrame.
func EncodeRequest(r Request) ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("netd: encode request: %w", err)
	}
	return append(data, '\n'), nil
}

func DecodeRequest(b []byte) (Request, error) {
	var r Request
	if err := json.Unmarshal(trimFrame(b), &r); err != nil {
		return Request{}, fmt.Errorf("netd: decode request: %w", err)
	}
	return r, nil
}

func EncodeResponse(r Response) ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("netd: encode response: %w", err)
	}
	return append(data, '\n'), nil
}

func DecodeResponse(b []byte) (Response, error) {
	var r Response
	if err := json.Unmarshal(trimFrame(b), &r); err != nil {
		return Response{}, fmt.Errorf("netd: decode response: %w", err)
	}
	return r, nil
}

func trimFrame(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }

// ---- lease store -----------------------------------------------------------

// Leases maps a workspace name to the guest address allocated for it. It is
// persisted as a small JSON object so the allocation survives a helper restart
// (the netns it describes outlives the process, but not a reboot).
type Leases struct {
	path string
	m    map[string]string
}

// OpenLeases reads the lease file (a missing file is an empty set).
func OpenLeases(path string) (*Leases, error) {
	if path == "" {
		path = DefaultLeaseFile
	}
	l := &Leases{path: path, m: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, fmt.Errorf("netd: read leases %s: %w", path, err)
	}
	if len(data) == 0 {
		return l, nil
	}
	if err := json.Unmarshal(data, &l.m); err != nil {
		return nil, fmt.Errorf("netd: parse leases %s: %w", path, err)
	}
	return l, nil
}

// Get returns the address leased to workspace, if any.
func (l *Leases) Get(workspace string) (string, bool) {
	ip, ok := l.m[workspace]
	return ip, ok
}

// All returns a copy of every lease (the allocator's "taken" set).
func (l *Leases) All() map[string]string {
	out := make(map[string]string, len(l.m))
	maps.Copy(out, l.m)
	return out
}

// Names returns the leased workspace names, sorted (stable diagnostics).
func (l *Leases) Names() []string {
	out := make([]string, 0, len(l.m))
	for k := range l.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Put records a lease and persists the set.
func (l *Leases) Put(workspace, ip string) error {
	l.m[workspace] = ip
	return l.save()
}

// Remove drops a lease and persists the set.
func (l *Leases) Remove(workspace string) error {
	if _, ok := l.m[workspace]; !ok {
		return nil
	}
	delete(l.m, workspace)
	return l.save()
}

// save writes the lease file atomically (temp + rename), 0600.
func (l *Leases) save() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("netd: mkdir lease dir: %w", err)
	}
	data, err := json.MarshalIndent(l.m, "", "  ")
	if err != nil {
		return fmt.Errorf("netd: marshal leases: %w", err)
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("netd: write leases: %w", err)
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return fmt.Errorf("netd: replace leases: %w", err)
	}
	return nil
}

package sandbox

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// The guest→host NATS path (PLAN-24 D5).
//
// PLAN-16 Phase 2 / PLAN-18 D6 assumed the in-VM agent would reach a NATS
// listener bound on the bridge IP, behind a new hole in both walls. It does not
// need one. The per-workspace CONNECT proxies run IN-PROCESS inside the
// de-privileged aped front, and that same process hosts the embedded NATS server
// on 127.0.0.1 — so the proxy's upstream dial for this route is a loopback dial
// the front makes to ITSELF. Nothing new binds anywhere, and the guest's single
// permitted destination stays the proxy it already uses:
//
//	guest agent ──TCP──▶ bridge IP : this workspace's proxy port   (the hole that already exists)
//	                       │  system-route check → audit line → net.Dial
//	                       ▼
//	                     127.0.0.1 : <mgmt port>                   (inside the front's own netns)
//	                       │
//	                       ▼
//	                     embedded NATS server                      (same process, still loopback-bound)

const (
	// AgentNatsHost is the SENTINEL hostname the in-guest agent CONNECTs to.
	//
	// A name and not a loopback literal, deliberately. Handing the guest
	// "nats://127.0.0.1:4222" would be correct only for code that goes through
	// the proxy dialer; anything that dials the string directly — a diagnostic, a
	// library that ignores a custom dialer, a later "fix" — would reach the
	// GUEST's own loopback and fail in a way that looks like the endpoint being
	// down rather than the address meaning something else there. A name that
	// resolves nowhere inside the guest fails loudly instead.
	//
	// ".internal" is the reserved suffix for exactly this: a name meaningful only
	// inside an infrastructure boundary, which cannot be registered publicly.
	AgentNatsHost = "aped.internal"
	// AgentNatsPort is the port on that sentinel. It is the stock NATS port
	// because it names a NATS endpoint; it is NOT the port the front actually
	// listens on (that is the node's --mgmt-port), and the two are deliberately
	// independent — the guest-facing authority is a contract, the listener is
	// deployment configuration.
	AgentNatsPort = 4222
	// AgentNatsURL is what aped injects as APE_NATS_URL.
	AgentNatsURL = "nats://" + AgentNatsHost + ":" + "4222"
	// SystemRouteReasonAgentNats labels this route's audit lines. It is what makes
	// node-granted agent traffic filterable out of (or into) a workspace's trail —
	// `jq 'select(.reason|startswith("system route:"))' egress-audit.jsonl`.
	SystemRouteReasonAgentNats = "system route: agent nats"
)

// AgentNatsRoute builds the system route that carries the in-guest agent's NATS
// connection, given the URL handed to guests and the front's own management
// listener.
//
// guestURL is normally AgentNatsURL. It is a parameter rather than a constant so
// a deployment that must use a different authority still gets an exact-match
// route rather than silently getting none — but a loopback literal is refused,
// because inside a guest that string addresses the guest.
//
// mgmtHost/mgmtPort are the front's listener; an empty host means 127.0.0.1,
// matching the front's own default.
func AgentNatsRoute(guestURL, mgmtHost string, mgmtPort int) (SystemRoute, error) {
	raw := strings.TrimSpace(guestURL)
	if raw == "" {
		return SystemRoute{}, nil // no guest endpoint configured → no route
	}
	host, port, err := parseNatsAuthority(raw)
	if err != nil {
		return SystemRoute{}, err
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return SystemRoute{}, fmt.Errorf(
			"sandbox: guest NATS URL %q uses a loopback literal — inside a guest that address is the GUEST's "+
				"own loopback, so the agent would never reach this host. Use %s", guestURL, AgentNatsURL,
		)
	}
	if mgmtPort <= 0 {
		return SystemRoute{}, fmt.Errorf("sandbox: agent NATS route needs the front's management port (got %d)", mgmtPort)
	}
	target := strings.TrimSpace(mgmtHost)
	if target == "" {
		target = "127.0.0.1"
	}
	return SystemRoute{
		Host:   host,
		Port:   port,
		Target: net.JoinHostPort(target, strconv.Itoa(mgmtPort)),
		Reason: SystemRouteReasonAgentNats,
	}, nil
}

// parseNatsAuthority splits a nats:// URL (or a bare host:port) into host and
// port, defaulting the port to the stock 4222.
func parseNatsAuthority(raw string) (host, port string, err error) {
	if !strings.Contains(raw, "://") {
		raw = "nats://" + raw
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", fmt.Errorf("sandbox: parse guest NATS URL %q: %w", raw, perr)
	}
	host = u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("sandbox: guest NATS URL %q carries no host", raw)
	}
	port = u.Port()
	if port == "" {
		port = strconv.Itoa(AgentNatsPort)
	}
	return strings.ToLower(host), port, nil
}

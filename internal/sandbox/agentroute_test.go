package sandbox

import (
	"strings"
	"testing"
)

func TestAgentNatsRouteTargetsTheFrontsOwnListener(t *testing.T) {
	got, err := AgentNatsRoute(AgentNatsURL, "127.0.0.1", 4223)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != AgentNatsHost || got.Port != "4222" {
		t.Errorf("authority = %s:%s, want %s:4222", got.Host, got.Port, AgentNatsHost)
	}
	if got.Target != "127.0.0.1:4223" {
		t.Errorf("Target = %s, want the front's own listener 127.0.0.1:4223", got.Target)
	}
	if got.Reason != SystemRouteReasonAgentNats {
		t.Errorf("Reason = %q, want the distinguishing label", got.Reason)
	}
}

// The guest-facing authority and the host listener are independent on purpose:
// the guest always CONNECTs to aped.internal:4222 whatever port the node's front
// happens to bind.
func TestAgentNatsRouteDecouplesGuestAuthorityFromHostPort(t *testing.T) {
	got, err := AgentNatsRoute(AgentNatsURL, "", 51234)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != "4222" {
		t.Errorf("guest-facing port = %s, want 4222 regardless of the listener", got.Port)
	}
	if got.Target != "127.0.0.1:51234" {
		t.Errorf("Target = %s, want the default loopback host with the real port", got.Target)
	}
}

// The trap PLAN-24 names: a loopback literal handed to a guest addresses the
// GUEST. Refuse it at configuration time rather than let it fail silently at the
// first heartbeat.
func TestAgentNatsRouteRefusesALoopbackLiteral(t *testing.T) {
	for _, u := range []string{"nats://127.0.0.1:4222", "nats://[::1]:4222", "127.0.0.1:4222"} {
		_, err := AgentNatsRoute(u, "127.0.0.1", 4223)
		if err == nil {
			t.Errorf("AgentNatsRoute(%q) accepted a loopback literal", u)
			continue
		}
		if !strings.Contains(err.Error(), AgentNatsURL) {
			t.Errorf("AgentNatsRoute(%q) error does not name the fix: %v", u, err)
		}
	}
}

func TestAgentNatsRouteEmptyURLIsNoRoute(t *testing.T) {
	got, err := AgentNatsRoute("", "127.0.0.1", 4223)
	if err != nil {
		t.Fatalf("an unconfigured endpoint must not be an error: %v", err)
	}
	if got.Host != "" {
		t.Errorf("got a route %+v for an empty URL, want none", got)
	}
}

func TestAgentNatsRouteNeedsAManagementPort(t *testing.T) {
	if _, err := AgentNatsRoute(AgentNatsURL, "127.0.0.1", 0); err == nil {
		t.Error("a route with no target port was accepted; it would dial nowhere")
	}
}

func TestAgentNatsURLIsTheSentinel(t *testing.T) {
	// Pinned: the URL constant and the host/port constants must not drift apart —
	// the route matches on the authority the URL carries.
	if AgentNatsURL != "nats://aped.internal:4222" {
		t.Errorf("AgentNatsURL = %q", AgentNatsURL)
	}
	host, port, err := parseNatsAuthority(AgentNatsURL)
	if err != nil || host != AgentNatsHost || port != "4222" {
		t.Errorf("parseNatsAuthority(%q) = %q, %q, %v", AgentNatsURL, host, port, err)
	}
}

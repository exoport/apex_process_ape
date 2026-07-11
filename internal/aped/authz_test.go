package aped

import "testing"

func TestVMTokenSlug(t *testing.T) {
	for in, want := range map[string]string{
		"dev-1":   "vm-dev-1",
		"Dev 1":   "vm-dev-1",
		"a.b*c>d": "vm-a-b-c-d",
	} {
		if got := VMToken(in); got != want {
			t.Errorf("VMToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubjectMatch(t *testing.T) {
	tests := []struct {
		pattern, subject string
		want             bool
	}{
		{"ape.vmm.>", "ape.vmm.node1.create", true},
		{"ape.vmm.>", "ape.vmm.node1", true},
		{"ape.vmm.>", "ape.vmm", false}, // `>` needs at least one trailing token
		{"ape.metrics.vm-a.>", "ape.metrics.vm-a.proj.sid", true},
		{"ape.metrics.vm-a.>", "ape.metrics.vm-b.proj.sid", false},
		{"ape.metrics.*.>", "ape.metrics.vm-a.proj.sid", true},
		{"vm-*", "vm-a", false},                   // '*' is a whole-token wildcard, "vm-*" is a literal
		{"_INBOX.>", "_INBOX.abc", true},          // default inbox
		{"_INBOX.>", "_INBOX_vm-vm-a.abc", false}, // distinct top-level token
		{"_INBOX_vm-vm-a.>", "_INBOX_vm-vm-a.x", true},
		{"ape.blob.uri-request", "ape.blob.uri-request", true},
		{"ape.blob.uri-request", "ape.blob.uri-request.x", false},
	}
	for _, tc := range tests {
		if got := subjectMatch(tc.pattern, tc.subject); got != tc.want {
			t.Errorf("subjectMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}

func TestVMGrantPermitsDenyWins(t *testing.T) {
	g := VMGrant("dev-1")

	// Publish: own telemetry + blob offload allowed; management/audit/other-VM denied.
	for subj, want := range map[string]bool{
		"ape.metrics.vm-dev-1.proj.sid":           true,
		"ape.evt.vm-dev-1.proj.pipeline.run.step": true,
		"ape.log.vm-dev-1.proj.sid.info":          true,
		"ape.blob.uri-request":                    true,
		"ape.vmm.node1.create":                    false, // management — the escape barrier
		"ape.audit.node1.create":                  false,
		"ape.metrics.vm-other.x":                  false, // another VM's telemetry
	} {
		if got := g.PermitsPublish(subj); got != want {
			t.Errorf("PermitsPublish(%q) = %v, want %v", subj, got, want)
		}
	}

	// Subscribe: own job intake + own scoped inbox allowed; management, the
	// default inbox, and another VM's inbox denied.
	for subj, want := range map[string]bool{
		"ape.svc.vm-dev-1.pipeline.run": true,
		"_INBOX_vm-vm-dev-1.reply":      true,
		"ape.vmm.node1.create":          false,
		"ape.vmm.>":                     false,
		"_INBOX.reply":                  false, // default inbox — cannot sniff others' replies
		"_INBOX_vm-vm-other.reply":      false, // another VM's inbox
		"ape.metrics.vm-dev-1.x":        false, // per-VM cred is pub-only on telemetry
	} {
		if got := g.PermitsSubscribe(subj); got != want {
			t.Errorf("PermitsSubscribe(%q) = %v, want %v", subj, got, want)
		}
	}
}

func TestOperatorGrantScopedToNode(t *testing.T) {
	g := OperatorGrant("node1")
	if !g.PermitsPublish("ape.vmm.node1.create") {
		t.Error("operator should publish its node's management verbs")
	}
	if !g.PermitsPublish("$SRV.PING") {
		t.Error("operator should reach $SRV discovery")
	}
	if !g.PermitsSubscribe("_INBOX.reply") {
		t.Error("operator should receive its own replies")
	}
	if !g.PermitsSubscribe("ape.vmm.node1.exec.s1.stdout") {
		t.Error("operator should receive its node's interactive session streams (attach)")
	}
	if g.PermitsSubscribe("ape.vmm.node2.exec.s1.stdout") {
		t.Error("operator must not subscribe another node's session streams")
	}
	if g.PermitsSubscribe("ape.vmm.node1.create") {
		t.Error("operator subscribes replies + sessions only, not the management verbs directly")
	}
}

func TestServiceGrantUnrestrictedWithinAccount(t *testing.T) {
	// An empty allow list means unrestricted within the (isolated) account.
	g := serviceGrant()
	if !g.PermitsSubscribe("ape.vmm.node1.create") || !g.PermitsPublish("_INBOX.x") {
		t.Error("service grant should be unrestricted within HOST_OPS")
	}
}

package aped

import (
	"strings"

	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// Subject roots — the frozen ape.* taxonomy (docs/reference/events.md).
// Additive-only; never rename or repurpose a segment.
const (
	subjectVMM     = "ape.vmm"     // ape.vmm.<node>.>  management (HOST_OPS only)
	subjectAudit   = "ape.audit"   // ape.audit.<node>.<event>  privileged-op audit
	subjectEvt     = "ape.evt"     // ape.evt.<user>.…  progress events
	subjectLog     = "ape.log"     // ape.log.<user>.…  structured logs
	subjectMetrics = "ape.metrics" // ape.metrics.<user>.…  usage/cost metrics
	subjectSvc     = "ape.svc"     // ape.svc.<name>.…  job-daemon intake (PLAN-14)
	subjectBlob    = "ape.blob.uri-request"
	subjectSRV     = "$SRV" // NATS-micro discovery
)

// vmInboxPrefix is the scoped reply-inbox token a per-VM credential uses in
// place of the default _INBOX. A distinct top-level token (not "_INBOX.<vm>")
// so a deny on the default "_INBOX.>" cannot reach it and one VM cannot name
// another VM's inbox. The in-VM ape agent sets nats.CustomInboxPrefix to this.
const vmInboxPrefix = "_INBOX_vm"

// Grant is a transport-agnostic description of a credential's subject
// permissions with default-deny + deny-wins semantics — the same model NATS
// enforces server-side. It is the pure input to mint.go's JWT encoding and to
// the front-end's subject pre-check (PermitsPublish/PermitsSubscribe), so the
// exact grant a per-VM cred receives is unit-testable without a live server.
type Grant struct {
	PubAllow []string
	PubDeny  []string
	SubAllow []string
	SubDeny  []string
	// AllowResponses grants a one-shot publish on the reply subject of a
	// message this credential received (NATS allow_responses) — the
	// request/reply responder leg without a broad publish grant (e.g. the
	// in-VM `ape service` replying to a job on ape.svc.vm-<id>.>).
	AllowResponses bool
}

// VMToken returns the per-VM subject token "vm-<slug>" used as the <user>
// segment of a per-VM credential's telemetry subjects and as its JWT name
// claim (so natsconn.Identity().SubjectToken resolves to it — PLAN-18 D6).
func VMToken(vmID string) string { return "vm-" + natsconn.SubjectToken(vmID) }

// VMGrant returns the TELEMETRY per-VM credential grant (PLAN-18 D2/D6): pub
// only to its own ape.{evt,log,metrics}.vm-<id>.> (+ ape.blob.uri-request for
// transcript offload), sub only to ape.svc.vm-<id>.> (its PLAN-14 job intake)
// and a scoped reply inbox; explicitly denied ape.vmm.> and ape.audit.>, the
// default _INBOX.>, and — by default-deny — every other VM's ape.*.vm-*.>.
// A fully-compromised guest owning these creds can poison only its own
// telemetry; it can neither name a management subject nor address another VM.
func VMGrant(vmID string) Grant {
	tok := VMToken(vmID)
	return Grant{
		PubAllow: []string{
			subjectEvt + "." + tok + ".>",
			subjectLog + "." + tok + ".>",
			subjectMetrics + "." + tok + ".>",
			subjectBlob,
		},
		// Redundant with default-deny (ape.vmm.> is not in PubAllow) but
		// explicit and deny-wins, so a future broadening of PubAllow can never
		// silently expose the management/audit roots.
		PubDeny: []string{
			subjectVMM + ".>",
			subjectAudit + ".>",
		},
		SubAllow: []string{
			subjectSvc + "." + tok + ".>",
			vmInboxPrefix + "-" + tok + ".>",
		},
		SubDeny: []string{
			subjectVMM + ".>",
			"_INBOX.>", // the default inbox — deny so a VM cannot sniff another's replies
		},
		AllowResponses: true, // reply to jobs received on ape.svc.vm-<id>.>
	}
}

// VMInbox returns the scoped reply-inbox prefix for a per-VM credential
// (feed to nats.CustomInboxPrefix on the in-VM client). It matches the
// SubAllow entry VMGrant issues.
func VMInbox(vmID string) string { return vmInboxPrefix + "-" + VMToken(vmID) }

// OperatorGrant returns the scoped HOST_OPS grant for a host `ape` operator on
// a given node: publish the management verbs + discovery, subscribe its own
// replies + discovery + this node's interactive exec/attach session streams.
// This is the credential the local `ape` CLI presents to drive the vmm service.
// Account isolation already bars a TELEMETRY guest from this account entirely;
// this grant additionally scopes the operator to one node's management tree.
//
// The exec-session subtree (ape.vmm.<node>.exec.>) is in SubAllow because an
// interactive attach RECEIVES the process stdout/stderr/exit + stdin-credit
// grants on it (bulk stdio does not ride request/reply — PLAN-18 D2); the verb
// replies still come on _INBOX. Publishing stdin/resize/credit is already
// covered by the ape.vmm.<node>.> PubAllow. Scoped to this node's exec tree —
// not a broadening of management-verb access.
func OperatorGrant(node string) Grant {
	tok := natsconn.SubjectToken(node)
	return Grant{
		PubAllow: []string{
			subjectVMM + "." + tok + ".>",
			subjectSRV + ".>",
		},
		SubAllow: []string{
			"_INBOX.>",
			subjectVMM + "." + tok + ".exec.>",
			subjectSRV + ".>",
		},
	}
}

// serviceGrant is the HOST_OPS credential aped-front itself presents to run the
// vmm micro service (subscribe the management tree + discovery, respond on the
// requester's inbox). It is unrestricted within HOST_OPS — the account holds
// only aped's own trusted identities, so account isolation is the boundary and
// over-scoping the service cred within it buys nothing but micro-framework
// subject surprises. Kept as a distinct constructor for auditability.
func serviceGrant() Grant { return Grant{} } // empty allow lists → full access within its account

// telemetryIngestGrant is the TELEMETRY credential aped-front presents to
// ingest per-VM telemetry: subscribe every VM's ape.{evt,log,metrics}.<tok>.>
// read-only — it never publishes into the guest tree. The <tok> position is a
// single-token `*` wildcard (NATS wildcards are whole-token, so a literal
// "vm-*" segment would never match); within the isolated TELEMETRY account the
// only publishers are per-VM vm-<id> creds, so `*` carries only VM telemetry.
func telemetryIngestGrant() Grant {
	return Grant{
		SubAllow: []string{
			subjectEvt + ".*.>",
			subjectLog + ".*.>",
			subjectMetrics + ".*.>",
			"_INBOX.>",
		},
	}
}

// PermitsPublish reports whether this grant permits publishing to subject,
// applying default-deny + deny-wins exactly as the NATS server does: a match
// in Deny always wins; otherwise a match in a non-empty Allow is required; an
// empty Allow means "everything not denied".
func (g Grant) PermitsPublish(subject string) bool {
	return permits(g.PubAllow, g.PubDeny, subject)
}

// PermitsSubscribe reports whether this grant permits subscribing to subject
// (same default-deny + deny-wins semantics as PermitsPublish).
func (g Grant) PermitsSubscribe(subject string) bool {
	return permits(g.SubAllow, g.SubDeny, subject)
}

// permits is the shared default-deny + deny-wins decision. An empty allow list
// means unrestricted (subject only to deny); a non-empty allow list is a
// closed allow-list.
func permits(allow, deny []string, subject string) bool {
	for _, p := range deny {
		if subjectMatch(p, subject) {
			return false // deny always wins
		}
	}
	if len(allow) == 0 {
		return true // unrestricted within the account
	}
	for _, p := range allow {
		if subjectMatch(p, subject) {
			return true
		}
	}
	return false // default-deny
}

// subjectMatch reports whether a concrete NATS subject matches a subscription
// pattern using NATS wildcard rules: `*` matches exactly one token and `>`
// matches one or more trailing tokens. Both are split on ".".
func subjectMatch(pattern, subject string) bool {
	pt := strings.Split(pattern, ".")
	st := strings.Split(subject, ".")
	for i, p := range pt {
		if p == ">" {
			// `>` must match at least one remaining subject token.
			return i < len(st)
		}
		if i >= len(st) {
			return false // pattern longer than subject and no `>`
		}
		if p != "*" && p != st[i] {
			return false
		}
	}
	return len(pt) == len(st) // no `>`: token counts must be equal
}

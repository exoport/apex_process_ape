package aped

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// now is overridable in tests for deterministic timestamps.
var now = func() time.Time { return time.Now().UTC() }

// AuditRecord is the structured record aped writes per privileged op (PLAN-18
// D9 / Appendix A). It logs the RESOLVED request (canonical paths, image
// digest, device IDs) and the policy decision — the CVE lesson: record what
// will actually run, not the caller's summary. It complements the NATS auth log
// (who published) and kernel auditd (device access) for three-layer attribution.
//
//nolint:tagliatelle // snake_case matches the Appendix-A audit-record contract
type AuditRecord struct {
	TS           string         `json:"ts"`
	BoundaryPeer *BoundaryPeer  `json:"boundary_peer,omitempty"` // SO_PEERCRED of priv.sock
	Caller       string         `json:"caller"`                  // SubjectToken from NATS creds (front-attested)
	Op           string         `json:"op"`                      // CreateVM | StartVM | DestroyVM | …
	Resolved     ResolvedArgs   `json:"resolved"`
	Policy       PolicyDecision `json:"policy"`
	Outcome      Outcome        `json:"outcome"`
}

// BoundaryPeer is the SO_PEERCRED identity of the process that sent the command
// over the AF_UNIX priv socket (the de-privileged front-end, in normal
// operation).
type BoundaryPeer struct {
	UID uint32 `json:"uid"`
	PID uint32 `json:"pid"`
}

// ResolvedArgs is the concrete, fully-resolved argument set that was authorized
// and (attempted to be) run. Fields are omitempty so a non-create op logs only
// what applies.
//
//nolint:tagliatelle // snake_case matches the Appendix-A audit-record contract
type ResolvedArgs struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Image       string `json:"image,omitempty"`
	Mount       string `json:"mount,omitempty"`
	PCIBDF      string `json:"pci_bdf,omitempty"`
	USB         string `json:"usb,omitempty"`
	VMMUID      string `json:"vmm_uid,omitempty"`
}

// PolicyDecision records which rule fired and whether it allowed the op.
type PolicyDecision struct {
	Rule     string `json:"rule"`
	Decision string `json:"decision"` // allow | deny
}

// Outcome records the result of the op.
//
//nolint:tagliatelle // snake_case matches the Appendix-A audit-record contract
type Outcome struct {
	OK    bool   `json:"ok"`
	VMID  string `json:"vm_id,omitempty"`
	Error string `json:"error,omitempty"`
}

// Decision constants.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Auditor writes audit records to an append-only log and (optionally) forwards
// them over NATS on ape.audit.<node>.<event>. Both sinks are best-effort and
// serialized under a mutex; a nil sink is skipped. The append-only log's
// immutability (chattr +a) is applied out-of-band by the deployment — the
// executor runs with empty capabilities and cannot set it at runtime.
type Auditor struct {
	mu   sync.Mutex
	w    io.Writer
	pub  func(subject string, data []byte)
	node string
}

// NewAuditor builds an Auditor. w is the append-only log sink (nil to skip);
// pub forwards a record over NATS (nil to skip); node is the <node> subject
// token.
func NewAuditor(w io.Writer, pub func(subject string, data []byte), node string) *Auditor {
	return &Auditor{w: w, pub: pub, node: natsconn.SubjectToken(node)}
}

// Record stamps, serializes, and emits an audit record to both sinks. It never
// returns an error — auditing must not fail the op it records — but a write
// failure is itself surfaced on the next record's best effort (the file sink
// error is dropped; callers that need hard-fail auditing wrap the writer).
func (a *Auditor) Record(rec AuditRecord) {
	if rec.TS == "" {
		rec.TS = now().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.w != nil {
		_, _ = a.w.Write(append(data, '\n')) // JSONL
	}
	if a.pub != nil {
		a.pub(a.subject(rec.Op), data)
	}
}

// subject renders ape.audit.<node>.<event>, slugging the op into a single event
// token (so an op string can never inject extra subject levels).
func (a *Auditor) subject(op string) string {
	event := natsconn.SubjectToken(op)
	if event == "" {
		event = "op"
	}
	return fmt.Sprintf("%s.%s.%s", subjectAudit, a.node, event)
}

// OpenAuditLog opens (creating) the append-only audit log 0600. The parent dir
// must exist and be writable (ReadWritePaths=/var/log/aped in the unit).
func OpenAuditLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("aped: open audit log %s: %w", path, err)
	}
	return f, nil
}

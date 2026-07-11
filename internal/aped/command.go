package aped

import (
	"encoding/json"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// Op is the closed set of typed commands the de-privileged front-end may send
// the root executor over the priv socket (PLAN-18 D1). One per Backend verb;
// the executor accepts nothing else — no free-form request, no caller host
// path — and re-validates every one against policy before acting.
type Op string

const (
	OpCapabilities Op = "capabilities"
	OpCreate       Op = "create"
	OpStart        Op = "start"
	OpStop         Op = "stop"
	OpFreeze       Op = "freeze"
	OpUnfreeze     Op = "unfreeze"
	OpSuspend      Op = "suspend"
	OpResume       Op = "resume"
	OpExec         Op = "exec"
	OpAttach       Op = "attach"
	OpSnapshot     Op = "snapshot"
	OpList         Op = "list"
	OpInspect      Op = "inspect"
	OpDestroy      Op = "destroy"
)

// Command is one typed, fully-resolved request. Exactly one payload pointer is
// set for the ops that carry one; ID carries the workspace id for the id-verbs.
type Command struct {
	Op       Op                         `json:"op"`
	ID       string                     `json:"id,omitempty"`
	Create   *CreateCommand             `json:"create,omitempty"`
	Destroy  *workspace.DestroyRequest  `json:"destroy,omitempty"`
	Exec     *workspace.ExecRequest     `json:"exec,omitempty"`
	Attach   *AttachStreamCommand       `json:"attach,omitempty"`
	Snapshot *workspace.SnapshotRequest `json:"snapshot,omitempty"`
}

// AttachStreamCommand opens an interactive streamed process over the priv socket
// (PLAN-18 D2 OpAttach). Exactly one of Exec (a one-shot command with streamed
// stdio) or Attach (the interactive login shell) is set — the executor relays the
// containerd task's PTY over the connection instead of the one-shot reply.
type AttachStreamCommand struct {
	Exec   *workspace.ExecRequest   `json:"exec,omitempty"`
	Attach *workspace.AttachRequest `json:"attach,omitempty"`
}

// CreateCommand is the resolved create payload. The front resolves the thin
// wire CreateRequest into this fully-resolved spec (composed home, canonical
// mount, image, per-VM .creds bind) before it crosses the boundary (D1); the
// executor validates the concrete values and provisions them. Caller is the
// front-attested NATS identity, recorded in the audit log alongside the
// SO_PEERCRED peer.
type CreateCommand struct {
	Spec   sandbox.WorkspaceSpec `json:"spec"`
	Caller string                `json:"caller,omitempty"`
}

// Response is the executor's reply to one Command. Code "" means success and
// Payload carries the op-specific reply; otherwise Code is a workspace.Code*
// value the vmm handler renders via req.Error.
type Response struct {
	Code    string          `json:"code,omitempty"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// Audit carries the audit record(s) the executor emitted for this command.
	// The executor is network-less (RunExecutor builds its Auditor with a nil
	// NATS sink) and writes them to its own append-only file; it hands them back
	// here so the de-privileged front — which holds the NATS conn — can forward
	// them on ape.audit.<node>.> (PLAN-18 D9). Empty for read-only ops and for
	// the SO_PEERCRED-rejected path (that record stays file-only — a rejected
	// peer is never the front, so it must not be handed the audit trail).
	Audit []AuditRecord `json:"audit,omitempty"`
}

// encode/decode carry a resolved sandbox.WorkspaceSpec, whose fields are
// untagged — both ends use encoding/json on the same Go type, so it round-trips
// by field name; musttag is not applicable here.
//
//nolint:musttag // WorkspaceSpec crosses the priv socket by Go field name
func encodeCommand(c Command) ([]byte, error) { return json.Marshal(c) }

//nolint:musttag // WorkspaceSpec crosses the priv socket by Go field name
func decodeCommand(b []byte) (Command, error) {
	var c Command
	err := json.Unmarshal(b, &c)
	return c, err
}
func encodeResponse(r Response) ([]byte, error) { return json.Marshal(r) }
func decodeResponse(b []byte) (Response, error) {
	var r Response
	err := json.Unmarshal(b, &r)
	return r, err
}

// errorResponse renders a Backend error as a Response, mapping it to the frozen
// workspace.Code* set (unclassified → VALIDATION, the PLAN-14 catch-all).
func errorResponse(err error) Response {
	code := workspace.Code(err)
	if code == "" {
		code = workspace.CodeValidation
	}
	return Response{Code: code, Error: err.Error()}
}

// okResponse marshals a success payload.
func okResponse(v any) Response {
	data, err := json.Marshal(v)
	if err != nil {
		return errorResponse(err)
	}
	return Response{Payload: data}
}

// asError reconstructs a sentinel error from a Response Code, so the front-side
// privClient returns errors that classify with errors.Is (and the vmm handler
// re-derives the same wire code). An unrecognized code falls back to the raw
// message.
func (r Response) asError() error {
	if r.Code == "" {
		return nil
	}
	return codeError(r.Code, r.Error)
}

// codeError maps a wire Code back to its sentinel error (shared with the vmm
// NATS client), wrapped so workspace.Code re-derives the same code on the far
// side.
func codeError(code, msg string) error { return workspace.ErrorForCode(code, msg) }

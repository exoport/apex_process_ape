package aped

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/vmmstream"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// subjectRootVMM is the PLAN-18 management subject root; the endpoint group is
// subjectRootVMM.<node> (docs/reference/events.md).
const subjectRootVMM = "ape.vmm"

// VMMConfig configures the vmm micro service.
type VMMConfig struct {
	// Node is the <node> subject segment of ape.vmm.<node>.>; it is slugged to
	// a single subject token.
	Node string
	// Backend is the dispatch target for every verb. In aped-front this is the
	// AF_UNIX priv client (forwarding to the root executor); in tests it is an
	// in-memory fake. The service is transport-agnostic by construction.
	Backend workspace.Backend
	// NatsConn is the front's connection; the interactive attach bridge runs the
	// session's server side on it. nil disables interactive attach (attach.open →
	// UNSUPPORTED) — the one-shot verbs need no NATS handle of their own.
	NatsConn *nats.Conn
	// Socket is the priv socket the bridge dials for a streaming attach/exec.
	// Empty disables interactive attach.
	Socket string
	// Publish forwards the executor's attach open-audit record on
	// ape.audit.<node>.>. Mirrors the privClient's forwarding for the one leg
	// (OpAttach) that does not round-trip through the privClient. nil skips it.
	Publish func(subject string, data []byte)
}

// VMM is the ape.vmm NATS-micro service (PLAN-18 D2): one endpoint per
// workspace.Backend verb, dispatching to the configured Backend and returning
// errors via micro req.Error with the frozen workspace.Code* set. It mirrors
// the PLAN-14 job-daemon shape (internal/service) elevated to VM management.
type VMM struct {
	node    string
	backend workspace.Backend
	nc      *nats.Conn
	socket  string
	publish func(subject string, data []byte)
	session atomic.Uint64 // interactive attach session id counter
}

// NewVMM builds a vmm service dispatcher.
func NewVMM(cfg VMMConfig) *VMM {
	node := natsconn.SubjectToken(cfg.Node)
	if node == "" {
		node = "node"
	}
	return &VMM{node: node, backend: cfg.Backend, nc: cfg.NatsConn, socket: cfg.Socket, publish: cfg.Publish}
}

// Group returns the endpoint group subject, ape.vmm.<node>.
func (v *VMM) Group() string { return subjectRootVMM + "." + v.node }

// Register adds the endpoint group and every vmm endpoint onto svc. Endpoint
// subjects match the frozen contract exactly (docs/reference/events.md); they
// are additive-only.
func (v *VMM) Register(svc micro.Service) error {
	grp := svc.AddGroup(v.Group())
	endpoints := []struct {
		name, subject string
		h             micro.HandlerFunc
	}{
		{"capabilities", "capabilities", v.handleCapabilities},
		{"create", "create", v.handleCreate},
		{"start", "start", v.idVerb((*VMM).start)},
		{"stop", "stop", v.idVerb((*VMM).stop)},
		{"freeze", "freeze", v.idVerb((*VMM).freeze)},
		{"unfreeze", "unfreeze", v.idVerb((*VMM).unfreeze)},
		{"suspend", "suspend", v.idVerb((*VMM).suspend)},
		{"resume", "resume", v.idVerb((*VMM).resume)},
		{"exec", "exec", v.handleExec},
		{"attach-open", "attach.open", v.handleAttachOpen},
		{"snapshot", "snapshot", v.handleSnapshot},
		{"list", "list", v.handleList},
		{"inspect", "inspect", v.handleInspect},
		{"destroy", "destroy", v.handleDestroy},
	}
	for _, e := range endpoints {
		if err := grp.AddEndpoint(e.name, e.h, micro.WithEndpointSubject(e.subject)); err != nil {
			return fmt.Errorf("aped: add vmm endpoint %s: %w", e.subject, err)
		}
	}
	return nil
}

func (v *VMM) handleCapabilities(req micro.Request) {
	caps, err := v.backend.Capabilities(context.Background())
	if err != nil {
		v.respondErr(req, err)
		return
	}
	_ = req.RespondJSON(workspace.CapabilitiesReply{V: workspace.WireVersion, Capabilities: caps})
}

func (v *VMM) handleCreate(req micro.Request) {
	var r workspace.CreateRequest
	if !v.decode(req, &r) {
		return
	}
	ws, err := v.backend.Create(context.Background(), r)
	if err != nil {
		v.respondErr(req, err)
		return
	}
	_ = req.RespondJSON(workspace.CreateReply{V: workspace.WireVersion, Workspace: ws})
}

// idVerb adapts one of the id-only Backend verbs (start/stop/freeze/unfreeze/
// suspend/resume) into a handler: decode {id}, call fn, ack OKReply.
func (v *VMM) idVerb(fn func(*VMM, context.Context, string) error) micro.HandlerFunc {
	return func(req micro.Request) {
		var r workspace.IDRequest
		if !v.decode(req, &r) {
			return
		}
		if strings.TrimSpace(r.ID) == "" {
			_ = req.Error(workspace.CodeValidation, "id is required", nil)
			return
		}
		// micro.Request carries no context; Background is the only option.
		if err := fn(v, context.Background(), r.ID); err != nil {
			v.respondErr(req, err)
			return
		}
		_ = req.RespondJSON(workspace.OKReply{V: workspace.WireVersion, OK: true})
	}
}

func (v *VMM) start(ctx context.Context, id string) error    { return v.backend.Start(ctx, id) }
func (v *VMM) stop(ctx context.Context, id string) error     { return v.backend.Stop(ctx, id) }
func (v *VMM) freeze(ctx context.Context, id string) error   { return v.backend.Freeze(ctx, id) }
func (v *VMM) unfreeze(ctx context.Context, id string) error { return v.backend.Unfreeze(ctx, id) }
func (v *VMM) suspend(ctx context.Context, id string) error  { return v.backend.Suspend(ctx, id) }
func (v *VMM) resume(ctx context.Context, id string) error   { return v.backend.Resume(ctx, id) }

func (v *VMM) handleDestroy(req micro.Request) {
	var r workspace.DestroyReq
	if !v.decode(req, &r) {
		return
	}
	if strings.TrimSpace(r.ID) == "" {
		_ = req.Error(workspace.CodeValidation, "id is required", nil)
		return
	}
	if err := v.backend.Destroy(context.Background(), r.ID, r.DestroyRequest); err != nil {
		v.respondErr(req, err)
		return
	}
	_ = req.RespondJSON(workspace.OKReply{V: workspace.WireVersion, OK: true})
}

func (v *VMM) handleExec(req micro.Request) {
	var r workspace.ExecReq
	if !v.decode(req, &r) {
		return
	}
	if strings.TrimSpace(r.ID) == "" || len(r.Cmd) == 0 {
		_ = req.Error(workspace.CodeValidation, "id and cmd are required", nil)
		return
	}
	status, err := v.backend.Exec(context.Background(), r.ID, r.ExecRequest)
	if err != nil {
		v.respondErr(req, err)
		return
	}
	_ = req.RespondJSON(workspace.ExecReply{V: workspace.WireVersion, ExitStatus: status})
}

// handleAttachOpen opens an interactive session and returns its id + the subject
// prefix the client streams over (D2). It is the pivot of the exec/attach bridge:
// it dials a streaming priv connection to the executor (which starts the
// containerd task PTY), stands up the vmmstream server session over that
// connection, and — only after the server has subscribed every inbound channel —
// answers with the prefix, so the client starts once the server is listening
// (output is credit-gated until the client primes). The session then runs until
// the process exits.
func (v *VMM) handleAttachOpen(req micro.Request) {
	var r workspace.AttachOpenReq
	if !v.decode(req, &r) {
		return
	}
	if strings.TrimSpace(r.ID) == "" {
		_ = req.Error(workspace.CodeValidation, "id is required", nil)
		return
	}
	if v.nc == nil || v.socket == "" {
		v.respondErr(req, fmt.Errorf("%w: interactive attach is not available on this node", workspace.ErrUnsupported))
		return
	}

	// Open the streamed process on the executor (a Cmd opens a streamed exec; else
	// the login shell). The open-audit record rides the handshake Response — on
	// both success and a policy/exec failure, so a denied attach is forwarded too
	// (matching the one-shot verbs' deny-forwarding).
	conn, audit, err := openExecStream(v.socket, Command{Op: OpAttach, ID: r.ID, Attach: attachStreamFromReq(r.AttachRequest)})
	forwardAuditRecords(v.publish, v.node, audit)
	if err != nil {
		v.respondErr(req, err)
		return
	}

	sid := fmt.Sprintf("s%d", v.session.Add(1))
	prefix := fmt.Sprintf("%s.exec.%s", v.Group(), sid)
	sess, err := vmmstream.NewServerSession(v.nc, prefix, connToProcess(conn), 0)
	if err != nil {
		_ = conn.Close()
		v.respondErr(req, err)
		return
	}
	_ = v.nc.Flush() // the server is fully subscribed before the client is told

	_ = req.RespondJSON(workspace.AttachOpenReply{
		V:             workspace.WireVersion,
		SessionID:     sid,
		SubjectPrefix: prefix,
	})
	go func() {
		defer func() { _ = conn.Close() }()
		code, runErr := sess.Run(context.Background())
		// Forward a completion record so live audit consumers see the session end,
		// not just its open. The OPEN record (executor-attested: SO_PEERCRED peer +
		// policy) stays the authoritative security event on ape.audit.<node>.<op>;
		// this correlated <op>Exit record is the front's operational outcome notice.
		forwardAuditRecords(v.publish, v.node, completionAudit(audit, r.ID, code, runErr))
	}()
}

// completionAudit derives the session-completion record the front forwards when
// an interactive exec/attach ends. The executor's OPEN record (open[0],
// SO_PEERCRED-attested with its policy decision) is the authoritative security
// event; this reuses that identity, marks a distinct <op>Exit event, and carries
// the observed outcome — a clean exit code, or the teardown error when the
// session was reaped (an abandoned client) rather than exiting on its own.
func completionAudit(open []AuditRecord, id string, code int, runErr error) []AuditRecord {
	base := AuditRecord{Op: "AttachVM", Resolved: ResolvedArgs{WorkspaceID: id}}
	if len(open) > 0 {
		base = open[0]
	}
	outcome := Outcome{OK: true, VMID: id}
	switch {
	case runErr != nil:
		outcome = Outcome{OK: false, VMID: id, Error: "session ended: " + runErr.Error()}
	case code != 0:
		outcome = Outcome{OK: false, VMID: id, Error: fmt.Sprintf("exited with code %d", code)}
	}
	return []AuditRecord{{
		TS:           now().Format(time.RFC3339Nano),
		BoundaryPeer: base.BoundaryPeer,
		Caller:       base.Caller,
		Op:           base.Op + "Exit",
		Resolved:     base.Resolved,
		Policy:       base.Policy,
		Outcome:      outcome,
	}}
}

// attachStreamFromReq maps the wire AttachRequest to the executor's stream
// command: a non-empty Cmd opens a streamed exec, otherwise the login shell.
func attachStreamFromReq(r workspace.AttachRequest) *AttachStreamCommand {
	if len(r.Cmd) > 0 {
		return &AttachStreamCommand{Exec: &workspace.ExecRequest{Cmd: r.Cmd, TTY: r.TTY, Env: nil}}
	}
	return &AttachStreamCommand{Attach: &r}
}

func (v *VMM) handleSnapshot(req micro.Request) {
	var r workspace.SnapshotReq
	if !v.decode(req, &r) {
		return
	}
	if strings.TrimSpace(r.ID) == "" {
		_ = req.Error(workspace.CodeValidation, "id is required", nil)
		return
	}
	ref, err := v.backend.Snapshot(context.Background(), r.ID, r.SnapshotRequest)
	if err != nil {
		v.respondErr(req, err)
		return
	}
	_ = req.RespondJSON(workspace.SnapshotReply{V: workspace.WireVersion, SnapshotRef: ref})
}

func (v *VMM) handleList(req micro.Request) {
	list, err := v.backend.List(context.Background())
	if err != nil {
		v.respondErr(req, err)
		return
	}
	_ = req.RespondJSON(workspace.ListReply{V: workspace.WireVersion, Workspaces: list})
}

func (v *VMM) handleInspect(req micro.Request) {
	var r workspace.IDRequest
	if !v.decode(req, &r) {
		return
	}
	if strings.TrimSpace(r.ID) == "" {
		_ = req.Error(workspace.CodeValidation, "id is required", nil)
		return
	}
	status, err := v.backend.Inspect(context.Background(), r.ID)
	if err != nil {
		v.respondErr(req, err)
		return
	}
	_ = req.RespondJSON(workspace.InspectReply{V: workspace.WireVersion, Status: status})
}

// decode unmarshals the request body into dst, answering VALIDATION on malformed
// JSON. Returns false when it already answered (the caller must return).
func (v *VMM) decode(req micro.Request, dst any) bool {
	if err := json.Unmarshal(req.Data(), dst); err != nil {
		_ = req.Error(workspace.CodeValidation, "malformed request JSON: "+err.Error(), nil)
		return false
	}
	return true
}

// respondErr renders a Backend error as a micro req.Error using the frozen
// workspace.Code* set. An unclassified error (a runner/exec failure not wrapped
// in a sentinel) maps to VALIDATION — the same catch-all the PLAN-14 daemon
// uses for operational failures, since the frozen code set has no INTERNAL.
func (v *VMM) respondErr(req micro.Request, err error) {
	code := workspace.Code(err)
	if code == "" {
		code = workspace.CodeValidation
	}
	_ = req.Error(code, err.Error(), nil)
}

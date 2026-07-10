package aped

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// Provisioner performs the one privileged act the executor cannot express as a
// Backend verb: provisioning a fully-resolved spec (runner.Provision + registry
// write). It is injected so the executor is testable without containerd/Kata
// (the production impl wraps sandbox.Runner + Registry — see NewShellProvisioner).
type Provisioner func(ctx context.Context, spec sandbox.WorkspaceSpec) (workspace.Workspace, error)

// Executor is the network-less root command server (PLAN-18 D1, `aped run`). It
// serves the AF_UNIX priv socket, gates every connection on SO_PEERCRED,
// re-validates every resolved command against policy, drives the workspace
// Backend (+ Provisioner for create), and writes an append-only audit record
// per privileged op. It holds no network address family beyond AF_UNIX.
type Executor struct {
	backend     workspace.Backend // id-verbs + list/inspect/capabilities
	provision   Provisioner       // resolved-spec create
	policy      *Policy
	auditor     *Auditor
	allowedUIDs map[uint32]bool
	node        string

	mu sync.Mutex // serializes dispatch (registry writes are not concurrency-safe)
}

// ExecutorConfig configures NewExecutor.
type ExecutorConfig struct {
	Backend     workspace.Backend
	Provision   Provisioner
	Policy      *Policy
	Auditor     *Auditor
	AllowedUIDs []uint32 // peer uids permitted over the priv socket (the aped-front uid)
	Node        string
}

// NewExecutor builds an Executor. A nil Auditor is replaced with a no-op one.
func NewExecutor(cfg ExecutorConfig) *Executor {
	allowed := make(map[uint32]bool, len(cfg.AllowedUIDs))
	for _, uid := range cfg.AllowedUIDs {
		allowed[uid] = true
	}
	auditor := cfg.Auditor
	if auditor == nil {
		auditor = NewAuditor(nil, nil, cfg.Node)
	}
	return &Executor{
		backend:     cfg.Backend,
		provision:   cfg.Provision,
		policy:      cfg.Policy,
		auditor:     auditor,
		allowedUIDs: allowed,
		node:        cfg.Node,
	}
}

// Serve accepts and handles connections until ctx is cancelled or l fails.
// Cancelling ctx closes the listener to unblock Accept.
func (e *Executor) Serve(ctx context.Context, l privListener) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // clean shutdown: ctx cancel closed the listener
			}
			return fmt.Errorf("aped: priv accept: %w", err)
		}
		go e.handleConn(ctx, conn)
	}
}

// handleConn processes one connection: SO_PEERCRED gate → one command → one
// response. It always reads the inbound packet before replying — closing a
// SEQPACKET socket with unread inbound data resets the connection and clobbers
// the reply, so even a rejected peer's command is drained first. The connection
// carries a single request/reply (streaming exec is a future addition).
func (e *Executor) handleConn(ctx context.Context, conn privConn) {
	defer func() { _ = conn.Close() }()

	peer, err := conn.Peer()
	if err != nil {
		return // cannot verify the peer → drop silently
	}

	// Drain the inbound command (bounded, so a silent peer can't hang us). The
	// bytes are not decoded until the peer is authorized.
	_ = conn.SetReadDeadline(now().Add(30 * time.Second))
	raw, err := conn.Recv()
	if err != nil {
		return
	}

	if !peerAllowed(peer, e.allowedUIDs) {
		e.auditor.Record(AuditRecord{
			BoundaryPeer: &BoundaryPeer{UID: peer.UID, PID: peer.PID},
			Op:           "RejectPeer",
			Policy:       PolicyDecision{Rule: "so_peercred", Decision: DecisionDeny},
			Outcome:      Outcome{OK: false, Error: "peer uid not authorized"},
		})
		e.send(conn, Response{Code: workspace.CodeDenied, Error: "priv peer not authorized"})
		return
	}

	cmd, err := decodeCommand(raw)
	if err != nil {
		e.send(conn, errorResponse(fmt.Errorf("%w: malformed command: %s", workspace.ErrValidation, err.Error())))
		return
	}

	e.mu.Lock()
	resp := e.dispatch(ctx, cmd, peer)
	e.mu.Unlock()
	e.send(conn, resp)
}

func (e *Executor) send(conn privConn, resp Response) {
	data, err := encodeResponse(resp)
	if err != nil {
		data, _ = encodeResponse(errorResponse(err))
	}
	_ = conn.Send(data)
}

// dispatch executes one command. Every mutating op is audited; read-only ops
// (capabilities/list/inspect) are not.
func (e *Executor) dispatch(ctx context.Context, cmd Command, peer Peer) Response {
	switch cmd.Op {
	case OpCapabilities:
		caps, err := e.backend.Capabilities(ctx)
		return respondValue(caps, err)
	case OpCreate:
		return e.doCreate(ctx, cmd, peer)
	case OpStart:
		return e.mutate(peer, "StartVM", cmd.ID, func() error { return e.backend.Start(ctx, cmd.ID) })
	case OpStop:
		return e.mutate(peer, "StopVM", cmd.ID, func() error { return e.backend.Stop(ctx, cmd.ID) })
	case OpFreeze:
		return e.mutate(peer, "FreezeVM", cmd.ID, func() error { return e.backend.Freeze(ctx, cmd.ID) })
	case OpUnfreeze:
		return e.mutate(peer, "UnfreezeVM", cmd.ID, func() error { return e.backend.Unfreeze(ctx, cmd.ID) })
	case OpSuspend:
		return e.mutate(peer, "SuspendVM", cmd.ID, func() error { return e.backend.Suspend(ctx, cmd.ID) })
	case OpResume:
		return e.mutate(peer, "ResumeVM", cmd.ID, func() error { return e.backend.Resume(ctx, cmd.ID) })
	case OpExec:
		if cmd.Exec == nil {
			return errorResponse(fmt.Errorf("%w: exec command missing payload", workspace.ErrValidation))
		}
		status, err := e.backend.Exec(ctx, cmd.ID, *cmd.Exec)
		e.audit(peer, "", "ExecVM", ResolvedArgs{WorkspaceID: cmd.ID}, decisionFor(err), outcomeFor(err, cmd.ID))
		return respondValue(status, err)
	case OpSnapshot:
		req := workspace.SnapshotRequest{}
		if cmd.Snapshot != nil {
			req = *cmd.Snapshot
		}
		ref, err := e.backend.Snapshot(ctx, cmd.ID, req)
		return respondValue(ref, err)
	case OpDestroy:
		req := workspace.DestroyRequest{}
		if cmd.Destroy != nil {
			req = *cmd.Destroy
		}
		return e.mutate(peer, "DestroyVM", cmd.ID, func() error { return e.backend.Destroy(ctx, cmd.ID, req) })
	case OpList:
		list, err := e.backend.List(ctx)
		return respondValue(list, err)
	case OpInspect:
		status, err := e.backend.Inspect(ctx, cmd.ID)
		return respondValue(status, err)
	default:
		return errorResponse(fmt.Errorf("%w: unknown op %q", workspace.ErrValidation, cmd.Op))
	}
}

// doCreate re-validates the resolved spec against policy (the executor's
// authoritative check — the CVE lesson) before provisioning, and audits both
// the decision and the outcome.
func (e *Executor) doCreate(ctx context.Context, cmd Command, peer Peer) Response {
	if cmd.Create == nil {
		return errorResponse(fmt.Errorf("%w: create command missing payload", workspace.ErrValidation))
	}
	spec := cmd.Create.Spec
	caller := cmd.Create.Caller
	resolved := auditResolved(spec)

	count := 0
	if list, err := e.backend.List(ctx); err == nil {
		count = len(list)
	}
	if err := e.policy.CheckCreate(resolvedCreateFromSpec(spec), count); err != nil {
		e.audit(peer, caller, "CreateVM", resolved, DecisionDeny, Outcome{OK: false, Error: err.Error()})
		return errorResponse(err)
	}

	ws, err := e.provision(ctx, spec)
	outcome := Outcome{OK: err == nil}
	if err != nil {
		outcome.Error = err.Error()
	} else {
		outcome.VMID = ws.ID
	}
	e.audit(peer, caller, "CreateVM", resolved, DecisionAllow, outcome)
	if err != nil {
		return errorResponse(err)
	}
	return okResponse(ws)
}

// mutate runs a mutating id-verb, audits it, and returns an OK/typed-error
// response.
func (e *Executor) mutate(peer Peer, op, id string, fn func() error) Response {
	if id == "" {
		return errorResponse(fmt.Errorf("%w: id is required", workspace.ErrValidation))
	}
	err := fn()
	e.audit(peer, "", op, ResolvedArgs{WorkspaceID: id}, decisionFor(err), outcomeFor(err, id))
	if err != nil {
		return errorResponse(err)
	}
	return okResponse(workspace.OKReply{V: workspace.WireVersion, OK: true})
}

func (e *Executor) audit(peer Peer, caller, op string, resolved ResolvedArgs, decision string, outcome Outcome) {
	e.auditor.Record(AuditRecord{
		BoundaryPeer: &BoundaryPeer{UID: peer.UID, PID: peer.PID},
		Caller:       caller,
		Op:           op,
		Resolved:     resolved,
		Policy:       PolicyDecision{Rule: "policy:" + op, Decision: decision},
		Outcome:      outcome,
	})
}

// peerAllowed is the SO_PEERCRED gate: strict uid membership, default-deny (an
// empty set rejects everyone). No implicit root allowance — the executor is
// configured with exactly the aped-front uid, so the gate is testable
// regardless of the test process's own uid.
func peerAllowed(peer Peer, allowed map[uint32]bool) bool {
	return allowed[peer.UID]
}

// resolvedCreateFromSpec derives the policy-check input from a resolved spec.
func resolvedCreateFromSpec(spec sandbox.WorkspaceSpec) ResolvedCreate {
	mount := ""
	if spec.Mount == sandbox.MountHostFS {
		mount = spec.ProjectRoot
	}
	return ResolvedCreate{Image: spec.Image, MountPath: mount, Devices: nil}
}

// auditResolved builds the audit args for a create from its spec.
func auditResolved(spec sandbox.WorkspaceSpec) ResolvedArgs {
	mount := ""
	if spec.Mount == sandbox.MountHostFS {
		mount = spec.ProjectRoot
	}
	return ResolvedArgs{WorkspaceID: spec.Name, Image: spec.Image, Mount: mount}
}

func decisionFor(err error) string {
	if errors.Is(err, workspace.ErrPolicyDenied) {
		return DecisionDeny
	}
	return DecisionAllow // the command was authorized; err (if any) is operational
}

func outcomeFor(err error, id string) Outcome {
	if err != nil {
		return Outcome{OK: false, VMID: id, Error: err.Error()}
	}
	return Outcome{OK: true, VMID: id}
}

// respondValue marshals a success value or renders the error.
func respondValue(v any, err error) Response {
	if err != nil {
		return errorResponse(err)
	}
	return okResponse(v)
}

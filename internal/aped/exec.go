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

// NetnsProvider is the executor's view of the privileged network helper
// (internal/netd, PLAN-21 D3): the one thing the hardened executor cannot do
// itself. EnsureNetns wires a workspace's egress namespace and returns its PATH —
// the executor only ever handles that string, never a socket, netlink handle, or
// namespace of its own. reuse asks for an existing link to be returned untouched.
type NetnsProvider interface {
	EnsureNetns(ctx context.Context, workspace string, proxyPort int, reuse bool) (string, error)
	DeleteNetns(ctx context.Context, workspace string) error
}

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
	registry    *sandbox.Registry // usage stamping (PLAN-22 D5b); nil disables it
	allowedUIDs map[uint32]bool
	node        string
	netns       NetnsProvider

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
	// Registry, when set, is stamped with a workspace's last use on exec/attach/start
	// so an operator can tell a busy workspace from an abandoned one. It is the SAME
	// registry the driver writes; the executor is the only place that sees every
	// mutating verb, which is why the stamp lives here.
	Registry *sandbox.Registry
	// Netns is the privileged network helper. Nil means this executor cannot
	// provide egress: a create whose spec was granted egress is REFUSED rather than
	// provisioned networkless behind a proxy env var that would silently hang.
	Netns NetnsProvider
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
		netns:       cfg.Netns,
		registry:    cfg.Registry,
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

	// Interactive exec/attach takes over the connection as a bidirectional stream
	// (PLAN-18 D2) — a long-lived relay, not the one-shot dispatch. It must NOT
	// hold e.mu (which serializes registry writes): an interactive session lasts
	// minutes and would block every other op.
	if cmd.Op == OpAttach {
		// An interactive session is the strongest "in use" signal there is, so stamp it
		// at open rather than at exit: a session that is still running matters most.
		e.touchUsage(cmd.ID, nil)
		e.handleStream(ctx, conn, cmd, peer)
		return
	}

	e.mu.Lock()
	resp, records := e.dispatch(ctx, cmd, peer)
	e.mu.Unlock()
	// Hand the records the executor just wrote to its append-only file back to
	// the front so it can forward them on ape.audit.<node>.> — the executor is
	// itself network-less (PLAN-18 D1/D9).
	resp.Audit = records
	e.send(conn, resp)
}

func (e *Executor) send(conn privConn, resp Response) {
	data, err := encodeResponse(resp)
	if err != nil {
		data, _ = encodeResponse(errorResponse(err))
	}
	_ = conn.Send(data)
}

// dispatch executes one command, returning the response plus the audit
// record(s) it emitted (for the front to forward on ape.audit.<node>.>). Every
// mutating op is audited; read-only ops (capabilities/list/inspect/snapshot)
// are not, and return a nil record slice.
func (e *Executor) dispatch(ctx context.Context, cmd Command, peer Peer) (Response, []AuditRecord) {
	switch cmd.Op {
	case OpCapabilities:
		caps, err := e.backend.Capabilities(ctx)
		return respondValue(caps, err), nil
	case OpCreate:
		return e.doCreate(ctx, cmd, peer)
	case OpStart:
		return e.mutate(peer, "StartVM", cmd.ID, func() error {
			err := e.backend.Start(ctx, cmd.ID)
			e.touchUsage(cmd.ID, err)
			return err
		})
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
			return errorResponse(fmt.Errorf("%w: exec command missing payload", workspace.ErrValidation)), nil
		}
		status, err := e.backend.Exec(ctx, cmd.ID, *cmd.Exec)
		e.touchUsage(cmd.ID, err)
		rec := e.audit(peer, "", "ExecVM", ResolvedArgs{WorkspaceID: cmd.ID}, decisionFor(err), outcomeFor(err, cmd.ID))
		return respondValue(status, err), []AuditRecord{rec}
	case OpSnapshot:
		req := workspace.SnapshotRequest{}
		if cmd.Snapshot != nil {
			req = *cmd.Snapshot
		}
		ref, err := e.backend.Snapshot(ctx, cmd.ID, req)
		return respondValue(ref, err), nil
	case OpDestroy:
		req := workspace.DestroyRequest{}
		if cmd.Destroy != nil {
			req = *cmd.Destroy
		}
		return e.mutate(peer, "DestroyVM", cmd.ID, func() error {
			err := e.backend.Destroy(ctx, cmd.ID, req)
			// Tear the egress namespace down whether or not the container went away
			// cleanly: a leaked netns would hold an address lease and a bridge port.
			// Unknown/absent workspaces are a no-op in the helper.
			if e.netns != nil {
				if derr := e.netns.DeleteNetns(ctx, cmd.ID); derr != nil {
					e.auditor.Note("netns teardown for " + cmd.ID + ": " + derr.Error())
				}
			}
			return err
		})
	case OpList:
		list, err := e.backend.List(ctx)
		return respondValue(list, err), nil
	case OpInspect:
		status, err := e.backend.Inspect(ctx, cmd.ID)
		return respondValue(status, err), nil
	default:
		return errorResponse(fmt.Errorf("%w: unknown op %q", workspace.ErrValidation, cmd.Op)), nil
	}
}

// doCreate re-validates the resolved spec against policy (the executor's
// authoritative check — the CVE lesson) before provisioning, and audits both
// the decision and the outcome. It returns the single audit record it emits so
// the front forwards it (a denied create is exactly what an audit consumer
// needs, so the deny record is returned too).
func (e *Executor) doCreate(ctx context.Context, cmd Command, peer Peer) (Response, []AuditRecord) {
	if cmd.Create == nil {
		return errorResponse(fmt.Errorf("%w: create command missing payload", workspace.ErrValidation)), nil
	}
	spec := cmd.Create.Spec
	caller := cmd.Create.Caller
	resolved := auditResolved(spec)

	count := 0
	if list, err := e.backend.List(ctx); err == nil {
		count = len(list)
	}
	if err := e.policy.CheckCreate(resolvedCreateFromSpec(spec), count); err != nil {
		rec := e.audit(peer, caller, "CreateVM", resolved, DecisionDeny, Outcome{OK: false, Error: err.Error()})
		return errorResponse(err), []AuditRecord{rec}
	}

	// Egress was authorized above; now wire it. The netns is created by the
	// privileged helper AFTER the policy decision and BEFORE provisioning, so a
	// denied create never touches the network — and the spec the driver receives
	// carries only a path (PLAN-21 D3/D4).
	if err := e.attachEgressNetns(ctx, &spec); err != nil {
		rec := e.audit(peer, caller, "CreateVM", resolved, DecisionAllow, Outcome{OK: false, Error: err.Error()})
		return errorResponse(err), []AuditRecord{rec}
	}

	ws, err := e.provision(ctx, spec)
	if err != nil {
		// A failed provision must not leave a dangling namespace behind.
		e.releaseEgressNetns(ctx, spec)
	}
	outcome := Outcome{OK: err == nil}
	if err != nil {
		outcome.Error = err.Error()
	} else {
		outcome.VMID = ws.ID
	}
	rec := e.audit(peer, caller, "CreateVM", resolved, DecisionAllow, outcome)
	if err != nil {
		return errorResponse(err), []AuditRecord{rec}
	}
	return okResponse(ws), []AuditRecord{rec}
}

// touchUsage stamps a workspace's last-use time after a verb that means someone
// actually worked in it. Failures are swallowed on purpose: losing a usage stamp is a
// worse-report problem, and must never fail the exec or start the user asked for.
func (e *Executor) touchUsage(id string, opErr error) {
	if e.registry == nil || opErr != nil || id == "" {
		return
	}
	if err := e.registry.Touch(id, now().Format(time.RFC3339)); err != nil {
		e.auditor.Note("usage stamp for " + id + ": " + err.Error())
	}
}

// attachEgressNetns asks the privileged helper for the workspace's pre-wired
// network namespace and records its path on the spec. A spec without granted
// egress is left untouched (the workspace stays networkless). A spec WITH granted
// egress on an executor that has no helper is refused: provisioning it would hand
// the guest an HTTPS_PROXY it cannot route to, which fails as a hang rather than
// an error.
func (e *Executor) attachEgressNetns(ctx context.Context, spec *sandbox.WorkspaceSpec) error {
	if !spec.HasEgress() {
		return nil
	}
	if e.netns == nil {
		return fmt.Errorf("%w: egress was granted but this executor has no network helper "+
			"(start aped-netd.service and pass --netd-socket)", workspace.ErrUnsupported)
	}
	_, port, err := sandbox.ParseProxyHostPort(spec.HTTPSProxy)
	if err != nil {
		return fmt.Errorf("%w: %w", workspace.ErrValidation, err)
	}
	path, err := e.netns.EnsureNetns(ctx, spec.Name, port, false)
	if err != nil {
		return err
	}
	spec.NetnsPath = path
	return nil
}

// releaseEgressNetns removes a namespace wired for a create that then failed.
func (e *Executor) releaseEgressNetns(ctx context.Context, spec sandbox.WorkspaceSpec) {
	if e.netns == nil || spec.NetnsPath == "" {
		return
	}
	if err := e.netns.DeleteNetns(ctx, spec.Name); err != nil {
		e.auditor.Note("netns rollback for " + spec.Name + ": " + err.Error())
	}
}

// mutate runs a mutating id-verb, audits it, and returns an OK/typed-error
// response plus the single audit record it emitted.
func (e *Executor) mutate(peer Peer, op, id string, fn func() error) (Response, []AuditRecord) {
	if id == "" {
		return errorResponse(fmt.Errorf("%w: id is required", workspace.ErrValidation)), nil
	}
	err := fn()
	rec := e.audit(peer, "", op, ResolvedArgs{WorkspaceID: id}, decisionFor(err), outcomeFor(err, id))
	if err != nil {
		return errorResponse(err), []AuditRecord{rec}
	}
	return okResponse(workspace.OKReply{V: workspace.WireVersion, OK: true}), []AuditRecord{rec}
}

// audit records one privileged op and returns the stamped record (so the caller
// can attach it to the priv Response for front-side NATS forwarding).
func (e *Executor) audit(peer Peer, caller, op string, resolved ResolvedArgs, decision string, outcome Outcome) AuditRecord {
	return e.auditor.Record(AuditRecord{
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
	return ResolvedCreate{
		Image: spec.Image, MountPath: mount, Devices: nil,
		EgressDomains: spec.EgressDomains,
		Mounts:        spec.Mounts,
	}
}

// auditResolved builds the audit args for a create from its spec. The granted
// egress allowlist is part of the record: "which domains did this VM get" is
// exactly what an auditor needs, and it is a resolved value, not a request.
func auditResolved(spec sandbox.WorkspaceSpec) ResolvedArgs {
	mount := ""
	if spec.Mount == sandbox.MountHostFS {
		mount = spec.ProjectRoot
	}
	return ResolvedArgs{
		WorkspaceID: spec.Name, Image: spec.Image, Mount: mount,
		EgressDomains: spec.EgressDomains,
	}
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

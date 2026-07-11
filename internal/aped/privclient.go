package aped

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// privClient is the de-privileged front-end's view of the root executor: a
// workspace.Backend that forwards each verb over the AF_UNIX priv socket
// (PLAN-18 D1). The vmm micro service dispatches to this, so the same service
// code runs against the fake Backend in tests and the real executor in prod.
//
// Create is the one verb that resolves before forwarding: the thin wire
// CreateRequest is turned into a fully-resolved spec (compose/proxy/mint) here,
// de-privileged, and only the resolved spec crosses the boundary.
type privClient struct {
	socket  string
	resolve sandbox.SpecResolver
	publish func(subject string, data []byte) // nil → no ape.audit.<node>.> forwarding
	node    string                            // slugged <node> for the audit subject
}

// PrivClientConfig configures NewPrivClient.
type PrivClientConfig struct {
	// Socket is the AF_UNIX priv socket the executor serves.
	Socket string
	// Resolve turns a wire CreateRequest into a fully-resolved spec (front-side,
	// de-privileged). A nil resolver fails Create with ErrValidation.
	Resolve sandbox.SpecResolver
	// Publish forwards an executor-returned audit record on ape.audit.<node>.>.
	// The executor is network-less, so it hands back the records it wrote to its
	// own append-only file (Response.Audit); the front — which holds the NATS
	// conn — publishes them here (PLAN-18 D9). Nil skips forwarding.
	Publish func(subject string, data []byte)
	// Node is the <node> token stamped into forwarded audit subjects.
	Node string
}

// NewPrivClient returns a workspace.Backend that forwards to the executor over
// the priv socket, optionally forwarding executor audit records over NATS.
func NewPrivClient(cfg PrivClientConfig) workspace.Backend {
	return &privClient{
		socket:  cfg.Socket,
		resolve: cfg.Resolve,
		publish: cfg.Publish,
		node:    natsconn.SubjectToken(cfg.Node),
	}
}

var _ workspace.Backend = (*privClient)(nil)

// roundTrip sends one command and reads one response over a fresh connection,
// forwarding any audit records the executor returned before handing the
// response back (so a denied/failed op's record is forwarded too).
func (p *privClient) roundTrip(cmd Command) (Response, error) {
	conn, err := dialPriv(p.socket)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = conn.Close() }()
	data, err := encodeCommand(cmd)
	if err != nil {
		return Response{}, err
	}
	if err := conn.Send(data); err != nil {
		return Response{}, err
	}
	raw, err := conn.Recv()
	if err != nil {
		return Response{}, err
	}
	resp, err := decodeResponse(raw)
	if err != nil {
		return Response{}, err
	}
	p.forwardAudit(resp.Audit)
	return resp, nil
}

// forwardAudit publishes the executor-returned audit records for this one-shot
// verb on ape.audit.<node>.<event>.
func (p *privClient) forwardAudit(records []AuditRecord) {
	forwardAuditRecords(p.publish, p.node, records)
}

// forwardAuditRecords publishes each executor-returned audit record on
// ape.audit.<node>.<event>. It is a no-op without a publish sink; marshal
// failures are dropped — audit forwarding must never fail the op it records.
// Shared by the privClient (one-shot verbs) and the vmm attach bridge (the
// streamed OpAttach, whose open record rides the handshake Response).
func forwardAuditRecords(publish func(subject string, data []byte), node string, records []AuditRecord) {
	if publish == nil {
		return
	}
	for i := range records {
		data, err := json.Marshal(records[i])
		if err != nil {
			continue
		}
		publish(auditSubject(node, records[i].Op), data)
	}
}

// call round-trips cmd, maps a Code response to a sentinel error, and unmarshals
// the success payload into out (out may be nil).
func (p *privClient) call(cmd Command, out any) error {
	resp, err := p.roundTrip(cmd)
	if err != nil {
		return err
	}
	if e := resp.asError(); e != nil {
		return e
	}
	if out != nil && len(resp.Payload) > 0 {
		return json.Unmarshal(resp.Payload, out)
	}
	return nil
}

func (p *privClient) Capabilities(context.Context) (workspace.Capabilities, error) {
	var caps workspace.Capabilities
	err := p.call(Command{Op: OpCapabilities}, &caps)
	return caps, err
}

func (p *privClient) Create(ctx context.Context, req workspace.CreateRequest) (workspace.Workspace, error) {
	if p.resolve == nil {
		return workspace.Workspace{}, fmt.Errorf("%w: front-end has no spec resolver", workspace.ErrValidation)
	}
	spec, err := p.resolve(ctx, req)
	if err != nil {
		return workspace.Workspace{}, err
	}
	var ws workspace.Workspace
	err = p.call(Command{Op: OpCreate, Create: &CreateCommand{Spec: spec}}, &ws)
	return ws, err
}

func (p *privClient) Start(_ context.Context, id string) error {
	return p.call(Command{Op: OpStart, ID: id}, nil)
}

func (p *privClient) Stop(_ context.Context, id string) error {
	return p.call(Command{Op: OpStop, ID: id}, nil)
}

func (p *privClient) Freeze(_ context.Context, id string) error {
	return p.call(Command{Op: OpFreeze, ID: id}, nil)
}

func (p *privClient) Unfreeze(_ context.Context, id string) error {
	return p.call(Command{Op: OpUnfreeze, ID: id}, nil)
}

func (p *privClient) Suspend(_ context.Context, id string) error {
	return p.call(Command{Op: OpSuspend, ID: id}, nil)
}

func (p *privClient) Resume(_ context.Context, id string) error {
	return p.call(Command{Op: OpResume, ID: id}, nil)
}

func (p *privClient) Destroy(_ context.Context, id string, req workspace.DestroyRequest) error {
	return p.call(Command{Op: OpDestroy, ID: id, Destroy: &req}, nil)
}

func (p *privClient) Exec(_ context.Context, id string, req workspace.ExecRequest) (workspace.ExitStatus, error) {
	var st workspace.ExitStatus
	err := p.call(Command{Op: OpExec, ID: id, Exec: &req}, &st)
	return st, err
}

func (p *privClient) Snapshot(_ context.Context, id string, req workspace.SnapshotRequest) (workspace.SnapshotRef, error) {
	var ref workspace.SnapshotRef
	err := p.call(Command{Op: OpSnapshot, ID: id, Snapshot: &req}, &ref)
	return ref, err
}

func (p *privClient) List(context.Context) ([]workspace.Workspace, error) {
	var list []workspace.Workspace
	err := p.call(Command{Op: OpList}, &list)
	return list, err
}

func (p *privClient) Inspect(_ context.Context, id string) (workspace.Status, error) {
	var st workspace.Status
	err := p.call(Command{Op: OpInspect, ID: id}, &st)
	return st, err
}

// Attach/Logs/Events ride bulk streams, not the single request/reply priv
// round-trip. Interactive attach is wired end-to-end (NATS session subjects →
// PTY) under Tier-2; here they are unsupported.
func (p *privClient) Attach(context.Context, string, workspace.AttachRequest, workspace.Stream) (workspace.ExitStatus, error) {
	return workspace.ExitStatus{}, workspace.ErrUnsupported
}

func (p *privClient) Logs(context.Context, string, workspace.LogsRequest) (io.ReadCloser, error) {
	return nil, workspace.ErrUnsupported
}

func (p *privClient) Events(context.Context) (<-chan workspace.Event, error) {
	return nil, workspace.ErrUnsupported
}

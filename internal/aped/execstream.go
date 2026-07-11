package aped

import (
	"context"
	"fmt"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// openExecStream is the FRONT side of the OpAttach handshake: it dials a
// streaming priv connection, sends the command, and reads the executor's single
// ack/err Response. On success it returns the live connection (ready for
// connToProcess) plus the open-audit records the executor emitted; on error it
// closes the connection. Everything after the ack is stream frames.
func openExecStream(socket string, cmd Command) (privConn, []AuditRecord, error) {
	conn, err := dialPriv(socket)
	if err != nil {
		return nil, nil, err
	}
	data, err := encodeCommand(cmd)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := conn.Send(data); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	raw, err := conn.Recv()
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	resp, err := decodeResponse(raw)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if e := resp.asError(); e != nil {
		_ = conn.Close()
		return nil, resp.Audit, e
	}
	return conn, resp.Audit, nil
}

// handleStream serves an interactive exec/attach on the priv connection (PLAN-18
// D2 OpAttach). It opens the process on the InteractiveBackend (the containerd
// driver — a Kata task exec with a PTY), acknowledges the open with one final
// request/reply Response (carrying the audit record the front forwards), then
// hands the connection to relayProcessToConn, which streams the PTY frames until
// the process exits. The one-shot dispatch path never sees this connection.
//
// The executor stays authoritative: it type-asserts its own backend (so a
// non-interactive shellDriver fails closed with UNSUPPORTED) and audits the open
// exactly as the request/reply exec verb does — the same SO_PEERCRED-gated peer,
// resolved id, policy decision, and outcome.
func (e *Executor) handleStream(ctx context.Context, conn privConn, cmd Command, peer Peer) {
	ib, ok := e.backend.(sandbox.InteractiveBackend)
	if !ok {
		e.send(conn, errorResponse(fmt.Errorf("%w: interactive attach requires the containerd driver", workspace.ErrUnsupported)))
		return
	}
	if cmd.ID == "" || cmd.Attach == nil || (cmd.Attach.Exec == nil && cmd.Attach.Attach == nil) {
		e.send(conn, errorResponse(fmt.Errorf("%w: attach requires an id and an exec or attach payload", workspace.ErrValidation)))
		return
	}

	op := "AttachVM"
	var (
		proc workspace.Process
		err  error
	)
	if cmd.Attach.Exec != nil {
		op = "ExecVM"
		proc, err = ib.OpenExec(ctx, cmd.ID, *cmd.Attach.Exec)
	} else {
		proc, err = ib.OpenAttach(ctx, cmd.ID, *cmd.Attach.Attach)
	}
	if err != nil {
		rec := e.audit(peer, "", op, ResolvedArgs{WorkspaceID: cmd.ID}, decisionFor(err), outcomeFor(err, cmd.ID))
		resp := errorResponse(err)
		resp.Audit = []AuditRecord{rec}
		e.send(conn, resp)
		return
	}

	// Handshake OK. The open is a privileged op, so audit it and attach the record
	// to this final Response — the front forwards it on ape.audit.<node>.>. From
	// here the connection carries only stream frames (the exit code reaches the
	// client on the session's exit channel, not another Response).
	rec := e.audit(peer, "", op, ResolvedArgs{WorkspaceID: cmd.ID}, DecisionAllow, Outcome{OK: true, VMID: cmd.ID})
	ack, aerr := encodeResponse(Response{Audit: []AuditRecord{rec}})
	if aerr != nil {
		e.send(conn, errorResponse(aerr))
		return
	}
	if conn.Send(ack) != nil {
		return // the front went away before streaming began
	}

	// An interactive session can idle for long stretches; drop the recv deadline
	// the one-shot path armed so a quiet stdin does not tear the stream down.
	_ = conn.SetReadDeadline(time.Time{})
	_, _ = relayProcessToConn(ctx, conn, proc)
}

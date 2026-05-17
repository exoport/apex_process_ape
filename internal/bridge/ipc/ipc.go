// Package ipc carries NDJSON messages between the parent ape process
// (`ape chat` / `ape pipeline`) and the bridge subprocess (`ape mcp-bridge`)
// over a single TCP connection.
//
// The wire shape is documented in docs/reference/bridge-ipc.md. The
// IPC abstraction is small enough that a future plan can swap TCP+NDJSON
// for stdlib WebSocket or NATS-embedded without touching the SSE broker
// or the MCP runtime — keep the Message struct authoritative.
//
// Ported from https://github.com/diegosz/claude_mcp_bridge_poc, commit 4e542d0 (MIT),
// extended for PLAN-5 / C3 with new frame types (call, hook, step-bind, stop)
// and a JSON Payload escape hatch for opaque hook envelopes.
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
)

// Frame types. Documented in PLAN-5 / C3 — IPC wire table.
const (
	TypeReady     = "ready"     // bridge → parent (handshake)
	TypeMessage   = "message"   // parent → bridge (wake await_message)
	TypeReply     = "reply"     // bridge → parent (skill called reply())
	TypeCall      = "call"      // bridge → parent (mirror of every MCP tool call)
	TypeHook      = "hook"      // `ape notify` → parent (hook envelope forwarded)
	TypeStepBind  = "step-bind" // parent → bridge (session_id → step mapping)
	TypeStop      = "stop"      // parent → bridge (SIGTERM about to fire; clean shutdown)
	TypeBufferOvf = "buffer-overflow"
)

// Message is the canonical IPC frame. Fields are populated per the
// `Type` discriminator; consumers should switch on Type and treat
// unrelated fields as best-effort. Payload is reserved for opaque
// blobs (hook envelopes) where the parent does not need to interpret
// the inner shape.
type Message struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Event     string          `json:"event,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	Step      string          `json:"step,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	ID        string          `json:"id,omitempty"` // pairs deferred await_message entries with their flush
}

// Write encodes msg as a single newline-terminated JSON line on w.
// Errors are returned to the caller; the PoC swallowed them. PLAN-5
// surfaces them so the parent can log + close.
func Write(w io.Writer, msg Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ipc: marshal: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", b); err != nil {
		return fmt.Errorf("ipc: write: %w", err)
	}
	return nil
}

// Read blocks calling fn for each NDJSON line on conn until EOF or
// scanner error. Malformed lines are skipped (the PoC behaviour); a
// scanner error is returned so the caller can mark the bridge errored
// and emit the SSE `error` event per PLAN-5 / C3 "bridge crash" path.
func Read(conn net.Conn, fn func(Message)) error {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		fn(m)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("ipc: scan: %w", err)
	}
	return nil
}

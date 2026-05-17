// Package mcp implements the MCP server spawned by Claude Code as a
// stdio subprocess via `ape mcp-bridge`. It speaks MCP JSON-RPC 2.0
// over stdin/stdout and relays to/from the parent ape process over a
// TCP IPC connection (see internal/bridge/ipc).
//
// Two tools are exposed to Claude Code:
//
//   - await_message: deferred response — holds the pending request ID
//     until the parent delivers a message via IPC, then responds. Up
//     to one pending await at a time; concurrent calls return "" immediately.
//     Unsolicited inbound messages buffer in a 5-element FIFO; the
//     next await drains the head with no wait. PLAN-5 / C3.
//
//   - reply: non-blocking — sends the content over IPC; the parent
//     publishes an SSE `reply` event for the browser. PLAN-5 / C3.
//
// Bridge also mirrors every MCP tool call to the parent (TypeCall) so
// bridge-calls.jsonl can be written by the parent process. PLAN-5 / C6.
//
// Ported from https://github.com/diegosz/claude_mcp_bridge_poc, commit 4e542d0 (MIT).
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
)

// DefaultAwaitTimeout is the cap on await_message blocks. 240s sits
// safely under the 5-minute prompt-cache TTL so a long wait does not
// dissolve the cached context for the surrounding session. PLAN-5 / C3.
const DefaultAwaitTimeout = 240 * time.Second

// MessageBufferCap is the FIFO size for unsolicited inbound messages
// (parent → bridge `message` frames that arrive while no await is
// pending). Drops oldest on overflow (LRU) and emits a TypeBufferOvf
// IPC frame so the parent can record `buffer-overflow` in
// bridge-calls.jsonl. PLAN-5 / C3.
const MessageBufferCap = 5

// Options configures the bridge runtime. The subcommand reads
// APE_IPC_PORT from env; callers (notably tests) can override via
// IPCAddr.
type Options struct {
	// IPCAddr is the TCP address of the parent's IPC listener. If
	// empty, the bridge reads APE_IPC_PORT from env and dials
	// 127.0.0.1:<port>.
	IPCAddr string
	// Stdin / Stdout are the MCP transport. Defaults to os.Stdin /
	// os.Stdout. Tests inject pipes.
	Stdin  io.Reader
	Stdout io.Writer
	// Stderr is where errors land. Defaults to os.Stderr.
	Stderr io.Writer
	// AwaitTimeout overrides DefaultAwaitTimeout. Used by tests.
	AwaitTimeout time.Duration
	// Now is the clock; defaults to time.Now. Used by tests.
	Now func() time.Time
}

// Run executes the MCP bridge against opts. Returns when stdin closes
// or ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.AwaitTimeout == 0 {
		opts.AwaitTimeout = DefaultAwaitTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	addr := opts.IPCAddr
	if addr == "" {
		port := os.Getenv("APE_IPC_PORT")
		if port == "" {
			return errors.New("mcp.Run: APE_IPC_PORT not set and IPCAddr empty")
		}
		if _, err := strconv.Atoi(port); err != nil {
			return fmt.Errorf("mcp.Run: APE_IPC_PORT %q is not an integer", port)
		}
		addr = "127.0.0.1:" + port
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("mcp.Run: ipc dial %s: %w", addr, err)
	}
	defer conn.Close()

	b := &bridge{
		opts:    opts,
		conn:    conn,
		writeMu: &sync.Mutex{},
		stdin:   bufio.NewScanner(opts.Stdin),
	}
	b.stdin.Buffer(make([]byte, 256*1024), 256*1024)

	return b.run(ctx)
}

type bridge struct {
	opts Options
	conn net.Conn

	writeMu *sync.Mutex
	stdin   *bufio.Scanner

	// Pending await_message slot. At most one outstanding.
	pendingMu   sync.Mutex
	pendingID   json.RawMessage
	pendingDead time.Time

	// FIFO buffer of unsolicited messages (PLAN-5 / C3).
	bufMu  sync.Mutex
	buffer []string

	// Session id learned from `step-bind` frames or first hook
	// envelope; tagged onto every TypeCall so the parent can join
	// against the in-memory sessionID → step table without an extra
	// lookup. Optional.
	sessionMu sync.Mutex
	sessionID string
}

func (b *bridge) run(ctx context.Context) error {
	// IPC reader: deliver queued messages to the pending await_message,
	// or buffer if none pending.
	ipcErr := make(chan error, 1)
	go func() {
		ipcErr <- ipc.Read(b.conn, b.onIPC)
	}()

	// Timeout sweeper.
	doneCh := make(chan struct{})
	go b.sweepTimeouts(ctx, doneCh)
	defer close(doneCh)

	// MCP scanner loop — never blocks; deferred responses happen above.
	for b.stdin.Scan() {
		line := b.stdin.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := b.handleLine(line); err != nil {
			fmt.Fprintf(b.opts.Stderr, "mcp.bridge: handle: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if err := b.stdin.Err(); err != nil {
		return fmt.Errorf("mcp.bridge: stdin scan: %w", err)
	}
	// stdin closed cleanly — drain any final IPC error and return.
	select {
	case err := <-ipcErr:
		if err != nil {
			fmt.Fprintf(b.opts.Stderr, "mcp.bridge: ipc reader: %v\n", err)
		}
	default:
	}
	return nil
}

func (b *bridge) onIPC(msg ipc.Message) {
	switch msg.Type {
	case ipc.TypeMessage:
		b.deliver(msg.Content)
	case ipc.TypeStepBind:
		b.sessionMu.Lock()
		b.sessionID = msg.SessionID
		b.sessionMu.Unlock()
	case ipc.TypeStop:
		// Parent is shutting us down; closing the conn here is a no-op
		// because defer in Run handles it. Future hook: signal MCP
		// loop to exit by writing a synthetic line. For now we rely on
		// the parent SIGTERMing claude, which closes our stdin.
	}
}

// deliver hands content to the pending await_message slot if one
// exists, otherwise buffers it (FIFO, cap MessageBufferCap, LRU on
// overflow with a `buffer-overflow` IPC frame).
func (b *bridge) deliver(content string) {
	b.pendingMu.Lock()
	id := b.pendingID
	b.pendingID = nil
	b.pendingMu.Unlock()
	if id != nil {
		b.respondAwait(id, content)
		return
	}
	b.bufMu.Lock()
	defer b.bufMu.Unlock()
	if len(b.buffer) >= MessageBufferCap {
		dropped := b.buffer[0]
		b.buffer = b.buffer[1:]
		// Best-effort IPC notification; ignore write errors.
		_ = ipc.Write(b.conn, ipc.Message{Type: ipc.TypeBufferOvf, Content: dropped})
	}
	b.buffer = append(b.buffer, content)
}

func (b *bridge) sweepTimeouts(ctx context.Context, done <-chan struct{}) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			b.pendingMu.Lock()
			expired := b.pendingID != nil && b.opts.Now().After(b.pendingDead)
			var id json.RawMessage
			if expired {
				id = b.pendingID
				b.pendingID = nil
			}
			b.pendingMu.Unlock()
			if expired {
				b.respondAwait(id, "")
			}
		}
	}
}

func (b *bridge) handleLine(line []byte) error {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return nil //nolint:nilerr // PoC behaviour: skip malformed lines silently
	}

	// Notifications: no id, no response.
	switch req.Method {
	case "notifications/initialized":
		return ipc.Write(b.conn, ipc.Message{Type: ipc.TypeReady})
	}
	if req.ID == nil {
		return nil
	}

	switch req.Method {
	case "initialize":
		b.respond(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ape-mcp-bridge", "version": "0.1.0"},
		})
		b.mirrorCall(req.ID, "initialize", req.Params, nil)

	case "ping":
		b.respond(req.ID, map[string]any{})

	case "tools/list":
		b.respond(req.ID, map[string]any{"tools": toolsList()})

	case "tools/call":
		var params struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			b.respond(req.ID, map[string]any{})
			return nil //nolint:nilerr // malformed call: respond empty, do not error
		}
		switch params.Name {
		case "await_message":
			b.callAwait(req.ID, params.Arguments)
		case "reply":
			b.callReply(req.ID, params.Arguments)
		default:
			b.respond(req.ID, map[string]any{})
		}
	default:
		b.respond(req.ID, map[string]any{})
	}
	return nil
}

func (b *bridge) callAwait(id json.RawMessage, args map[string]json.RawMessage) {
	timeout := b.opts.AwaitTimeout
	if raw, ok := args["timeout_seconds"]; ok {
		var t int
		if json.Unmarshal(raw, &t) == nil && t > 0 {
			timeout = time.Duration(t) * time.Second
		}
	}
	// Mirror the deferred-call entry; the eventual flush emits another TypeCall.
	b.mirrorCall(id, "await_message", json.RawMessage(`{"deferred":true}`), nil)

	// Drain the FIFO buffer first.
	b.bufMu.Lock()
	if len(b.buffer) > 0 {
		head := b.buffer[0]
		b.buffer = b.buffer[1:]
		b.bufMu.Unlock()
		b.respondAwait(id, head)
		return
	}
	b.bufMu.Unlock()

	b.pendingMu.Lock()
	if b.pendingID != nil {
		// Concurrent call — reject immediately.
		b.pendingMu.Unlock()
		b.respondAwait(id, "")
		return
	}
	b.pendingID = id
	b.pendingDead = b.opts.Now().Add(timeout)
	b.pendingMu.Unlock()
}

func (b *bridge) callReply(id json.RawMessage, args map[string]json.RawMessage) {
	var content string
	_ = json.Unmarshal(args["content"], &content)
	_ = ipc.Write(b.conn, ipc.Message{Type: ipc.TypeReply, Content: content})
	b.respond(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "sent"}},
		"isError": false,
	})
	b.mirrorCall(id, "reply", marshalRaw(map[string]any{"content": content}),
		marshalRaw(map[string]any{"sent": true}))
}

func (b *bridge) respondAwait(id json.RawMessage, text string) {
	b.respond(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": false,
	})
	// Mirror the flush so bridge-calls.jsonl records await_message
	// resolution paired by ID with the deferred entry above.
	b.mirrorCall(id, "await_message", json.RawMessage(`{"flush":true}`),
		marshalRaw(map[string]any{"text": text, "timeout": text == ""}))
}

func (b *bridge) respond(id json.RawMessage, result any) {
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
	bts, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(b.opts.Stderr, "mcp.bridge: marshal response: %v\n", err)
		return
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if _, err := fmt.Fprintf(b.opts.Stdout, "%s\n", bts); err != nil {
		fmt.Fprintf(b.opts.Stderr, "mcp.bridge: write response: %v\n", err)
	}
}

func (b *bridge) mirrorCall(id json.RawMessage, tool string, params, result json.RawMessage) {
	frame := ipc.Message{
		Type:   ipc.TypeCall,
		Tool:   tool,
		Params: params,
		Result: result,
		ID:     string(id),
	}
	b.sessionMu.Lock()
	frame.SessionID = b.sessionID
	b.sessionMu.Unlock()
	_ = ipc.Write(b.conn, frame)
}

// marshalRaw is a Marshal that returns json.RawMessage. On error
// (which shouldn't happen for our shapes) it returns null.
func marshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

func toolsList() []any {
	return []any{
		map[string]any{
			"name":        "await_message",
			"description": "Block until a message arrives from the Web UI. Returns the message text, or empty string on timeout. Call this in a loop to handle the interactive session.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Seconds to wait before returning an empty string. Default 240 (under the 5-minute prompt-cache TTL).",
						"default":     int(DefaultAwaitTimeout / time.Second),
					},
				},
			},
		},
		map[string]any{
			"name":        "reply",
			"description": "Send a message to the Web UI immediately.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "Text to display in the Web UI.",
					},
				},
				"required": []string{"content"},
			},
		},
	}
}

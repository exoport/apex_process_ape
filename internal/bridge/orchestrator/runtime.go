package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
)

// BridgeRuntime owns the mode-agnostic core of the bridge orchestrator:
// the IPC listener that accepts the bridge subprocess + many `ape
// notify` connections, frame routing (TypeReply / TypeCall / TypeHook
// / TypeBufferOvf), the stop-callback registry, the bridge-ready
// handshake, and a pub-sub event channel that consumers (web broker,
// TUI observer, stdout writer) subscribe to.
//
// PLAN-6 / Phase B: factored out of Hub so that web mode composes
// `BridgeRuntime + broker + page`, TUI mode composes `BridgeRuntime
// + Bubble Tea observer`, and `none` UI mode composes `BridgeRuntime
// + stdout writer`. The broker (HTTP/SSE) stays strictly web-only;
// BridgeRuntime starts no HTTP listener.
type BridgeRuntime struct {
	opts BridgeRuntimeOptions

	ipcLn   net.Listener
	ipcAddr string
	ipcPort int

	bridgeMu      sync.Mutex
	bridgeConn    net.Conn
	bridgeReadyCh chan struct{}
	bridgeReadyOk bool

	stopMu sync.Mutex
	stopFn func()

	subsMu sync.Mutex
	subs   []chan RuntimeEvent

	closeOnce sync.Once
}

// BridgeRuntimeOptions configures a BridgeRuntime. The direct-callback
// fields (OnReply / OnCall / OnHook) are invoked synchronously on the
// accept-loop goroutine and exist for consumers (e.g., the pipeline
// runner's runlog writer) that want push-style frame access without
// going through Subscribe.
//
// Subscribers (Subscribe()) receive every RuntimeEvent asynchronously
// via channels, including the await_message specializations the Hub
// used to derive directly from TypeCall frames. Both paths fire for
// every relevant frame.
type BridgeRuntimeOptions struct {
	OnReply func(content string)
	OnCall  func(ToolCall)
	OnHook  func(HookEvent)
}

// RuntimeEventKind discriminates the values RuntimeEvent can carry.
type RuntimeEventKind int

const (
	// RuntimeEventReply — bridge delivered a TypeReply frame (a user-
	// supplied response to await_message). Reply field is set.
	RuntimeEventReply RuntimeEventKind = iota
	// RuntimeEventCall — bridge delivered a TypeCall frame (an MCP
	// tool invocation). Call field is set.
	RuntimeEventCall
	// RuntimeEventHook — bridge delivered a TypeHook frame (a Claude
	// Code hook event forwarded by `ape notify`). Hook field is set.
	RuntimeEventHook
	// RuntimeEventAwaitPending — bridge delivered a TypeCall frame for
	// the `await_message` tool that is opening a deferred wait. No
	// payload; presence is the signal.
	RuntimeEventAwaitPending
	// RuntimeEventAwaitResolved — bridge delivered a TypeCall frame
	// for `await_message` that is flushing a queued reply. No payload.
	RuntimeEventAwaitResolved
	// RuntimeEventBufferOverflow — bridge surfaced a buffer-overflow
	// notice; presented as a synthetic ToolCall in the Call field for
	// consistency with web-mode display today.
	RuntimeEventBufferOverflow
)

// RuntimeEvent is the unit of fan-out from BridgeRuntime to consumers.
// Only the field for the matching Kind is populated.
type RuntimeEvent struct {
	Kind  RuntimeEventKind
	Reply string
	Call  *ToolCall
	Hook  *HookEvent
}

// NewBridgeRuntime constructs an unstarted runtime.
func NewBridgeRuntime(opts BridgeRuntimeOptions) *BridgeRuntime {
	return &BridgeRuntime{
		opts:          opts,
		bridgeReadyCh: make(chan struct{}),
	}
}

// Listen reserves the IPC TCP port on 127.0.0.1. Idempotent.
// ctx scopes the bind operation; the listener itself lives until
// Serve returns.
func (r *BridgeRuntime) Listen(ctx context.Context) error {
	if r.ipcLn != nil {
		return nil
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("runtime.Listen: ipc: %w", err)
	}
	r.ipcLn = ln
	r.ipcAddr = ln.Addr().String()
	_, portStr, _ := net.SplitHostPort(r.ipcAddr)
	r.ipcPort, _ = strconv.Atoi(portStr)
	return nil
}

// Serve runs the IPC accept loop until ctx cancels. Blocks. Multiple
// Serve calls are idempotent at the Listen level — only the first
// caller's goroutine accepts; subsequent callers see the loop exit
// when ctx ends.
func (r *BridgeRuntime) Serve(ctx context.Context) error {
	if r.ipcLn == nil {
		if err := r.Listen(ctx); err != nil {
			return err
		}
	}
	go r.acceptLoop()
	<-ctx.Done()
	r.closeOnce.Do(func() {
		_ = r.ipcLn.Close()
		r.closeAllSubscribers()
	})
	return ctx.Err()
}

// IPCPort returns the IPC TCP port. Used to populate APE_IPC_PORT.
func (r *BridgeRuntime) IPCPort() int { return r.ipcPort }

// BridgeReady returns a channel that closes when the bridge subprocess
// sends its first TypeReady frame.
func (r *BridgeRuntime) BridgeReady() <-chan struct{} { return r.bridgeReadyCh }

// SendStepBind tells the bridge which step a session id maps to.
func (r *BridgeRuntime) SendStepBind(sessionID, step string) {
	r.bridgeMu.Lock()
	conn := r.bridgeConn
	r.bridgeMu.Unlock()
	if conn == nil {
		return
	}
	_ = ipc.Write(conn, ipc.Message{Type: ipc.TypeStepBind, SessionID: sessionID, Step: step})
}

// SendMessage forwards a user-supplied message back to the bridge as
// a TypeMessage frame. The web broker calls this on POST /api/send;
// the TUI await-reply input calls it directly.
func (r *BridgeRuntime) SendMessage(content string) {
	r.bridgeMu.Lock()
	conn := r.bridgeConn
	r.bridgeMu.Unlock()
	if conn == nil {
		return
	}
	_ = ipc.Write(conn, ipc.Message{Type: ipc.TypeMessage, Content: content})
}

// SetStopFn registers a callback that fires when RequestStop is called.
// The pipeline runner cancels its run context inside the callback.
func (r *BridgeRuntime) SetStopFn(fn func()) {
	r.stopMu.Lock()
	r.stopFn = fn
	r.stopMu.Unlock()
}

// RequestStop invokes the registered stop callback, if any. The web
// broker calls this on POST /api/stop; the TUI's stop modal calls it
// directly.
func (r *BridgeRuntime) RequestStop() {
	r.stopMu.Lock()
	fn := r.stopFn
	r.stopMu.Unlock()
	if fn != nil {
		fn()
	}
}

// Subscribe returns a channel that receives every RuntimeEvent. The
// channel is buffered (32 slots) and the runtime drops events when a
// subscriber falls behind to avoid backpressure into the accept loop;
// drops are silent. Callers should drain quickly. The returned channel
// is closed when the runtime's Serve context cancels.
func (r *BridgeRuntime) Subscribe() <-chan RuntimeEvent {
	ch := make(chan RuntimeEvent, 32)
	r.subsMu.Lock()
	r.subs = append(r.subs, ch)
	r.subsMu.Unlock()
	return ch
}

func (r *BridgeRuntime) publish(evt RuntimeEvent) {
	r.subsMu.Lock()
	subs := make([]chan RuntimeEvent, len(r.subs))
	copy(subs, r.subs)
	r.subsMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
			// drop on full subscriber buffer
		}
	}
}

func (r *BridgeRuntime) closeAllSubscribers() {
	r.subsMu.Lock()
	defer r.subsMu.Unlock()
	for _, ch := range r.subs {
		close(ch)
	}
	r.subs = nil
}

func (r *BridgeRuntime) acceptLoop() {
	for {
		conn, err := r.ipcLn.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}
		go r.handleConn(conn)
	}
}

func (r *BridgeRuntime) handleConn(conn net.Conn) {
	defer conn.Close()
	isBridge := false
	_ = ipc.Read(conn, func(m ipc.Message) {
		if m.Type == ipc.TypeReady && !isBridge {
			isBridge = true
			r.bridgeMu.Lock()
			r.bridgeConn = conn
			r.bridgeMu.Unlock()
			if !r.bridgeReadyOk {
				r.bridgeReadyOk = true
				close(r.bridgeReadyCh)
			}
		}
		r.dispatch(m)
	})
	if isBridge {
		r.bridgeMu.Lock()
		if r.bridgeConn == conn {
			r.bridgeConn = nil
		}
		r.bridgeMu.Unlock()
	}
}

func (r *BridgeRuntime) dispatch(m ipc.Message) {
	switch m.Type {
	case ipc.TypeReply:
		if r.opts.OnReply != nil {
			r.opts.OnReply(m.Content)
		}
		r.publish(RuntimeEvent{Kind: RuntimeEventReply, Reply: m.Content})
	case ipc.TypeCall:
		call := ToolCall{
			ID:        m.ID,
			Tool:      m.Tool,
			Params:    m.Params,
			Result:    m.Result,
			SessionID: m.SessionID,
			At:        time.Now().UTC(),
		}
		if r.opts.OnCall != nil {
			r.opts.OnCall(call)
		}
		r.publish(RuntimeEvent{Kind: RuntimeEventCall, Call: &call})
		if m.Tool == "await_message" {
			if isDeferredEntry(m.Params) {
				r.publish(RuntimeEvent{Kind: RuntimeEventAwaitPending})
			} else if isFlushEntry(m.Params) {
				r.publish(RuntimeEvent{Kind: RuntimeEventAwaitResolved})
			}
		}
	case ipc.TypeHook:
		evt := HookEvent{
			Event:     m.Event,
			SessionID: m.SessionID,
			AgentID:   m.AgentID,
			Step:      m.Step,
			Payload:   m.Payload,
			At:        time.Now().UTC(),
		}
		if r.opts.OnHook != nil {
			r.opts.OnHook(evt)
		}
		r.publish(RuntimeEvent{Kind: RuntimeEventHook, Hook: &evt})
	case ipc.TypeBufferOvf:
		call := ToolCall{
			Tool:   "buffer-overflow",
			Params: json.RawMessage([]byte(strconv.Quote(m.Content))),
			At:     time.Now().UTC(),
		}
		if r.opts.OnCall != nil {
			r.opts.OnCall(call)
		}
		r.publish(RuntimeEvent{Kind: RuntimeEventBufferOverflow, Call: &call})
	}
}

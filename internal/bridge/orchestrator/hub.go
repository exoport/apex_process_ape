package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/broker"
	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
)

// Hub is the orchestrator pieces that exist independently of any one
// `claude` invocation: the broker, the IPC listener that accepts the
// bridge subprocess + many `ape notify` connections, and the frame-
// routing logic. `ape pipeline` (web mode) embeds a Hub for the
// duration of a whole pipeline run; each step spawns its own claude.
// `ape chat` uses Session, which wraps a Hub plus single-cmd lifecycle.
// PLAN-5 / C1 (pipeline web mode integration).
type Hub struct {
	opts HubOptions

	broker     *broker.Broker
	brokerAddr string

	ipcLn   net.Listener
	ipcAddr string
	ipcPort int

	bridgeMu      sync.Mutex
	bridgeConn    net.Conn
	bridgeReadyCh chan struct{}
	bridgeReadyOk bool

	stopMu sync.Mutex
	stopFn func()

	closeOnce sync.Once
}

// HubOptions configures a Hub. The fields mirror the subset of
// Session.Options that don't involve spawning claude.
type HubOptions struct {
	PageHTML         string
	MountExtras      func(mux *http.ServeMux)
	FragmentRenderer FragmentRenderer
	ReplayEvents     func() []broker.Event

	// OnReply / OnCall / OnHook receive IPC frames decoded by the
	// hub's accept loop. The caller writes them to runlog.Writer.
	OnReply func(content string)
	OnCall  func(ToolCall)
	OnHook  func(HookEvent)
}

// NewHub constructs an unstarted Hub.
func NewHub(opts HubOptions) *Hub {
	return &Hub{
		opts:          opts,
		bridgeReadyCh: make(chan struct{}),
	}
}

// Listen reserves IPC + broker ports. Idempotent. Returns the web UI URL.
func (h *Hub) Listen() (string, error) {
	if h.ipcLn != nil {
		return "http://" + h.brokerAddr + "/", nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("hub.Listen: ipc: %w", err)
	}
	h.ipcLn = ln
	h.ipcAddr = ln.Addr().String()
	_, portStr, _ := net.SplitHostPort(h.ipcAddr)
	h.ipcPort, _ = strconv.Atoi(portStr)

	h.broker = broker.New(broker.Options{
		PageHTML:     h.opts.PageHTML,
		OnSend:       h.handleSend,
		OnStop:       h.requestStop,
		ReplayEvents: h.replayEvents,
		Mux:          h.opts.MountExtras,
	})
	addr, err := h.broker.Listen()
	if err != nil {
		h.ipcLn.Close()
		h.ipcLn = nil
		return "", fmt.Errorf("hub.Listen: broker: %w", err)
	}
	h.brokerAddr = addr
	return "http://" + addr + "/", nil
}

// Serve runs the broker and the IPC accept loop in goroutines. Blocks
// until ctx cancels. Multiple Serve calls are idempotent — only the
// first attaches handlers.
func (h *Hub) Serve(ctx context.Context) error {
	if h.ipcLn == nil {
		if _, err := h.Listen(); err != nil {
			return err
		}
	}
	go h.acceptLoop()

	brokerErr := make(chan error, 1)
	go func() { brokerErr <- h.broker.Serve(ctx) }()

	select {
	case <-ctx.Done():
		h.closeOnce.Do(func() { _ = h.ipcLn.Close() })
		<-brokerErr
		return ctx.Err()
	case err := <-brokerErr:
		h.closeOnce.Do(func() { _ = h.ipcLn.Close() })
		return err
	}
}

// IPCPort returns the IPC TCP port. Used to populate APE_IPC_PORT and
// APE_BRIDGE_PORT in inline configs.
func (h *Hub) IPCPort() int { return h.ipcPort }

// BrokerPort returns the HTTP broker port. Used by `ape sessions`.
func (h *Hub) BrokerPort() int {
	if h.broker == nil {
		return 0
	}
	_, portStr, _ := net.SplitHostPort(h.brokerAddr)
	p, _ := strconv.Atoi(portStr)
	return p
}

// URL returns "http://<broker-addr>/".
func (h *Hub) URL() string {
	if h.brokerAddr == "" {
		return ""
	}
	return "http://" + h.brokerAddr + "/"
}

// Publish forwards an event to every SSE subscriber. Caller decides
// the event name + payload (HTML fragment in production).
func (h *Hub) Publish(name, data string) {
	if h.broker == nil {
		return
	}
	h.broker.Publish(broker.Event{Name: name, Data: data})
}

// SetStopFn registers a callback for POST /api/stop. The caller (the
// pipeline runner) cancels its run context inside the callback.
func (h *Hub) SetStopFn(fn func()) {
	h.stopMu.Lock()
	h.stopFn = fn
	h.stopMu.Unlock()
}

// SendStepBind tells the bridge which step a session id maps to. The
// pipeline runner calls this once per step when the first hook lets
// us learn the session id, or proactively when we have a way to
// resolve it (e.g. --resume / --session id passed to claude). PLAN-5 / C4.
func (h *Hub) SendStepBind(sessionID, step string) {
	h.bridgeMu.Lock()
	conn := h.bridgeConn
	h.bridgeMu.Unlock()
	if conn == nil {
		return
	}
	_ = ipc.Write(conn, ipc.Message{Type: ipc.TypeStepBind, SessionID: sessionID, Step: step})
}

// BridgeReady returns a channel that closes when the bridge subprocess
// sends its first TypeReady frame. Pipeline runner uses this to
// synchronise step-bind on the very first step.
func (h *Hub) BridgeReady() <-chan struct{} { return h.bridgeReadyCh }

func (h *Hub) requestStop() {
	h.stopMu.Lock()
	fn := h.stopFn
	h.stopMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (h *Hub) handleSend(content string) {
	h.bridgeMu.Lock()
	conn := h.bridgeConn
	h.bridgeMu.Unlock()
	if conn == nil {
		return
	}
	_ = ipc.Write(conn, ipc.Message{Type: ipc.TypeMessage, Content: content})
}

func (h *Hub) replayEvents() []broker.Event {
	if h.opts.ReplayEvents != nil {
		return h.opts.ReplayEvents()
	}
	frag := h.fragRenderer().PipelineInit()
	return []broker.Event{{Name: "pipeline-init", Data: frag}}
}

func (h *Hub) fragRenderer() FragmentRenderer {
	if h.opts.FragmentRenderer != nil {
		return h.opts.FragmentRenderer
	}
	return defaultRenderer{}
}

func (h *Hub) acceptLoop() {
	for {
		conn, err := h.ipcLn.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}
		go h.handleConn(conn)
	}
}

func (h *Hub) handleConn(conn net.Conn) {
	defer conn.Close()
	isBridge := false
	_ = ipc.Read(conn, func(m ipc.Message) {
		if m.Type == ipc.TypeReady && !isBridge {
			isBridge = true
			h.bridgeMu.Lock()
			h.bridgeConn = conn
			h.bridgeMu.Unlock()
			if !h.bridgeReadyOk {
				h.bridgeReadyOk = true
				close(h.bridgeReadyCh)
			}
		}
		h.dispatch(m)
	})
	if isBridge {
		h.bridgeMu.Lock()
		if h.bridgeConn == conn {
			h.bridgeConn = nil
		}
		h.bridgeMu.Unlock()
	}
}

func (h *Hub) dispatch(m ipc.Message) {
	switch m.Type {
	case ipc.TypeReply:
		h.Publish("reply", h.fragRenderer().Reply(m.Content))
		if h.opts.OnReply != nil {
			h.opts.OnReply(m.Content)
		}
	case ipc.TypeCall:
		if h.opts.OnCall != nil {
			h.opts.OnCall(ToolCall{
				ID:        m.ID,
				Tool:      m.Tool,
				Params:    m.Params,
				Result:    m.Result,
				SessionID: m.SessionID,
				At:        time.Now().UTC(),
			})
		}
		if m.Tool == "await_message" {
			if isDeferredEntry(m.Params) {
				h.Publish("await-pending", h.fragRenderer().AwaitPending())
			} else if isFlushEntry(m.Params) {
				h.Publish("await-resolved", h.fragRenderer().AwaitResolved())
			}
		}
	case ipc.TypeHook:
		if h.opts.OnHook != nil {
			h.opts.OnHook(HookEvent{
				Event:     m.Event,
				SessionID: m.SessionID,
				AgentID:   m.AgentID,
				Step:      m.Step,
				Payload:   m.Payload,
				At:        time.Now().UTC(),
			})
		}
		h.Publish("hook", h.fragRenderer().Hook(m.Event, m.SessionID, m.Step))
	case ipc.TypeBufferOvf:
		if h.opts.OnCall != nil {
			h.opts.OnCall(ToolCall{
				Tool:   "buffer-overflow",
				Params: json.RawMessage([]byte(strconv.Quote(m.Content))),
				At:     time.Now().UTC(),
			})
		}
	}
}

package orchestrator

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/diegosz/apex_process_ape/internal/bridge/broker"
)

// Hub composes a BridgeRuntime with a web-mode HTTP/SSE broker and a
// page renderer. `ape pipeline --web` embeds a Hub for the duration of
// a whole pipeline run; each step spawns its own claude. The Hub keeps
// the broker alive across step boundaries so the browser doesn't lose
// its SSE connection mid-pipeline.
//
// PLAN-6 / Phase B: the IPC accept, frame routing, stop-fn registry,
// bridge-ready handshake, and event publishing live in BridgeRuntime.
// Hub now layers (a) the SSE broker for browser fan-out and (b) the
// HTTP page + replay state on top. TUI and `none`-UI modes use
// BridgeRuntime directly without a Hub. PLAN-5 / C1 (pipeline web
// mode integration) wire and contracts are preserved.
type Hub struct {
	runtime *BridgeRuntime

	opts HubOptions

	broker     *broker.Broker
	brokerAddr string

	// subscribes to runtime events; closed when the Hub's Serve ctx
	// cancels so the fanOutToSSE goroutine exits cleanly.
	subOnce sync.Once
}

// HubOptions configures a Hub. The on-frame callbacks (OnReply / OnCall
// / OnHook) are forwarded into BridgeRuntimeOptions verbatim — callers
// see no behaviour difference. ReplayEvents, PageHTML, MountExtras, and
// FragmentRenderer are web-only and configure the broker + page.
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
		runtime: NewBridgeRuntime(BridgeRuntimeOptions{
			OnReply: opts.OnReply,
			OnCall:  opts.OnCall,
			OnHook:  opts.OnHook,
		}),
		opts: opts,
	}
}

// Listen reserves IPC + broker ports. Idempotent. Returns the web UI URL.
// ctx scopes the bind operations; the listeners themselves live until
// Serve returns.
func (h *Hub) Listen(ctx context.Context) (string, error) {
	if err := h.runtime.Listen(ctx); err != nil {
		return "", err
	}
	if h.broker != nil {
		return "http://" + h.brokerAddr + "/", nil
	}
	h.broker = broker.New(broker.Options{
		PageHTML:     h.opts.PageHTML,
		OnSend:       h.runtime.SendMessage,
		OnStop:       h.runtime.RequestStop,
		ReplayEvents: h.replayEvents,
		Mux:          h.opts.MountExtras,
	})
	addr, err := h.broker.Listen(ctx)
	if err != nil {
		return "", fmt.Errorf("hub.Listen: broker: %w", err)
	}
	h.brokerAddr = addr
	return "http://" + addr + "/", nil
}

// Serve runs the broker and the runtime's IPC accept loop in goroutines.
// Blocks until ctx cancels.
func (h *Hub) Serve(ctx context.Context) error {
	if h.broker == nil {
		if _, err := h.Listen(ctx); err != nil {
			return err
		}
	}
	h.subOnce.Do(func() {
		go h.fanOutToSSE(h.runtime.Subscribe())
	})

	rtErr := make(chan error, 1)
	go func() { rtErr <- h.runtime.Serve(ctx) }()

	brokerErr := make(chan error, 1)
	go func() { brokerErr <- h.broker.Serve(ctx) }()

	select {
	case <-ctx.Done():
		<-rtErr
		<-brokerErr
		return ctx.Err()
	case err := <-brokerErr:
		<-rtErr
		return err
	case err := <-rtErr:
		<-brokerErr
		return err
	}
}

// IPCPort returns the runtime's IPC TCP port.
func (h *Hub) IPCPort() int { return h.runtime.IPCPort() }

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

// Publish forwards an event to every SSE subscriber.
func (h *Hub) Publish(name, data string) {
	if h.broker == nil {
		return
	}
	h.broker.Publish(broker.Event{Name: name, Data: data})
}

// SetStopFn delegates to the runtime.
func (h *Hub) SetStopFn(fn func()) { h.runtime.SetStopFn(fn) }

// SendStepBind delegates to the runtime.
func (h *Hub) SendStepBind(sessionID, step string) { h.runtime.SendStepBind(sessionID, step) }

// BridgeReady delegates to the runtime.
func (h *Hub) BridgeReady() <-chan struct{} { return h.runtime.BridgeReady() }

// Runtime exposes the underlying BridgeRuntime for advanced callers
// (e.g., tests, future TUI integrations that need to subscribe directly).
// Most callers should use the Hub's wrapping methods instead.
func (h *Hub) Runtime() *BridgeRuntime { return h.runtime }

// fanOutToSSE bridges RuntimeEvents into broker.Publish calls so the
// browser receives the same SSE event names the Hub used to publish
// inline from dispatch(). PLAN-5 / C8 wire schema unchanged.
func (h *Hub) fanOutToSSE(events <-chan RuntimeEvent) {
	for evt := range events {
		switch evt.Kind {
		case RuntimeEventReply:
			h.Publish("reply", h.fragRenderer().Reply(evt.Reply))
		case RuntimeEventHook:
			if evt.Hook != nil {
				h.Publish("hook", h.fragRenderer().HookFromEvent(*evt.Hook))
			}
		case RuntimeEventAwaitPending:
			h.Publish("await-pending", h.fragRenderer().AwaitPending())
		case RuntimeEventAwaitResolved:
			h.Publish("await-resolved", h.fragRenderer().AwaitResolved())
		case RuntimeEventCall, RuntimeEventBufferOverflow:
			// Call frames are written to runlog via the OnCall direct
			// callback; the SSE surface used to be silent on them and
			// remains so to preserve PLAN-5 wire-schema equivalence.
		}
	}
}

func (h *Hub) replayEvents() []broker.Event {
	// Always lead with a `connected` status flip so the page's
	// "connecting…" banner becomes "connected" deterministically, no
	// matter which htmx-sse event name the browser fires. PLAN-5 / C8.
	events := []broker.Event{
		{Name: "connected", Data: h.fragRenderer().Connected()},
	}
	if h.opts.ReplayEvents != nil {
		events = append(events, h.opts.ReplayEvents()...)
		return events
	}
	events = append(events, broker.Event{
		Name: "pipeline-init",
		Data: h.fragRenderer().PipelineInit(),
	})
	return events
}

func (h *Hub) fragRenderer() FragmentRenderer {
	if h.opts.FragmentRenderer != nil {
		return h.opts.FragmentRenderer
	}
	return defaultRenderer{}
}

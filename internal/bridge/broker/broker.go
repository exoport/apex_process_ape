// Package broker hosts the local HTTP surface for the bridged web UI:
// the SSE event stream, the `/api/send` POST that wakes await_message,
// and the `/api/stop` POST that SIGTERMs the active step. Designed to
// be embedded by the parent ape process (`ape chat` / `ape pipeline`
// web mode); the parent decides what to publish and how to react to
// the lifecycle callbacks.
//
// Binds to 127.0.0.1 only. PLAN-5 / C3.
//
// Ported from https://github.com/diegosz/claude_mcp_bridge_poc, commit 4e542d0 (MIT),
// extended for PLAN-5 with explicit per-connection replay (`pipeline-init`),
// per-event named SSE events (so HTMX SSE extension can route OOB
// swaps), and lifecycle callbacks.
package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"sync"
	"time"
)

// Event is one item published over SSE. Name is the named-event
// label (used by HTMX SSE `sse-swap` matchers); Data is the wire
// payload (an HTML fragment in production; opaque to the broker).
//
// The locked SSE wire schema (PLAN-5 / C8): pipeline-init, stage-start,
// stage-update, stage-end, hook, reply, await-pending, await-resolved,
// stopped, error. The broker does not enforce the schema — callers
// can publish any Name — but the standard set is what the UI listens
// for.
type Event struct {
	Name string
	Data string
}

// Options configures the broker. ListenAddr defaults to 127.0.0.1:0
// (random free port); OnSend / OnStop are required for the POST
// handlers to do useful work (a nil callback returns 503 / 409).
type Options struct {
	// ListenAddr is the TCP address to bind. Must start with
	// "127.0.0.1" (PLAN-5 / C5 hard invariant); the broker rejects
	// other binds. Empty means "127.0.0.1:0".
	ListenAddr string
	// PageHTML is the body returned by GET /. Empty serves a tiny
	// placeholder. C8 wires the full HTMX page; C3 leaves it
	// minimal so the broker works standalone.
	PageHTML string
	// OnSend is invoked from POST /api/send before the 204 is
	// returned. nil → 503.
	OnSend func(content string)
	// OnStop is invoked from POST /api/stop before the 202 is
	// returned. nil → 409.
	OnStop func()
	// ReplayEvents is called on every new SSE subscription so the
	// broker can re-emit a fresh `pipeline-init` (and any open
	// `await-pending`) into the new client's stream. Returns the
	// list of events to replay; if nil, no replay is performed.
	// PLAN-5 / C3 "browser-close behaviour".
	ReplayEvents func() []Event
}

// Broker is the HTTP/SSE surface. New construct one; Listen reserves
// a port; Serve blocks running the HTTP server until ctx is cancelled.
// Publish fans out to every active SSE subscriber.
type Broker struct {
	opts Options
	ln   net.Listener
	srv  *http.Server

	mu      sync.Mutex
	clients map[chan Event]struct{}
}

// New returns an unstarted broker. Call Listen, then Serve.
func New(opts Options) *Broker {
	return &Broker{
		opts:    opts,
		clients: make(map[chan Event]struct{}),
	}
}

// Listen reserves the TCP port and returns the bound address. Idempotent
// after the first successful call (subsequent calls return the same
// address). The listener is mandatory 127.0.0.1 — Listen rejects any
// other bind address. PLAN-5 / C5.
func (b *Broker) Listen() (string, error) {
	if b.ln != nil {
		return b.ln.Addr().String(), nil
	}
	addr := b.opts.ListenAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("broker.Listen: split %q: %w", addr, err)
	}
	if host != "127.0.0.1" {
		return "", fmt.Errorf("broker.Listen: refuse to bind on %q (only 127.0.0.1 is allowed)", host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("broker.Listen: %w", err)
	}
	b.ln = ln
	return ln.Addr().String(), nil
}

// Serve runs the HTTP server. Blocks until ctx is cancelled or the
// listener fails. Call Listen first.
func (b *Broker) Serve(ctx context.Context) error {
	if b.ln == nil {
		if _, err := b.Listen(); err != nil {
			return err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", b.handleRoot)
	mux.HandleFunc("/api/events", b.handleEvents)
	mux.HandleFunc("/api/send", b.handleSend)
	mux.HandleFunc("/api/stop", b.handleStop)

	b.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		err := b.srv.Serve(b.ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.srv.Shutdown(shutdownCtx)
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// Addr returns the bound address. Empty before Listen is called.
func (b *Broker) Addr() string {
	if b.ln == nil {
		return ""
	}
	return b.ln.Addr().String()
}

// Publish fans out event to every active SSE subscriber. Non-blocking:
// subscribers with a full buffer (16) drop the event. Logs nothing —
// SSE is best-effort within a connection's lifetime, and the durable
// record is the run-dir JSONL streams (PLAN-5 / C6).
func (b *Broker) Publish(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- event:
		default:
		}
	}
}

func (b *Broker) subscribe() chan Event {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

// ClientCount returns the number of active SSE subscribers. Tests use
// this to wait for a connection before publishing.
func (b *Broker) ClientCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

func (b *Broker) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if b.opts.PageHTML != "" {
		_, _ = fmt.Fprint(w, b.opts.PageHTML)
		return
	}
	// Placeholder — C8 replaces with the full HTMX page.
	_, _ = fmt.Fprintf(w, "<!doctype html><title>ape bridge</title><body>bridge online; addr=%s</body>", html.EscapeString(b.Addr()))
}

func (b *Broker) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// Flush headers immediately so the browser's EventSource fires
	// onopen. Without this, headers are buffered until the first
	// Write and the UI stays in "connecting" state until an event
	// arrives. PLAN-5 / C3 SSE flush invariant — locked here.
	flusher.Flush()

	ch := b.subscribe()
	defer b.unsubscribe(ch)

	// Replay current state to the new subscriber so a tab reopen
	// rebuilds the UI from scratch (PLAN-5 / C3 — no backlog).
	if b.opts.ReplayEvents != nil {
		for _, ev := range b.opts.ReplayEvents() {
			writeSSE(w, flusher, ev)
		}
	}

	for {
		select {
		case ev := <-ch:
			writeSSE(w, flusher, ev)
		case <-r.Context().Done():
			return
		}
	}
}

// writeSSE writes one SSE message and flushes. The PoC's missing flush
// is the bug PLAN-5 locks with a regression test (broker_test.go).
func writeSSE(w http.ResponseWriter, flusher http.Flusher, ev Event) {
	if ev.Name != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", ev.Name)
	}
	// Data may contain newlines; SSE requires each line to be
	// prefixed with `data: `. For HTML fragments this is rare, but
	// hook envelopes can carry multi-line content — handle it.
	for _, line := range splitLines(ev.Data) {
		_, _ = fmt.Fprintf(w, "data: %s\n", line)
	}
	_, _ = fmt.Fprint(w, "\n")
	flusher.Flush()
}

func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		out = append(out, s[start:])
	}
	// If the string ended with \n, drop the trailing empty token so
	// we don't emit a spurious "data: " line.
	if n := len(out); n > 1 && out[n-1] == "" {
		out = out[:n-1]
	}
	return out
}

func (b *Broker) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 64 KB cap per PLAN-5 / C3.
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if b.opts.OnSend == nil {
		http.Error(w, "bridge not connected", http.StatusServiceUnavailable)
		return
	}
	b.opts.OnSend(body.Content)
	w.WriteHeader(http.StatusNoContent)
}

func (b *Broker) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if b.opts.OnStop == nil {
		http.Error(w, "no run active", http.StatusConflict)
		return
	}
	b.opts.OnStop()
	w.WriteHeader(http.StatusAccepted)
}

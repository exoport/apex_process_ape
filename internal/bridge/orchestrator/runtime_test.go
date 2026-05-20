package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
)

// TestBridgeRuntime_ListenIsIdempotent verifies repeated Listen calls
// return the same port and don't allocate a second listener.
func TestBridgeRuntime_ListenIsIdempotent(t *testing.T) {
	r := NewBridgeRuntime(BridgeRuntimeOptions{})
	if err := r.Listen(); err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	port := r.IPCPort()
	if port == 0 {
		t.Fatal("IPCPort returned 0 after Listen")
	}
	if err := r.Listen(); err != nil {
		t.Fatalf("second Listen: %v", err)
	}
	if r.IPCPort() != port {
		t.Errorf("port changed across Listen calls: %d != %d", r.IPCPort(), port)
	}
}

// TestBridgeRuntime_DispatchFanout exercises the dispatch path for
// every frame type and verifies both the direct callback and the
// Subscribe channel receive matching values.
func TestBridgeRuntime_DispatchFanout(t *testing.T) {
	var (
		replyCalls []string
		callCalls  []ToolCall
		hookCalls  []HookEvent
		mu         sync.Mutex
	)
	r := NewBridgeRuntime(BridgeRuntimeOptions{
		OnReply: func(c string) { mu.Lock(); replyCalls = append(replyCalls, c); mu.Unlock() },
		OnCall:  func(c ToolCall) { mu.Lock(); callCalls = append(callCalls, c); mu.Unlock() },
		OnHook:  func(h HookEvent) { mu.Lock(); hookCalls = append(hookCalls, h); mu.Unlock() },
	})
	events := r.Subscribe()

	// Direct dispatch (no IPC accept loop needed for this unit test).
	r.dispatch(ipc.Message{Type: ipc.TypeReply, Content: "hello"})
	r.dispatch(ipc.Message{Type: ipc.TypeCall, Tool: "Read", Params: json.RawMessage(`{}`), ID: "c1"})
	r.dispatch(ipc.Message{Type: ipc.TypeHook, Event: "PreToolUse", SessionID: "s1"})
	r.dispatch(ipc.Message{Type: ipc.TypeBufferOvf, Content: "overflow"})

	// Drain the subscriber channel non-blockingly.
	var got []RuntimeEvent
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case e := <-events:
			got = append(got, e)
			if len(got) == 4 {
				break loop
			}
		case <-deadline:
			t.Fatalf("timed out collecting events; got %d", len(got))
		}
	}

	wantKinds := []RuntimeEventKind{
		RuntimeEventReply,
		RuntimeEventCall,
		RuntimeEventHook,
		RuntimeEventBufferOverflow,
	}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Errorf("event %d Kind = %v, want %v", i, got[i].Kind, k)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(replyCalls) != 1 || replyCalls[0] != "hello" {
		t.Errorf("replyCalls = %v", replyCalls)
	}
	if len(callCalls) != 2 || callCalls[0].Tool != "Read" || callCalls[1].Tool != "buffer-overflow" {
		t.Errorf("callCalls = %+v", callCalls)
	}
	if len(hookCalls) != 1 || hookCalls[0].Event != "PreToolUse" {
		t.Errorf("hookCalls = %+v", hookCalls)
	}
}

// TestBridgeRuntime_AwaitMessageEmitsPendingAndResolved verifies the
// `await_message` specialization that web mode used to derive inline
// from dispatch is preserved by the runtime via specialized event
// kinds. Hub fans these out to SSE; TUI consumes them directly.
func TestBridgeRuntime_AwaitMessageEmitsPendingAndResolved(t *testing.T) {
	r := NewBridgeRuntime(BridgeRuntimeOptions{})
	events := r.Subscribe()

	r.dispatch(ipc.Message{
		Type:   ipc.TypeCall,
		Tool:   "await_message",
		Params: json.RawMessage(`{"deferred":true}`),
	})
	r.dispatch(ipc.Message{
		Type:   ipc.TypeCall,
		Tool:   "await_message",
		Params: json.RawMessage(`{"flush":true}`),
	})

	var sawPending, sawResolved bool
	deadline := time.After(500 * time.Millisecond)
	for !sawPending || !sawResolved {
		select {
		case e := <-events:
			switch e.Kind {
			case RuntimeEventAwaitPending:
				sawPending = true
			case RuntimeEventAwaitResolved:
				sawResolved = true
			}
		case <-deadline:
			t.Fatalf("timed out; pending=%v resolved=%v", sawPending, sawResolved)
		}
	}
}

// TestBridgeRuntime_SubscriberDropsOnFullBuffer verifies the runtime
// does not block the dispatch path when a subscriber's channel fills.
// PLAN-6 / C3 invariant — accept loop must never wedge on a slow UI.
func TestBridgeRuntime_SubscriberDropsOnFullBuffer(t *testing.T) {
	r := NewBridgeRuntime(BridgeRuntimeOptions{})
	_ = r.Subscribe() // Never drained — should drop.

	// Pump more events than the buffer (32) to force the drop path.
	for i := 0; i < 100; i++ {
		r.dispatch(ipc.Message{Type: ipc.TypeReply, Content: "x"})
	}
	// If this returns, the drop path is working.
}

// TestBridgeRuntime_RequestStopInvokesRegisteredFn verifies the
// stop-fn registry contract.
func TestBridgeRuntime_RequestStopInvokesRegisteredFn(t *testing.T) {
	r := NewBridgeRuntime(BridgeRuntimeOptions{})
	called := 0
	r.SetStopFn(func() { called++ })
	r.RequestStop()
	r.RequestStop()
	if called != 2 {
		t.Errorf("called = %d, want 2", called)
	}

	// Unregistering and calling should be a no-op.
	r.SetStopFn(nil)
	r.RequestStop()
	if called != 2 {
		t.Errorf("called after unregister = %d, want 2", called)
	}
}

// TestBridgeRuntime_BridgeReadyCloses verifies BridgeReady() closes
// after the first TypeReady frame is processed through handleConn.
// Uses a real net.Pipe to exercise the connection path.
func TestBridgeRuntime_BridgeReadyCloses(t *testing.T) {
	r := NewBridgeRuntime(BridgeRuntimeOptions{})
	if err := r.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = r.Serve(ctx)
		close(done)
	}()

	// Dial the runtime's IPC listener and send a TypeReady frame.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", r.IPCPort()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := ipc.Write(conn, ipc.Message{Type: ipc.TypeReady}); err != nil {
		t.Fatalf("write Ready: %v", err)
	}

	select {
	case <-r.BridgeReady():
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeReady did not close within 2s")
	}

	cancel()
	<-done
}

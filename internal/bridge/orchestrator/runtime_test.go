package orchestrator

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
)

// TestBridgeRuntime_ListenIsIdempotent verifies repeated Listen calls
// return the same port and don't allocate a second listener.
func TestBridgeRuntime_ListenIsIdempotent(t *testing.T) {
	r := NewBridgeRuntime(BridgeRuntimeOptions{})
	if err := r.Listen(t.Context()); err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	port := r.IPCPort()
	if port == 0 {
		t.Fatal("IPCPort returned 0 after Listen")
	}
	if err := r.Listen(t.Context()); err != nil {
		t.Fatalf("second Listen: %v", err)
	}
	if r.IPCPort() != port {
		t.Errorf("port changed across Listen calls: %d != %d", r.IPCPort(), port)
	}
}

// TestBridgeRuntime_BridgeReadyCloses verifies BridgeReady() closes
// after the first TypeReady frame is processed through handleConn.
// Uses a real net.Pipe to exercise the connection path.
func TestBridgeRuntime_BridgeReadyCloses(t *testing.T) {
	r := NewBridgeRuntime(BridgeRuntimeOptions{})
	if err := r.Listen(t.Context()); err != nil {
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

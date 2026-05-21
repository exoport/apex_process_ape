package ipc

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

func TestWrite_RoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var (
		mu       sync.Mutex
		received []Message
	)
	done := make(chan struct{})

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close()
		_ = Read(conn, func(m Message) {
			mu.Lock()
			received = append(received, m)
			mu.Unlock()
		})
		close(done)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	want := []Message{
		{Type: TypeReady},
		{Type: TypeReply, Content: "hello"},
		{Type: TypeCall, Tool: "reply", Params: json.RawMessage(`{"content":"x"}`), Result: json.RawMessage(`{"isError":false}`)},
		{Type: TypeHook, Event: "PreToolUse", SessionID: "abc", Payload: json.RawMessage(`{"tool":"Bash"}`)},
		{Type: TypeStepBind, SessionID: "abc", Step: "design/architecture"},
	}
	for _, msg := range want {
		if err := Write(conn, msg); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	conn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not finish within 2s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != len(want) {
		t.Fatalf("received %d messages, want %d: %+v", len(received), len(want), received)
	}
	for i, got := range received {
		if got.Type != want[i].Type {
			t.Errorf("msg %d: type = %q, want %q", i, got.Type, want[i].Type)
		}
	}
	// Verify Payload survived as raw JSON.
	if string(received[3].Payload) != `{"tool":"Bash"}` {
		t.Errorf("hook payload roundtrip = %s", string(received[3].Payload))
	}
}

func TestRead_SkipsMalformedLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	gotCh := make(chan Message, 4)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = Read(conn, func(m Message) { gotCh <- m })
		close(gotCh)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Send: valid, malformed, valid.
	if _, err := conn.Write([]byte("{\"type\":\"ready\"}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("not-json\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("{\"type\":\"reply\",\"content\":\"hi\"}\n")); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	got := make([]Message, 0, cap(gotCh))
	for m := range gotCh {
		got = append(got, m)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 valid messages (malformed skipped), got %d: %+v", len(got), got)
	}
	if got[0].Type != TypeReady || got[1].Type != TypeReply {
		t.Errorf("messages out of order: %+v", got)
	}
}

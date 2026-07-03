package apecmd

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
)

func TestRunNotify_ForwardsHookFrame(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	var (
		mu  sync.Mutex
		got []ipc.Message
	)
	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = ipc.Read(conn, func(m ipc.Message) {
			mu.Lock()
			got = append(got, m)
			mu.Unlock()
		})
		close(done)
	}()

	envelope := strings.NewReader(`{"session_id":"sess-42","tool":"Bash","tool_input":{"command":"ls"}}`)
	runNotify("PreToolUse", envelope, port)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hook listener did not receive a frame in time")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("received %d frames, want 1: %+v", len(got), got)
	}
	m := got[0]
	if m.Type != ipc.TypeHook {
		t.Errorf("type = %q, want hook", m.Type)
	}
	if m.Event != "PreToolUse" {
		t.Errorf("event = %q, want PreToolUse", m.Event)
	}
	if m.SessionID != "sess-42" {
		t.Errorf("session_id = %q, want sess-42", m.SessionID)
	}
	if !strings.Contains(string(m.Payload), `"tool":"Bash"`) {
		t.Errorf("payload should round-trip envelope: %s", string(m.Payload))
	}
}

func TestRunNotify_SilentOnMissingPort(_ *testing.T) {
	// Must not panic, must not error — empty port means "bridge not
	// running", and the hook loop should keep going. PLAN-5 / C4.
	runNotify("PreToolUse", strings.NewReader(`{}`), "")
}

func TestRunNotify_SilentOnDialFailure(_ *testing.T) {
	// Port 1 is reserved and refuses connections on Linux.
	runNotify("PreToolUse", strings.NewReader(`{}`), "1")
}

func TestExtractIDs_HappyPath(t *testing.T) {
	sid, aid := extractIDs([]byte(`{"session_id":"abc","agent_id":"xyz"}`))
	if sid != "abc" || aid != "xyz" {
		t.Errorf("got (%q, %q), want (abc, xyz)", sid, aid)
	}
}

func TestExtractIDs_TolerantOnMalformed(t *testing.T) {
	sid, aid := extractIDs([]byte(`{not valid json`))
	if sid != "" || aid != "" {
		t.Errorf("malformed envelope should return empties, got (%q, %q)", sid, aid)
	}
}

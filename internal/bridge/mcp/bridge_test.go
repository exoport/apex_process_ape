package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
)

// --- Helpers shared across tests ---

// bridgeHarness wraps a running bridge with a stable parent-side IPC
// reader. Each test gets its own harness; ipcFrames is drained by a
// goroutine spawned in setupParent, so tests fetch frames via waitIPC
// without losing bytes between calls.
type bridgeHarness struct {
	stdin     io.Writer
	stdout    *bufio.Reader
	ipcAccept <-chan net.Conn
	cancel    context.CancelFunc
}

// startBridgeWithStdio runs Run(ctx, opts) against an in-process pair
// of pipes for stdin / stdout, plus a freshly-listened parent IPC
// socket.
func startBridgeWithStdio(t *testing.T) *bridgeHarness {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	accCh := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accCh <- c
	}()

	bridgeStdinR, bridgeStdinW := io.Pipe()
	bridgeStdoutR, bridgeStdoutW := io.Pipe()

	ctx, c := context.WithCancel(context.Background())
	go func() {
		_ = Run(ctx, Options{
			IPCAddr:      ln.Addr().String(),
			Stdin:        bridgeStdinR,
			Stdout:       bridgeStdoutW,
			Stderr:       io.Discard,
			AwaitTimeout: 2 * time.Second,
		})
		_ = bridgeStdoutW.Close()
	}()

	t.Cleanup(func() {
		c()
		_ = bridgeStdinW.Close()
		_ = ln.Close()
	})

	return &bridgeHarness{
		stdin:     bridgeStdinW,
		stdout:    bufio.NewReader(bridgeStdoutR),
		ipcAccept: accCh,
		cancel:    c,
	}
}

// parentReader wraps a parent IPC conn with a goroutine drain into a
// channel. Tests use waitFor to consume frames in order.
type parentReader struct {
	conn   net.Conn
	frames chan ipc.Message
	done   chan struct{}
}

func startParentReader(conn net.Conn) *parentReader {
	pr := &parentReader{
		conn:   conn,
		frames: make(chan ipc.Message, 64),
		done:   make(chan struct{}),
	}
	go func() {
		_ = ipc.Read(conn, func(m ipc.Message) {
			select {
			case pr.frames <- m:
			default:
			}
		})
		close(pr.done)
	}()
	return pr
}

func (pr *parentReader) waitFor(t *testing.T, match func(ipc.Message) bool) ipc.Message {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m := <-pr.frames:
			if match(m) {
				return m
			}
		case <-deadline:
			t.Fatal("parentReader.waitFor: timeout")
			return ipc.Message{}
		}
	}
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

func writeRPC(t *testing.T, w io.Writer, m rpcMsg) {
	t.Helper()
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal rpc: %v", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
}

func readRPCResponse(t *testing.T, r *bufio.Reader, wantID int) rpcMsg {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var m rpcMsg
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m.ID == wantID && len(m.Result) > 0 {
			return m
		}
	}
	t.Fatalf("no response with id=%d within timeout", wantID)
	return rpcMsg{}
}

// --- Tests ---

// driveHandshake performs the initialize / notifications/initialized
// handshake and waits for the parent's IPC accept + Ready frame. Used
// by every test that needs a live parent reader.
func driveHandshake(t *testing.T, h *bridgeHarness) *parentReader {
	t.Helper()
	writeRPC(t, h.stdin, rpcMsg{ID: 1, Method: "initialize"})
	readRPCResponse(t, h.stdout, 1)
	writeRPC(t, h.stdin, rpcMsg{Method: "notifications/initialized"})
	var conn net.Conn
	select {
	case conn = <-h.ipcAccept:
	case <-time.After(2 * time.Second):
		t.Fatal("parent did not accept IPC connection")
	}
	t.Cleanup(func() { conn.Close() })
	pr := startParentReader(conn)
	pr.waitFor(t, func(m ipc.Message) bool { return m.Type == ipc.TypeReady })
	return pr
}

func TestBridge_InitializeHandshake(t *testing.T) {
	h := startBridgeWithStdio(t)
	writeRPC(t, h.stdin, rpcMsg{ID: 1, Method: "initialize"})
	resp := readRPCResponse(t, h.stdout, 1)
	if !strings.Contains(string(resp.Result), `"protocolVersion":"2024-11-05"`) {
		t.Errorf("initialize response missing protocolVersion: %s", string(resp.Result))
	}
	if !strings.Contains(string(resp.Result), `"ape-mcp-bridge"`) {
		t.Errorf("initialize response missing serverInfo: %s", string(resp.Result))
	}
	writeRPC(t, h.stdin, rpcMsg{Method: "notifications/initialized"})
	var conn net.Conn
	select {
	case conn = <-h.ipcAccept:
	case <-time.After(2 * time.Second):
		t.Fatal("parent did not accept IPC connection")
	}
	defer conn.Close()
	pr := startParentReader(conn)
	pr.waitFor(t, func(m ipc.Message) bool { return m.Type == ipc.TypeReady })
}

func TestBridge_ToolsList(t *testing.T) {
	h := startBridgeWithStdio(t)
	writeRPC(t, h.stdin, rpcMsg{ID: 7, Method: "tools/list"})
	resp := readRPCResponse(t, h.stdout, 7)
	if !strings.Contains(string(resp.Result), `"name":"await_message"`) {
		t.Errorf("tools/list missing await_message: %s", string(resp.Result))
	}
	if !strings.Contains(string(resp.Result), `"name":"reply"`) {
		t.Errorf("tools/list missing reply: %s", string(resp.Result))
	}
	if !strings.Contains(string(resp.Result), `"default":240`) {
		t.Errorf("tools/list missing default:240 for await_message: %s", string(resp.Result))
	}
}

func TestBridge_AwaitMessage_DeferredResponse(t *testing.T) {
	h := startBridgeWithStdio(t)
	pr := driveHandshake(t, h)

	writeRPC(t, h.stdin, rpcMsg{
		ID:     42,
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"await_message","arguments":{"timeout_seconds":10}}`),
	})

	earlyDone := make(chan rpcMsg, 1)
	go func() { earlyDone <- readRPCResponse(t, h.stdout, 42) }()
	select {
	case got := <-earlyDone:
		t.Fatalf("await_message responded before parent message arrived: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}

	if err := ipc.Write(pr.conn, ipc.Message{Type: ipc.TypeMessage, Content: "hello from user"}); err != nil {
		t.Fatalf("parent write: %v", err)
	}

	select {
	case got := <-earlyDone:
		if !strings.Contains(string(got.Result), `"hello from user"`) {
			t.Errorf("await_message result missing user text: %s", string(got.Result))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("await_message never resolved after parent message")
	}
}

func TestBridge_AwaitMessage_TimeoutReturnsEmpty(t *testing.T) {
	h := startBridgeWithStdio(t)
	driveHandshake(t, h)
	writeRPC(t, h.stdin, rpcMsg{
		ID:     99,
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"await_message","arguments":{"timeout_seconds":1}}`),
	})
	resp := readRPCResponse(t, h.stdout, 99)
	if !strings.Contains(string(resp.Result), `"text":""`) {
		t.Errorf("timeout response should contain empty text: %s", string(resp.Result))
	}
}

func TestBridge_BufferedMessage_DrainsOnNextAwait(t *testing.T) {
	h := startBridgeWithStdio(t)
	pr := driveHandshake(t, h)
	if err := ipc.Write(pr.conn, ipc.Message{Type: ipc.TypeMessage, Content: "buffered"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	writeRPC(t, h.stdin, rpcMsg{
		ID:     11,
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"await_message","arguments":{"timeout_seconds":10}}`),
	})
	resp := readRPCResponse(t, h.stdout, 11)
	if !strings.Contains(string(resp.Result), `"buffered"`) {
		t.Errorf("await_message did not drain buffered message: %s", string(resp.Result))
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("buffered drain took %s (should be near-instant)", elapsed)
	}
}

func TestBridge_BufferOverflow_DropsOldest(t *testing.T) {
	h := startBridgeWithStdio(t)
	pr := driveHandshake(t, h)

	for i := 1; i <= 6; i++ {
		if err := ipc.Write(pr.conn, ipc.Message{Type: ipc.TypeMessage, Content: "m" + string(rune('0'+i))}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	// Expect at least one TypeBufferOvf frame.
	pr.waitFor(t, func(m ipc.Message) bool { return m.Type == ipc.TypeBufferOvf })

	// First await_message should drain m2 (m1 dropped on overflow).
	writeRPC(t, h.stdin, rpcMsg{
		ID:     50,
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"await_message","arguments":{"timeout_seconds":10}}`),
	})
	resp := readRPCResponse(t, h.stdout, 50)
	if strings.Contains(string(resp.Result), `"m1"`) {
		t.Errorf("buffer overflow should have dropped m1, got: %s", string(resp.Result))
	}
}

func TestBridge_Reply_ForwardsOverIPC(t *testing.T) {
	h := startBridgeWithStdio(t)
	pr := driveHandshake(t, h)

	writeRPC(t, h.stdin, rpcMsg{
		ID:     33,
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"reply","arguments":{"content":"hello browser"}}`),
	})

	got := pr.waitFor(t, func(m ipc.Message) bool {
		return m.Type == ipc.TypeReply && m.Content == "hello browser"
	})
	if got.Content != "hello browser" {
		t.Errorf("reply IPC frame content = %q, want hello browser", got.Content)
	}

	resp := readRPCResponse(t, h.stdout, 33)
	if !strings.Contains(string(resp.Result), `"text":"sent"`) {
		t.Errorf("reply response missing sent: %s", string(resp.Result))
	}
}

func TestBridge_StepBind_StoredAndMirrored(t *testing.T) {
	h := startBridgeWithStdio(t)
	pr := driveHandshake(t, h)

	if err := ipc.Write(pr.conn, ipc.Message{Type: ipc.TypeStepBind, SessionID: "sess-abc", Step: "design/architecture"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)

	writeRPC(t, h.stdin, rpcMsg{
		ID:     55,
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"reply","arguments":{"content":"x"}}`),
	})
	readRPCResponse(t, h.stdout, 55)

	got := pr.waitFor(t, func(m ipc.Message) bool {
		return m.Type == ipc.TypeCall && m.Tool == "reply"
	})
	if got.SessionID != "sess-abc" {
		t.Errorf("mirrored call SessionID = %q, want sess-abc", got.SessionID)
	}
}

// --- Mock-claude self-spawning idiom (C9) ---
//
// TestMockClaude_Main is a special "test" that actually runs a mock
// claude session against a bridge subprocess. It is gated by the
// APE_MOCK_CLAUDE_ROLE env var so the normal `go test` run does not
// execute it; integration tests below invoke it via os.Args[0].

func TestMain(m *testing.M) {
	if os.Getenv("APE_MOCK_CLAUDE_ROLE") == "bridge" {
		// Acting as the bridge subprocess.
		_ = Run(context.Background(), Options{})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestIntegration_MockClaudeReply(t *testing.T) {
	// End-to-end: parent IPC listener + self-spawned test binary in
	// "bridge" role (TestMain branch). The test drives the claude
	// side of the MCP wire (initialize / notifications / reply) and
	// asserts the parent observes a TypeReply.

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "APE_MOCK_CLAUDE_ROLE=bridge", "APE_IPC_PORT="+port)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	parent, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer parent.Close()
	pr := startParentReader(parent)

	out := bufio.NewReader(stdout)
	writeRPC(t, stdin, rpcMsg{ID: 1, Method: "initialize"})
	readRPCResponse(t, out, 1)
	writeRPC(t, stdin, rpcMsg{Method: "notifications/initialized"})
	pr.waitFor(t, func(m ipc.Message) bool { return m.Type == ipc.TypeReady })

	writeRPC(t, stdin, rpcMsg{
		ID:     2,
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"reply","arguments":{"content":"echo: hi"}}`),
	})
	got := pr.waitFor(t, func(m ipc.Message) bool { return m.Type == ipc.TypeReply })
	if got.Content != "echo: hi" {
		t.Errorf("reply content = %q, want 'echo: hi'", got.Content)
	}
	readRPCResponse(t, out, 2)
}

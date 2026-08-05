package vmmstream

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/nats-io/nats.go"
)

// A forward is the exec/attach transport carrying a different payload. These
// tests prove the payload half works on arbitrary bytes, and — the risk PLAN-24
// flags — that adding it did not perturb the interactive kinds. The exec/attach
// regression itself is TestSessionRoundTrip, which is unchanged and still runs;
// what is asserted here is that a forward shares its protocol rather than
// forking it.

// pipeConn is an in-memory io.ReadWriteCloser standing in for an accepted local
// connection.
type pipeConn struct {
	io.Reader
	io.WriteCloser
	closed chan struct{}
}

func newPipeConn(r io.Reader, w io.WriteCloser) *pipeConn {
	return &pipeConn{Reader: r, WriteCloser: w, closed: make(chan struct{})}
}

func (c *pipeConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return c.WriteCloser.Close()
}

// TestForwardCarriesArbitraryBytes drives Forward against a server session whose
// process echoes, proving a forward moves raw bytes — including the ones a PTY
// would mangle (CR, LF, NUL, high bytes). That is why the server opens the guest
// process with no terminal.
func TestForwardCarriesArbitraryBytes(t *testing.T) {
	url := natstest.RunServer(t)
	srvConn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer srvConn.Close()
	cliConn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer cliConn.Close()

	const prefix = "ape.vmm.node1.exec.f1"
	proc := newEchoProcess()
	srv, err := NewServerSession(srvConn, prefix, proc, 4)
	if err != nil {
		t.Fatalf("NewServerSession: %v", err)
	}
	_ = srvConn.Flush()
	go func() { _, _ = srv.Run(context.Background()) }()

	// Bytes a terminal would translate or swallow.
	payload := []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n\x00\x01\xff\x7f\n")

	localR, localW := io.Pipe() // what the "local peer" writes → session stdin
	var received bytes.Buffer
	conn := newPipeConn(localR, nopWriteCloser{&received})

	done := make(chan error, 1)
	go func() {
		_, ferr := Forward(context.Background(), cliConn, prefix, ForwardStreams{
			Local: conn,
			Diag:  io.Discard,
		}, 4)
		done <- ferr
	}()

	if _, werr := localW.Write(payload); werr != nil {
		t.Fatal(werr)
	}
	_ = localW.Close() // the local peer hangs up → EOF sentinel → guest half-close

	select {
	case ferr := <-done:
		if ferr != nil {
			t.Fatalf("Forward: %v", ferr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Forward did not return after the local peer closed")
	}

	if got := received.Bytes(); !bytes.Equal(got, payload) {
		t.Errorf("round-tripped bytes differ:\n got %q\nwant %q", got, payload)
	}
}

// The local connection must be closed when the session ends, so the local peer
// sees the connection drop rather than a silently dead socket.
func TestForwardClosesTheLocalConnection(t *testing.T) {
	url := natstest.RunServer(t)
	srvConn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer srvConn.Close()
	cliConn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer cliConn.Close()

	const prefix = "ape.vmm.node1.exec.f2"
	proc := newEchoProcess()
	srv, err := NewServerSession(srvConn, prefix, proc, 4)
	if err != nil {
		t.Fatal(err)
	}
	_ = srvConn.Flush()
	go func() { _, _ = srv.Run(context.Background()) }()

	localR, localW := io.Pipe()
	conn := newPipeConn(localR, nopWriteCloser{io.Discard})
	go func() {
		_, _ = Forward(context.Background(), cliConn, prefix, ForwardStreams{Local: conn}, 4)
	}()
	_ = localW.Close()

	select {
	case <-conn.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the local connection was left open after the session ended")
	}
}

// A forward must NOT splice the guest's diagnostics into the byte stream: a
// "nothing is listening" written into a forwarded HTTP response is
// indistinguishable from data to whatever is on the other end.
func TestForwardKeepsDiagnosticsOutOfTheByteStream(t *testing.T) {
	url := natstest.RunServer(t)
	srvConn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer srvConn.Close()
	cliConn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer cliConn.Close()

	const prefix = "ape.vmm.node1.exec.f3"
	// echoProcess writes "READY\n" to stderr — standing in for the guest end's
	// diagnostics.
	proc := newEchoProcess()
	srv, err := NewServerSession(srvConn, prefix, proc, 4)
	if err != nil {
		t.Fatal(err)
	}
	_ = srvConn.Flush()
	go func() { _, _ = srv.Run(context.Background()) }()

	localR, localW := io.Pipe()
	var data, diag bytes.Buffer
	conn := newPipeConn(localR, nopWriteCloser{&data})

	done := make(chan struct{})
	go func() {
		_, _ = Forward(context.Background(), cliConn, prefix, ForwardStreams{Local: conn, Diag: &diag}, 4)
		close(done)
	}()
	_, _ = localW.Write([]byte("hello"))
	_ = localW.Close()
	<-done

	if bytes.Contains(data.Bytes(), []byte("READY")) {
		t.Errorf("the guest's stderr leaked into the forwarded byte stream: %q", data.String())
	}
	if !bytes.Contains(diag.Bytes(), []byte("READY")) {
		t.Errorf("the guest's stderr was dropped instead of routed to Diag: %q", diag.String())
	}
}

// A nil Diag must not panic — it is the normal case for a caller that does not
// want the guest's chatter.
func TestForwardNilDiag(t *testing.T) {
	url := natstest.RunServer(t)
	srvConn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer srvConn.Close()
	cliConn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer cliConn.Close()

	const prefix = "ape.vmm.node1.exec.f4"
	srv, err := NewServerSession(srvConn, prefix, newEchoProcess(), 4)
	if err != nil {
		t.Fatal(err)
	}
	_ = srvConn.Flush()
	go func() { _, _ = srv.Run(context.Background()) }()

	localR, localW := io.Pipe()
	conn := newPipeConn(localR, nopWriteCloser{io.Discard})
	done := make(chan struct{})
	go func() {
		_, _ = Forward(context.Background(), cliConn, prefix, ForwardStreams{Local: conn}, 0)
		close(done)
	}()
	_ = localW.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Forward hung with a nil Diag")
	}
}

// nopWriteCloser adapts an io.Writer for pipeConn.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// Guard: a forward must be reachable over an ordinary net.Conn, which is what
// the CLI passes. This is a compile-time assertion in test form.
var _ = func() bool {
	var c net.Conn
	_ = ForwardStreams{Local: c}
	return true
}

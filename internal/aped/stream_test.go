package aped

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/vmmstream"
)

var errFakeClosed = errors.New("fake priv conn closed")

// fakePrivConn is a message-preserving in-memory privConn pair for testing the
// stream relay without a real AF_UNIX SEQPACKET socket (the real transport is
// proven by TestPrivSocketRoundTripAndPeer). Each Send delivers exactly one
// frame, mirroring SEQPACKET boundaries.
type fakePrivConn struct {
	tx     chan []byte
	rx     chan []byte
	closed chan struct{}
}

func newFakePrivPair() (execSide, frontSide *fakePrivConn) {
	a2b := make(chan []byte, 64)
	b2a := make(chan []byte, 64)
	return &fakePrivConn{tx: a2b, rx: b2a, closed: make(chan struct{})},
		&fakePrivConn{tx: b2a, rx: a2b, closed: make(chan struct{})}
}

func (f *fakePrivConn) Send(b []byte) error {
	cp := append([]byte(nil), b...) // the relay reuses its send buffer
	select {
	case f.tx <- cp:
		return nil
	case <-f.closed:
		return errFakeClosed
	}
}

func (f *fakePrivConn) Recv() ([]byte, error) {
	select {
	case b := <-f.rx:
		return b, nil
	case <-f.closed:
		return nil, io.EOF
	}
}

func (f *fakePrivConn) Peer() (Peer, error)             { return Peer{}, nil }
func (f *fakePrivConn) SetReadDeadline(time.Time) error { return nil }

func (f *fakePrivConn) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

// fakeStreamProcess echoes stdin→stdout, emits a fixed stderr banner, records
// resizes, and exits 7 once stdin half-closes.
type fakeStreamProcess struct {
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stderrR *io.PipeReader
	resizes chan vmmstream.WinSize
	done    chan struct{}
}

func newFakeStreamProcess() *fakeStreamProcess {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	p := &fakeStreamProcess{
		stdinW: inW, stdoutR: outR, stderrR: errR,
		resizes: make(chan vmmstream.WinSize, 1),
		done:    make(chan struct{}),
	}
	go func() { _, _ = io.WriteString(errW, "READY\n"); _ = errW.Close() }()
	go func() { _, _ = io.Copy(outW, inR); _ = outW.Close(); close(p.done) }()
	return p
}

func (p *fakeStreamProcess) Stdin() io.WriteCloser { return p.stdinW }
func (p *fakeStreamProcess) Stdout() io.Reader     { return p.stdoutR }
func (p *fakeStreamProcess) Stderr() io.Reader     { return p.stderrR }

func (p *fakeStreamProcess) Resize(cols, rows uint16) error {
	select {
	case p.resizes <- vmmstream.WinSize{Cols: cols, Rows: rows}:
	default:
	}
	return nil
}

func (p *fakeStreamProcess) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.done:
		return 7, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// TestPrivStreamRelayRoundTrip drives the executor-side relay and the front-side
// connProcess against each other over a fake SEQPACKET pair: stdin echoes back on
// stdout, the stderr banner arrives, a front→executor resize is applied, and the
// exit code propagates as the terminal frame. This is the priv-socket half of the
// exec/attach bridge; the NATS half is TestSessionRoundTrip.
func TestPrivStreamRelayRoundTrip(t *testing.T) {
	execConn, frontConn := newFakePrivPair()
	defer func() { _ = execConn.Close() }()
	defer func() { _ = frontConn.Close() }()

	proc := newFakeStreamProcess()
	relayDone := make(chan int, 1)
	go func() {
		code, _ := relayProcessToConn(t.Context(), execConn, proc)
		relayDone <- code
	}()

	fp := connToProcess(frontConn)

	// Resize front→executor and confirm it applied server-side (deterministic).
	if err := fp.Resize(120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	select {
	case sz := <-proc.resizes:
		if sz != (vmmstream.WinSize{Cols: 120, Rows: 40}) {
			t.Fatalf("resize applied = %+v, want {120 40}", sz)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resize not applied executor-side")
	}

	// Drain stdout/stderr concurrently (the demux blocks per-pipe until read).
	var stdout, stderr bytes.Buffer
	var drain sync.WaitGroup
	drain.Add(2)
	go func() { defer drain.Done(); _, _ = io.Copy(&stdout, fp.Stdout()) }()
	go func() { defer drain.Done(); _, _ = io.Copy(&stderr, fp.Stderr()) }()

	if _, err := io.WriteString(fp.Stdin(), "hello"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := fp.Stdin().Close(); err != nil { // half-close → echo drains → exit
		t.Fatalf("close stdin: %v", err)
	}

	code, err := fp.Wait(t.Context())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 7 {
		t.Fatalf("front exit code = %d, want 7", code)
	}
	drain.Wait()

	if stdout.String() != "hello" {
		t.Errorf("stdout = %q, want %q (echo)", stdout.String(), "hello")
	}
	if stderr.String() != "READY\n" {
		t.Errorf("stderr = %q, want %q (banner)", stderr.String(), "READY\n")
	}
	select {
	case c := <-relayDone:
		if c != 7 {
			t.Fatalf("relay exit code = %d, want 7", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not finish")
	}
}

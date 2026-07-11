package vmmstream

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/nats-io/nats.go"
)

// echoProcess is an in-process fake Process for the session round-trip: it echoes
// stdin→stdout, emits a fixed stderr banner, records resizes, and exits with a
// fixed non-zero code once the client half-closes stdin (so the test proves the
// exit code propagates, not just a zero default).
type echoProcess struct {
	stdinW   *io.PipeWriter
	stdoutR  *io.PipeReader
	stdoutW  *io.PipeWriter
	stderrR  *io.PipeReader
	stderrW  *io.PipeWriter
	resizes  chan WinSize
	done     chan struct{}
	killed   chan struct{}
	killOnce sync.Once
}

func newEchoProcess() *echoProcess {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	p := &echoProcess{
		stdinW:  inW,
		stdoutR: outR,
		stdoutW: outW,
		stderrR: errR,
		stderrW: errW,
		resizes: make(chan WinSize, 1),
		done:    make(chan struct{}),
		killed:  make(chan struct{}),
	}
	// stderr: a one-line banner, then EOF.
	go func() {
		_, _ = io.WriteString(errW, "READY\n")
		_ = errW.Close()
	}()
	// stdout: echo stdin until the client half-closes it, then EOF + exit.
	go func() {
		_, _ = io.Copy(outW, inR)
		_ = outW.Close()
		close(p.done)
	}()
	return p
}

func (p *echoProcess) Stdin() io.WriteCloser { return p.stdinW }
func (p *echoProcess) Stdout() io.Reader     { return p.stdoutR }
func (p *echoProcess) Stderr() io.Reader     { return p.stderrR }

func (p *echoProcess) Resize(cols, rows uint16) error {
	select {
	case p.resizes <- WinSize{Cols: cols, Rows: rows}:
	default:
	}
	return nil
}

func (p *echoProcess) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.done:
		return 7, nil
	case <-p.killed:
		return 137, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Kill unblocks a running Wait and EOFs the output pipes so Run's pumps stop —
// modelling connProcess.Kill (the front's real server-side process).
func (p *echoProcess) Kill(context.Context) error {
	p.killOnce.Do(func() {
		close(p.killed)
		_ = p.stdoutW.Close()
		_ = p.stderrW.Close()
	})
	return nil
}

// TestSessionRoundTrip drives Serve (server) and Attach (client) against each
// other over a loopback NATS server through the real per-session subjects
// (ape.vmm.<node>.exec.<sid>.{stdin,stdout,stderr,resize,control,exit}), proving
// the full multiplexed session end-to-end: client stdin echoes back on stdout, a
// stderr banner arrives, a resize is applied server-side, and the process exit
// code propagates — all three data streams flow-controlled on one control
// subject (PLAN-18 D2).
func TestSessionRoundTrip(t *testing.T) {
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

	const prefix = "ape.vmm.node1.exec.s1"
	proc := newEchoProcess()

	// Set up + flush the server (subscribes control/stdin/resize) BEFORE the
	// client starts, mirroring how the front finishes setup before answering
	// attach.open. Output stays gated at zero credit until the client primes.
	srv, err := NewServerSession(srvConn, prefix, proc, 4)
	if err != nil {
		t.Fatalf("NewServerSession: %v", err)
	}
	_ = srvConn.Flush()
	srvDone := make(chan int, 1)
	go func() {
		code, _ := srv.Run(context.Background())
		srvDone <- code
	}()

	ctx := t.Context()

	cliInR, cliInW := io.Pipe()
	resize := make(chan WinSize, 1)
	var stdout, stderr bytes.Buffer
	type result struct {
		code int
		err  error
	}
	cliDone := make(chan result, 1)
	go func() {
		code, aerr := Attach(ctx, cliConn, prefix, ClientStreams{
			Stdin: cliInR, Stdout: &stdout, Stderr: &stderr, Resize: resize,
		}, 4)
		cliDone <- result{code, aerr}
	}()

	// Resize first and wait for the server to apply it (deterministic: the fake
	// records it on a channel) before triggering exit — no sleep, no flake.
	resize <- WinSize{Cols: 120, Rows: 40}
	select {
	case sz := <-proc.resizes:
		if sz != (WinSize{Cols: 120, Rows: 40}) {
			t.Fatalf("resize applied = %+v, want {120 40}", sz)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resize not applied server-side")
	}

	// Drive stdin to completion: "hello" then half-close → process exits.
	if _, werr := io.WriteString(cliInW, "hello"); werr != nil {
		t.Fatalf("write stdin: %v", werr)
	}
	_ = cliInW.Close()

	select {
	case r := <-cliDone:
		if r.err != nil {
			t.Fatalf("attach: %v", r.err)
		}
		if r.code != 7 {
			t.Fatalf("client exit code = %d, want 7", r.code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client session did not complete (deadlock?)")
	}
	select {
	case code := <-srvDone:
		if code != 7 {
			t.Fatalf("server exit code = %d, want 7", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server session did not complete")
	}

	if stdout.String() != "hello" {
		t.Errorf("stdout = %q, want %q (echo)", stdout.String(), "hello")
	}
	if stderr.String() != "READY\n" {
		t.Errorf("stderr = %q, want %q (banner)", stderr.String(), "READY\n")
	}
}

// TestServerSessionIdleTimeout proves the server reaps an abandoned session: with
// no client ever attaching (so no keepalive pings and no credit grants), the idle
// watchdog fires, cancels the session, and Kills the process — the fix for the
// leaked-guest-exec gap where a dropped client left the exec running forever
// (NATS gives the server no disconnect signal — PLAN-18 D2).
func TestServerSessionIdleTimeout(t *testing.T) {
	url := natstest.RunServer(t)
	srvConn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer srvConn.Close()

	const prefix = "ape.vmm.node1.exec.sidle"
	proc := newEchoProcess() // Wait blocks — nothing half-closes stdin

	srv, err := NewServerSession(srvConn, prefix, proc, 4)
	if err != nil {
		t.Fatalf("NewServerSession: %v", err)
	}
	srv.idleTimeout = 150 * time.Millisecond // shrink the watchdog for the test
	_ = srvConn.Flush()

	srvDone := make(chan int, 1)
	go func() {
		code, _ := srv.Run(context.Background())
		srvDone <- code
	}()

	select {
	case <-srvDone:
	case <-time.After(3 * time.Second):
		t.Fatal("idle watchdog did not reap the abandoned session")
	}
	select {
	case <-proc.killed:
	default:
		t.Fatal("idle reap did not Kill the process")
	}
}

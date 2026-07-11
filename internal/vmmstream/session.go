package vmmstream

import (
	"context"
	"io"
	"sync"

	"github.com/nats-io/nats.go"
)

// DefaultCredit is the initial per-data-channel flow-control window, in frames.
// 16 × MaxFrameData ≈ 512 KiB in flight per channel — enough to keep a PTY busy
// without letting a fast producer outrun a slow consumer into a NATS drop.
const DefaultCredit = 16

// Process is the server-side end of an interactive session: a running process
// (a containerd task exec, or a test fake) whose stdio the session pipes over the
// NATS session subjects. Serve reads Stdout/Stderr and streams them to the
// client, writes client keystrokes into Stdin, forwards Resize, and blocks on
// Wait for the exit code. Serve closes Stdin when the client half-closes or the
// session tears down; the caller owns the process itself.
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Resize(cols, rows uint16) error
	Wait(ctx context.Context) (int, error)
}

// WinSize is a terminal size for a resize control frame.
type WinSize struct{ Cols, Rows uint16 }

// ClientStreams is the client-side local endpoint of a session: the local stdio
// Attach binds to the session subjects. Stdin may be nil (no input). Resize is
// optional; when non-nil, Attach forwards each size on the resize channel until
// it closes or the session ends (wire it to SIGWINCH).
type ClientStreams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Resize <-chan WinSize
}

// Serve binds proc's stdio to the session subjects under prefix
// (ape.vmm.<node>.exec.<sid>) and runs until proc exits or ctx is cancelled. It
// streams stdout/stderr to the client, feeds client stdin into proc, forwards
// resize control frames, and — only after all output is flushed — publishes the
// exit code on the exit channel. It returns proc's exit code. credit ≤ 0 uses
// DefaultCredit. This is the transport half the aped front runs; the process is
// supplied by the executor over the priv socket (or a fake, in tests).
func Serve(ctx context.Context, nc *nats.Conn, prefix string, proc Process, credit int) (int, error) {
	if credit < 1 {
		credit = DefaultCredit
	}
	ctrl := ChannelSubject(prefix, ChannelControl)

	// stdout/stderr: server produces → Senders (client refills credit on control).
	stdout, err := NewSender(nc, ChannelSubject(prefix, ChannelStdout), ctrl, ChannelStdout, credit)
	if err != nil {
		return 0, err
	}
	stderr, err := NewSender(nc, ChannelSubject(prefix, ChannelStderr), ctrl, ChannelStderr, credit)
	if err != nil {
		return 0, err
	}
	// stdin: server consumes ← Receiver (server refills credit on control).
	stdin, err := NewReceiver(nc, ChannelSubject(prefix, ChannelStdin), ctrl, ChannelStdin, credit)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stdin.Close() }()

	// resize: client → server control frames applied to the process PTY.
	resizeSub, err := nc.Subscribe(ChannelSubject(prefix, ChannelResize), func(m *nats.Msg) {
		if f, derr := DecodeControl(m.Data); derr == nil && f.Type == ControlResize {
			_ = proc.Resize(f.Cols, f.Rows)
		}
	})
	if err != nil {
		return 0, err
	}
	defer func() { _ = resizeSub.Unsubscribe() }()
	_ = nc.Flush()

	// Pump output: proc stdout/stderr → Senders. The WaitGroup lets us defer the
	// exit frame until every byte of output has been framed (EOF sentinel sent).
	var out sync.WaitGroup
	out.Add(2)
	go func() { defer out.Done(); pumpOut(ctx, stdout, proc.Stdout()) }()
	go func() { defer out.Done(); pumpOut(ctx, stderr, proc.Stderr()) }()

	// Pump input: stdin Receiver → proc stdin. Ends on the client's stdin EOF
	// sentinel or when stdin.Close() (deferred) unblocks the copy at teardown.
	go func() {
		_, _ = io.Copy(proc.Stdin(), stdin)
		_ = proc.Stdin().Close()
	}()

	code, waitErr := proc.Wait(ctx)

	// The process is gone: its stdout/stderr hit EOF, so the output pumps finish.
	// Wait for them (so trailing output is sent) before signalling exit.
	out.Wait()
	if perr := publishExit(nc, prefix, code); perr != nil && waitErr == nil {
		waitErr = perr
	}
	return code, waitErr
}

// Attach is the client half: it binds local stdio (s) to the session subjects
// under prefix and returns the process exit code once the session completes.
// stdout/stderr are drained to EOF and the exit frame consumed before returning,
// so no trailing output is lost regardless of cross-subject delivery order. ctx
// cancellation tears the session down early. credit ≤ 0 uses DefaultCredit.
func Attach(ctx context.Context, nc *nats.Conn, prefix string, s ClientStreams, credit int) (int, error) {
	if credit < 1 {
		credit = DefaultCredit
	}
	ctrl := ChannelSubject(prefix, ChannelControl)

	// Subscribe exit first so a fast process can't exit before we are listening.
	exitC := make(chan int, 1)
	exitSub, err := nc.Subscribe(ChannelSubject(prefix, ChannelExit), func(m *nats.Msg) {
		if f, derr := DecodeControl(m.Data); derr == nil && f.Type == ControlExit {
			select {
			case exitC <- f.Code:
			default:
			}
		}
	})
	if err != nil {
		return 0, err
	}
	defer func() { _ = exitSub.Unsubscribe() }()

	stdout, err := NewReceiver(nc, ChannelSubject(prefix, ChannelStdout), ctrl, ChannelStdout, credit)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := NewReceiver(nc, ChannelSubject(prefix, ChannelStderr), ctrl, ChannelStderr, credit)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stderr.Close() }()
	stdin, err := NewSender(nc, ChannelSubject(prefix, ChannelStdin), ctrl, ChannelStdin, credit)
	if err != nil {
		return 0, err
	}
	_ = nc.Flush()

	var out sync.WaitGroup
	out.Add(2)
	go func() { defer out.Done(); _, _ = io.Copy(s.Stdout, stdout) }()
	go func() { defer out.Done(); _, _ = io.Copy(s.Stderr, stderr) }()

	if s.Stdin != nil {
		go func() { pumpOut(ctx, stdin, s.Stdin); _ = stdin.CloseSend() }()
	}
	if s.Resize != nil {
		go resizePump(ctx, nc, prefix, s.Resize)
	}

	// Wait for output to drain (EOF sentinels) then read the exit code. exitC is
	// buffered, so it does not matter whether exit arrives before or after the
	// output EOFs (NATS does not order across subjects).
	outDone := make(chan struct{})
	go func() { out.Wait(); close(outDone) }()
	select {
	case <-outDone:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case code := <-exitC:
		return code, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// pumpOut copies r into s in ≤MaxFrameData writes until r hits EOF, then sends
// the EOF sentinel. A read error other than EOF ends the pump without a
// sentinel (the peer's exit frame still terminates the session).
func pumpOut(ctx context.Context, s *Sender, r io.Reader) {
	buf := make([]byte, MaxFrameData)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := s.Write(ctx, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if err == io.EOF {
				_ = s.CloseSend()
			}
			return
		}
	}
}

// resizePump forwards terminal sizes as resize control frames until the channel
// closes or ctx is done.
func resizePump(ctx context.Context, nc *nats.Conn, prefix string, sizes <-chan WinSize) {
	subj := ChannelSubject(prefix, ChannelResize)
	for {
		select {
		case sz, ok := <-sizes:
			if !ok {
				return
			}
			if b, err := (ControlFrame{Type: ControlResize, Cols: sz.Cols, Rows: sz.Rows}).Encode(); err == nil {
				_ = nc.Publish(subj, b)
				_ = nc.Flush()
			}
		case <-ctx.Done():
			return
		}
	}
}

// publishExit publishes the final exit code on the session's exit channel.
func publishExit(nc *nats.Conn, prefix string, code int) error {
	b, err := ControlFrame{Type: ControlExit, Code: code}.Encode()
	if err != nil {
		return err
	}
	if err := nc.Publish(ChannelSubject(prefix, ChannelExit), b); err != nil {
		return err
	}
	return nc.Flush()
}

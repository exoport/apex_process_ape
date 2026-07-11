package vmmstream

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
)

// DefaultCredit is the initial per-data-channel flow-control window, in frames.
// 16 × MaxFrameData ≈ 512 KiB in flight per channel — enough to keep a PTY busy
// without letting a fast producer outrun a slow consumer into a NATS drop.
const DefaultCredit = 16

// Client keepalive / server idle watchdog (PLAN-18 D2). NATS is subject-oriented
// and gives a publisher no signal that its subscriber vanished, so an abandoned
// interactive client (network drop / kill -9) is otherwise invisible: the server
// would relay forever and leak the guest exec. The client pings the control
// channel every KeepaliveInterval; the server reaps the session if no inbound
// control traffic (a ping OR a credit grant — both prove a live client) arrives
// within IdleTimeout, which tolerates a couple of dropped pings.
const (
	KeepaliveInterval = 15 * time.Second
	IdleTimeout       = 45 * time.Second
)

// Process is the server-side end of an interactive session (the running exec/
// attach process whose stdio Run pipes over the session subjects). It is the
// pure workspace.Process contract, re-exported so callers of this transport need
// not import both packages.
type Process = workspace.Process

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

// ServerSession is the server end of a session, set up but not yet running.
// Splitting setup from Run lets the aped front finish subscribing every inbound
// channel (control/stdin/resize) BEFORE it answers attach.open — so the client
// only starts once the server is listening. Output senders start at zero credit
// and publish nothing until the client primes (after it has subscribed), which
// closes the NATS-core no-retention race in both directions.
type ServerSession struct {
	nc          *nats.Conn
	prefix      string
	proc        Process
	stdout      *Sender
	stderr      *Sender
	stdin       *Receiver
	resizeSub   *nats.Subscription
	livenessSub *nats.Subscription
	ping        chan struct{} // any inbound control frame (ping/credit) → activity
	idleTimeout time.Duration // watchdog window; overridable in tests
}

// NewServerSession binds proc to the session subjects under prefix
// (ape.vmm.<node>.exec.<sid>): it creates the stdout/stderr senders (gated at
// zero credit), subscribes the stdin/resize inbound channels, and returns the
// ready-to-run session. The caller must nc.Flush() before signalling the client,
// then call Run. credit ≤ 0 uses DefaultCredit. This is the half the aped front
// runs; proc is supplied by the executor over the priv socket (or a fake, in
// tests) — the network-less executor holds no NATS conn of its own.
func NewServerSession(nc *nats.Conn, prefix string, proc Process, credit int) (*ServerSession, error) {
	if credit < 1 {
		credit = DefaultCredit
	}
	ctrl := ChannelSubject(prefix, ChannelControl)

	var cleanup []func()
	fail := func(err error) (*ServerSession, error) {
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
		return nil, err
	}

	stdout, err := NewSender(nc, ChannelSubject(prefix, ChannelStdout), ctrl, ChannelStdout, 0)
	if err != nil {
		return fail(err)
	}
	cleanup = append(cleanup, func() { _ = stdout.creditSub.Unsubscribe() })
	stderr, err := NewSender(nc, ChannelSubject(prefix, ChannelStderr), ctrl, ChannelStderr, 0)
	if err != nil {
		return fail(err)
	}
	cleanup = append(cleanup, func() { _ = stderr.creditSub.Unsubscribe() })
	// stdin: the client sender starts with credit (keystrokes come long after
	// setup, so it is not race-sensitive); this receiver refills on read.
	stdin, err := NewReceiver(nc, ChannelSubject(prefix, ChannelStdin), ctrl, ChannelStdin, credit)
	if err != nil {
		return fail(err)
	}
	cleanup = append(cleanup, func() { _ = stdin.Close() })

	s := &ServerSession{
		nc: nc, prefix: prefix, proc: proc, stdout: stdout, stderr: stderr, stdin: stdin,
		ping: make(chan struct{}, 1), idleTimeout: IdleTimeout,
	}
	sub, err := nc.Subscribe(ChannelSubject(prefix, ChannelResize), func(m *nats.Msg) {
		if f, derr := DecodeControl(m.Data); derr == nil && f.Type == ControlResize {
			_ = proc.Resize(f.Cols, f.Rows)
		}
	})
	if err != nil {
		return fail(err)
	}
	s.resizeSub = sub
	cleanup = append(cleanup, func() { _ = sub.Unsubscribe() })

	// Liveness: any inbound frame on the control subject (a client ping or a
	// credit grant) proves the client is alive, so signal the idle watchdog. The
	// signal is coalesced (buffered 1, dropped if full) so a busy credit stream
	// never blocks NATS dispatch.
	live, err := nc.Subscribe(ctrl, func(*nats.Msg) {
		select {
		case s.ping <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return fail(err)
	}
	s.livenessSub = live
	return s, nil
}

// Run streams proc's stdio over the session subjects until proc exits or ctx is
// cancelled, then — only after all output is flushed — publishes the exit code.
// It returns proc's exit code. Output publication is gated on the client's credit
// prime, so no frame is emitted before the client is subscribed.
func (s *ServerSession) Run(ctx context.Context) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = s.livenessSub.Unsubscribe() }()
	defer func() { _ = s.resizeSub.Unsubscribe() }()
	defer func() { _ = s.stdin.Close() }()

	// Idle watchdog: reap the session if the client falls silent (no ping/credit
	// within idleTimeout) — it has vanished, and NATS will not tell us. It cancels
	// ctx, which unblocks the pumps and Wait below.
	go s.watchIdle(ctx, cancel)

	// Pump output: proc stdout/stderr → senders. The WaitGroup lets us defer the
	// exit frame until every byte of output has been framed (EOF sentinel sent).
	var out sync.WaitGroup
	out.Add(2)
	go func() { defer out.Done(); pumpOut(ctx, s.stdout, s.proc.Stdout()) }()
	go func() { defer out.Done(); pumpOut(ctx, s.stderr, s.proc.Stderr()) }()

	// Pump input: stdin receiver → proc stdin. Ends on the client's stdin EOF
	// sentinel or when stdin.Close() (deferred) unblocks the copy at teardown.
	go func() {
		_, _ = io.Copy(s.proc.Stdin(), s.stdin)
		_ = s.proc.Stdin().Close()
	}()

	code, waitErr := s.proc.Wait(ctx)
	if waitErr != nil {
		// ctx cancelled (idle watchdog fired) or the process errored out: force it
		// down so the guest exec is reaped and the output pumps stop parking on a
		// Read whose producer is gone. Kill is idempotent on an exited process.
		_ = s.proc.Kill(ctx)
	}

	// The process is gone: its stdout/stderr hit EOF, so the output pumps finish.
	// Wait for them (so trailing output is sent) before signalling exit.
	out.Wait()
	if perr := publishExit(s.nc, s.prefix, code); perr != nil && waitErr == nil {
		waitErr = perr
	}
	return code, waitErr
}

// watchIdle cancels the session when no inbound control traffic (a client ping
// or a credit grant) arrives within idleTimeout — the mark of a vanished client,
// since NATS delivers no disconnect. Any activity resets the timer, so a live
// session (even one idling at a shell prompt, kept warm by keepalive pings)
// never trips it. One goroutine owns the timer, so the Stop/drain/Reset is safe.
func (s *ServerSession) watchIdle(ctx context.Context, cancel context.CancelFunc) {
	t := time.NewTimer(s.idleTimeout)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.ping:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(s.idleTimeout)
		case <-t.C:
			cancel()
			return
		}
	}
}

// Attach is the client half: it binds local stdio (s) to the session subjects
// under prefix and returns the process exit code once the session completes. It
// subscribes every inbound channel and primes output credit (releasing the
// server's zero-credit gate) before any output can flow, then drains stdout/
// stderr to EOF and consumes the exit frame before returning — so no trailing
// output is lost regardless of NATS cross-subject delivery order. ctx
// cancellation tears the session down early. credit ≤ 0 uses DefaultCredit.
func Attach(ctx context.Context, nc *nats.Conn, prefix string, s ClientStreams, credit int) (int, error) {
	if credit < 1 {
		credit = DefaultCredit
	}
	ctrl := ChannelSubject(prefix, ChannelControl)

	// Keepalive: ping the control channel on a timer so the server's idle watchdog
	// can tell this session from an abandoned one — NATS gives the server no signal
	// that we vanished. Bound to a child ctx cancelled when Attach returns.
	kaCtx, kaCancel := context.WithCancel(ctx)
	defer kaCancel()
	go keepalive(kaCtx, nc, ctrl)

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

	// Everything is subscribed; flush, then release the server's output gate.
	_ = nc.Flush()
	_ = stdout.Prime()
	_ = stderr.Prime()

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
	// buffered, so delivery order between the output EOFs and the exit frame does
	// not matter (NATS does not order across subjects).
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

// keepalive publishes a ping on the control subject every KeepaliveInterval so
// the server's idle watchdog can distinguish a live-but-quiet session from an
// abandoned client. It stops when ctx is done (Attach returned or was cancelled).
func keepalive(ctx context.Context, nc *nats.Conn, ctrl string) {
	ping, err := ControlFrame{Type: ControlPing}.Encode()
	if err != nil {
		return
	}
	t := time.NewTicker(KeepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = nc.Publish(ctrl, ping)
			_ = nc.Flush()
		}
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

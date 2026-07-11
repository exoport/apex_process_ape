package aped

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"github.com/exoport/apex_process_ape/internal/vmmstream"
)

// Interactive exec/attach rides the priv socket as framed bidirectional streams
// (PLAN-18 D2). The network-less executor holds the containerd task PTY but no
// NATS conn, so it relays the process stdio to the de-privileged front over the
// SEQPACKET priv socket, and the front bridges that to the NATS session subjects
// (internal/vmmstream). One SEQPACKET message is one frame: a 1-byte channel tag
// followed by the payload. Flow control on this leg is the kernel socket buffer
// (backpressure) — the credit window lives on the NATS leg only, so a slow NATS
// client backpressures through the front into the executor and, in turn, the PTY.

type streamCh byte

const (
	streamStdin  streamCh = 0 // payload: raw stdin bytes; empty payload = stdin half-close
	streamStdout streamCh = 1 // payload: raw stdout bytes
	streamStderr streamCh = 2 // payload: raw stderr bytes
	streamResize streamCh = 3 // payload: cols(uint16 BE) rows(uint16 BE)
	streamExit   streamCh = 4 // payload: code(int32 BE) — the terminal frame
)

// Fixed control payload sizes (big-endian): resize = 2×uint16, exit = int32.
const (
	resizePayloadLen = 4
	exitPayloadLen   = 4
)

// errStreamClosed is the sentinel a connProcess reports through Wait when the
// priv conn dies before the process sends its exit frame.
var errStreamClosed = errors.New("aped: exec stream closed before exit")

// relayProcessToConn is the EXECUTOR side: it relays proc's stdio to conn as
// stream frames — stdout/stderr as they are produced, inbound stdin/resize frames
// applied to proc, and (after all output drains) proc's exit code as the terminal
// frame. It returns when proc exits; the caller owns closing conn (which unblocks
// the inbound reader). proc is the containerd task exec in production, a fake in
// tests.
func relayProcessToConn(ctx context.Context, conn privConn, proc vmmstream.Process) (int, error) {
	var out sync.WaitGroup
	out.Add(2)
	go func() { defer out.Done(); frameCopy(conn, streamStdout, proc.Stdout()) }()
	go func() { defer out.Done(); frameCopy(conn, streamStderr, proc.Stderr()) }()

	// Inbound stdin/resize until the process exits or the conn drops. If the conn
	// drops FIRST — the front tore the session down, e.g. its idle watchdog reaped
	// an abandoned client — kill the guest exec so it does not leak; the SIGKILL
	// makes proc exit, which unblocks Wait below and reaps it cleanly. In the
	// normal flow readInbound returns only once the caller closes the conn (after
	// this func has already returned), so the Kill lands on an exited exec (a
	// benign not-found).
	go func() {
		readInbound(conn, proc)
		_ = proc.Kill(ctx)
	}()

	code, waitErr := proc.Wait(ctx)
	out.Wait() // stdout/stderr hit EOF once the process fds close
	_ = sendExit(conn, code)
	return code, waitErr
}

// frameCopy copies r into conn as ch-tagged frames until r hits EOF (or a send
// error). It does not frame an explicit EOF — the exit frame is the single
// terminal signal, and the front closes its readers on it.
func frameCopy(conn privConn, ch streamCh, r io.Reader) {
	buf := make([]byte, 1+vmmstream.MaxFrameData)
	buf[0] = byte(ch)
	for {
		n, err := r.Read(buf[1:])
		if n > 0 {
			if serr := conn.Send(buf[:1+n]); serr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// readInbound applies inbound stdin/resize frames to proc until conn errors.
func readInbound(conn privConn, proc vmmstream.Process) {
	for {
		frame, err := conn.Recv()
		if err != nil {
			return
		}
		if len(frame) == 0 {
			continue
		}
		switch payload := frame[1:]; streamCh(frame[0]) {
		case streamStdin:
			if len(payload) == 0 {
				_ = proc.Stdin().Close() // half-close
				continue
			}
			_, _ = proc.Stdin().Write(payload)
		case streamResize:
			if len(payload) >= resizePayloadLen {
				_ = proc.Resize(binary.BigEndian.Uint16(payload[0:2]), binary.BigEndian.Uint16(payload[2:4]))
			}
		case streamStdout, streamStderr, streamExit:
			// Executor-inbound frames only carry stdin/resize; ignore others.
		default:
		}
	}
}

// sendExit frames the terminal exit code.
func sendExit(conn privConn, code int) error {
	var f [1 + exitPayloadLen]byte
	f[0] = byte(streamExit)
	//nolint:gosec // exit codes are small (0-255); the int32↔uint32 round-trip is intentional
	binary.BigEndian.PutUint32(f[1:], uint32(int32(code)))
	return conn.Send(f[:])
}

// connProcess is the FRONT side: a vmmstream.Process backed by the priv stream.
// A demux goroutine routes inbound stdout/stderr frames into pipes the front's
// ServerSession reads, and the terminal exit frame into Wait; Stdin/Resize are
// written back as frames. The front runs vmmstream.NewServerSession over this,
// bridging the executor's PTY to the NATS session subjects.
type connProcess struct {
	conn    privConn
	stdin   *frameWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter
	exitC   chan exitResult
	once    sync.Once
}

type exitResult struct {
	code int
	err  error
}

var _ vmmstream.Process = (*connProcess)(nil)

// connToProcess wraps a priv conn as a front-side Process and starts its demux.
func connToProcess(conn privConn) *connProcess {
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	p := &connProcess{
		conn:    conn,
		stdin:   &frameWriter{conn: conn},
		stdoutR: outR, stdoutW: outW,
		stderrR: errR, stderrW: errW,
		exitC: make(chan exitResult, 1),
	}
	go p.demux()
	return p
}

// demux reads frames off the conn, routing stdout/stderr into the pipes and the
// exit frame into exitC (closing the pipes so the output pumps see EOF). A conn
// error before the exit frame finishes with errStreamClosed.
func (p *connProcess) demux() {
	for {
		frame, err := p.conn.Recv()
		if err != nil {
			p.finish(-1, errStreamClosed)
			return
		}
		if len(frame) == 0 {
			continue
		}
		switch payload := frame[1:]; streamCh(frame[0]) {
		case streamStdout:
			_, _ = p.stdoutW.Write(payload)
		case streamStderr:
			_, _ = p.stderrW.Write(payload)
		case streamExit:
			code := 0
			if len(payload) >= exitPayloadLen {
				//nolint:gosec // the int32↔uint32 exit-code round-trip is intentional
				code = int(int32(binary.BigEndian.Uint32(payload)))
			}
			p.finish(code, nil)
			return
		case streamStdin, streamResize:
			// Front-inbound frames only carry stdout/stderr/exit; ignore others.
		default:
		}
	}
}

// finish closes the output pipes (EOF to the pumps) and delivers the exit result
// exactly once.
func (p *connProcess) finish(code int, err error) {
	p.once.Do(func() {
		_ = p.stdoutW.Close()
		_ = p.stderrW.Close()
		p.exitC <- exitResult{code: code, err: err}
	})
}

func (p *connProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *connProcess) Stdout() io.Reader     { return p.stdoutR }
func (p *connProcess) Stderr() io.Reader     { return p.stderrR }

func (p *connProcess) Resize(cols, rows uint16) error {
	var f [1 + 4]byte
	f[0] = byte(streamResize)
	binary.BigEndian.PutUint16(f[1:3], cols)
	binary.BigEndian.PutUint16(f[3:5], rows)
	return p.conn.Send(f[:])
}

func (p *connProcess) Wait(ctx context.Context) (int, error) {
	select {
	case r := <-p.exitC:
		return r.code, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Kill tears the front side down when the session is abandoned: it finishes the
// output pipes (so the front's pumps unblock immediately, rather than parking
// on a Read that has no more producer) and drops the priv conn — which makes the
// executor's readInbound error out and SIGKILL the guest exec. Idempotent.
func (p *connProcess) Kill(context.Context) error {
	p.finish(-1, errStreamClosed)
	return p.conn.Close()
}

// frameWriter frames writes to a data channel; Close sends the empty stdin frame
// (half-close) WITHOUT closing the conn (which still carries output + exit).
type frameWriter struct{ conn privConn }

func (w *frameWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		n := min(len(b), vmmstream.MaxFrameData)
		frame := make([]byte, 1+n)
		frame[0] = byte(streamStdin)
		copy(frame[1:], b[:n])
		if err := w.conn.Send(frame); err != nil {
			return total, err
		}
		total += n
		b = b[n:]
	}
	return total, nil
}

func (w *frameWriter) Close() error { return w.conn.Send([]byte{byte(streamStdin)}) }

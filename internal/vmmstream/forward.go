package vmmstream

import (
	"context"
	"io"

	"github.com/nats-io/nats.go"
)

// The port-forward session kind (PLAN-24 D2).
//
// A forward is this transport carrying a different PAYLOAD, not a different
// protocol. The frames, the ≤32 KiB chunking, the channel-tagged credit grants,
// the EOF sentinels and the exit frame are byte-for-byte what an exec or attach
// uses — so a forward cannot perturb a live interactive session, and a change to
// the flow control is a change to all three at once rather than to one of them
// silently.
//
// What differs is the discipline at the ends:
//
//   - NO terminal. A PTY performs line-ending translation and echo, which would
//     corrupt an arbitrary byte stream in ways that look like a bug in whatever
//     is being forwarded. The server opens the guest process without one.
//   - NO resize. There is no terminal to resize, so the channel is simply unused.
//   - stderr is DIAGNOSTIC, not data. The guest end writes its errors there
//     precisely so they cannot be spliced into the forwarded stream; this side
//     keeps them apart for the same reason.

// ForwardStreams is the client-side local endpoint of a forward session.
type ForwardStreams struct {
	// Local is the accepted connection whose bytes ride the session. It is read
	// from AND written to, and closed when the session ends.
	Local io.ReadWriteCloser
	// Diag receives the guest end's stderr — a dial failure, typically. nil
	// discards it. It must never be the same writer as Local: a diagnostic
	// delivered into the byte stream is indistinguishable from data to whatever
	// is on the other end.
	Diag io.Writer
}

// Forward binds a local connection to a forward session's subjects and returns
// the guest process's exit code once the session completes.
//
// Closing semantics are what make a forward behave like a socket rather than
// like a command:
//
//   - The local peer hanging up EOFs the stdin pump, which sends the EOF
//     sentinel; the guest end half-closes its socket, so a service that reads to
//     EOF sees the end of the request instead of waiting forever.
//   - The guest service closing EOFs stdout, which ends the session; Local is
//     closed here so the local peer sees the connection drop rather than a
//     silently dead socket.
//
// ctx cancellation tears the session down from this side.
func Forward(ctx context.Context, nc *nats.Conn, prefix string, s ForwardStreams, credit int) (int, error) {
	defer func() { _ = s.Local.Close() }()

	diag := s.Diag
	if diag == nil {
		diag = io.Discard
	}
	// Deliberately Attach: one implementation of the client half, so the forward
	// path cannot drift from the exec/attach path it shares a protocol with.
	// Resize is nil — there is no terminal — which leaves the resize channel
	// unused rather than carrying meaningless frames.
	return Attach(ctx, nc, prefix, ClientStreams{
		Stdin:  s.Local,
		Stdout: s.Local,
		Stderr: diag,
	}, credit)
}

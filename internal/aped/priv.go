package aped

import (
	"errors"
	"time"
)

// maxCommandFrame bounds a single priv-socket message. Commands are small
// (the largest is a resolved WorkspaceSpec — a few KB); 1 MiB is a generous
// ceiling. SOCK_SEQPACKET truncates a larger message, so the receive buffer
// must be at least this size.
const maxCommandFrame = 1 << 20

// ErrPrivUnsupported is returned by the priv transport on non-Linux platforms —
// the AF_UNIX SEQPACKET + SO_PEERCRED boundary is Linux-only. aped itself runs
// only on Linux; the stub keeps the Windows cross-compile green.
var ErrPrivUnsupported = errors.New("aped: the priv socket requires Linux (AF_UNIX SEQPACKET + SO_PEERCRED)")

// Peer is the SO_PEERCRED identity of a priv-socket peer — the authoritative,
// kernel-attested uid/pid of the connecting process. The executor gates on it.
type Peer struct {
	UID uint32
	PID uint32
}

// privConn is one framed command connection. Each Send is one SEQPACKET
// message and each Recv reads exactly one, so command boundaries are preserved
// by the kernel (no length-prefix framing needed).
type privConn interface {
	Send(b []byte) error
	Recv() ([]byte, error)
	// Peer returns the connecting process's SO_PEERCRED identity.
	Peer() (Peer, error)
	// SetReadDeadline bounds a blocking Recv so a silent peer cannot hang the
	// executor goroutine.
	SetReadDeadline(t time.Time) error
	Close() error
}

// privListener accepts framed command connections on the priv socket.
type privListener interface {
	Accept() (privConn, error)
	Addr() string
	Close() error
}

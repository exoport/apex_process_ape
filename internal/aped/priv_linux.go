//go:build linux

package aped

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// privSocketMode is the priv socket permission: only root + group `ape` may
// connect() (the gate belongs on this AF_UNIX socket, not the NATS listener).
const privSocketMode = 0o660

// unixConn is a privConn over an AF_UNIX SOCK_SEQPACKET connection.
type unixConn struct{ c *net.UnixConn }

func (u *unixConn) Send(b []byte) error {
	if len(b) > maxCommandFrame {
		return fmt.Errorf("aped: command frame %d exceeds max %d", len(b), maxCommandFrame)
	}
	_, err := u.c.Write(b)
	return err
}

func (u *unixConn) Recv() ([]byte, error) {
	buf := make([]byte, maxCommandFrame)
	n, err := u.c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// Peer reads the connecting process's SO_PEERCRED — kernel-attested, so the
// executor's uid gate cannot be spoofed by the peer (this is the SO_PEERCRED
// relocation D1 calls for: a real local socket where it is authoritative).
func (u *unixConn) Peer() (Peer, error) {
	raw, err := u.c.SyscallConn()
	if err != nil {
		return Peer{}, fmt.Errorf("aped: syscall conn: %w", err)
	}
	var ucred *unix.Ucred
	var cerr error
	if ctlErr := raw.Control(func(fd uintptr) {
		ucred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); ctlErr != nil {
		return Peer{}, fmt.Errorf("aped: peercred control: %w", ctlErr)
	}
	if cerr != nil {
		return Peer{}, fmt.Errorf("aped: getsockopt SO_PEERCRED: %w", cerr)
	}
	return Peer{UID: ucred.Uid, PID: uint32(ucred.Pid)}, nil //nolint:gosec // a kernel-reported pid is non-negative
}

func (u *unixConn) SetReadDeadline(t time.Time) error { return u.c.SetReadDeadline(t) }

func (u *unixConn) Close() error { return u.c.Close() }

// unixListener is a privListener over an AF_UNIX SOCK_SEQPACKET listener.
type unixListener struct {
	l    *net.UnixListener
	path string
}

func (ul *unixListener) Accept() (privConn, error) {
	c, err := ul.l.AcceptUnix()
	if err != nil {
		return nil, err
	}
	return &unixConn{c: c}, nil
}

func (ul *unixListener) Addr() string { return ul.path }

func (ul *unixListener) Close() error { return ul.l.Close() }

// listenPriv binds the AF_UNIX SEQPACKET priv socket at path (0660). In
// production the socket is provided by the aped-priv.socket systemd unit with
// SocketMode=0660/SocketGroup=ape; this self-bind path is for the non-socket-
// activated run and for tests. A stale socket file is removed first.
func listenPriv(path string) (privListener, error) {
	_ = os.Remove(path)
	addr := &net.UnixAddr{Name: path, Net: "unixpacket"}
	l, err := net.ListenUnix("unixpacket", addr)
	if err != nil {
		return nil, fmt.Errorf("aped: listen priv socket %s: %w", path, err)
	}
	// Only root + group `ape` may connect() (the gate the design wants on the
	// AF_UNIX socket, not the loopback TCP listener). Group ownership is set by
	// the deployment; here we tighten the mode.
	if err := os.Chmod(path, privSocketMode); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("aped: chmod priv socket: %w", err)
	}
	return &unixListener{l: l, path: path}, nil
}

// dialPriv connects to the priv socket.
func dialPriv(path string) (privConn, error) {
	addr := &net.UnixAddr{Name: path, Net: "unixpacket"}
	c, err := net.DialUnix("unixpacket", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("aped: dial priv socket %s: %w", path, err)
	}
	return &unixConn{c: c}, nil
}

// socketActivatedListener adopts a systemd socket-activated listener (LISTEN_FDS
// starting at fd 3), returning ok=false when not socket-activated so the caller
// falls back to listenPriv. This backs `aped run --socket-activated` +
// aped-priv.socket (Appendix A).
func socketActivatedListener() (l privListener, ok bool, err error) {
	if os.Getenv("LISTEN_FDS") == "" {
		return nil, false, nil
	}
	if pidStr := os.Getenv("LISTEN_PID"); pidStr != "" {
		if pid, perr := strconv.Atoi(pidStr); perr == nil && pid != os.Getpid() {
			return nil, false, nil // fds are for a different process
		}
	}
	n, perr := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if perr != nil || n < 1 {
		return nil, false, nil //nolint:nilerr // absent/garbled LISTEN_FDS = not socket-activated; fall back
	}
	const firstFD = 3
	f := os.NewFile(uintptr(firstFD), "aped-priv.sock")
	fl, err := net.FileListener(f)
	if err != nil {
		return nil, false, fmt.Errorf("aped: adopt socket-activated fd: %w", err)
	}
	ul, isUnix := fl.(*net.UnixListener)
	if !isUnix {
		_ = fl.Close()
		return nil, false, errors.New("aped: socket-activated fd is not a unix listener")
	}
	return &unixListener{l: ul, path: "/run/aped/priv.sock"}, true, nil
}

//go:build linux

package aped

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPrivSocketRoundTripAndPeer proves the AF_UNIX SEQPACKET transport
// preserves message boundaries and that SO_PEERCRED reports the dialer's
// kernel-attested uid/pid — the authoritative identity the executor gates on.
func TestPrivSocketRoundTripAndPeer(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "priv.sock")
	l, err := listenPriv(sock)
	if err != nil {
		t.Fatalf("listenPriv: %v", err)
	}
	defer func() { _ = l.Close() }()

	type result struct {
		peer Peer
		cmd  []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			done <- result{err: err}
			return
		}
		defer func() { _ = c.Close() }()
		peer, perr := c.Peer()
		if perr != nil {
			done <- result{err: perr}
			return
		}
		b, rerr := c.Recv()
		if rerr != nil {
			done <- result{err: rerr}
			return
		}
		_ = c.Send([]byte("pong"))
		done <- result{peer: peer, cmd: b}
	}()

	conn, err := dialPriv(sock)
	if err != nil {
		t.Fatalf("dialPriv: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Send([]byte("ping")); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := conn.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(resp) != "pong" {
		t.Fatalf("response = %q, want pong", resp)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("server side: %v", r.err)
		}
		if string(r.cmd) != "ping" {
			t.Fatalf("server received %q, want ping", r.cmd)
		}
		if r.peer.UID != uint32(os.Getuid()) {
			t.Fatalf("SO_PEERCRED uid = %d, want %d", r.peer.UID, os.Getuid())
		}
		if r.peer.PID != uint32(os.Getpid()) {
			t.Fatalf("SO_PEERCRED pid = %d, want %d", r.peer.PID, os.Getpid())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not complete in time")
	}
}

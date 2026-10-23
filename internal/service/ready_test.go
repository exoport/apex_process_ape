//go:build linux || darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/natstest"
	natsd "github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

// V11RC1-F2: `ape service` announces itself ready only once the server
// holds its endpoints' interest. A NATS Subscribe returns before the server
// has processed the SUB, so readiness is the Flush round trip after the
// last endpoint is added, not the return of Register. The daemon reaches the
// server here through a link that holds its writes back, so a SUB is late
// by that much: a ready line printed before the round trip lets a request
// sent on it from another connection, once and with no retry, reach the
// server first and get no responders.
func TestRunAnnouncesReadyOnlyOnceSubscribed(t *testing.T) {
	url := natstest.RunServer(t)
	slow := laggingProxy(t, url, 400*time.Millisecond)
	root := serviceProject(t)

	ready := &lineSignal{want: "▶ ape service", c: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, Options{Name: "ape", ProjectRoot: root, NatsURL: slow, ApeVersion: "0.0.0", Stderr: ready})
	}()
	stop := func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("Run returned %v, want nil after clean drain", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after context cancellation")
		}
	}

	cli, err := nats.Connect(url) // straight to the server, no lag
	if err != nil {
		cancel()
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cli.Drain() }()

	select {
	case <-ready.c:
	case err := <-errc:
		cancel()
		t.Fatalf("Run returned before announcing ready: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Run did not announce ready within 15s")
	}
	_, err = cli.Request("ape.svc.ape."+eventing.ProjectSlug(root)+".status", []byte("{}"), 2*time.Second)
	if err != nil {
		stop()
		t.Fatalf("a request sent once Run announced ready: %v", err)
	}
	stop()
}

// D408: the ready line follows a flush that succeeded. Here the link to the
// server goes quiet once the connection is up, so the SUBs and the flush's
// PING never arrive and the round trip times out: Run must fail, print no
// ready line, and return rather than serve.
func TestRunNotReadyWhenTheFlushTimesOut(t *testing.T) {
	defer func(d time.Duration) { readyFlushTimeout = d }(readyFlushTimeout)
	readyFlushTimeout = 300 * time.Millisecond

	url := natstest.RunServer(t)
	mute := mutingProxy(t, url)
	root := serviceProject(t)

	var stderr strings.Builder
	err := runUntilReturn(t, Options{Name: "ape", ProjectRoot: root, NatsURL: mute, ApeVersion: "0.0.0", Stderr: &stderr})
	if err == nil {
		t.Fatal("Run returned nil, want an error for an unconfirmed flush")
	}
	if !strings.Contains(err.Error(), "did not confirm") || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("error = %v, want the unconfirmed-endpoints error", err)
	}
	if strings.Contains(stderr.String(), "▶ ape service") {
		t.Errorf("a ready line was printed for an unconfirmed flush:\n%s", stderr.String())
	}
}

// D408: a server that refuses the endpoint subscriptions answers each SUB
// with -ERR before the flush's PONG. Run must fail naming the denied
// pattern, print no ready line, and return rather than serve nothing.
func TestRunNotReadyWhenTheServerRefusesTheEndpoints(t *testing.T) {
	url := denyingServer(t, "ape.svc.>")
	root := serviceProject(t)

	var stderr strings.Builder
	err := runUntilReturn(t, Options{Name: "ape", ProjectRoot: root, NatsURL: url, ApeVersion: "0.0.0", Stderr: &stderr})
	if err == nil {
		t.Fatal("Run returned nil, want an error for refused endpoints")
	}
	pattern := "ape.svc.ape." + eventing.ProjectSlug(root) + ".>"
	for _, want := range []string{"permissions violation", pattern, "not ready"} {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
	if !errors.Is(err, nats.ErrPermissionViolation) {
		t.Errorf("error = %v, want it to wrap nats.ErrPermissionViolation", err)
	}
	if strings.Contains(stderr.String(), "▶ ape service") {
		t.Errorf("a ready line was printed for refused endpoints:\n%s", stderr.String())
	}
}

// runUntilReturn runs Run and fails the test if it is still running (that
// is, serving) after a bound well past the ready check's.
func runUntilReturn(t *testing.T, o Options) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, o) }()
	select {
	case err := <-errc:
		return err
	case <-time.After(15 * time.Second):
		cancel()
		<-errc
		t.Fatal("Run was still serving 15s in; want it to return without announcing ready")
		return nil
	}
}

// denyingServer starts an embedded nats-server whose only user may not
// subscribe to deny, and returns a URL carrying that user.
func denyingServer(t *testing.T, deny string) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Host = "127.0.0.1"
	opts.Port = -1
	opts.Users = []*natsd.User{{
		Username:    "s",
		Password:    "s",
		Permissions: &natsd.Permissions{Subscribe: &natsd.SubjectPermission{Deny: []string{deny}}},
	}}
	s := natsserver.RunServer(&opts)
	t.Cleanup(s.Shutdown)
	return "nats://s:s@" + strings.TrimPrefix(s.ClientURL(), "nats://")
}

// mutingProxy forwards loopback connections to the NATS server at target
// and returns its own nats:// URL. The client's bytes reach the server up
// to and including its first PING (the CONNECT handshake), and are dropped
// after it; the server's bytes always come back. So a connection comes up,
// and nothing written on it afterwards reaches the server.
func mutingProxy(t *testing.T, target string) string {
	t.Helper()
	addr := strings.TrimPrefix(target, "nats://")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", addr)
			if err != nil {
				_ = client.Close()
				continue
			}
			mu.Lock()
			conns = append(conns, client, server)
			mu.Unlock()
			wg.Go(func() {
				_, _ = io.Copy(client, server)
				_ = client.Close()
			})
			wg.Go(func() {
				var seen []byte
				buf := make([]byte, 32<<10)
				muted := false
				for {
					n, err := client.Read(buf)
					if n > 0 && !muted {
						seen = append(seen, buf[:n]...)
						if _, werr := server.Write(buf[:n]); werr != nil {
							return
						}
						muted = strings.Contains(string(seen), "PING\r\n")
					}
					if err != nil {
						_ = server.Close()
						return
					}
				}
			})
		}
	})
	return "nats://" + ln.Addr().String()
}

// serviceProject makes a git working tree carrying an _apex/service.yaml
// that serves and allows only itself, for the tests that drive Run.
func serviceProject(t *testing.T) string {
	t.Helper()
	root := gitRepo(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "_apex"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("project_root: %s\nallow:\n  - %s\n", root, root)
	if err := os.WriteFile(filepath.Join(root, "_apex", "service.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// lineSignal is an io.Writer that closes c the first time what was written
// to it contains want.
type lineSignal struct {
	want string
	c    chan struct{}

	mu   sync.Mutex
	buf  strings.Builder
	once sync.Once
}

func (w *lineSignal) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf.Write(p)
	hit := strings.Contains(w.buf.String(), w.want)
	w.mu.Unlock()
	if hit {
		w.once.Do(func() { close(w.c) })
	}
	return len(p), nil
}

// laggingProxy forwards loopback connections to the NATS server at target
// and returns its own nats:// URL. Every byte a client writes reaches the
// server lag later, in order; the server's bytes come back at once. A
// client's PING is late by the same lag, so Flush still means the server
// has processed everything written before it.
func laggingProxy(t *testing.T, target string, lag time.Duration) string {
	t.Helper()
	addr := strings.TrimPrefix(target, "nats://")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", addr)
			if err != nil {
				_ = client.Close()
				continue
			}
			mu.Lock()
			conns = append(conns, client, server)
			mu.Unlock()
			wg.Go(func() {
				_, _ = io.Copy(client, server)
				_ = client.Close()
			})
			wg.Go(func() { lagCopy(server, client, lag) })
		}
	})
	return "nats://" + ln.Addr().String()
}

// lagCopy copies src to dst, each chunk written lag after it was read.
func lagCopy(dst, src net.Conn, lag time.Duration) {
	type chunk struct {
		b  []byte
		at time.Time
	}
	q := make(chan chunk, 4096)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = dst.Close() }()
		for c := range q {
			time.Sleep(time.Until(c.at))
			if _, err := dst.Write(c.b); err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			select {
			case q <- chunk{b: append([]byte(nil), buf[:n]...), at: time.Now().Add(lag)}:
			case <-done: // dst is gone
				return
			}
		}
		if err != nil {
			close(q)
			<-done
			return
		}
	}
}

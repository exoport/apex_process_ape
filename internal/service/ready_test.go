//go:build linux || darwin

package service

import (
	"context"
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

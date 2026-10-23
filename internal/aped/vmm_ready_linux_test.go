//go:build linux

package aped

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/vmmstream"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats-server/v2/server"
)

// F13 (D444): attach.open and forward.open announce a session ready only once
// the server holds every one of its subscriptions. Each test runs the whole
// control plane (newStack) with a front credential the server refuses some
// session subjects to — the stand-in for a narrowed grant or a subscription
// limit — and asserts the client hears an error rather than a prefix nothing
// will ever answer on.

// denyStack is a fake-backend stack whose front may not subscribe to subjects,
// with testWS already created.
func denyStack(t *testing.T, subjects ...string) *fullStack {
	t.Helper()
	fs := newStack(t, stackOpts{fake: true, svcGrant: &Grant{SubDeny: subjects}})
	if _, err := fs.client.Create(context.Background(), workspace.CreateRequest{Name: testWS, Image: testImage, Mount: "ephemeral"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	return fs
}

// assertNotReady checks that an open failed with the not-ready error, not one
// the CLI would read as "this node has no interactive attach" (UNSUPPORTED makes
// `ape sandbox exec` fall back to a one-shot exec).
func assertNotReady(t *testing.T, verb string, err error, subject string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s answered ready although the server refused the session's subscription to %s", verb, subject)
	}
	if errors.Is(err, workspace.ErrUnsupported) {
		t.Fatalf("%s error = %v: classified UNSUPPORTED, which the CLI reads as a node without the bridge", verb, err)
	}
	if !strings.Contains(err.Error(), "not ready") || !strings.Contains(err.Error(), subject) {
		t.Fatalf("%s error = %q, want a not-ready error naming %s", verb, err, subject)
	}
	t.Logf("%s → %v", verb, err)
}

// waitKilled waits for the guest process the front opened for a session to be
// torn down — the executor kills it when the front drops the priv conn.
func waitKilled(t *testing.T, p *fakeStreamProcess) {
	t.Helper()
	select {
	case <-p.killed:
	case <-time.After(5 * time.Second):
		t.Fatal("the guest process of a session that was never announced is still running")
	}
}

// heldUnder counts the subscriptions the server holds under prefix, on any
// connection.
func heldUnder(t *testing.T, srv *Server, prefix string) int {
	t.Helper()
	cz, err := srv.ns.Connz(&server.ConnzOptions{SubscriptionsDetail: true})
	if err != nil {
		t.Fatalf("connz: %v", err)
	}
	n := 0
	for _, c := range cz.Conns {
		for _, d := range c.SubsDetail {
			if strings.HasPrefix(d.Subject, prefix+".") {
				n++
			}
		}
	}
	return n
}

func (fs *fullStack) procs() []*fakeStreamProcess {
	fs.fake.mu.Lock()
	defer fs.fake.mu.Unlock()
	return append([]*fakeStreamProcess(nil), fs.fake.procs...)
}

// TestAttachOpenNotReadyWhenASubscriptionIsRefused: one refused subject of five
// is enough. The server accepts the session's other subscriptions; the client
// must still hear an error, and the session's accepted subscriptions and its
// guest process must be gone afterwards.
func TestAttachOpenNotReadyWhenASubscriptionIsRefused(t *testing.T) {
	const prefix = "ape.vmm.node1.exec.s1"
	fs := denyStack(t, prefix+".stdin")

	_, err := fs.client.AttachOpen(context.Background(), testWS, workspace.AttachRequest{TTY: true})
	assertNotReady(t, "attach.open", err, prefix+".stdin")

	procs := fs.procs()
	if len(procs) != 1 {
		t.Fatalf("opened %d guest processes, want 1", len(procs))
	}
	waitKilled(t, procs[0])
	if n := heldUnder(t, fs.srv, prefix); n != 0 {
		t.Errorf("the server still holds %d subscriptions under %s after the session was refused", n, prefix)
	}
}

// TestForwardOpenNotReadyWhenASubscriptionIsRefused is the same check for
// forward.open, which shares the session machinery but has its own handler.
func TestForwardOpenNotReadyWhenASubscriptionIsRefused(t *testing.T) {
	const prefix = "ape.vmm.node1.exec.f1"
	fs := denyStack(t, prefix+".>")

	_, err := fs.client.ForwardOpen(context.Background(), testWS, 22)
	assertNotReady(t, "forward.open", err, prefix+".")

	procs := fs.procs()
	if len(procs) != 1 {
		t.Fatalf("opened %d guest processes, want 1", len(procs))
	}
	waitKilled(t, procs[0])
}

// TestAttachOpenRefusalStaysWithItsSession opens two sessions at once on the
// one shared front connection, the server refusing only the one numbered s1. The
// refusal must reach that session's client and no other: the second session is
// announced ready and carries a whole round trip. This is what rules out
// reading the connection's last error, which either session could have set.
func TestAttachOpenRefusalStaysWithItsSession(t *testing.T) {
	const denied = "ape.vmm.node1.exec.s1"
	fs := denyStack(t, denied+".>")
	ctx := context.Background()

	type result struct {
		open workspace.AttachOpenReply
		err  error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		wg.Go(func() {
			<-start
			open, err := fs.client.AttachOpen(ctx, testWS, workspace.AttachRequest{TTY: true})
			results[i] = result{open, err}
		})
	}
	close(start)
	wg.Wait()

	var ok *workspace.AttachOpenReply
	failed := 0
	for i := range results {
		switch r := results[i]; {
		case r.err != nil:
			failed++
			assertNotReady(t, "attach.open", r.err, denied+".")
		default:
			ok = &results[i].open
		}
	}
	if failed != 1 || ok == nil {
		t.Fatalf("results = %+v, want exactly one refused session and one ready", results)
	}
	if ok.SubjectPrefix == denied {
		t.Fatalf("the session announced ready is the refused one (%s)", denied)
	}

	// The refused session's guest process is torn down, and only that one: the
	// ready session's process must still be running when its client attaches.
	procs := fs.procs()
	if len(procs) != 2 {
		t.Fatalf("opened %d guest processes, want 2", len(procs))
	}
	deadline := time.Now().Add(5 * time.Second)
	for killedCount(procs) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := killedCount(procs); n != 1 {
		t.Fatalf("%d guest processes killed before the ready session ran, want 1 (the refused session's only)", n)
	}

	// The ready session works end to end.
	stdinR, stdinW := io.Pipe()
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		code, aerr := vmmstream.Attach(ctx, fs.clientConn, ok.SubjectPrefix, vmmstream.ClientStreams{
			Stdin: stdinR, Stdout: &stdout, Stderr: &stderr,
		}, 4)
		if aerr == nil && code != 7 {
			aerr = errors.New("exit code is not the echo process's 7")
		}
		done <- aerr
	}()
	if _, err := io.WriteString(stdinW, "hello"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = stdinW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attach on the ready session: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the ready session did not complete")
	}
	if stdout.String() != "hello" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello")
	}
}

func killedCount(procs []*fakeStreamProcess) int {
	n := 0
	for _, p := range procs {
		select {
		case <-p.killed:
			n++
		default:
		}
	}
	return n
}

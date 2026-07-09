package natsconn_test

import (
	"context"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/nats-io/nats.go"
)

func TestResolve(t *testing.T) {
	t.Setenv(natsconn.EnvURL, "nats://env:4222")
	t.Setenv(natsconn.EnvCreds, "/env/user.creds")

	// Flags win.
	got := natsconn.Resolve("nats://flag:4222", "/flag/user.creds")
	if got.URL != "nats://flag:4222" || got.CredsFile != "/flag/user.creds" {
		t.Errorf("flags should win, got %+v", got)
	}
	// Empty flags fall back to env.
	got = natsconn.Resolve("", "")
	if got.URL != "nats://env:4222" || got.CredsFile != "/env/user.creds" {
		t.Errorf("env fallback failed, got %+v", got)
	}
	if !got.Enabled() {
		t.Error("Enabled() should be true with a URL")
	}
	if (natsconn.Config{}).Enabled() {
		t.Error("empty Config must be disabled")
	}
}

func TestConnectDisabled(t *testing.T) {
	nc, err := natsconn.Connect(context.Background(), natsconn.Config{}, "ape/test")
	if err != nil {
		t.Fatalf("disabled Connect should not error: %v", err)
	}
	if nc != nil {
		t.Fatal("disabled Connect should return a nil conn")
	}
}

func TestConnectReachable(t *testing.T) {
	url := natstest.RunServer(t)
	nc, err := natsconn.Connect(context.Background(), natsconn.Config{URL: url}, "ape/test")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = nc.Drain() }()
	if !nc.IsConnected() {
		t.Fatal("expected connected")
	}
	if err := nc.Publish("ape.test", []byte("hi")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func TestConnectUnreachable(t *testing.T) {
	// Port 1 is not listening; RetryOnFailedConnect(false) fails fast.
	_, err := natsconn.Connect(context.Background(), natsconn.Config{URL: "nats://127.0.0.1:1"}, "ape/test")
	if err == nil {
		t.Fatal("expected an error for an unreachable cluster")
	}
}

// TestServerEnforcedSubjectScope is the PLAN-13 exit-gate check: a
// credential whose publish permission is scoped to ape.*.<token>.> is
// enforced by the server, so identity is server-enforced, not merely
// self-reported.
func TestServerEnforcedSubjectScope(t *testing.T) {
	url, account := natstest.StartOperator(t)

	// A user named "alice" scoped to publish only under its own token.
	aliceCreds, _ := natstest.MintCreds(t, account, "alice",
		[]string{"ape.*.alice.>"}, []string{"_INBOX.>"})
	// A permissive admin used only to observe what actually gets delivered.
	adminCreds, _ := natstest.MintCreds(t, account, "admin",
		[]string{">"}, []string{">"})

	// The token ape derives from the credential must match the scope.
	id, err := natsconn.DecodeIdentity(aliceCreds)
	if err != nil {
		t.Fatalf("DecodeIdentity: %v", err)
	}
	if id.SubjectToken != "alice" {
		t.Fatalf("SubjectToken = %q, want alice", id.SubjectToken)
	}

	admin, err := nats.Connect(url, nats.UserCredentials(adminCreds))
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer admin.Close()
	sub, err := admin.SubscribeSync("ape.evt.>")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := admin.Flush(); err != nil {
		t.Fatalf("admin flush: %v", err)
	}

	alice, err := natsconn.Connect(context.Background(),
		natsconn.Config{URL: url, CredsFile: aliceCreds}, "ape/test")
	if err != nil {
		t.Fatalf("alice connect: %v", err)
	}
	defer func() { _ = alice.Drain() }()

	allowed := "ape.evt.alice.proj.pipeline.r1.run-start"
	denied := "ape.evt.bob.proj.pipeline.r1.run-start"
	_ = alice.Publish(allowed, []byte(`{"v":1}`))
	_ = alice.Publish(denied, []byte(`{"v":1}`))
	_ = alice.Flush()

	// The allowed message is delivered.
	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("expected the allowed message, got: %v", err)
	}
	if msg.Subject != allowed {
		t.Fatalf("first delivered subject = %q, want %q", msg.Subject, allowed)
	}
	// The denied publish is rejected server-side — nothing more arrives.
	if extra, err := sub.NextMsg(300 * time.Millisecond); err == nil {
		t.Fatalf("off-token publish should be denied, but received %q", extra.Subject)
	}
}

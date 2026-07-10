//go:build linux || darwin

package aped

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func startTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := StartServer(ServerConfig{Host: "127.0.0.1", Port: -1})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	t.Cleanup(s.Shutdown)
	return s
}

// connectCreds connects with the given .creds bytes and returns the conn plus a
// channel that receives async errors (permissions violations land here).
func connectCreds(t *testing.T, url string, creds []byte, inbox string) (nc *nats.Conn, errc chan error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.creds")
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	errc = make(chan error, 16)
	opts := []nats.Option{
		nats.UserCredentials(path),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case errc <- err:
			default:
			}
		}),
	}
	if inbox != "" {
		opts = append(opts, nats.CustomInboxPrefix(inbox))
	}
	var err error
	nc, err = nats.Connect(url, opts...)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc, errc
}

// expectDenied asserts the server raises a permissions violation after act.
func expectDenied(t *testing.T, nc *nats.Conn, errc chan error, what string, act func()) {
	t.Helper()
	drain(errc)
	act()
	_ = nc.Flush()
	select {
	case err := <-errc:
		if !strings.Contains(strings.ToLower(err.Error()), "permission") {
			t.Fatalf("%s: want permissions violation, got %v", what, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: expected a permissions violation, got none", what)
	}
}

func drain(errc chan error) {
	for {
		select {
		case <-errc:
		default:
			return
		}
	}
}

// TestVMCredEnforcement is the Phase-2 guest→host-escape acceptance at the
// credential layer: a per-VM (TELEMETRY) cred can publish its own
// ape.metrics.vm-<id> (routed to the ingest subscriber) but is server-rejected
// on ape.vmm.> (pub AND sub), on the default _INBOX, on another VM's telemetry,
// and on another VM's inbox.
func TestVMCredEnforcement(t *testing.T) {
	s := startTestServer(t)

	vmCreds, _, err := MintVMCreds(s.Telemetry(), "a", time.Hour)
	if err != nil {
		t.Fatalf("MintVMCreds: %v", err)
	}
	vmA, vmErrc := connectCreds(t, s.ClientURL(), vmCreds, VMInbox("a"))

	ingestCreds, _, err := s.Telemetry().MintUser("aped-ingest", telemetryIngestGrant(), 0)
	if err != nil {
		t.Fatalf("mint ingest: %v", err)
	}
	ingest, _ := connectCreds(t, s.ClientURL(), ingestCreds, "")

	// Positive: VM-A publishes its own metrics; the ingest subscriber (same
	// TELEMETRY account) receives it — the publish is allowed and routed.
	sub, err := ingest.SubscribeSync("ape.metrics.*.>")
	if err != nil {
		t.Fatalf("ingest subscribe: %v", err)
	}
	_ = ingest.Flush()
	if err := vmA.Publish("ape.metrics.vm-a.proj.sid", []byte(`{"v":1}`)); err != nil {
		t.Fatalf("vm-a publish own metrics: %v", err)
	}
	_ = vmA.Flush()
	if _, err := sub.NextMsg(2 * time.Second); err != nil {
		t.Fatalf("ingest did not receive vm-a metrics (own publish should be allowed): %v", err)
	}

	// Deny matrix — each server-rejected with a permissions violation.
	expectDenied(t, vmA, vmErrc, "pub ape.vmm.>", func() { _ = vmA.Publish("ape.vmm.node1.create", nil) })
	expectDenied(t, vmA, vmErrc, "sub ape.vmm.>", func() { _, _ = vmA.SubscribeSync("ape.vmm.>") })
	expectDenied(t, vmA, vmErrc, "sub default _INBOX", func() { _, _ = vmA.SubscribeSync("_INBOX.>") })
	expectDenied(t, vmA, vmErrc, "pub other-VM metrics", func() { _ = vmA.Publish("ape.metrics.vm-b.x", nil) })
	expectDenied(t, vmA, vmErrc, "sub other-VM inbox", func() { _, _ = vmA.SubscribeSync("_INBOX_vm-vm-b.>") })

	// Its own scoped inbox is fine (proves the deny targets others, not itself).
	drain(vmErrc)
	if _, err := vmA.SubscribeSync(VMInbox("a") + ".reply"); err != nil {
		t.Fatalf("subscribe own scoped inbox: %v", err)
	}
	_ = vmA.Flush()
	select {
	case err := <-vmErrc:
		t.Fatalf("own scoped inbox wrongly denied: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestServerKeyPersistence proves aped's operator + account identities survive
// a restart (D7): a per-VM cred minted before the restart still validates
// against the restarted server.
func TestServerKeyPersistence(t *testing.T) {
	dir := t.TempDir()
	s1, err := StartServer(ServerConfig{Host: "127.0.0.1", Port: -1, StoreDir: dir})
	if err != nil {
		t.Fatalf("StartServer #1: %v", err)
	}
	hoPub, tePub := s1.HostOps().Public(), s1.Telemetry().Public()
	vmCreds, _, err := MintVMCreds(s1.Telemetry(), "a", time.Hour)
	if err != nil {
		t.Fatalf("MintVMCreds: %v", err)
	}
	s1.Shutdown()

	s2, err := StartServer(ServerConfig{Host: "127.0.0.1", Port: -1, StoreDir: dir})
	if err != nil {
		t.Fatalf("StartServer #2: %v", err)
	}
	t.Cleanup(s2.Shutdown)
	if s2.HostOps().Public() != hoPub || s2.Telemetry().Public() != tePub {
		t.Fatal("account identities not persisted across restart")
	}
	// The pre-restart cred connects to the restarted instance (connectCreds
	// fatals on an auth rejection).
	nc, _ := connectCreds(t, s2.ClientURL(), vmCreds, VMInbox("a"))
	if !nc.IsConnected() {
		t.Fatal("pre-restart per-VM cred rejected after restart")
	}
}

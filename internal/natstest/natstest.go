// Package natstest provides embedded nats-server helpers for the NATS
// feature tests (PLAN-13/14/17). It exists so CI stays hermetic — no live
// cluster, no nats-server binary on PATH — while the eventing/blobstore/
// natsconn packages exercise real publish/subscribe and JetStream paths.
//
// It is test-support only: nothing in the ape binary imports it, so the
// embedded server never links into a release build. It is a normal
// (non-_test) package purely so the three feature-test packages can share
// one implementation instead of copy-pasting the rig.
package natstest

import (
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
)

// RunServer starts an embedded core (no-JetStream) nats-server on a random
// loopback port and returns its client URL. The server is shut down via
// t.Cleanup.
func RunServer(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Host = "127.0.0.1"
	opts.Port = -1
	s := natsserver.RunServer(&opts)
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

// RunJetStreamServer starts an embedded nats-server with JetStream enabled
// (a temp store dir cleaned up with the test) and returns its client URL.
// Used by the blobstore Object Store backend tests.
func RunJetStreamServer(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Host = "127.0.0.1"
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	s := natsserver.RunServer(&opts)
	t.Cleanup(s.Shutdown)
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("natstest: JetStream server not ready in time")
	}
	return s.ClientURL()
}

// StartOperatorJetStream is StartOperator with JetStream enabled and the
// account's JetStream limits unlimited: credentials meet the JetStream API
// as a deployed cluster's do (C1, D757).
func StartOperatorJetStream(t *testing.T) (url string, accountKey KeyPair) {
	t.Helper()
	return startOperatorJetStream(t, nil)
}

// startOperatorJetStream is StartOperatorJetStream with the server's log
// going to logger (nil: none).
func startOperatorJetStream(t *testing.T, logger server.Logger) (url string, accountKey KeyPair) {
	t.Helper()
	okp := createOperator(t)
	akp := createAccount(t)
	apub := publicKey(t, akp)
	ac := jwt.NewAccountClaims(apub)
	ac.Name = "APP"
	ac.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
	ajwt, err := ac.Encode(okp)
	if err != nil {
		t.Fatalf("natstest: encode account jwt: %v", err)
	}
	// JetStream in operator mode needs a system account.
	skp := createAccount(t)
	spub := publicKey(t, skp)
	sc := jwt.NewAccountClaims(spub)
	sc.Name = "SYS"
	sjwt, err := sc.Encode(okp)
	if err != nil {
		t.Fatalf("natstest: encode system account jwt: %v", err)
	}
	opts := &server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), SystemAccount: spub}
	opts.TrustedKeys = []string{publicKey(t, okp)}
	res := &server.MemAccResolver{}
	for pub, j := range map[string]string{apub: ajwt, spub: sjwt} {
		if err := res.Store(pub, j); err != nil {
			t.Fatalf("natstest: store account jwt: %v", err)
		}
	}
	opts.AccountResolver = res
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("natstest: new operator server: %v", err)
	}
	if logger != nil {
		s.SetLogger(logger, false, false)
	}
	go s.Start()
	t.Cleanup(s.Shutdown)
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("natstest: operator server not ready in time")
	}
	return s.ClientURL(), KeyPair{akp}
}

// StartOperator starts an embedded nats-server in operator/JWT mode with a
// single in-memory account, so tests can mint per-user credentials with
// scoped publish/subscribe permissions and assert the server enforces
// them. Returns the client URL and the account key pair used to sign user
// JWTs (feed it to MintCreds).
func StartOperator(t *testing.T) (url string, accountKey KeyPair) {
	t.Helper()
	okp := createOperator(t)
	opub := publicKey(t, okp)

	akp := createAccount(t)
	apub := publicKey(t, akp)
	ajwt := encodeAccount(t, apub, okp)

	opts := &server.Options{Host: "127.0.0.1", Port: -1}
	opts.TrustedKeys = []string{opub}
	res := &server.MemAccResolver{}
	if err := res.Store(apub, ajwt); err != nil {
		t.Fatalf("natstest: store account jwt: %v", err)
	}
	opts.AccountResolver = res

	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("natstest: new operator server: %v", err)
	}
	go s.Start()
	t.Cleanup(s.Shutdown)
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("natstest: operator server not ready in time")
	}
	return s.ClientURL(), KeyPair{akp}
}

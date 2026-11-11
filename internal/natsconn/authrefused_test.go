package natsconn

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/natstest"
)

// FINDING-FD6-1: the refusals as a live server sends them. A credential
// that expires or is revoked while connected ends the connection; nats.go
// closes it on the second refused reconnect, and its LastError is the
// server's text, not a sentinel. A connect refused for an expired or
// revoked credential (member's path, V8LM2-1) carries the same text. Each
// shape is recorded as it came, then read.
func TestAuthRefused_TheServersOwnRefusals(t *testing.T) {
	srv := natstest.StartCredServer(t)
	closedWith := func(t *testing.T, nc *nats.Conn) error {
		t.Helper()
		require.Eventually(t, nc.IsClosed, 20*time.Second, 50*time.Millisecond, "the connection is closed for good")
		return nc.LastError()
	}
	opts := []nats.Option{nats.MaxReconnects(-1), nats.ReconnectWait(100 * time.Millisecond)}
	shapes := map[string]func(t *testing.T) error{
		"expired while connected": func(t *testing.T) error {
			t.Helper()
			creds, _ := srv.Mint(t, "exp", time.Now().Add(2*time.Second))
			nc, err := nats.Connect(srv.URL, append(opts, nats.UserCredentials(creds))...)
			require.NoError(t, err)
			t.Cleanup(nc.Close)
			return closedWith(t, nc)
		},
		"revoked while connected": func(t *testing.T) error {
			t.Helper()
			creds, pub := srv.Mint(t, "rev", time.Time{})
			nc, err := nats.Connect(srv.URL, append(opts, nats.UserCredentials(creds))...)
			require.NoError(t, err)
			t.Cleanup(nc.Close)
			time.Sleep(1100 * time.Millisecond) // revocation is by issue time, to the second
			srv.Revoke(t, pub)
			return closedWith(t, nc)
		},
		"connect, expired": func(t *testing.T) error {
			t.Helper()
			creds, _ := srv.Mint(t, "gone", time.Now().Add(-time.Minute))
			_, err := nats.Connect(srv.URL, nats.UserCredentials(creds))
			require.Error(t, err)
			return err
		},
		"connect, revoked": func(t *testing.T) error {
			t.Helper()
			creds, pub := srv.Mint(t, "out", time.Time{})
			time.Sleep(1100 * time.Millisecond)
			srv.Revoke(t, pub)
			_, err := nats.Connect(srv.URL, nats.UserCredentials(creds))
			require.Error(t, err)
			return err
		},
		"connect, an untrusted account": func(t *testing.T) error {
			t.Helper()
			creds, _ := natstest.MintCreds(t, natstest.NewAccountKey(t), "stranger", nil, nil)
			_, err := nats.Connect(srv.URL, nats.UserCredentials(creds))
			require.Error(t, err)
			return err
		},
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			err := shape(t)
			t.Logf("FINDING-FD6-1 %s: %T %q", name, err, err)
			require.True(t, AuthRefused(err), "%s: %v is the credential's refusal", name, err)
		})
	}
}

// The controls: an outage or a closed connection is not the credential's.
func TestAuthRefused_NotAnOutage(t *testing.T) {
	for _, err := range []error{
		nil, nats.ErrNoServers, nats.ErrConnectionClosed, nats.ErrTimeout, nats.ErrNoResponders,
		errors.New("dial tcp 127.0.0.1:4222: connect: connection refused"), nats.ErrPermissionViolation,
	} {
		require.False(t, AuthRefused(err), "%v", err)
	}
	require.True(t, AuthRefused(fmt.Errorf("wrapped: %w", nats.ErrAuthRevoked)))
}

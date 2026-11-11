package natstest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
)

// CredServer is an operator-mode JetStream server whose credentials can
// expire and be revoked while a connection holds them (FINDING-FD6-1): the
// server ends such a connection, refuses its reconnects, and nats.go closes
// it on the second refusal, with the server's own text as its last error.
type CredServer struct {
	URL string
	op  *operator
}

// StartCredServer starts a CredServer.
func StartCredServer(t *testing.T) *CredServer {
	t.Helper()
	op := startOperator(t, nil)
	return &CredServer{URL: op.s.ClientURL(), op: op}
}

// Mint writes an unrestricted credential of the account named name,
// expiring at expires (zero: never). It returns the creds path and the
// user's public key, which Revoke takes.
func (c *CredServer) Mint(t *testing.T, name string, expires time.Time) (credsPath, userPub string) {
	t.Helper()
	ukp := createUser(t)
	userPub = publicKey(t, ukp)
	uc := jwt.NewUserClaims(userPub)
	uc.Name = name
	if !expires.IsZero() {
		uc.Expires = expires.Unix()
	}
	ujwt, err := uc.Encode(c.op.account.kp)
	if err != nil {
		t.Fatalf("natstest: encode user jwt: %v", err)
	}
	seed, err := ukp.Seed()
	if err != nil {
		t.Fatalf("natstest: user seed: %v", err)
	}
	creds, err := jwt.FormatUserConfig(ujwt, seed)
	if err != nil {
		t.Fatalf("natstest: format creds: %v", err)
	}
	credsPath = filepath.Join(t.TempDir(), name+".creds")
	if err := os.WriteFile(credsPath, creds, 0o600); err != nil {
		t.Fatalf("natstest: write creds: %v", err)
	}
	return credsPath, userPub
}

// Revoke revokes every credential of userPub issued up to now, as an
// operator's `nsc revocations add-user` does: the account's claims are
// updated on the running server, which ends the user's connections.
func (c *CredServer) Revoke(t *testing.T, userPub string) {
	t.Helper()
	c.op.claims.Revoke(userPub)
	ajwt, err := c.op.claims.Encode(c.op.operator)
	if err != nil {
		t.Fatalf("natstest: encode account jwt: %v", err)
	}
	ac, err := jwt.DecodeAccountClaims(ajwt)
	if err != nil {
		t.Fatalf("natstest: decode account jwt: %v", err)
	}
	acc, err := c.op.s.LookupAccount(ac.Subject)
	if err != nil {
		t.Fatalf("natstest: lookup account: %v", err)
	}
	c.op.s.UpdateAccountClaims(acc, ac)
}

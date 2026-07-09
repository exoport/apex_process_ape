package natstest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// KeyPair wraps an nkeys account key pair so test callers don't need to
// import nkeys directly. Obtain one from StartOperator.
type KeyPair struct{ kp nkeys.KeyPair }

// MintCreds mints a user JWT signed by the account key, scoped to the
// given publish/subscribe allow lists, and writes a .creds file (JWT +
// seed) to a temp path. Returns the creds path and the user's public key.
// A nil/empty allow list leaves that permission unrestricted.
func MintCreds(t *testing.T, account KeyPair, name string, pubAllow, subAllow []string) (credsPath, userPublicKey string) {
	t.Helper()
	ukp := createUser(t)
	upub := publicKey(t, ukp)

	uc := jwt.NewUserClaims(upub)
	uc.Name = name
	if len(pubAllow) > 0 {
		uc.Pub.Allow = jwt.StringList(pubAllow)
	}
	if len(subAllow) > 0 {
		uc.Sub.Allow = jwt.StringList(subAllow)
	}
	ujwt, err := uc.Encode(account.kp)
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
	return credsPath, upub
}

// MintStandaloneCreds writes a .creds file for a user with the given name
// using a throwaway account, for tests that only decode the JWT offline
// (no running server). Returns the creds path and the user public key.
func MintStandaloneCreds(t *testing.T, name string) (credsPath, userPublicKey string) {
	t.Helper()
	return MintCreds(t, KeyPair{createAccount(t)}, name, nil, nil)
}

func createOperator(t *testing.T) nkeys.KeyPair {
	t.Helper()
	kp, err := nkeys.CreateOperator()
	if err != nil {
		t.Fatalf("natstest: create operator: %v", err)
	}
	return kp
}

func createAccount(t *testing.T) nkeys.KeyPair {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("natstest: create account: %v", err)
	}
	return kp
}

func createUser(t *testing.T) nkeys.KeyPair {
	t.Helper()
	kp, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("natstest: create user: %v", err)
	}
	return kp
}

func publicKey(t *testing.T, kp nkeys.KeyPair) string {
	t.Helper()
	pub, err := kp.PublicKey()
	if err != nil {
		t.Fatalf("natstest: public key: %v", err)
	}
	return pub
}

func encodeAccount(t *testing.T, accountPub string, operator nkeys.KeyPair) string {
	t.Helper()
	ac := jwt.NewAccountClaims(accountPub)
	ac.Name = "APP"
	ajwt, err := ac.Encode(operator)
	if err != nil {
		t.Fatalf("natstest: encode account jwt: %v", err)
	}
	return ajwt
}

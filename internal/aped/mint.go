package aped

import (
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// Account bundles an nkeys account key pair with its public key. aped runs the
// embedded server in operator/JWT mode with two accounts (HOST_OPS,
// TELEMETRY); an Account signs the user JWTs that connect into it — including
// the per-VM telemetry creds minted at Create (PLAN-18 D2). Signing users
// directly with the account identity key (rather than a separate signing key)
// matches the proven natstest rig and keeps the local daemon simple; scoped
// signing keys are a remote-tier hardening (D2/D10).
type Account struct {
	kp  nkeys.KeyPair
	pub string
}

// NewAccount generates a fresh account key pair.
func NewAccount() (Account, error) {
	kp, err := nkeys.CreateAccount()
	if err != nil {
		return Account{}, fmt.Errorf("aped: create account key: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return Account{}, fmt.Errorf("aped: account public key: %w", err)
	}
	return Account{kp: kp, pub: pub}, nil
}

// AccountFromSeed reconstructs an Account from a persisted nkey seed (D7
// durable state — the account identity survives an aped restart so per-VM
// creds minted before the restart keep validating).
func AccountFromSeed(seed []byte) (Account, error) {
	kp, err := nkeys.FromSeed(seed)
	if err != nil {
		return Account{}, fmt.Errorf("aped: account from seed: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return Account{}, fmt.Errorf("aped: account public key: %w", err)
	}
	return Account{kp: kp, pub: pub}, nil
}

// Public returns the account's public key (its identity, stored in the account
// JWT and the server's resolver).
func (a Account) Public() string { return a.pub }

// Seed returns the account's private nkey seed for persistence. Handle as a
// secret (0600, never logged).
func (a Account) Seed() ([]byte, error) { return a.kp.Seed() }

// Encode signs an account JWT for this account with the operator key, for
// storage in the server's account resolver.
func (a Account) Encode(name string, operator nkeys.KeyPair) (string, error) {
	ac := jwt.NewAccountClaims(a.pub)
	ac.Name = name
	// aped is the sole operator/minter, so a per-account user limit only risks
	// wedging legitimate per-VM minting; leave it unlimited (account isolation,
	// not a count, is the boundary).
	ac.Limits.Conn = -1
	ac.Limits.LeafNodeConn = -1
	ajwt, err := ac.Encode(operator)
	if err != nil {
		return "", fmt.Errorf("aped: encode account jwt: %w", err)
	}
	return ajwt, nil
}

// MintUser mints a user credential in this account carrying the given Grant,
// and returns the .creds bytes (the NATS user-config: JWT + seed, ready to
// write 0600) plus the user's public key. name becomes the JWT name claim —
// the token natsconn.Identity derives and the server can scope subjects to.
// expires bounds the credential's lifetime (0 = no expiry); per-VM creds use a
// short expiry, re-minted while the VM lives (D2).
func (a Account) MintUser(name string, g Grant, expires time.Duration) (creds []byte, userPub string, err error) {
	ukp, err := nkeys.CreateUser()
	if err != nil {
		return nil, "", fmt.Errorf("aped: create user key: %w", err)
	}
	upub, err := ukp.PublicKey()
	if err != nil {
		return nil, "", fmt.Errorf("aped: user public key: %w", err)
	}

	uc := jwt.NewUserClaims(upub)
	uc.Name = name
	uc.Pub.Allow = jwt.StringList(g.PubAllow)
	uc.Pub.Deny = jwt.StringList(g.PubDeny)
	uc.Sub.Allow = jwt.StringList(g.SubAllow)
	uc.Sub.Deny = jwt.StringList(g.SubDeny)
	if g.AllowResponses {
		// One reply per received request — the request/reply responder leg
		// without a standing publish grant on reply inboxes.
		uc.Resp = &jwt.ResponsePermission{MaxMsgs: 1}
	}
	if expires > 0 {
		uc.Expires = time.Now().Add(expires).Unix()
	}

	ujwt, err := uc.Encode(a.kp)
	if err != nil {
		return nil, "", fmt.Errorf("aped: encode user jwt: %w", err)
	}
	seed, err := ukp.Seed()
	if err != nil {
		return nil, "", fmt.Errorf("aped: user seed: %w", err)
	}
	creds, err = jwt.FormatUserConfig(ujwt, seed)
	if err != nil {
		return nil, "", fmt.Errorf("aped: format creds: %w", err)
	}
	return creds, upub, nil
}

// MintVMCreds mints a per-VM TELEMETRY credential for vmID: pub-only to its own
// ape.{evt,log,metrics}.vm-<id>.> and denied every management subject (VMGrant,
// PLAN-18 D2/D6). tele is the TELEMETRY account. The returned .creds bytes are
// injected into the guest as a read-only bind (APE_NATS_CREDS); userPub is
// recorded for break-glass revocation.
func MintVMCreds(tele Account, vmID string, expires time.Duration) (creds []byte, userPub string, err error) {
	return tele.MintUser(VMToken(vmID), VMGrant(vmID), expires)
}

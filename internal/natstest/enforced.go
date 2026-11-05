package natstest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/jwt/v2"
)

// Perms is a credential's subject permissions: allow and deny lists for
// publish and subscribe, as a user JWT carries them. An empty allow list
// leaves that direction unrestricted.
type Perms struct {
	PubAllow, PubDeny, SubAllow, SubDeny []string
	// AllowResponses: one reply to each request received (a service).
	AllowResponses bool
}

// Enforced is an operator-mode JetStream server that keeps every
// permissions violation it refuses (V12, D807+): a credential kind's
// allow-list is measured by running that kind's own code under it, and a
// call the list forgot is a violation the server logs, whether or not
// the code looked at the error. Cleanup fails the test on any violation
// it did not expect (Expect).
type Enforced struct {
	URL     string
	Account KeyPair
	// Admin is an unrestricted credential of the account, for a test's
	// own setup (`ape nats declare`'s part) and its reads.
	Admin string

	mu         sync.Mutex
	violations []string
	expected   []string
}

// StartEnforced starts an Enforced server.
func StartEnforced(t *testing.T) *Enforced {
	t.Helper()
	e := &Enforced{}
	e.URL, e.Account = startOperatorJetStream(t, violationLogger{e})
	e.Admin, _ = MintCreds(t, e.Account, "admin", nil, nil)
	t.Cleanup(func() {
		for _, v := range e.Violations() {
			if !e.isExpected(v) {
				t.Errorf("natstest: the server refused a call: %s", v)
			}
		}
	})
	return e
}

// Mint writes a credential of the account named name with perms, as
// MintCreds does.
func (e *Enforced) Mint(t *testing.T, name string, p Perms) string {
	t.Helper()
	return mintPerms(t, e.Account, name, p)
}

// Expect marks violations whose text contains sub as the test's own
// (a refusal it asserts), so Cleanup does not fail on them.
func (e *Enforced) Expect(sub string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expected = append(e.expected, sub)
}

// Violations are the server's logged refusals so far, each with the
// connection that made it.
func (e *Enforced) Violations() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.violations...)
}

func (e *Enforced) isExpected(v string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, sub := range e.expected {
		if strings.Contains(v, sub) {
			return true
		}
	}
	return false
}

// violationLogger keeps the server's publish and subscription
// violations; it drops everything else.
type violationLogger struct{ e *Enforced }

func (violationLogger) Noticef(string, ...any) {}
func (violationLogger) Warnf(string, ...any)   {}
func (violationLogger) Fatalf(string, ...any)  {}
func (violationLogger) Debugf(string, ...any)  {}
func (violationLogger) Tracef(string, ...any)  {}

func (l violationLogger) Errorf(format string, a ...any) {
	if s := fmt.Sprintf(format, a...); strings.Contains(s, "Violation") {
		l.e.mu.Lock()
		l.e.violations = append(l.e.violations, s)
		l.e.mu.Unlock()
	}
}

func mintPerms(t *testing.T, account KeyPair, name string, p Perms) string {
	t.Helper()
	ukp := createUser(t)
	uc := jwt.NewUserClaims(publicKey(t, ukp))
	uc.Name = name
	uc.Pub.Allow = jwt.StringList(p.PubAllow)
	uc.Pub.Deny = jwt.StringList(p.PubDeny)
	uc.Sub.Allow = jwt.StringList(p.SubAllow)
	uc.Sub.Deny = jwt.StringList(p.SubDeny)
	if p.AllowResponses {
		uc.Resp = &jwt.ResponsePermission{MaxMsgs: 1}
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
	path := filepath.Join(t.TempDir(), name+".creds")
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		t.Fatalf("natstest: write creds: %v", err)
	}
	return path
}

package aped

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/nats-io/jwt/v2"
)

func TestMintVMCredsClaims(t *testing.T) {
	acct, err := NewAccount()
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	creds, userPub, err := MintVMCreds(acct, "dev-1", time.Hour)
	if err != nil {
		t.Fatalf("MintVMCreds: %v", err)
	}

	token, err := jwt.ParseDecoratedJWT(creds)
	if err != nil {
		t.Fatalf("ParseDecoratedJWT: %v", err)
	}
	uc, err := jwt.DecodeUserClaims(token)
	if err != nil {
		t.Fatalf("DecodeUserClaims: %v", err)
	}

	if uc.Name != "vm-dev-1" {
		t.Errorf("name = %q, want vm-dev-1", uc.Name)
	}
	if uc.Subject != userPub {
		t.Errorf("subject = %q, want %q", uc.Subject, userPub)
	}
	if uc.IssuerAccount == "" && uc.Issuer != acct.Public() {
		t.Errorf("issuer = %q, want the TELEMETRY account %q", uc.Issuer, acct.Public())
	}
	if !slices.Contains(uc.Pub.Allow, "ape.metrics.vm-dev-1.>") {
		t.Errorf("pub.allow missing own metrics: %v", uc.Pub.Allow)
	}
	if !slices.Contains(uc.Pub.Deny, "ape.vmm.>") {
		t.Errorf("pub.deny missing ape.vmm.>: %v", uc.Pub.Deny)
	}
	if !slices.Contains(uc.Sub.Allow, "ape.svc.vm-dev-1.>") {
		t.Errorf("sub.allow missing own job intake: %v", uc.Sub.Allow)
	}
	if !slices.Contains(uc.Sub.Deny, "_INBOX.>") {
		t.Errorf("sub.deny missing default inbox: %v", uc.Sub.Deny)
	}
	if uc.Resp == nil {
		t.Error("resp (allow_responses) not set — the in-VM service cannot reply to jobs")
	}
	if uc.Expires == 0 {
		t.Error("expires not set for a bounded per-VM cred")
	}
}

func TestMintVMCredsIdentityRoundTrip(t *testing.T) {
	acct, err := NewAccount()
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	creds, _, err := MintVMCreds(acct, "dev-1", 0)
	if err != nil {
		t.Fatalf("MintVMCreds: %v", err)
	}
	path := filepath.Join(t.TempDir(), "vm.creds")
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	// The guest resolves its own identity offline from the injected .creds
	// (PLAN-18 D6): the subject token must be the per-VM token, so children
	// inherit correct per-VM attribution for free.
	id, err := natsconn.DecodeIdentity(path)
	if err != nil {
		t.Fatalf("DecodeIdentity: %v", err)
	}
	if id.SubjectToken != "vm-dev-1" {
		t.Errorf("identity token = %q, want vm-dev-1", id.SubjectToken)
	}
}

func TestAccountFromSeedRoundTrip(t *testing.T) {
	a, err := NewAccount()
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	seed, err := a.Seed()
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	b, err := AccountFromSeed(seed)
	if err != nil {
		t.Fatalf("AccountFromSeed: %v", err)
	}
	if a.Public() != b.Public() {
		t.Errorf("reconstructed account public = %q, want %q", b.Public(), a.Public())
	}
}

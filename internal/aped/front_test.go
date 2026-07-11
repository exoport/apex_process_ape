package aped

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureOperatorCredsReuse proves aped reuses a persisted operator
// credential across restart instead of re-minting (the churn the operator hits
// today), and re-mints only when the cred no longer validates for the current
// account/node.
func TestEnsureOperatorCredsReuse(t *testing.T) {
	acct, err := NewAccount()
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	path := filepath.Join(t.TempDir(), "operator.creds")

	// First start: no file → mint + write.
	reused, err := ensureOperatorCreds(acct, "node1", path)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if reused {
		t.Fatal("first ensure reused, want minted")
	}
	first, err := os.ReadFile(path)
	if err != nil || len(first) == 0 {
		t.Fatalf("operator creds not written: %v", err)
	}

	// Restart with the SAME (persisted) account: reuse, file byte-identical.
	reused, err = ensureOperatorCreds(acct, "node1", path)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if !reused {
		t.Fatal("second ensure re-minted, want reused (churn)")
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("operator creds changed on reuse — the churn is not fixed")
	}
}

// TestEnsureOperatorCredsReMint covers the three cases that must re-mint: a
// foreign signing account (a wiped/rotated store), a changed node scope, and a
// corrupt file.
func TestEnsureOperatorCredsReMint(t *testing.T) {
	acct, err := NewAccount()
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	dir := t.TempDir()

	// A cred minted for node1 by acct.
	path := filepath.Join(dir, "op.creds")
	if _, err := ensureOperatorCreds(acct, "node1", path); err != nil {
		t.Fatalf("seed mint: %v", err)
	}

	// A DIFFERENT account (store rotated) cannot reuse → re-mint.
	other, err := NewAccount()
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if reused, err := ensureOperatorCreds(other, "node1", path); err != nil || reused {
		t.Fatalf("foreign-account ensure = (reused=%v, %v), want re-mint", reused, err)
	}

	// A different node (scope changed) → re-mint. (The file now holds other's
	// node1 cred; requiring node2's pub subject must fail the scope check.)
	if reused, err := ensureOperatorCreds(other, "node2", path); err != nil || reused {
		t.Fatalf("node-change ensure = (reused=%v, %v), want re-mint", reused, err)
	}

	// A corrupt file → re-mint (fail-safe), not an error.
	corrupt := filepath.Join(dir, "corrupt.creds")
	if err := os.WriteFile(corrupt, []byte("not a creds file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reused, err := ensureOperatorCreds(acct, "node1", corrupt); err != nil || reused {
		t.Fatalf("corrupt-file ensure = (reused=%v, %v), want re-mint", reused, err)
	}
	if b, _ := os.ReadFile(corrupt); bytes.Equal(b, []byte("not a creds file")) {
		t.Fatal("corrupt creds not replaced")
	}
}

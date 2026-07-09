package natsconn_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

func TestSubjectToken(t *testing.T) {
	cases := map[string]string{
		"alice":       "alice",
		"Alice Smith": "alice-smith",
		"a.b.c":       "a-b-c",
		"star*name":   "star-name",
		"gt>name":     "gt-name",
		"  trimmed  ": "trimmed",
		"UPPER":       "upper",
		"":            "",
		"a\tb":        "a-b",
	}
	for in, want := range cases {
		if got := natsconn.SubjectToken(in); got != want {
			t.Errorf("SubjectToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeIdentity(t *testing.T) {
	credsPath, userPub := natstest.MintStandaloneCreds(t, "Alice Smith")

	id, err := natsconn.DecodeIdentity(credsPath)
	if err != nil {
		t.Fatalf("DecodeIdentity: %v", err)
	}
	if id.Name != "Alice Smith" {
		t.Errorf("Name = %q, want %q", id.Name, "Alice Smith")
	}
	if id.Subject != userPub {
		t.Errorf("Subject = %q, want %q", id.Subject, userPub)
	}
	if id.SubjectToken != "alice-smith" {
		t.Errorf("SubjectToken = %q, want %q", id.SubjectToken, "alice-smith")
	}
}

func TestDecodeIdentity_EmptyNameFallsBackToKey(t *testing.T) {
	credsPath, userPub := natstest.MintStandaloneCreds(t, "")

	id, err := natsconn.DecodeIdentity(credsPath)
	if err != nil {
		t.Fatalf("DecodeIdentity: %v", err)
	}
	// The public key is uppercase base32; the token is the lowercased form.
	want := strings.ToLower(userPub)
	if id.SubjectToken != want {
		t.Errorf("SubjectToken = %q, want key-derived %q", id.SubjectToken, want)
	}
	if id.SubjectToken == "" {
		t.Error("SubjectToken must never be empty")
	}
}

func TestConfigIdentity_NoCreds(t *testing.T) {
	_, err := natsconn.Config{URL: "nats://x"}.Identity()
	if !errors.Is(err, natsconn.ErrNoCreds) {
		t.Fatalf("Identity() err = %v, want ErrNoCreds", err)
	}
}

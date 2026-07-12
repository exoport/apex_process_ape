package framework

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const orBody = "@_apex/apex-operating-rules.md"

// block renders the canonical LF managed block with the operating-rules
// import body.
func block() string {
	return ManagedBlockBegin + "\n" + orBody + "\n" + ManagedBlockEnd
}

func TestUpsertManagedBlock_AbsentFileCreatesMinimal(t *testing.T) {
	out, changed, err := UpsertManagedBlock(nil, orBody)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !changed {
		t.Error("absent file must report changed")
	}
	want := block() + "\n"
	if string(out) != want {
		t.Errorf("minimal file mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestUpsertManagedBlock_AppendsAfterUserContent(t *testing.T) {
	existing := "# My Project\n\nHouse rules here.\n"
	out, changed, err := UpsertManagedBlock([]byte(existing), orBody)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !changed {
		t.Error("must report changed")
	}
	s := string(out)
	if !strings.HasPrefix(s, existing) {
		t.Errorf("user content must be preserved verbatim as a prefix:\n%q", s)
	}
	for _, frag := range []string{ManagedBlockBegin, orBody, ManagedBlockEnd} {
		if !strings.Contains(s, frag) {
			t.Errorf("output missing %q:\n%s", frag, s)
		}
	}
	if !strings.HasSuffix(s, "\n") {
		t.Error("output must end with a trailing newline")
	}
}

func TestUpsertManagedBlock_Idempotent(t *testing.T) {
	inputs := []string{
		"",
		"# Title\n",
		"# Title\n\nbody\n",
		"no trailing newline",
		block(),                        // already-present block, no trailing NL
		"# Title\n\n" + block() + "\n", // block after content
		"before\n" + block() + "\nafter\n",
	}
	for _, in := range inputs {
		first, _, err := UpsertManagedBlock([]byte(in), orBody)
		if err != nil {
			t.Fatalf("input %q: first upsert err: %v", in, err)
		}
		second, changed, err := UpsertManagedBlock(first, orBody)
		if err != nil {
			t.Fatalf("input %q: second upsert err: %v", in, err)
		}
		if changed {
			t.Errorf("input %q: second upsert must be a no-op (changed=false)", in)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("input %q: not idempotent:\n first=%q\nsecond=%q", in, first, second)
		}
	}
}

func TestUpsertManagedBlock_ReplacesInPlacePreservingOutside(t *testing.T) {
	before := "# Head\n\n"
	after := "\n## Tail\n\nfooter\n"
	stale := before + ManagedBlockBegin + "\n@old/thing.md\n" + ManagedBlockEnd + after
	out, changed, err := UpsertManagedBlock([]byte(stale), orBody)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !changed {
		t.Error("stale body must report changed")
	}
	s := string(out)
	if !strings.HasPrefix(s, before) {
		t.Errorf("content before the block not preserved:\n%q", s)
	}
	if !strings.HasSuffix(s, after) {
		t.Errorf("content after the block not preserved:\n%q", s)
	}
	if strings.Contains(s, "@old/thing.md") {
		t.Error("stale import must be replaced")
	}
	if !strings.Contains(s, orBody) {
		t.Error("new import must be present")
	}
}

func TestUpsertManagedBlock_PreservesCRLF(t *testing.T) {
	existing := "# Windows\r\n\r\ntext\r\n"
	out, _, err := UpsertManagedBlock([]byte(existing), orBody)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	s := string(out)
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Errorf("CRLF file must not gain bare LF newlines:\n%q", s)
	}
	if !strings.Contains(s, ManagedBlockBegin+"\r\n"+orBody+"\r\n"+ManagedBlockEnd) {
		t.Errorf("block must use CRLF:\n%q", s)
	}
}

func TestUpsertManagedBlock_CollapsesDuplicates(t *testing.T) {
	dup := block() + "\n\nmiddle\n\n" + block() + "\n"
	out, changed, err := UpsertManagedBlock([]byte(dup), orBody)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !changed {
		t.Error("duplicate blocks must be collapsed (changed)")
	}
	if n := strings.Count(string(out), ManagedBlockBegin); n != 1 {
		t.Errorf("expected exactly one block after collapse, got %d:\n%s", n, out)
	}
	if !strings.Contains(string(out), "middle") {
		t.Error("non-block content between duplicates must be preserved")
	}
}

func TestUpsertManagedBlock_MalformedRefuses(t *testing.T) {
	cases := []string{
		"text\n" + ManagedBlockBegin + "\n@x\n",                                   // begin without end
		"text\n" + ManagedBlockEnd + "\n",                                         // end without begin
		ManagedBlockBegin + "\n" + ManagedBlockBegin + "\n@x\n" + ManagedBlockEnd, // nested begin
	}
	for _, in := range cases {
		_, _, err := UpsertManagedBlock([]byte(in), orBody)
		if !errors.Is(err, ErrMalformedManagedBlock) {
			t.Errorf("input %q: want ErrMalformedManagedBlock, got %v", in, err)
		}
	}
}

func TestFindManagedBlock(t *testing.T) {
	body, ok, err := FindManagedBlock([]byte("x\n" + block() + "\ny\n"))
	if err != nil || !ok {
		t.Fatalf("expected to find block: ok=%v err=%v", ok, err)
	}
	if body != orBody {
		t.Errorf("body = %q, want %q", body, orBody)
	}

	_, ok, err = FindManagedBlock([]byte("no block here\n"))
	if err != nil || ok {
		t.Errorf("expected no block: ok=%v err=%v", ok, err)
	}

	_, _, err = FindManagedBlock([]byte(ManagedBlockEnd + "\n"))
	if !errors.Is(err, ErrMalformedManagedBlock) {
		t.Errorf("malformed find: want ErrMalformedManagedBlock, got %v", err)
	}
}

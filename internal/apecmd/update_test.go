package apecmd

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/output"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestPrintUpdateResult_Human is the regression guard for the broken
// human output: printUpdateResult used to type-assert against an
// anonymous struct that never matched the (then function-local)
// updateResult type, so the human branch always fell through to a raw
// %v dump. It must render the "current:" line.
func TestPrintUpdateResult_Human(t *testing.T) {
	res := updateResult{
		CurrentVersion: "0.0.35",
		LatestVersion:  "0.0.36",
		Updated:        false,
		Message:        "already up to date",
	}
	out := captureStdout(t, func() {
		if err := printUpdateResult(res, output.FormatHuman); err != nil {
			t.Fatalf("printUpdateResult: %v", err)
		}
	})
	if !strings.Contains(out, "current: 0.0.35") {
		t.Errorf("human output missing 'current:' line; got:\n%s", out)
	}
	if !strings.Contains(out, "latest:  0.0.36") {
		t.Errorf("human output missing 'latest:' line; got:\n%s", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("human output looks like a struct dump, not formatted lines:\n%s", out)
	}
}

func TestPrintUpdateResult_JSON(t *testing.T) {
	res := updateResult{CurrentVersion: "0.0.35", LatestVersion: "0.0.36", Updated: true, Message: "updated to 0.0.36"}
	out := captureStdout(t, func() {
		if err := printUpdateResult(res, output.FormatJSON); err != nil {
			t.Fatalf("printUpdateResult: %v", err)
		}
	})
	var got updateResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json output not parseable: %v\n%s", err, out)
	}
	if got != res {
		t.Errorf("json round-trip mismatch: got %+v want %+v", got, res)
	}
}

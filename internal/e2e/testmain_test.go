package e2e

import (
	"os"
	"testing"
)

// TestMain: every project a test runs `ape project init` in has ape's git
// pre-commit hook (Q89), which runs whatever ape is first on PATH — not
// the binary under test, which the tests call by path. A test's own git
// commit skips it, as ape's own commits do; apecmd's hook tests drive it.
func TestMain(m *testing.M) {
	_ = os.Setenv("APE_SCANNED", "1")
	os.Exit(m.Run())
}

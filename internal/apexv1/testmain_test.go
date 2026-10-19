package apexv1

import (
	"os"
	"testing"
)

// TestMain: every repository a test runs Init or the lift in has ape's git
// pre-commit hook, which runs whatever ape is on PATH; a test's own git
// commit skips it, as ape's own commits do (EnvScanned). The hook's tests
// unset it.
func TestMain(m *testing.M) {
	_ = os.Setenv(EnvScanned, "1")
	os.Exit(m.Run())
}

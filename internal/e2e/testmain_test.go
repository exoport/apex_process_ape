package e2e

import (
	"os"
	"testing"

	"github.com/exoport/apex_process_ape/internal/gittest"
	"github.com/exoport/apex_process_ape/internal/usercachetest"
)

// TestMain: every project a test runs `ape project init` in has ape's git
// pre-commit hook (Q89), which runs whatever ape is first on PATH — not
// the binary under test, which the tests call by path. A test's own git
// commit skips it, as ape's own commits do; apecmd's hook tests drive it.
//
// And no ape a test runs touches the real <user cache>/ape: every task,
// prompt and pipeline launch writes its usage-runs file there (W4). The
// cache is a fresh directory, inherited by every ape the tests start and
// removed after.
func TestMain(m *testing.M) {
	_ = os.Setenv("APE_SCANNED", "1")
	// No test repository runs git's auto-maintenance (D360).
	gittest.QuietMaintenance()
	cleanup := usercachetest.Isolate("ape-e2e-cache-")
	code := m.Run()
	cleanup()
	os.Exit(code)
}

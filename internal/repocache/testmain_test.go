package repocache

import (
	"os"
	"testing"

	"github.com/exoport/apex_process_ape/internal/gittest"
)

func TestMain(m *testing.M) {
	// No test repository runs git's auto-maintenance (D360).
	gittest.QuietMaintenance()
	os.Exit(m.Run())
}

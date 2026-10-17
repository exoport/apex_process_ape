package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sessions"
)

// asApeBinary turns this test binary into ape for a subprocess, so a detached
// board start — which re-runs os.Executable() — can be tested without a build
// step. The child goes through Execute and ExitCode exactly as cmd/ape does.
const asApeBinary = "APE_APECMD_TEST_AS_BINARY"

// testRegistryEnv names the live-session registry this test binary, and
// every copy of it a test runs as ape, reads and writes: never the user's
// ~/.ape/registry.json. A test-binary variable only — ape itself has no
// way to move its registry.
const testRegistryEnv = "APE_APECMD_TEST_REGISTRY"

func TestMain(m *testing.M) {
	path := os.Getenv(testRegistryEnv)
	var made string
	if path == "" {
		dir, err := os.MkdirTemp("", "ape-apecmd-registry-")
		if err != nil {
			panic(err)
		}
		made, path = dir, filepath.Join(dir, "registry.json")
		_ = os.Setenv(testRegistryEnv, path)
	}
	sessions.Path = func() string { return path }
	if os.Getenv(asApeBinary) == "1" {
		if err := Execute(); err != nil {
			code, silent := ExitCode(err)
			if !silent {
				_, _ = os.Stderr.WriteString("Error: " + err.Error() + "\n")
			}
			os.Exit(code)
		}
		os.Exit(ExitOK)
	}
	code := m.Run()
	if made != "" {
		_ = os.RemoveAll(made)
	}
	os.Exit(code)
}

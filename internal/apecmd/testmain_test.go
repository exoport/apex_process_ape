package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/gittest"
	"github.com/exoport/apex_process_ape/internal/sessions"
	"github.com/exoport/apex_process_ape/internal/usercachetest"
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
	// No test, and no copy of this binary a test runs as ape, reads or
	// writes the real <user cache>/ape (the W4 usage reading and
	// usage-runs, the probe, the update check, the clones): a fresh
	// directory, made once here and inherited by every copy, removed after.
	cleanup := usercachetest.Isolate("ape-apecmd-cache-")
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
	// Every repository a test runs `ape project init` in has ape's git
	// pre-commit hook, which runs whatever ape is on PATH: a test's own
	// git commit skips it, as ape's own commits do (the hook tests unset
	// it, with this binary on PATH as ape; a copy running as ape keeps
	// the environment it was given).
	_ = os.Setenv(apexv1.EnvScanned, "1")
	// No test repository runs git's auto-maintenance (D360).
	gittest.QuietMaintenance()
	code := m.Run()
	cleanup()
	if made != "" {
		_ = os.RemoveAll(made)
	}
	os.Exit(code)
}

package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
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
	// Every repository a test runs `ape project init` in has ape's git
	// pre-commit hook, which runs whatever ape is on PATH: a test's own
	// git commit skips it, as ape's own commits do (the hook tests unset
	// it, with this binary on PATH as ape; a copy running as ape keeps
	// the environment it was given).
	_ = os.Setenv(apexv1.EnvScanned, "1")
	// No test repository runs git's auto-maintenance (D360): the
	// `git maintenance run --auto --detach` a commit starts outlives it —
	// on git 2.56 holding .git/objects/maintenance.lock until it exits —
	// and races t.TempDir's cleanup ("unlinkat …/.git/objects: directory
	// not empty"). gc.auto covers a git whose commit runs `gc --auto`.
	for k, v := range map[string]string{
		"GIT_CONFIG_COUNT": "2",
		"GIT_CONFIG_KEY_0": "maintenance.auto", "GIT_CONFIG_VALUE_0": "false",
		"GIT_CONFIG_KEY_1": "gc.auto", "GIT_CONFIG_VALUE_1": "0",
	} {
		_ = os.Setenv(k, v)
	}
	code := m.Run()
	if made != "" {
		_ = os.RemoveAll(made)
	}
	os.Exit(code)
}

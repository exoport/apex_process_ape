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
	os.Exit(m.Run())
}

package pipeline

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
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

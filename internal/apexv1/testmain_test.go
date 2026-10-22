package apexv1

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/exoport/apex_process_ape/internal/usercachetest"
)

// TestMain: every repository a test runs Init or the lift in has ape's git
// pre-commit hook, which runs whatever ape is on PATH; a test's own git
// commit skips it, as ape's own commits do (EnvScanned). The hook's tests
// unset it.
//
// And no test reads or writes the real <user cache>/ape (W4: ape status
// shows the usage reading kept there): the cache is a fresh directory,
// removed after.
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
	cleanup := usercachetest.Isolate("ape-apexv1-cache-")
	code := m.Run()
	cleanup()
	// V11RC1-F1: on a whole-package run, each W5–W8 code's finding was
	// produced by some test and passed checkFixContract.
	if f := flag.Lookup("test.run"); code == 0 && f != nil && f.Value.String() == "" {
		if missing := fixContractUnseen(); len(missing) > 0 {
			sort.Strings(missing)
			fmt.Fprintf(os.Stderr, "V11RC1-F1: no test produced a finding of %v, so its message/fix split is unchecked\n", missing)
			code = 1
		}
	}
	os.Exit(code)
}

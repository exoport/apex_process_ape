// Package gittest sets git configuration for a test binary through the
// environment (GIT_CONFIG_COUNT, GIT_CONFIG_KEY_n, GIT_CONFIG_VALUE_n), the
// one channel that reaches every git a test starts, directly or through ape.
//
// Those variables are one list, so a test that sets GIT_CONFIG_COUNT=1 for
// its own key replaces whatever TestMain put there instead of adding to it:
// two tests did exactly that and silently re-enabled git's auto-maintenance
// for their own runs (D360). AppendConfig adds a key; nothing else in a test
// sets GIT_CONFIG_COUNT, which TestNoTestSetsGitConfigCountDirectly holds.
package gittest

import (
	"os"
	"strconv"
	"testing"
)

const (
	countEnv = "GIT_CONFIG_COUNT"
	keyEnv   = "GIT_CONFIG_KEY_"
	valueEnv = "GIT_CONFIG_VALUE_"
)

// QuietMaintenance, called from TestMain before m.Run, keeps every test
// repository out of git's auto-maintenance (D360): the
// `git maintenance run --auto --detach` a commit starts outlives it — on
// git 2.56 holding .git/objects/maintenance.lock until it exits — and races
// t.TempDir's cleanup ("unlinkat …/.git/objects: directory not empty").
// gc.auto covers a git whose commit runs `gc --auto`. It sets the list
// outright: TestMain is where the list starts.
func QuietMaintenance() {
	for k, v := range map[string]string{
		countEnv:       "2",
		keyEnv + "0":   "maintenance.auto",
		valueEnv + "0": "false",
		keyEnv + "1":   "gc.auto",
		valueEnv + "1": "0",
	} {
		_ = os.Setenv(k, v)
	}
}

// AppendConfig adds key=value to the environment's git configuration for the
// rest of t, keeping every key already there (TestMain's included), and
// restores the previous list when t ends. Like t.Setenv, it cannot be used
// in a parallel test.
func AppendConfig(t *testing.T, key, value string) {
	t.Helper()
	n := 0
	if s, ok := os.LookupEnv(countEnv); ok && s != "" {
		var err error
		n, err = strconv.Atoi(s)
		if err != nil || n < 0 {
			t.Fatalf("gittest: %s=%q is not a count", countEnv, s)
		}
	}
	idx := strconv.Itoa(n)
	t.Setenv(keyEnv+idx, key)
	t.Setenv(valueEnv+idx, value)
	t.Setenv(countEnv, strconv.Itoa(n+1))
}

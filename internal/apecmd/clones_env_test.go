package apecmd

import "os"

// No test in this package may sync a developer's real clones: with
// $APEX_GOVERNANCE_REPO set on the machine, every framework update test
// would otherwise move that checkout, and one resolving the framework with
// nothing set would clone into the real user cache. Cleared for the whole
// binary, before any test runs; a test that wants either sets its own.
func init() {
	_ = os.Setenv("APEX_GOVERNANCE_REPO", "")
	if dir, err := os.MkdirTemp("", "ape-test-cache-"); err == nil {
		_ = os.Setenv("XDG_CACHE_HOME", dir) // os.UserCacheDir on Linux
		_ = os.Setenv("LocalAppData", dir)   // and on Windows
	}
}

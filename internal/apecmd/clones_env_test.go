package apecmd

import "os"

// No test in this package may sync a developer's real clones: with
// $APEX_GOVERNANCE_REPO set on the machine, every framework update test
// would otherwise move that checkout. Cleared for the whole binary, before
// any test runs; a test that wants it sets its own. (The other half, a
// framework resolved with nothing set cloning into the real user cache,
// is TestMain's: the whole cache is a temp directory.)
func init() {
	_ = os.Setenv("APEX_GOVERNANCE_REPO", "")
}

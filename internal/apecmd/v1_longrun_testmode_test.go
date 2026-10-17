package apecmd

// This package's tests run the long verbs in-process: the test binary is
// not ape, and a detached worker re-runs os.Executable(). The detach tests
// turn it back on, with the binary acting as ape (asApeBinary).
func init() { v1DetachDefault = false }

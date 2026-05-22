//go:build windows

package repl

// terminateGroup is a no-op on Windows. ConPTY's process model lacks
// POSIX-style process groups; the caller's subsequent
// Process.Kill() handles direct-child termination. If a future plan
// needs to reap grandchildren under Windows, the right API is a Job
// Object via golang.org/x/sys/windows.
func terminateGroup(_ int) {}

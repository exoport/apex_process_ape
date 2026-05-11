//go:build windows

package pipeline

import "os/exec"

// Windows lacks Unix process groups; per-process job objects are the
// closest equivalent but would require non-trivial extra wiring. For
// PLAN-2 / F1 we fall back to the default cmd.Process.Kill() path that
// exec.CommandContext installs, which terminates the immediate claude
// child but leaves any grandchildren orphaned. claude's Windows
// subprocess shape currently spawns direct children only, so the
// observed orphan-subagent gap on Unix doesn't reproduce here.
func configureProcessGroup(_ *exec.Cmd) {}

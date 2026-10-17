//go:build windows

package apexv1

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return c.Process.Kill()
}

// processAlive is false on Windows: a journal left there is always
// treated as a crash.
func processAlive(int) bool { return false }

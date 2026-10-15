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

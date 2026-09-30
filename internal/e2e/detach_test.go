//go:build !windows

package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var detachedID = regexp.MustCompile(`(?m)^detached: (\S+)$`)

func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return ee.ExitCode()
	}
	return -1
}

// --detach returns at once with an id, and `ape run wait` exits with the
// run's own exit code — the same one the command gives when run in the
// foreground. A pipeline that does not exist fails without any claude.
func TestDetach_WaitReturnsTheRunsOwnExitCode(t *testing.T) {
	ape := buildApe(t)
	project := t.TempDir()
	_, fgErr := exec.Command(ape, "pipeline", "no-such-pipeline", "--cwd", project).CombinedOutput()
	foreground := exitStatus(fgErr)
	require.NotZero(t, foreground, "the fixture must fail, or this compares nothing")

	out, err := exec.Command(ape, "pipeline", "no-such-pipeline", "--detach", "--cwd", project).CombinedOutput()
	require.NoError(t, err, "--detach itself succeeds: %s", out)
	m := detachedID.FindStringSubmatch(string(out))
	require.NotNil(t, m, "no id in: %s", out)
	id := m[1]

	wait := exec.Command(ape, "run", "wait", id, "--cwd", project, "--timeout", "30s")
	wout, werr := wait.CombinedOutput()
	require.Equal(t, foreground, exitStatus(werr), "run wait exits with the run's own code: %s", wout)

	status, err := exec.Command(ape, "run", "status", id, "--cwd", project).Output()
	require.NoError(t, err)
	require.Contains(t, string(status), `"exit_code": `+strconv.Itoa(foreground))
	require.FileExists(t, filepath.Join(project, "_output", "ape", "detached", id+".log"))
}

// The point of --detach: a tool that stops everything its shell started
// (Claude Code's background-command limit stops descendants, setsid'd ones
// included) must not reach the run. The run here is `ape task` against a
// stand-in claude that never becomes ready, so it lasts without spending
// anything. The starting shell's whole session is killed; the supervisor,
// orphaned at launch, keeps going; `run wait --timeout` says 75 while it
// does; and when the supervisor is finally signalled, the run's exit is
// recorded rather than lost.
func TestDetach_SurvivesTheStartingShellsTree(t *testing.T) {
	ape := buildApe(t)
	project := t.TempDir()
	fake := t.TempDir()
	require.NoError(t, writeFile(filepath.Join(fake, "claude"), "#!/bin/sh\nexec sleep 300\n"))
	require.NoError(t, os.Chmod(filepath.Join(fake, "claude"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-foo"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-foo", "SKILL.md"), "# foo\n"))
	marker := filepath.Join(t.TempDir(), "returned")

	shell := exec.Command("sh", "-c", ape+" task apex-foo --detach --cwd "+project+" > "+marker+".out && touch "+marker+"; exec sleep 300")
	shell.Env = append(os.Environ(), "PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off")
	shell.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, shell.Start())
	t.Cleanup(func() { _ = syscall.Kill(-shell.Process.Pid, syscall.SIGKILL); _, _ = shell.Process.Wait() })
	require.Eventually(t, func() bool { return fileExists(marker) }, 20*time.Second, 50*time.Millisecond,
		"--detach did not return")
	out, err := os.ReadFile(marker + ".out")
	require.NoError(t, err)
	m := detachedID.FindStringSubmatch(string(out))
	require.NotNil(t, m, "no id in: %s", out)
	id := m[1]
	sup := supervisorPID(t, project, id)
	t.Cleanup(func() { _ = syscall.Kill(sup, syscall.SIGKILL) })

	// Stop the shell and everything in its session, as a tool limit does.
	require.NoError(t, syscall.Kill(-shell.Process.Pid, syscall.SIGKILL))
	_, _ = shell.Process.Wait()
	time.Sleep(500 * time.Millisecond)
	require.NoError(t, syscall.Kill(sup, 0), "the supervisor died with the shell that started it")

	busy := exec.Command(ape, "run", "wait", id, "--cwd", project, "--timeout", "1s")
	wout, werr := busy.CombinedOutput()
	require.Equal(t, 75, exitStatus(werr), "%s", wout)
	require.Contains(t, string(wout), "still running; wait again")

	// Stopping the supervisor stops the run, and the exit is recorded.
	require.NoError(t, syscall.Kill(sup, syscall.SIGTERM))
	final := exec.Command(ape, "run", "wait", id, "--cwd", project, "--timeout", "30s")
	fout, ferr := final.CombinedOutput()
	code := exitStatus(ferr)
	require.NotEqual(t, 75, code, "the run never ended: %s", fout)
	require.NotEqual(t, 0, code, "a stopped run is not a success: %s", fout)
	require.Contains(t, string(fout), "ended with exit")
}

func supervisorPID(t *testing.T, project, id string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(project, "_output", "ape", "detached", id+".json"))
		if err != nil {
			return false
		}
		m := regexp.MustCompile(`"supervisor_pid": (\d+)`).FindSubmatch(data)
		if m == nil {
			return false
		}
		pid, _ = strconv.Atoi(string(m[1]))
		return pid > 0
	}, 10*time.Second, 50*time.Millisecond)
	return pid
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

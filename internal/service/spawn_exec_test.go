//go:build linux || darwin

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeApe writes an executable /bin/sh stand-in for the ape binary and
// returns its path. body is the script after the shebang.
func fakeApe(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ape")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake ape: %v", err)
	}
	return p
}

func TestSpawnRecordsEnvArgvAndCwd(t *testing.T) {
	// The fake echoes what the daemon injected; its stdout is captured to
	// the per-job log, which we then read back.
	bin := fakeApe(t, `echo "JOBID=$APE_JOB_ID"
echo "URL=$APE_NATS_URL"
echo "ARGS=$*"
echo "PWD=$(pwd)"
exit 0
`)
	project := t.TempDir()
	sp, err := NewSpawner(bin, "nats://127.0.0.1:4222", "", "")
	if err != nil {
		t.Fatalf("NewSpawner: %v", err)
	}

	done := make(chan int, 1)
	jobID := "20260709-080000-abcdef0"
	pid, logPath, err := sp.Spawn(KindTask, jobID, RunRequest{ProjectRoot: project, Skill: "apex-shard-doc"}, func(code int) { done <- code })
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d, want > 0", pid)
	}
	if got := waitCode(t, done); got != 0 {
		t.Fatalf("exit code = %d, want 0", got)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read job log: %v", err)
	}
	log := string(data)
	for _, want := range []string{
		"JOBID=" + jobID,
		"URL=nats://127.0.0.1:4222",
		"ARGS=task apex-shard-doc --quiet --cwd " + project,
		"PWD=" + resolve(t, project),
	} {
		if !strings.Contains(log, want) {
			t.Errorf("job log missing %q\n---\n%s", want, log)
		}
	}
	if want := filepath.Join(project, "_output", "ape", "service", jobID+".log"); logPath != want {
		t.Errorf("log path = %q, want %q", logPath, want)
	}
}

func TestSpawnTerminateGroupKillsChild(t *testing.T) {
	// A long sleeper: terminateGroup must take it (and any descendant) down.
	bin := fakeApe(t, "sleep 30\n")
	project := t.TempDir()
	sp, err := NewSpawner(bin, "", "", "")
	if err != nil {
		t.Fatalf("NewSpawner: %v", err)
	}

	done := make(chan int, 1)
	pid, _, err := sp.Spawn(KindPipeline, "20260709-080000-0000000", RunRequest{ProjectRoot: project, Pipeline: "x"}, func(code int) { done <- code })
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	terminateGroup(pid)

	select {
	case code := <-done:
		// Signalled children report -1 (no clean exit code).
		if code != -1 {
			t.Fatalf("exit code = %d, want -1 (signalled)", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child not terminated within 5s")
	}
}

func waitCode(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case c := <-done:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit within 5s")
		return -1
	}
}

// resolve returns the symlink-resolved path, matching what `pwd` prints
// inside the child (macOS /var → /private/var, etc.).
func resolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeMarker(t *testing.T, p *Project, name string, m CheckMarker) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(p.RunsDir(), 0o755))
	data, err := json.Marshal(m)
	require.NoError(t, err)
	path := filepath.Join(p.RunsDir(), checkMarkerPrefix+name+".json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

// Q144 (D300): a lock or marker counts only while the process that wrote
// it lives — its pid, and that process's start time.
func TestOwnerAlive(t *testing.T) {
	needSh(t)
	require.True(t, ownerAlive(os.Getpid(), selfStart()))
	require.True(t, ownerAlive(os.Getpid(), ""), "no start recorded: the pid decides")
	require.False(t, ownerAlive(deadPID(t), ""))
	if _, ok := processStart(os.Getpid()); ok {
		require.False(t, ownerAlive(os.Getpid(), "another-start"), "a reused pid is a different process")
	}
	l := &RunLock{PID: os.Getpid(), PIDStart: "another-start"}
	if _, ok := processStart(os.Getpid()); ok {
		require.False(t, l.Alive())
	}
}

// RK-4b (Q144, D300): the pre-tool hook refuses an edit a running check's
// cleanup would undo, naming the run; never one under development/, and
// never for a marker whose process is gone.
func TestPreTool_DeniesAnEditWhileACheckRuns(t *testing.T) {
	needSh(t)
	p := fixture(t, baseFiles())
	edit := func(rel string) PreToolDecision {
		in, err := json.Marshal(map[string]string{"file_path": filepath.Join(p.Root, filepath.FromSlash(rel))})
		require.NoError(t, err)
		return PreTool(HookPayload{ToolName: "Edit", ToolInput: in, Cwd: p.Root})
	}
	require.False(t, edit("greet.sh").Deny, "no check running")

	live := writeMarker(t, p, "live", CheckMarker{PID: os.Getpid(), PIDStart: selfStart(), Cmd: "make test", Log: "_apex/state/logs/c-1/T1.log", Started: "2026-10-21T03:00:00Z", Verb: "change verify", Scope: "c-1", RunID: "R1"})
	d := edit("greet.sh")
	require.True(t, d.Deny)
	for _, want := range []string{"`make test`", "`ape change verify c-1` (run R1)", "pid ", "would be lost", "`ape change verify c-1 --wait 9m` attaches"} {
		require.Contains(t, d.Reason, want)
	}
	require.False(t, edit("development/changes/c-1/notes.md").Deny, "the cleanup never touches development/")
	require.NoError(t, os.Remove(live))

	dead := writeMarker(t, p, "dead", CheckMarker{PID: deadPID(t), Cmd: "make test", Started: "2026-10-21T03:00:00Z"})
	require.False(t, edit("greet.sh").Deny, "a crashed check never blocks an edit")
	require.NoFileExists(t, dead, "and its marker is cleared")

	if _, ok := processStart(os.Getpid()); ok {
		reused := writeMarker(t, p, "reused", CheckMarker{PID: os.Getpid(), PIDStart: "another-start", Cmd: "make test", Started: "2026-10-21T03:00:00Z"})
		require.False(t, edit("greet.sh").Deny, "a marker whose pid now names another process is dead")
		require.NoFileExists(t, reused)
	}
}

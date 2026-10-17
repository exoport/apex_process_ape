package apecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// runRunCmd runs an `ape run` verb and returns its stdout and exit code.
func runRunCmd(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := newRunCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err == nil {
		return out.String(), 0
	}
	return out.String(), exitCodeOf(t, err)
}

func reportOf(t *testing.T, out string) map[string]any {
	t.Helper()
	var rep map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rep), out)
	return rep
}

// --wait is --timeout's new name (D61's dispatch is `ape run wait <id>
// --wait 9m`), and --json reports the run as one object in every state:
// running (75), lost (76) and ended (its own code).
func TestRunWait_WaitAliasAndJSONStates(t *testing.T) {
	root := t.TempDir()
	dir := runlog.DetachedRoot(root)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, writeHandle(handlePath(root, "busy"), &detachedHandle{ID: "busy", Argv: []string{"task", "apex-apply"}, StartedAt: time.Now(), Depth: 1}))
	require.NoError(t, writeHandle(handlePath(root, "lost"), &detachedHandle{ID: "lost", SupervisorPID: deadPID, StartedAt: time.Now()}))
	three := 3
	require.NoError(t, writeHandle(handlePath(root, "done"), &detachedHandle{ID: "done", ExitCode: &three, Depth: 2, ParentRun: "orch"}))

	for _, flag := range []string{"--wait", "--timeout"} {
		out, code := runRunCmd(t, "wait", "busy", "--cwd", root, flag, "100ms")
		require.Equal(t, exitCodeStillRunning, code, flag)
		require.Contains(t, out, "still running; wait again", flag)
	}
	out, code := runRunCmd(t, "wait", "busy", "--cwd", root, "--wait", "100ms", "--json")
	require.Equal(t, exitCodeStillRunning, code)
	rep := reportOf(t, out)
	require.Equal(t, "running", rep["state"])
	require.Nil(t, rep["exit_code"])
	require.EqualValues(t, 1, rep["depth"])
	require.Equal(t, "", rep["parent_run"])

	out, code = runRunCmd(t, "wait", "lost", "--cwd", root, "--json")
	require.Equal(t, exitCodeRunLost, code)
	require.Equal(t, "lost", reportOf(t, out)["state"])

	out, code = runRunCmd(t, "wait", "done", "--cwd", root, "--json")
	require.Equal(t, 3, code, "the run's own exit code")
	rep = reportOf(t, out)
	require.Equal(t, "ended", rep["state"])
	require.EqualValues(t, 3, rep["exit_code"])
	require.Equal(t, "orch", rep["parent_run"])

	// run status --json is the same object, exit 0.
	out, code = runRunCmd(t, "status", "lost", "--cwd", root, "--json")
	require.Zero(t, code)
	require.Equal(t, "lost", reportOf(t, out)["state"])
	out, code = runRunCmd(t, "status", "done", "--cwd", root)
	require.Zero(t, code)
	require.Contains(t, out, `"state": "ended, exit 3"`, "without --json, the handle as before")
}

// result is the child session's last assistant message, read from the
// transcript ape snapshotted into its run directory, cut at 2048 bytes on a
// rune boundary and marked when cut.
func TestRunWait_JSONCarriesTheSessionsResult(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(runlog.DetachedRoot(root), 0o755))
	runDir := filepath.Join(runlog.TasksRoot(root), "apex-change", "20261017-120000-abc")
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "transcripts"), 0o755))
	m := "run_id: 20261017-120000-abc\nstatus: completed\nstages:\n  - name: apex-change\n    steps:\n      - skill: apex-change\n        sessions:\n" +
		"          - session_id: main-1\n          - session_id: agent-1\n            parent_session_id: main-1\n"
	require.NoError(t, os.WriteFile(pipeline.ManifestPath(runDir), []byte(m), 0o644))
	line := func(text string, sidechain bool) string {
		b, err := json.Marshal(map[string]any{
			"type": "assistant", "isSidechain": sidechain,
			"message": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}},
		})
		require.NoError(t, err)
		return string(b) + "\n"
	}
	transcript := filepath.Join(runDir, "transcripts", "main-1.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(line("planning", false)+line("a sub-agent's word", true)+line("PLANNED add-wave", false)), 0o644))
	zero := 0
	require.NoError(t, writeHandle(handlePath(root, "r1"), &detachedHandle{ID: "r1", ExitCode: &zero, RunDir: runDir, RunID: "20261017-120000-abc", Depth: 2, ParentRun: "orch"}))

	out, code := runRunCmd(t, "wait", "r1", "--cwd", root, "--json")
	require.Zero(t, code)
	rep := reportOf(t, out)
	require.Equal(t, "PLANNED add-wave", rep["result"])
	require.NotContains(t, rep, "result_truncated")
	require.Equal(t, "20261017-120000-abc", rep["run_id"])

	long := strings.Repeat("é", 1500) // 3000 bytes
	require.NoError(t, os.WriteFile(transcript, []byte(line(long, false)), 0o644))
	out, _ = runRunCmd(t, "status", "r1", "--cwd", root, "--json")
	rep = reportOf(t, out)
	got, _ := rep["result"].(string)
	require.Equal(t, true, rep["result_truncated"])
	require.LessOrEqual(t, len(got), resultCap)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, 2048, len(got))
}

func TestCapUTF8(t *testing.T) {
	s, cut := capUTF8("héllo", 2)
	require.Equal(t, "h", s, "never half a rune")
	require.True(t, cut)
	s, cut = capUTF8("hi", 2)
	require.Equal(t, "hi", s)
	require.False(t, cut)
}

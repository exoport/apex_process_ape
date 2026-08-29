package apecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const reconcileTracker = `created_at: '20260101000000'
updated_at: '20260101000000'
development_status:
  epic-1: in-progress
  1-1: done
  1-2: done
`

// runReconcile executes the real command and returns stdout, stderr and the
// error separately — the whole point here is that the three do not move
// together.
func runReconcile(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	cmd := newSprintReconcileCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

// giveItABoard writes a board document and nothing else. No server is started
// anywhere in this file: the refresh is a file write, so a running board is
// not part of the contract.
func giveItABoard(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".aboard", "run"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".aboard", "aboard.json"),
		[]byte(`{"version":1,"rev":1,"nextId":1,"tabs":[]}`), 0o644))
}

// brokenBoard is a document nothing can parse — the failure that must stay
// inside the refresh.
func brokenBoard(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".aboard"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".aboard", "aboard.json"),
		[]byte("{ not json"), 0o644))
}

// The tab lands, from the command, with no server anywhere.
func TestSprintReconcile_WritesTheTabWithNoServerRunning(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", reconcileTracker)
	giveItABoard(t, root)

	_, stderr, err := runReconcile(t, "--epic", "1")
	require.NoError(t, err)
	require.Empty(t, stderr)

	body, rerr := os.ReadFile(filepath.Join(root, ".aboard", "aboard.json"))
	require.NoError(t, rerr)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	tabs, ok := doc["tabs"].([]any)
	require.True(t, ok)
	require.Len(t, tabs, 1, "reconcile did not write the Sprint tab")
}

// THE ACCEPTANCE CRITERION THE REQUEST SAYS TO TEST DELIBERATELY.
//
// `ape sprint reconcile` sits on mutation paths inside apex-review-story,
// apex-code-review and apex-epic-batch-review, where the framework's operating
// rules read a non-zero exit as a CONTENT VERDICT — it converts a defer into a
// patch, raises unfixed_patches and demotes the story. A board that is down
// must therefore be undetectable from stdout and from the exit code, or a
// stopped server silently corrupts review outcomes on projects that happen to
// run a board and not on projects that do not.
//
// Asserted as EQUALITY against a run with no board at all, not as "looks
// fine": the claim is that the two are indistinguishable.
func TestSprintReconcile_ABrokenBoardChangesNeitherStdoutNorExitCode(t *testing.T) {
	withoutRoot := newTestProject(t, realProjectConfig)
	writeStory(t, withoutRoot, "sprint-status.yaml", reconcileTracker)
	wantOut, _, wantErr := runReconcile(t, "--epic", "1")
	require.NoError(t, wantErr)
	require.NotEmpty(t, wantOut)

	withRoot := newTestProject(t, realProjectConfig)
	writeStory(t, withRoot, "sprint-status.yaml", reconcileTracker)
	brokenBoard(t, withRoot)
	gotOut, gotErrText, gotErr := runReconcile(t, "--epic", "1")

	require.NoError(t, gotErr, "a broken board changed reconcile's exit code")
	require.Equal(t, wantOut, gotOut, "a broken board changed reconcile's stdout")
	require.NotEmpty(t, gotErrText, "a document nothing can parse should say so, on stderr")
}

// The same guarantee for the machine-readable path, which is the one the
// skills actually use: stdout must stay parseable JSON with a dead board.
func TestSprintReconcile_JSONStdoutIsUnaffectedByABrokenBoard(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", reconcileTracker)
	brokenBoard(t, root)

	out, _, err := runReconcile(t, "--epic", "1", "--output-format", "json")
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed),
		"stderr from the board refresh leaked into the JSON on stdout:\n%s", out)
	require.Contains(t, parsed, "changes")
}

// A project that never ran a board pays nothing and hears nothing.
func TestSprintReconcile_NoBoardIsCompletelySilent(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", reconcileTracker)

	_, stderr, err := runReconcile(t, "--epic", "1")
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.NoDirExists(t, filepath.Join(root, ".aboard"),
		"reconcile created a board; starting one is the human's call")
}

// --check is a dry run. A dry run that mutates a board is not a dry run,
// whatever the board happens to be showing.
func TestSprintReconcile_CheckDoesNotTouchTheBoard(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeStory(t, root, "sprint-status.yaml", reconcileTracker)
	giveItABoard(t, root)

	_, stderr, err := runReconcile(t, "--epic", "1", "--check")
	require.NoError(t, err)
	require.Empty(t, stderr, "--check attempted a board write")

	body, rerr := os.ReadFile(filepath.Join(root, ".aboard", "aboard.json"))
	require.NoError(t, rerr)
	require.NotContains(t, string(body), "apex-sprint", "--check wrote a tab")
}

// A tracker named with --file may sit outside any project, which is how the
// framework's call sites name it. There is no project root to find a board
// under, and that must not be an error.
func TestSprintReconcile_FileFlagOutsideAProjectStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sprint-status.yaml")
	require.NoError(t, os.WriteFile(path, []byte(reconcileTracker), 0o644))
	t.Chdir(dir)

	_, stderr, err := runReconcile(t, "--epic", "1", "--file", path)
	require.NoError(t, err)
	require.Empty(t, stderr)
}

package apecmd

import (
	"encoding/json"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// seedArchivedCLI commits an archived change's folder at rel as an ape
// archive left it.
func seedArchivedCLI(t *testing.T, root, rel, id string) {
	t.Helper()
	writeAndCommit(t, root, rel+"/change.md", "---\nid: "+id+"\nmilestone: M1\ntouches: [greeting]\n---\n# "+id+"\n## Tasks\n- T1 x — check: true\n",
		"apex: seed "+id)
	writeAndCommit(t, root, rel+"/state.json", `{"id": "`+id+`", "status": "archived", "archived_to": "`+rel+`"}`+"\n", "apex: "+id+" archived")
}

func archivedCount(t *testing.T, root string) int {
	t.Helper()
	stdout, _, err := v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var r struct {
		Archived int `json:"archived"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &r), stdout)
	return r.Archived
}

// Q76: `ape project archive-layout` — the seq-06 migration's command and
// its check — on 2 changes in the older layout and 1 in the newer.
func TestProjectArchiveLayout(t *testing.T) {
	root := v1Repo(t)
	seedArchivedCLI(t, root, "development/archive/2026-01-01-add-hug", "add-hug")
	seedArchivedCLI(t, root, "development/archive/2026-01-02-add-wave", "add-wave")
	seedArchivedCLI(t, root, "development/archive/changes/2026-02-01-fix-typo", "fix-typo")
	require.Equal(t, 3, archivedCount(t, root), "status reads both layouts")

	stdout, _, err := v1Exec(t, newProjectCmd, "archive-layout", "--check", "--json", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), "--check: 1 not applied")
	require.Contains(t, stdout, `"archive.legacy-layout"`)
	require.Contains(t, stdout, `"from": "development/archive/2026-01-01-add-hug"`)
	_, stderr, err := v1Exec(t, newProjectCmd, "archive-layout", "--check", "--cwd", root)
	require.Error(t, err)
	require.Contains(t, stderr, "2 archived change(s) in the older layout")

	stdout, _, err = v1Exec(t, newProjectCmd, "archive-layout", "--json", "--cwd", root)
	require.NoError(t, err)
	var res apexv1.ArchiveLayoutResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Len(t, res.Moved, 2)
	require.Len(t, res.Rewritten, 2)
	require.NotEmpty(t, res.Commit)
	require.Equal(t, apexv1.ArchiveLayoutSubject, v1Git(t, root, "log", "-1", "--format=%s"))
	require.Equal(t, 3, archivedCount(t, root))

	stdout, _, err = v1Exec(t, newProjectCmd, "archive-layout", "--check", "--cwd", root)
	require.NoError(t, err, "--check: 0 applied")
	require.Contains(t, stdout, "nothing left to move")
	head := v1Git(t, root, "rev-parse", "HEAD")
	stdout, _, err = v1Exec(t, newProjectCmd, "archive-layout", "--cwd", root)
	require.NoError(t, err)
	require.Equal(t, "archive layout: nothing to move\n", stdout)
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"), "a second run commits nothing")
}

// A state.json in the newer layout is judged as one in the older: only
// ape's commits write it (D155).
func TestProvenance_StateForgedInTheNewerLayout(t *testing.T) {
	root := v1Repo(t)
	rel := "development/archive/changes/2026-10-16-add-wave/state.json"
	writeAndCommit(t, root, rel, `{"id": "add-wave", "status": "archived"}`+"\n", "wip")
	d, ok := provenanceCodes(t, root)["state.forged"]
	require.True(t, ok)
	require.Equal(t, rel, d.File)
}

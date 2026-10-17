package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/exoport/apex_process_ape/internal/statusboard"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Every v1 leaf says whether it refreshes the board: a new verb that
// changes what status reports and refreshes nothing leaves the tab stale
// with nothing to say so. And every listed path exists, so a rename does
// not silently drop a verb from the refresh.
func TestBoardRefresh_EveryV1LeafIsClassified(t *testing.T) {
	root := newRootCmd()
	seen := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.HasSubCommands() {
			for _, sub := range c.Commands() {
				walk(sub)
			}
			return
		}
		if !c.Runnable() || c.Hidden || c.Name() == "help" {
			return
		}
		p := boardPath(c)
		seen[p] = true
		require.True(t, boardRefreshVerbs[p] != boardSkipVerbs[p],
			"%q must be in exactly one of boardRefreshVerbs and boardSkipVerbs (v1_board.go)", p)
	}
	for _, name := range boardGroups {
		for _, c := range root.Commands() {
			if c.Name() == name {
				walk(c)
			}
		}
	}
	var listed []string
	for p := range boardRefreshVerbs {
		listed = append(listed, p)
	}
	for p := range boardSkipVerbs {
		listed = append(listed, p)
	}
	sort.Strings(listed)
	for _, p := range listed {
		require.True(t, seen[p], "%q is listed in v1_board.go but is no command", p)
	}
}

// boardFor gives the project a board document and nothing else — no
// server, which is the state most boards are in most of the time.
func boardFor(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".aboard", "run"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".aboard", "aboard.json"),
		[]byte(`{"version":1,"rev":1,"nextId":1,"tabs":[]}`), 0o644))
}

func boardDoc(t *testing.T, root string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, ".aboard", "aboard.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	return doc
}

func boardTabJSON(t *testing.T, root string) string {
	t.Helper()
	tabs, ok := boardDoc(t, root)["tabs"].([]any)
	require.True(t, ok)
	require.Len(t, tabs, 1)
	b, err := json.Marshal(tabs[0])
	require.NoError(t, err)
	return string(b)
}

// D60: a state-changing verb refreshes the tab, through the real command
// tree, with no server anywhere; and status --json carries the same Next.
func TestBoardRefresh_AfterAStateChangingVerb(t *testing.T) {
	root := v1Repo(t)
	boardFor(t, root)

	stdout, stderr, err := v1Exec(t, newRootCmd, "next", "add", "the CSV export ignores --tz", "--cwd", root)
	require.NoError(t, err, stderr)
	require.Empty(t, stderr)
	require.NotContains(t, stdout, "board", "the refresh wrote to stdout")
	tab := boardTabJSON(t, root)
	require.Contains(t, tab, `"key":"`+statusboard.TabKey+`"`)
	require.Contains(t, tab, "the CSV export ignores --tz")
	require.Contains(t, tab, `"title":"M1 — Skeleton"`)

	out, _, err := v1Exec(t, newRootCmd, "status", "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Next        string   `json:"next"`
		RoadmapNext []string `json:"roadmap_next"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &st))
	require.NotEmpty(t, st.Next, "the next step is still a string")
	require.Len(t, st.RoadmapNext, 1)
	require.Contains(t, st.RoadmapNext[0], "the CSV export ignores --tz")
	require.NotContains(t, out, `"board"`, "status without --board reports no board")
}

// A project without .aboard/ pays nothing, hears nothing, and is not given
// a board.
func TestBoardRefresh_NoBoardIsCompletelySilent(t *testing.T) {
	root := v1Repo(t)
	_, stderr, err := v1Exec(t, newRootCmd, "next", "add", "x", "--cwd", root)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.NoDirExists(t, filepath.Join(root, ".aboard"), "a refresh created a board")
}

// A board nothing can parse changes neither the exit code nor stdout of the
// command it follows; it says so on stderr.
func TestBoardRefresh_ABrokenBoardChangesNeitherStdoutNorExitCode(t *testing.T) {
	root := v1Repo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".aboard"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".aboard", "aboard.json"), []byte("{ not json"), 0o644))

	stdout, stderr, err := v1Exec(t, newRootCmd, "milestone", "new", "Waves", "--gate", "./gate.sh", "--goal", "waves", "--json", "--cwd", root)
	require.NoError(t, err, "a broken board changed the exit code")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &parsed), "stdout is no longer the command's JSON:\n%s", stdout)
	require.Contains(t, stderr, "status board refresh skipped")
}

// A usage error wrote nothing, so it refreshes nothing.
func TestBoardRefresh_NotAfterAUsageError(t *testing.T) {
	root := v1Repo(t)
	boardFor(t, root)
	_, _, err := v1Exec(t, newRootCmd, "next", "add", "two\nlines", "--cwd", root)
	code, _ := ExitCode(err)
	require.Equal(t, ExitUsage, code)
	require.Empty(t, boardDoc(t, root)["tabs"], "a usage error refreshed the board")
}

// `ape status --board` refreshes now and says where it wrote; a second
// call with nothing changed writes nothing; without a board it says so and
// exits 0.
func TestStatusBoard(t *testing.T) {
	root := v1Repo(t)

	out, stderr, err := v1Exec(t, newRootCmd, "status", "--board", "--cwd", root)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Contains(t, out, "Board: none in "+root)
	require.NoDirExists(t, filepath.Join(root, ".aboard"))

	boardFor(t, root)
	out, _, err = v1Exec(t, newRootCmd, "status", "--board", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, out, "Board: tab \"APEX status\" (key ape-status) written to "+filepath.Join(root, ".aboard", "aboard.json"))
	require.Contains(t, boardTabJSON(t, root), `"name":"APEX status"`)

	out, _, err = v1Exec(t, newRootCmd, "status", "--board", "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Project string               `json:"project"`
		Board   *statusboard.Outcome `json:"board"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &st))
	require.NotEmpty(t, st.Project, "the status report is still all there")
	require.NotNil(t, st.Board)
	require.Equal(t, statusboard.ActionUnchanged, st.Board.Action)
	rev, ok := boardDoc(t, root)["rev"].(float64)
	require.True(t, ok)
	require.Equal(t, 2, int(rev), "an unchanged view was written again")
}

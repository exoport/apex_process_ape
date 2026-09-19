package change

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The fixture is a COPY of the framework's own table (framework
// e5e9d5cb). Parsing an invented shape would prove only that ape can
// read what ape wrote; the point of this parser is that it reads what
// the framework ships. `make check-framework` re-checks it against a
// live checkout, which is what catches the copy going stale.
func realTable(t *testing.T) *RouteTable {
	t.Helper()
	table := LoadRouteTable(filepath.Join("testdata", "change-routes.yaml"))
	require.NotEmpty(t, table.Routes, "the framework's own table parses")
	return table
}

func TestRouteTable_FillsTheClosedPlaceholderSet(t *testing.T) {
	printed := realTable(t).Lookup("rung 2 12-3_do-the-thing", RouteFill{
		RequestFile: "'/p/_output/ape/changes/x/request.txt'",
		StoryKey:    "12-3_do-the-thing",
		ChangeID:    "20260919-120000-abc1234",
	})

	require.Equal(t, "rung 2 12-3_do-the-thing", printed.Route)
	require.NotEmpty(t, printed.Summary)
	require.Len(t, printed.Commands, 1)
	require.Contains(t, printed.Commands[0], "apex-story-amend")
	require.Contains(t, printed.Commands[0], "--args \"--story 12-3_do-the-thing\"")
	require.Contains(t, printed.Commands[0],
		"--prompt-file '/p/_output/ape/changes/x/request.txt' --prompt-flag --instruction")
	require.Empty(t, printed.Note)
	require.NotEmpty(t, printed.Notes)
}

// Angle markers are literal: `--epic <N>` is the conductor's to fill,
// and substituting it would invent an epic number.
func TestRouteTable_AngleMarkersAreLeftForTheConductor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"routes:\n  lean-story:\n    summary: one story, seeded and built\n    commands:\n"+
			"      - ape task apex-create-epics-and-stories --args \"--additive --epic <N>\""+
			" --prompt-file {request_file} --prompt-flag --instruction\n"+
			"      - ape task apex-create-story --args \"<story-key>\"\n"), 0o644))

	printed := LoadRouteTable(path).Lookup("lean-story", RouteFill{RequestFile: "'/p/request.txt'"})
	require.Len(t, printed.Commands, 2)
	require.Contains(t, printed.Commands[0], "--epic <N>")
	require.Contains(t, printed.Commands[0], "--prompt-file '/p/request.txt'")
	require.Contains(t, printed.Commands[1], "<story-key>")
	require.Empty(t, printed.Note)
}

// A chain that SEEDS the story its later steps name cannot resolve
// `{story_key}` at print time — the story does not exist yet — so those
// steps carry the conductor's angle marker instead. ape prints the whole
// chain and fills only what it can know.
//
// The framework's first cut of this table used `{story_key}` there, and
// both routes degraded to their name: one unfillable command invalidates
// the list, because a four-step chain with an empty `--args ""` in step
// two is worse than no chain. Copying the framework's file rather than
// inventing a fixture is what caught it.
func TestRouteTable_ASeedChainCarriesTheConductorsMarker(t *testing.T) {
	printed := realTable(t).Lookup("lean-story", RouteFill{RequestFile: "'/p/request.txt'"})
	require.Len(t, printed.Commands, 4)
	require.Contains(t, printed.Commands[0], "--epic <N>")
	require.Contains(t, printed.Commands[1], "<story-key>")
	require.Empty(t, printed.Note)
	require.NotEmpty(t, printed.Notes)
}

// The rule the framework's own table states: `{story_key}` is usable
// only where the story already exists, which is rung 2 and nowhere else.
func TestRouteTable_TheResolvedKeyPlaceholderIsRung2Only(t *testing.T) {
	for name, route := range realTable(t).Routes {
		for _, cmd := range route.Commands {
			if strings.Contains(cmd, "{story_key}") {
				require.Equal(t, "rung-2", name,
					"a route that does not already have its story cannot resolve a key")
			}
		}
	}
}

// A route with no placeholders needs no fill at all: ux and governance
// are plain re-runs.
func TestRouteTable_APlainRouteNeedsNoFill(t *testing.T) {
	printed := realTable(t).Lookup("ux", RouteFill{})
	require.Len(t, printed.Commands, 1)
	require.Contains(t, printed.Commands[0], "apex-create-wireframes")
	require.NotContains(t, printed.Commands[0], "--validate-and-fix",
		"the ux route is a plain re-run; the skill's own continue path does the rest")
}

// Every degradation lands the same way: the route's name, a note saying
// why, and no commands. A command with a hole in it is worse than none.
func TestRouteTable_Degradations(t *testing.T) {
	t.Run("no table at all", func(t *testing.T) {
		printed := LoadRouteTable(filepath.Join(t.TempDir(), "absent.yaml")).
			Lookup("lean-story", RouteFill{RequestFile: "'/p/r.txt'"})
		require.Equal(t, "lean-story", printed.Route)
		require.Empty(t, printed.Commands)
		require.Contains(t, printed.Note, "no entry for this route")
	})

	t.Run("an unparseable table", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "broken.yaml")
		require.NoError(t, os.WriteFile(path, []byte("routes: [unclosed\n"), 0o644))
		printed := LoadRouteTable(path).Lookup("lean-story", RouteFill{})
		require.Empty(t, printed.Commands, "a reference table can never fail a finished run")
	})

	t.Run("a route the table does not carry", func(t *testing.T) {
		printed := realTable(t).Lookup("teleport", RouteFill{})
		require.Equal(t, "teleport", printed.Route)
		require.Empty(t, printed.Commands)
	})

	t.Run("no route named", func(t *testing.T) {
		printed := realTable(t).Lookup("", RouteFill{})
		require.Contains(t, printed.Note, "named no route")
	})

	t.Run("a story key that would not resolve", func(t *testing.T) {
		printed := realTable(t).Lookup("rung 2 12-3", RouteFill{RequestFile: "'/p/r.txt'"})
		require.Empty(t, printed.Commands, "the key lands in a shell command, so it is never pasted")
		require.Contains(t, printed.Note, "could not be resolved")
		require.NotEmpty(t, printed.Notes, "the route's own notes still print")
	})

	t.Run("a run with no request to put in a file", func(t *testing.T) {
		printed := realTable(t).Lookup("lean-story", RouteFill{})
		require.Empty(t, printed.Commands)
		require.Contains(t, printed.Note, "no request")
	})

	t.Run("a placeholder ape does not fill invalidates the table", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "invented.yaml")
		require.NoError(t, os.WriteFile(path, []byte(
			"routes:\n  lean-story:\n    summary: x\n    commands:\n      - ape task x --args \"{epic_number}\"\n"), 0o644))
		printed := LoadRouteTable(path).Lookup("lean-story", RouteFill{})
		require.Empty(t, printed.Commands)
		require.Contains(t, printed.Note, "{epic_number}")
		require.Contains(t, printed.Note, "the table is invalid")
	})
}

func TestStoryKeyFrom(t *testing.T) {
	require.Equal(t, "12-3_do-the-thing", StoryKeyFrom("rung 2 12-3_do-the-thing"))
	require.Empty(t, StoryKeyFrom("lean-story"))
	require.Empty(t, StoryKeyFrom("rung 2"))
}

// The printed line is pasted into a shell, and the path comes from a
// project's own output_folder — which ape does not get to choose.
func TestShellQuote(t *testing.T) {
	require.Equal(t, "'/p/a b/request.txt'", ShellQuote("/p/a b/request.txt"))
	require.Equal(t, `'/p/it'\''s/request.txt'`, ShellQuote("/p/it's/request.txt"))
	require.NotContains(t, ShellQuote("/p/$(whoami)/r.txt"), "\"")
	require.True(t, strings.HasPrefix(ShellQuote("/p/$(whoami)/r.txt"), "'"))
}

package outputstyles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeTable(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, TableFile), []byte(body), 0o644))
	return root
}

// An absent table is the state of every project on a framework that
// predates it, and it must enrol nothing rather than fail — the whole
// version-skew contract rests on this.
func TestLoad_AbsentFileEnrolsNothing(t *testing.T) {
	tbl, err := Load(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, 0, tbl.Len())
	_, ok := tbl.Style("apex-anything")
	require.False(t, ok)
}

func TestLoad_ParsesRowsHeaderAndComments(t *testing.T) {
	root := writeTable(t, `skill,style
# governance skills measured faster under a terse style
apex-adr-survey,Concise
apex-story-batch-dev,Default

apex-create-prd,Explanatory
`)
	tbl, err := Load(root)
	require.NoError(t, err)
	require.Equal(t, 3, tbl.Len())

	style, ok := tbl.Style("apex-adr-survey")
	require.True(t, ok)
	require.Equal(t, "Concise", style)

	require.Equal(t, []string{"apex-adr-survey", "apex-create-prd", "apex-story-batch-dev"}, tbl.Skills())
	require.Empty(t, tbl.Warnings)
}

// One unusable row must not take the rest of the table with it: the file
// is framework-owned and a project cannot fix it locally.
func TestLoad_BadRowIsSkippedNotFatal(t *testing.T) {
	root := writeTable(t, `apex-good,Concise
apex-lonely
,Concise
apex-empty,
`)
	tbl, err := Load(root)
	require.NoError(t, err)
	require.Equal(t, 1, tbl.Len())
	_, ok := tbl.Style("apex-good")
	require.True(t, ok)
	require.Len(t, tbl.Warnings, 3)
}

// The row most likely to be wrong is the one that looks right. Claude
// Code ignores a style name it cannot resolve, so a miscased built-in
// leaves the skill on the default while the CSV reads as if it were
// enrolled. ape reports it and does NOT silently retarget the row.
func TestLoad_MiscasedBuiltinWarnsAndIsKeptVerbatim(t *testing.T) {
	root := writeTable(t, "apex-x,concise\n")
	tbl, err := Load(root)
	require.NoError(t, err)

	style, ok := tbl.Style("apex-x")
	require.True(t, ok)
	require.Equal(t, "concise", style, "the row must reach claude exactly as written")
	require.Len(t, tbl.Warnings, 1)
	require.Contains(t, tbl.Warnings[0], `"Concise"`)
}

// A project may ship its own style; ape cannot enumerate what a machine
// has installed, so an unrecognised name is carried without complaint.
func TestLoad_CustomStyleNameIsNotAWarning(t *testing.T) {
	root := writeTable(t, "apex-x,ApexTerse\n")
	tbl, err := Load(root)
	require.NoError(t, err)
	style, _ := tbl.Style("apex-x")
	require.Equal(t, "ApexTerse", style)
	require.Empty(t, tbl.Warnings)
}

// `default` is Claude Code's own key for the standard style and
// `Default` is what ape pins; both reach the same session, so neither
// may be reported as a miscased built-in.
func TestLoad_BothDefaultSpellingsAreClean(t *testing.T) {
	root := writeTable(t, "apex-a,Default\napex-b,default\n")
	tbl, err := Load(root)
	require.NoError(t, err)
	require.Empty(t, tbl.Warnings)
}

func TestLoad_UnparseableFileIsAnError(t *testing.T) {
	root := writeTable(t, "apex-x,\"unterminated\n")
	_, err := Load(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), TableFile)
}

func TestEntries_SortedPairs(t *testing.T) {
	root := writeTable(t, "b-skill,Concise\na-skill,Explanatory\n")
	tbl, err := Load(root)
	require.NoError(t, err)
	require.Equal(t, [][2]string{{"a-skill", "Explanatory"}, {"b-skill", "Concise"}}, tbl.Entries())
}

// A nil Table is what every caller holds when loading failed, so the
// accessors have to be safe on it rather than relying on the caller.
func TestNilTableIsInert(t *testing.T) {
	var tbl *Table
	require.Equal(t, 0, tbl.Len())
	require.Nil(t, tbl.Skills())
	require.Nil(t, tbl.Entries())
	_, ok := tbl.Style("apex-x")
	require.False(t, ok)
}

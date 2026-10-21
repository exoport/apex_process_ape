package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/migration"
	"github.com/stretchr/testify/require"
)

// RG-3 (D251): apexV1Project tells an apex-v1 project — by its installed
// apex-v1 release or its apex-v1 layout — from an apex-v0 one or an empty
// directory, which may still take an apex-v0 release.
func TestApexV1Project(t *testing.T) {
	empty := t.TempDir()
	require.False(t, apexV1Project(empty))

	v0 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(v0, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(v0, "_apex", "ape-commands.yaml"), []byte("min_ape_version: 0.31.0\n"), 0o644))
	require.False(t, apexV1Project(v0), "an apex-v0 install")

	v1rel := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(v1rel, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(v1rel, "_apex", "ape-commands.yaml"), []byte("min_ape_version: 1.0.0\n"), 0o644))
	require.True(t, apexV1Project(v1rel), "an apex-v1 install")

	layout := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(layout, "_apex", "state"), 0o755))
	require.True(t, apexV1Project(layout), "the apex-v1 layout, whatever is installed")
}

// RG-3b (D277): `--plan` against a repo whose newest final is apex-v0
// (the default repo before the first apex-v1 final) listed that release's
// v0 migrations as runnable on an apex-v1 project. The plan's v1 floor
// now asks the project too, not only the incoming release.
func TestV1Install_AsksTheProjectWhenARepoIsSet(t *testing.T) {
	v0 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(v0, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(v0, "_apex", "ape-commands.yaml"), []byte("min_ape_version: 0.31.0\n"), 0o644))
	v1proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(v1proj, "_apex", "state"), 0o755))
	v0proj := t.TempDir()
	require.True(t, v1Install(v1proj, v0), "an apex-v1 project plans no apex-v0 entry, whatever the repo")
	require.False(t, v1Install(v0proj, v0), "an apex-v0 project against an apex-v0 release still does")
}

// RG-3b (c): SOURCE compares a row's path with the directory the plan
// read it from (a tag's export), not the clone: every row of a tag used
// to say "installed".
func TestMigrationSource_ReadsTheExportedSource(t *testing.T) {
	export, installed := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(installed, "v1.0.0_seq-01_lift.md"), nil, 0o644))
	row := func(dir, name string) *migration.Row {
		r := &migration.Row{}
		r.Path = filepath.Join(dir, name)
		return r
	}
	require.Equal(t, "incoming (new)", migrationSource(row(export, "v1.0.0_seq-02_x.md"), export, installed))
	require.Equal(t, "incoming (replaces installed)", migrationSource(row(export, "v1.0.0_seq-01_lift.md"), export, installed))
	require.Equal(t, "installed", migrationSource(row(installed, "v1.0.0_seq-01_lift.md"), export, installed))
}

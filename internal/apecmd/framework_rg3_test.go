package apecmd

import (
	"os"
	"path/filepath"
	"testing"

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

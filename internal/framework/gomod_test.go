package framework_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

func TestDefaultProjectName_FromGoMod(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/foo/myproject\n\ngo 1.26\n"), 0o644))

	require.Equal(t, "myproject", framework.DefaultProjectName(dir))
}

func TestDefaultProjectName_FromGoMod_SingleSegment(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module standalone\n\ngo 1.26\n"), 0o644))

	require.Equal(t, "standalone", framework.DefaultProjectName(dir))
}

func TestDefaultProjectName_FallsBackToDirBase(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fallback-name")
	require.NoError(t, os.Mkdir(dir, 0o755))

	require.Equal(t, "fallback-name", framework.DefaultProjectName(dir))
}

func TestDefaultProjectName_FallbackOnMalformedGoMod(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("not a real go.mod"), 0o644))

	// Falls back to directory base name when go.mod can't be parsed.
	require.Equal(t, "broken", framework.DefaultProjectName(dir))
}

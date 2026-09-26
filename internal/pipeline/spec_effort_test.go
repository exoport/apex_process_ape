package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/effort"
	"github.com/stretchr/testify/require"
)

func TestSpec_EffortHelpers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fx.yaml"), []byte("name: fx\neffort: high\nstages:\n"+
		"  a:\n    chain:\n      - skill: s0\n      - skill: s1\n        effort: low\n"+
		"  b:\n    effort: xtreme\n    chain:\n      - skill: s2\n"), 0o644))
	spec, err := LoadSpec("fx", root)
	require.NoError(t, err)

	level, source, err := spec.EffectiveEffort("a", 0, "medium")
	require.NoError(t, err)
	require.Equal(t, []string{"high", effort.SourcePipeline}, []string{level, source}, "the spec outranks --effort")
	level, source, _ = spec.EffectiveEffort("a", 1, "")
	require.Equal(t, []string{"low", effort.SourceStep}, []string{level, source})

	conflicts := spec.StageEffortConflicts()
	require.Len(t, conflicts, 1)
	require.Equal(t, "a", conflicts[0].Stage)
	require.Equal(t, "high", conflicts[0].Launch)
	require.Equal(t, "low", conflicts[0].Steps[0].Declared)

	errs := spec.EffortErrors()
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], `stage "b"`)
	require.Contains(t, errs[0], "xtreme")
}

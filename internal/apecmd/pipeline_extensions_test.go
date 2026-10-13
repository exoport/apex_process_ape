package apecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
)

const gatedSpec = `name: gated
stages:
  prep:
    chain:
      - skill: a
  patterns:
    requires-extensions: [ext-patterns]
    chain:
      - skill: b
  refresh:
    chain:
      - skill: c
        requires-extensions: [ext-features]
      - skill: d
`

func writeGatedProject(t *testing.T, spec, extensions string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gated.yaml"), []byte(spec), 0o644))
	if extensions != "" {
		require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"),
			[]byte("extensions: "+extensions+"\n"), 0o644))
	}
	return root
}

func TestResolveRunExtensions_ReportsPlanAndFrom(t *testing.T) {
	root := writeGatedProject(t, gatedSpec, "[ext-adrs]")
	spec, err := pipeline.LoadSpec("gated", root)
	require.NoError(t, err)

	var buf bytes.Buffer
	ext, err := resolveRunExtensions(&buf, spec, root, "patterns", false, false)
	require.NoError(t, err)
	require.Equal(t, []string{"ext-adrs"}, ext.Enabled)
	out := buf.String()
	require.Contains(t, out, "▸ extensions: ext-adrs")
	require.Contains(t, out, "▸ skip stage patterns (needs ext-patterns)")
	require.Contains(t, out, "▸ skip step refresh/1 c (needs ext-features)")
	require.Contains(t, out, "▸ --from patterns: that stage is skipped; starting at refresh")

	buf.Reset()
	_, err = resolveRunExtensions(&buf, spec, root, "", true, false)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "gates ignored")
}

func TestResolveRunExtensions_GatedWithoutConfigRefuses(t *testing.T) {
	root := writeGatedProject(t, gatedSpec, "")
	spec, err := pipeline.LoadSpec("gated", root)
	require.NoError(t, err)
	_, err = resolveRunExtensions(&bytes.Buffer{}, spec, root, "", false, false)
	require.ErrorContains(t, err, "--ignore-extensions")
}

// A pipeline that does not load is a FAIL row: `ape pipeline` refuses it,
// so counting it as installed reported a broken pipeline as fine.
func TestCheckPipelinesProject_FailsOnUnloadableSpec(t *testing.T) {
	bad := "name: gated\nstages:\n  s:\n    requires-extensions: [ext-pattern]\n    chain:\n      - skill: a\n"
	root := writeGatedProject(t, bad, "[ext-adrs]")
	res := checkPipelinesProject(context.Background(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusFail, res.Status, res.Message)
	require.Contains(t, res.Message, `unknown extension "ext-pattern"`)
}

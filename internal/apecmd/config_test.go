package apecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/stretchr/testify/require"
)

// realProjectConfig is a real v0 project's config: governance artifacts
// under development/governance, a non-default layout.
const realProjectConfig = `config_schema_version: "1"
project_name: axon
extensions: [ext-adrs, ext-patterns]
user_name: Boss
communication_language: English
document_output_language: English
user_skill_level: intermediate
apex_folder: _apex
output_folder: _output
development_folder: development
docs_folder: docs
implementation_folder: development/implementation
planning_folder: development/planning
governance_repository_path: ""
governance_folder: development/governance
governance_staleness: warn
functionality_folder: development/functionality
`

// newTestProject builds a project rooted at a temp dir and chdirs into
// it for the duration of the test, since the resolvers under test read
// the working directory.
func newTestProject(t *testing.T, cfg string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, apexcfg.DirName), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, apexcfg.DirName, apexcfg.BaseFile), []byte(cfg), 0o644,
	))
	t.Chdir(root)
	return root
}

// TestConfigResolve_JSONPayload covers the wire contract the framework
// skills read: snake_case keys, the four ext_* flags, and absolute paths.
func TestConfigResolve_JSONPayload(t *testing.T) {
	root := newTestProject(t, realProjectConfig)

	var buf bytes.Buffer
	cmd := newConfigResolveCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--output-format", "json"})
	require.NoError(t, cmd.Execute())

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))

	require.Equal(t, root, got["root"])
	require.Equal(t, false, got["local_overlay_applied"])
	require.Equal(t, "axon", got["project_name"])
	require.Equal(t, "development/governance", got["governance_folder"])

	ext, ok := got["ext"].(map[string]any)
	require.True(t, ok, "ext block present")
	require.Equal(t, true, ext["ext_adrs"])
	require.Equal(t, true, ext["ext_patterns"])
	require.Equal(t, false, ext["ext_features"])
	require.Equal(t, false, ext["ext_capabilities"])

	paths, ok := got["paths"].(map[string]any)
	require.True(t, ok, "paths block present")
	require.Equal(t, filepath.Join(root, "development", "governance", "adrs"), paths["adrs"])
	require.Equal(t, filepath.Join(root, "development", "deferred"), paths["deferred"])

	require.NotEmpty(t, got["date"])
	require.NotEmpty(t, got["timestamp"])
}

func TestConfigResolve_HumanAndYAML(t *testing.T) {
	newTestProject(t, realProjectConfig)

	for _, format := range []string{"human", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := newConfigResolveCmd()
			cmd.SetOut(&buf)
			cmd.SetArgs([]string{"--output-format", format})
			require.NoError(t, cmd.Execute())
			// Native separator: apexcfg resolves through filepath.Join, so
			// the rendered paths are the platform's — backslashed on Windows.
			require.Contains(t, buf.String(), filepath.Join("development", "governance"))
		})
	}
}

func TestConfigResolve_LocalOverlayReported(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	require.NoError(t, os.WriteFile(
		filepath.Join(root, apexcfg.DirName, apexcfg.LocalFile),
		[]byte("governance_repository_path: /srv/canon\n"), 0o644,
	))

	var buf bytes.Buffer
	cmd := newConfigResolveCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--output-format", "json"})
	require.NoError(t, cmd.Execute())

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Equal(t, true, got["local_overlay_applied"])
	require.Equal(t, "/srv/canon", got["governance_repository_path"])
	require.Equal(t, []any{"governance_repository_path"}, got["overlaid_keys"])
}

// TestConfigResolve_CwdFlag proves --cwd is honoured rather than the
// process working directory, which is how every skill will call it.
func TestConfigResolve_CwdFlag(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	var buf bytes.Buffer
	cmd := newConfigResolveCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--cwd", root, "--output-format", "json"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, buf.String(), "axon")
}

package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// H21 (D45): docs: names the changelog and user docs; project check
// reports a configured path that does not exist, and status carries the
// block for the skills.
func TestDocsConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	for yaml, want := range map[string]string{
		"docs: {changelog: CHANGELOG.md, user_docs: docs/}\n": "",
		"docs: {changelog: /etc/CHANGELOG}\n":                 "is not a path inside the project",
		"docs: {user_docs: ../elsewhere}\n":                   "is not a path inside the project",
		"docs: {change_log: CHANGELOG.md}\n":                  "docs.change_log: not a key",
	} {
		require.NoError(t, os.WriteFile(ConfigPath(root), []byte(yaml), 0o644))
		cfg, err := LoadConfig(root)
		if want == "" {
			require.NoError(t, err)
			require.Equal(t, DocsConfig{Changelog: "CHANGELOG.md", UserDocs: "docs/"}, cfg.Docs)
			continue
		}
		require.ErrorContains(t, err, want, yaml)
	}
}

func TestProjectCheck_DocsMissing(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\ndocs: {changelog: CHANGELOG.md, user_docs: docs/}\n"
	p := fixture(t, files)
	got := findingMessages(p.Check())
	require.Contains(t, got, "docs.changelog: CHANGELOG.md does not exist")
	require.Contains(t, got, "docs.user_docs: docs/ does not exist")
	for _, f := range ClassifyFindings(p.Check()) {
		if f.File == "_apex/config.yaml" {
			require.Equal(t, "docs.missing", f.Code)
			require.Equal(t, ActionPerson, ActionFor(f.Code))
		}
	}

	write(t, p, "CHANGELOG.md", "# Changelog\n")
	write(t, p, "docs/index.md", "# Docs\n")
	require.NotContains(t, findingMessages(p.Check()), "docs.")

	data, err := json.Marshal(p.Status())
	require.NoError(t, err)
	require.Contains(t, string(data), `"docs":{"changelog":"CHANGELOG.md","user_docs":"docs/"}`)
}

func TestStatus_DocsAlwaysPresent(t *testing.T) {
	p := fixture(t, baseFiles())
	data, err := json.Marshal(p.Status())
	require.NoError(t, err)
	require.Contains(t, string(data), `"docs":{}`)
}

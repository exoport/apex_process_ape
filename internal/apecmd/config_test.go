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

// realProjectConfig is the shape that broke `ape adr list`: governance
// artifacts under development/governance, which neither hardcoded probe
// in findADRDir ever looked at.
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

// seedADRs writes n ADR records plus an index that lists them, in the
// real corpus layout: {governance_folder}/adrs/.
func seedADRs(t *testing.T, root string, n int) {
	t.Helper()
	dir := filepath.Join(root, "development", "governance", "adrs")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "changelog"), 0o755))
	var index bytes.Buffer
	index.WriteString("generated_at: \"20260821120000\"\nadrs:\n")
	for i := 1; i <= n; i++ {
		name := adrFixtureName(i)
		body := "---\nid: " + adrFixtureID(i) + "\ntitle: Decision " + adrFixtureID(i) +
			"\nstatus: accepted\n---\n\n## Context\n\nx\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
		index.WriteString("  - id: " + adrFixtureID(i) + "\n")
		index.WriteString("    title: Decision " + adrFixtureID(i) + "\n")
		index.WriteString("    status: accepted\n")
		index.WriteString("    file: " + name + "\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.yaml"), index.Bytes(), 0o644))
}

func adrFixtureID(i int) string {
	return "ADR-" + zeroPad4(i)
}

func adrFixtureName(i int) string {
	return "adr-" + zeroPad4(i) + "_decision.md"
}

func zeroPad4(i int) string {
	s := []byte("0000")
	for pos := 3; pos >= 0 && i > 0; pos-- {
		s[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(s)
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
			require.Contains(t, buf.String(), "development/governance")
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

// TestADRList_ResolvesThroughConfig is the D1 regression and the
// definition of done: against a project whose governance_folder is
// development/governance, `ape adr list` must return every record. Before
// PLAN-25 it printed "no ADR directory found" and exited 0.
func TestADRList_ResolvesThroughConfig(t *testing.T) {
	const wantRecords = 64
	root := newTestProject(t, realProjectConfig)
	seedADRs(t, root, wantRecords)

	// The resolver is what regressed; assert on it directly so the test
	// pins the behaviour rather than the rendering.
	dir := findADRDir()
	require.Equal(t, filepath.Join(root, "development", "governance", "adrs"), dir,
		"findADRDir must resolve through _apex/config.yaml, not a hardcoded probe")

	var buf bytes.Buffer
	cmd := newADRListCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--output-format", "json"})
	require.NoError(t, cmd.Execute())

	var records []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &records))
	require.Len(t, records, wantRecords, "every ADR on disk must be listed")
	require.Equal(t, "ADR-0001", records[0]["id"])
}

// TestFindADRDir_LegacyFallback keeps the pre-PLAN-25 behaviour working
// for a tree with no _apex/config.yaml at all.
func TestFindADRDir_LegacyFallback(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "adrs"), 0o755))
	t.Chdir(root)

	require.Equal(t, filepath.Join("development", "adrs"), findADRDir(),
		"with no config, the legacy candidate still resolves")
}

func TestFindADRDir_NoProjectNoLegacy(t *testing.T) {
	t.Chdir(t.TempDir())
	require.Empty(t, findADRDir())
	require.Empty(t, findPatternsDir())
}

// TestFirstExistingDir_SkipsEmptyAndRoot guards the $APE_PROCESS_REPO
// candidate: unset, filepath.Join would yield "/development/adrs" — and
// a bare separator must never count as a hit.
func TestFirstExistingDir_SkipsEmptyAndRoot(t *testing.T) {
	require.Empty(t, firstExistingDir("", string(filepath.Separator)))
	dir := t.TempDir()
	require.Equal(t, dir, firstExistingDir("", dir))
	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	require.Empty(t, firstExistingDir(file), "a file is not a directory")
}

// TestPatternDir_ResolvesThroughConfig mirrors the ADR regression for
// the patterns family.
func TestPatternDir_ResolvesThroughConfig(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	dir := filepath.Join(root, "development", "governance", "patterns")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.Equal(t, dir, findPatternsDir())
}

// TestADRNew_WritesWhereListReads closes the disagreement between the
// two commands: `new` used to fall back to development/adrs while `list`
// read the resolved governance folder.
func TestADRNew_WritesWhereListReads(t *testing.T) {
	root := newTestProject(t, realProjectConfig)

	cmd := newADRNewCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"Use", "Markdown", "ADRs"})
	require.NoError(t, cmd.Execute())

	want := filepath.Join(root, "development", "governance", "adrs", "ADR-use-markdown-adrs.md")
	_, err := os.Stat(want)
	require.NoError(t, err, "adr new must write into the resolved governance folder")
}

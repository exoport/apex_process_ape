package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/memory"
	"github.com/stretchr/testify/require"
)

// writeProjectContext drops a project-context.md into the project's
// development_folder — the canonical location, and the only one
// `ape context check` measures.
func writeProjectContext(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "development")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "project-context.md"), []byte(body), 0o644))
}

// TestContextCheck_SharesTheMemoryBudgets is the request as filed: one
// caller of the two constants rather than two restatements of the
// numbers. Bytes chosen to sit either side of each default.
func TestContextCheck_SharesTheMemoryBudgets(t *testing.T) {
	cases := []struct {
		name  string
		size  int
		state memory.State
	}{
		{"ok", 1 << 10, memory.StateOK},
		{"over soft", 100 << 10, memory.StateOverSoft},
		{"over hard", 300 << 10, memory.StateOverHard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestProject(t, realProjectConfig)
			writeProjectContext(t, root, strings.Repeat("x", tc.size))
			out := runCmd(t, newContextCheckCmd(), "--output-format", "json")
			var c memory.Check
			require.NoError(t, json.Unmarshal([]byte(out), &c))
			require.Equal(t, tc.state, c.State)
			require.Equal(t, int64(memory.DefaultSoftBudget), c.SoftBudget)
			require.Equal(t, int64(memory.DefaultHardCeiling), c.HardCeiling)
		})
	}
}

// TestContextCheck_ExitZeroByDefault: the verdict is the state field,
// never the exit code. A failing exit here would abort the generator at
// exactly the moment compaction is due — the same contract
// `ape memory check` keeps.
func TestContextCheck_ExitZeroByDefault(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeProjectContext(t, root, strings.Repeat("x", 300<<10))

	out := runCmd(t, newContextCheckCmd(), "--output-format", "json")
	var c memory.Check
	require.NoError(t, json.Unmarshal([]byte(out), &c))
	require.Equal(t, memory.StateOverHard, c.State)
	// runCmd asserts Execute() returned nil, i.e. no exit code was raised.
}

func TestContextCheck_HumanMessages(t *testing.T) {
	root := newTestProject(t, realProjectConfig)

	writeProjectContext(t, root, strings.Repeat("x", 100<<10))
	out := runCmd(t, newContextCheckCmd())
	require.Contains(t, out, "project-context: ")
	require.Contains(t, out, "over-soft")
	require.Contains(t, out, "compaction is due")

	writeProjectContext(t, root, strings.Repeat("x", 300<<10))
	out = runCmd(t, newContextCheckCmd())
	require.Contains(t, out, "OVER THE HARD CEILING")
	require.Contains(t, out, "bug report")
}

// TestContextCheck_AbsentNamesTheCanonicalPath: a project that put the
// file somewhere else has to be able to see from the output alone which
// location was measured.
func TestContextCheck_AbsentNamesTheCanonicalPath(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	// A copy at the repo root is NOT the canonical path and is not found:
	// reader skills glob for one, a size gate names one.
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "project-context.md"), []byte(strings.Repeat("x", 300<<10)), 0o644))

	out := runCmd(t, newContextCheckCmd())
	require.Contains(t, out, "absent")
	require.Contains(t, out, filepath.Join("development", "project-context.md"))
}

func TestContextCheck_ThresholdOverrides(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeProjectContext(t, root, strings.Repeat("x", 2048))

	out := runCmd(t, newContextCheckCmd(), "--soft", "1024", "--hard", "4096", "--output-format", "json")
	var c memory.Check
	require.NoError(t, json.Unmarshal([]byte(out), &c))
	require.Equal(t, memory.StateOverSoft, c.State)
	require.Equal(t, int64(1024), c.SoftBudget)
	require.Equal(t, int64(4096), c.HardCeiling)
}

func TestContextCheck_FailAt(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeProjectContext(t, root, strings.Repeat("x", 100<<10))

	cmd := newContextCheckCmd()
	cmd.SetArgs([]string{"--fail-at", "soft"})
	cmd.SetOut(&strings.Builder{})
	err := cmd.Execute()
	require.Error(t, err, "--fail-at soft exits non-zero at over-soft")

	// The same file, the default policy: exit 0.
	runCmd(t, newContextCheckCmd(), "--fail-at", "hard")
}

func TestContextCheck_RejectsUnknownFailAt(t *testing.T) {
	newTestProject(t, realProjectConfig)
	cmd := newContextCheckCmd()
	cmd.SetArgs([]string{"--fail-at", "always"})
	cmd.SetOut(&strings.Builder{})
	require.Error(t, cmd.Execute())
}

// TestContextCheck_DevelopmentFolderUnset: with no development_folder
// there is no path to stat, so the check has no basis to report `absent`.
func TestContextCheck_DevelopmentFolderUnset(t *testing.T) {
	newTestProject(t, `config_schema_version: "1"
project_name: axon
apex_folder: _apex
output_folder: _output
`)
	cmd := newContextCheckCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(&strings.Builder{})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "development_folder is not configured")
}

func TestContextCmd_Surface(t *testing.T) {
	cmd := newContextCmd()
	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	require.True(t, names["check"], "ape context check missing")
}

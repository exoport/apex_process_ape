package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.1 W2 Part 4 through the CLI: gate --spec-only runs the spec's
// scenarios alone, reports both spec_only and mode, fails red as
// gate.spec-red (exit 1, action replan), and records nothing.
func TestV1Commands_GateSpecOnly(t *testing.T) {
	root := v1Repo(t)
	spec := "# Greeting\nPurpose: Say things.\n\n### Requirement: Hello\nThe system SHALL say hello.\n" +
		"#### Scenario: hello\n- GIVEN x WHEN y THEN z\n- check: test -f hello.txt\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "specs", "greeting"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "development", "specs", "greeting", "spec.md"), []byte(spec), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "spec")
	head := v1Git(t, root, "rev-parse", "HEAD")

	stdout, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--spec-only", "--json", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), stdout)
	var red struct {
		Exit        int `json:"exit"`
		Diagnostics []struct {
			Code   string `json:"code"`
			Action string `json:"action"`
		} `json:"diagnostics"`
		Result struct {
			SpecOnly   bool   `json:"spec_only"`
			Mode       string `json:"mode"`
			SpecChecks int    `json:"spec_checks"`
			SpecFailed []struct {
				Task string `json:"task"`
			} `json:"spec_failed"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &red), stdout)
	require.Equal(t, "gate.spec-red", red.Diagnostics[0].Code)
	require.Equal(t, "replan", red.Diagnostics[0].Action)
	require.True(t, red.Result.SpecOnly)
	require.Equal(t, "spec-only", red.Result.Mode)
	require.Equal(t, 1, red.Result.SpecChecks)
	require.Equal(t, "greeting/Hello/hello", red.Result.SpecFailed[0].Task)
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"), "no commit")
	require.NoFileExists(t, filepath.Join(root, "_apex", "state", "logs", "gate-M1.json"), "the gate's record is untouched")

	require.NoError(t, os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi\n"), 0o644))
	stdout, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--spec-only", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "M1 spec-only: 1 scenario check(s), 0 red — nothing recorded")
}

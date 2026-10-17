package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// configChanged is the config.changed finding of `ape project check`, if any.
func configChanged(t *testing.T, root string) (apexv1.Diagnostic, bool) {
	t.Helper()
	stdout, _, _ := v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	var out struct {
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
	}
	if stdout != "" {
		require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	}
	for _, d := range out.Diagnostics {
		if d.Code == "config.changed" {
			return d, true
		}
	}
	return apexv1.Diagnostic{}, false
}

func appendConfig(t *testing.T, root, text, msg string) {
	t.Helper()
	path := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	writeAndCommit(t, root, "_apex/config.yaml", string(data)+text, msg)
}

// D50: a guarded config block changed outside ape's commits is
// config.changed until a person accepts it or reverts the commit; an
// unguarded key is not judged, and a later change is judged again.
func TestProvenance_ConfigChanged(t *testing.T) {
	root := v1Repo(t)
	_, found := configChanged(t, root)
	require.False(t, found)

	appendConfig(t, root, "retries: 3\n", "chore: more retries")
	d, found := configChanged(t, root)
	require.True(t, found)
	require.Equal(t, apexv1.ActionPerson, d.Action)
	require.Equal(t, "_apex/config.yaml", d.File)
	require.Contains(t, d.Message, "(retries)")
	require.Contains(t, d.Message, "chore: more retries")

	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	stdout, _, err := v1Exec(t, newChangeCmd, "start", "add-wave", "--json", "--cwd", root)
	require.Error(t, err)
	require.Contains(t, stdout, `"change.provenance"`)

	t.Setenv("CLAUDECODE", "1")
	stdout, _, err = v1Exec(t, newProjectCmd, "accept-config", "--by", "model", "--json", "--cwd", root)
	require.Error(t, err)
	require.Contains(t, stdout, `"session.person-only"`)
	t.Setenv("CLAUDECODE", "")

	_, _, err = v1Exec(t, newProjectCmd, "accept-config", "--by", "ann", "--cwd", root)
	require.NoError(t, err)
	_, found = configChanged(t, root)
	require.False(t, found, "a person accepted it")

	appendConfig(t, root, "effort: high\n", "chore: effort")
	_, found = configChanged(t, root)
	require.False(t, found, "effort: is not a guarded block")

	path := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	writeAndCommit(t, root, "_apex/config.yaml", string(data)[:len(data)-len("retries: 3\neffort: high\n")]+"retries: 5\neffort: high\n", "chore: even more retries")
	d, found = configChanged(t, root)
	require.True(t, found, "a change after the acceptance is judged again")
	require.Contains(t, d.Message, "chore: even more retries")

	v1Git(t, root, "revert", "--no-edit", "HEAD")
	_, found = configChanged(t, root)
	require.False(t, found, "a revert and what it reverts are not judged")
}

// rc.3 widened the guard (D69 and the caps review): timeouts, caps,
// regression, commit, evidence, marketplaces, plugins, tools and
// human_review (D75) join
// limits, checks, retries and requires. A guarded key the config struct
// does not hold yet (tools:, human_review:) is compared all the same;
// models: stays unguarded.
func TestProvenance_ConfigChangedGuardList(t *testing.T) {
	root := v1Repo(t)
	path := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "apex-review: {model: opus, effort: medium}", "init's D65 defaults")
	writeAndCommit(t, root, "_apex/config.yaml", strings.Replace(string(data), "apex-review: {model: opus, effort: medium}", "apex-review: sonnet", 1), "chore: review on sonnet")
	_, found := configChanged(t, root)
	require.False(t, found, "models: is not guarded")

	for _, tc := range []struct{ key, text string }{
		{"timeouts", "timeouts:\n  gate: 2h\n"},
		{"caps", "caps:\n  change_md: 32KB\n"},
		{"regression", "regression: make test\n"},
		{"commit", "commit:\n  hooks: skip\n"},
		{"evidence", "evidence: archive\n"},
	} {
		appendConfig(t, root, tc.text, "chore: "+tc.key)
		d, found := configChanged(t, root)
		require.True(t, found, tc.key)
		require.Contains(t, d.Message, "("+tc.key+")")
		_, _, err := v1Exec(t, newProjectCmd, "accept-config", "--by", "ann", "--cwd", root)
		require.NoError(t, err)
		_, found = configChanged(t, root)
		require.False(t, found, "accepted: "+tc.key)
	}
	require.Contains(t, v1Git(t, root, "log", "-1", "--format=%b"),
		"Guarded: limits, checks, retries, requires, timeouts, caps, regression, commit, evidence, marketplaces, plugins, tools, human_review")
}

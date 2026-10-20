package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// D193/D194 at the CLI: change new --type (JSON "type"; usage.type, exit 2,
// for a type the project does not list); the default writes feat or fix;
// status --json carries the effective subject template and next_why.
func TestCommitSubject_CLI(t *testing.T) {
	root := v1Repo(t)
	stdout, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--json", "--cwd", root)
	require.NoError(t, err)
	var res struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, "feat", res.Type)

	// Q135 (D240): a tweak says what it is — no default type, exit 2
	// usage.type naming the project's types, nothing written.
	stdout, _, err = v1Exec(t, newChangeCmd, "new", "fix-typo", "--milestone", "none", "--touches", "greeting", "--json", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "usage.type"`)
	require.Contains(t, stdout, "a tweak says what it is: feat, fix, perf")
	require.NoDirExists(t, filepath.Join(root, "development", "changes", "fix-typo"))
	_, _, err = v1Exec(t, newChangeCmd, "new", "fix-typo", "--milestone", "none", "--type", "fix", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	doc, err := os.ReadFile(filepath.Join(root, "development", "changes", "fix-typo", "change.md"))
	require.NoError(t, err)
	require.Contains(t, string(doc), "\ntype: fix\n")

	_, _, err = v1Exec(t, newChangeCmd, "new", "write-docs", "--milestone", "M1", "--touches", "greeting", "--type", "docs", "--cwd", root)
	require.NoError(t, err)
	stdout, _, err = v1Exec(t, newChangeCmd, "new", "add-hug", "--milestone", "M1", "--touches", "greeting", "--type", "feature", "--json", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "usage.type"`)

	stdout, _, err = v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Next    string `json:"next"`
		NextWhy string `json:"next_why"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
		Config  struct {
			CommitSubject string `json:"commit_subject"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
		} `json:"config"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	require.Equal(t, "{type}: {title}", st.Config.CommitSubject)
	require.NotEmpty(t, st.Next)
	require.NotEmpty(t, st.NextWhy)
}

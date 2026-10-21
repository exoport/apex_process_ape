package apecmd

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// Q152: in an APEX v1 project `ape prompt` follows ape task's effort rule —
// --effort, then config effort:, else nothing — on claude's command line,
// never in CLAUDE_CODE_EFFORT_LEVEL. Elsewhere the v0 rule stands.
func TestPromptEffortPlan_V1FollowsTheTaskRule(t *testing.T) {
	outsideEverySession(t)
	root := v1Repo(t)
	cfgPath := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	top := regexp.MustCompile(`(?m)^effort:.*\n`)
	setTop := func(line string) {
		t.Helper()
		require.NoError(t, os.WriteFile(cfgPath, []byte(top.ReplaceAllString(string(data), "")+line), 0o644))
	}

	setTop("")
	plan, err := promptEffortPlan(promptOptions{projectRoot: root})
	require.NoError(t, err)
	require.Empty(t, plan.EnvEntries(), "never CLAUDE_CODE_EFFORT_LEVEL in v1")
	require.Empty(t, plan.ArgEntries(), "nothing decided, nothing passed")

	setTop("effort: medium\n")
	plan, err = promptEffortPlan(promptOptions{projectRoot: root})
	require.NoError(t, err)
	require.Equal(t, []string{"--effort", "medium"}, plan.ArgEntries())
	require.Empty(t, plan.EnvEntries())
	require.Equal(t, "config-default", plan.Source)

	plan, err = promptEffortPlan(promptOptions{projectRoot: root, effort: "low"})
	require.NoError(t, err)
	require.Equal(t, []string{"--effort", "low"}, plan.ArgEntries())
	require.Equal(t, "flag", plan.Source)

	setTop("effort: extreme\n")
	_, err = promptEffortPlan(promptOptions{projectRoot: root})
	require.ErrorContains(t, err, "effort:")

	plain := t.TempDir()
	plan, err = promptEffortPlan(promptOptions{projectRoot: plain})
	require.NoError(t, err)
	require.Equal(t, []string{"CLAUDE_CODE_EFFORT_LEVEL=xhigh"}, plan.EnvEntries(), "v0's legacy default")
	require.Empty(t, plan.ArgEntries())
}

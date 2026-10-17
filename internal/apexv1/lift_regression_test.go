package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// L4, from the dry runs: tenax's lint was green at the tag and red at the
// lift commit — the lift's own gofmt breakage — yet the lift called it "a
// pre-existing failure"; washmash's idf.py and spirometer's flutter were
// not installed (exit 127) and were recorded as red.
func TestJudgeLiftRegression(t *testing.T) {
	r := func(exit int) *CheckResult { return &CheckResult{Exit: exit, Log: "x.log"} }
	for _, c := range []struct {
		name          string
		before, after *CheckResult
		want          string
	}{
		{"green both", r(0), r(0), LiftRegressionGreen},
		{"green after a red tag", r(1), r(0), LiftRegressionGreen},
		{"red both", r(2), r(2), LiftRegressionRedAtTag},
		{"red only after the lift", r(0), r(2), LiftRegressionRedAfterLift},
		{"not found both", r(127), r(127), LiftRegressionCannotRun},
		{"not executable both", r(126), r(126), LiftRegressionCannotRun},
		{"not found after a red tag", r(2), r(127), LiftRegressionCannotRun},
		{"not found only after the lift", r(0), r(127), LiftRegressionRedAfterLift},
		{"red after a tag that could not run", r(127), r(1), LiftRegressionNoBaseline},
		{"red, no run at the tag", nil, r(1), LiftRegressionNoBaseline},
	} {
		got, note := judgeLiftRegression(c.before, c.after)
		require.Equal(t, c.want, got, c.name)
		require.NotEmpty(t, note, c.name)
	}
}

func liftWithGates(t *testing.T, gates string) string {
	t.Helper()
	root := liftFixture(t, false)
	pc := filepath.Join(root, "development", "project-context.md")
	data, err := os.ReadFile(pc)
	require.NoError(t, err)
	doc := strings.Replace(string(data), "## Code Validation\n", "## Code Validation\n\n```sh\n"+gates+"\n```\n", 1)
	doc = strings.Replace(doc, "```sh\ntest -f main.go\n```\n", "", 1)
	doc = strings.Replace(doc, "- `sh ./check.sh` — the unit checks\n", "", 1)
	require.NoError(t, os.WriteFile(pc, []byte(doc), 0o644))
	run(t, root, "commit", "-q", "-am", "gates")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)
	return root
}

func TestLift_RegressionAtTheTag(t *testing.T) {
	t.Run("red only after the lift", func(t *testing.T) {
		// A test that reads the v0 corpus the lift deletes (atomix read
		// sprint-status.yaml).
		root := liftWithGates(t, "test -f development/team-memory.md")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err, "the commit stands; the verdict says what broke")
		require.Equal(t, "test -f development/team-memory.md", res.Gates.Regression)
		require.Equal(t, 0, res.RegressionAtTag.Exit)
		require.NotEqual(t, 0, res.Regression.Exit)
		require.Equal(t, LiftRegressionRedAfterLift, res.RegressionVerdict)
		require.Contains(t, res.RegressionNote, "LIFT FAILURE")
	})
	t.Run("red at the tag too", func(t *testing.T) {
		root := liftWithGates(t, "test -f nowhere.txt")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, LiftRegressionRedAtTag, res.RegressionVerdict)
		require.Contains(t, res.RegressionNote, "pre-existing")
	})
	t.Run("cannot run here", func(t *testing.T) {
		root := liftWithGates(t, "ape-no-such-tool-xyz build")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, 127, res.RegressionAtTag.Exit)
		require.Equal(t, LiftRegressionCannotRun, res.RegressionVerdict)
		require.NotContains(t, res.RegressionNote, "red at")
	})
	t.Run("not run", func(t *testing.T) {
		root := liftWithGates(t, "test -f nowhere.txt")
		res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
		require.NoError(t, err)
		require.Nil(t, res.RegressionAtTag)
		require.Empty(t, res.RegressionVerdict)
	})
}

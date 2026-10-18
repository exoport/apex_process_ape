package apexv1

import (
	"encoding/json"
	"fmt"
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

// L11, from the dry runs: the disposition file reached 379 KB (radix),
// most of it references to removed paths in evidence logs — past any
// reader's cap. It summarises now. R3, from rc.3: still 109 KB in radix
// (78.6 KB of code and CI references, 195 in 115 files), and the JSON
// report beside it a 5.8 MB commit in tenax. The file stays within 16 KB,
// one line per code file; the report is ignored state, never committed.
func TestLift_DispositionStaysReviewable(t *testing.T) {
	root := liftFixture(t, false)
	var log strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&log, "run %d read development/implementation/sprint-status.yaml\n", i)
	}
	files := map[string]string{"development/governance/evidence/ratchet.md": log.String()}
	for i := range 120 {
		files[fmt.Sprintf("pkg/p%03d/p.go", i)] = fmt.Sprintf("package p%03d\n\nfunc TestStory1_%d() {}\n\nvar s = \"Story 1.1\"\n", i, i)
	}
	for i := range 30 {
		files[fmt.Sprintf("tools/ci/guard%02d.sh", i)] = "#!/bin/sh\ntest -d development/governance/adrs\n"
	}
	// 200 references in 85 more code files, and 300 rule candidates.
	for i := range 85 {
		var sh strings.Builder
		sh.WriteString("#!/bin/sh\n")
		for j := range 1 + i%4 {
			fmt.Fprintf(&sh, "cat development/implementation/sprint-status.yaml # %d\n", j)
		}
		files[fmt.Sprintf("tools/verify/check%02d.sh", i)] = sh.String()
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	var rules strings.Builder
	for i := range 300 {
		fmt.Fprintf(&rules, "- Rule %d: never write to the shared ledger outside a reviewed change\n", i)
	}
	files["CLAUDE.md"] = string(claude) + "\n## More rules\n\n" + rules.String()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "a big corpus")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)
	res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(res.DanglingRefs), 3030)
	require.GreaterOrEqual(t, len(res.IDsLeft), 240)

	require.GreaterOrEqual(t, len(res.Candidates), 300)

	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	require.LessOrEqual(t, len(disp), liftDispositionCap, "summaries, not lists")
	d := string(disp)
	require.Contains(t, d, "- `development/governance/evidence/ratchet.md` — 3000\n")
	require.Contains(t, d, "- `tools/verify/check03.sh` — 4, first at line 2: `development/implementation/sprint-status.yaml`\n", "one line per code file: count and first reference")
	require.NotContains(t, d, "check03.sh:", "never a line per reference")
	require.Contains(t, d, "more file(s) (in `_apex/state/lift-disposition.json`)")
	require.Contains(t, d, "more candidate(s), numbered on from", "the candidates are cut last, and said where they are")
	require.Less(t, strings.Index(d, "## Code and CI that still read removed paths"), strings.Index(d, "## ADRs"), "what can break comes first")

	// The report: whole, ignored, not in the lift commit.
	require.Equal(t, "_apex/state/lift-disposition.json", res.Report)
	var report liftReport
	data, err := os.ReadFile(filepath.Join(root, "_apex", "state", LiftReport))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &report))
	require.Len(t, report.DanglingRefs, len(res.DanglingRefs))
	require.Len(t, report.IDsLeft, len(res.IDsLeft))
	require.Len(t, report.Candidates, len(res.Candidates))
	require.Empty(t, run(t, root, "ls-files", "--", "_apex/state/lift-disposition.json", "development/lift-disposition.json"))
	require.Equal(t, "_apex/state/lift-disposition.json", run(t, root, "check-ignore", "_apex/state/lift-disposition.json"))
	require.Empty(t, run(t, root, "status", "--porcelain"), "the tree is clean after the lift")
	p, err := Open(root)
	require.NoError(t, err)
	got := findingMessages(p.Check())
	require.Contains(t, got, "development/lift-disposition.md: the lift's disposition list awaits review")
	require.NotContains(t, got, "lift-disposition.json", "nothing to delete")
}

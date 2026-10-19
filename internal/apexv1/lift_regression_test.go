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
		got, note, fresh := judgeLiftRegression(c.before, c.after, "", "")
		require.Empty(t, fresh, c.name)
		require.Equal(t, c.want, got, c.name)
		require.NotEmpty(t, note, c.name)
	}
}

// R4, from atomix on rc.3: gotest red at the tag (22 environmental
// failures) and red after the lift with 25 — three tests reading the
// deleted sprint-status.yaml — was called pre-existing. Both red, the
// failing tests are compared by name; with no names, by output hash.
func TestFailingTests_Runners(t *testing.T) {
	for _, c := range []struct {
		name, out string
		want      []string
	}{
		{
			// go test prints a bare FAIL line before the package's FAIL line.
			"go test", "=== RUN   TestA\n--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/the_real_pack_passes (0.01s)\nFAIL\n" +
				"FAIL\tgithub.com/x/alert\t0.123s\nFAIL\tgithub.com/x/cmd [build failed]\nok  \tgithub.com/x/ok\t0.1s\n",
			[]string{"TestA", "TestA/the_real_pack_passes", "github.com/x/alert", "github.com/x/cmd [build failed]"},
		},
		{
			"pytest", "tests/test_a.py F.\n=== short test summary info ===\nFAILED tests/test_a.py::test_b - AssertionError: 1 != 2\nERROR tests/test_c.py - ImportError\n",
			[]string{"tests/test_a.py::test_b", "tests/test_c.py"},
		},
		{"cargo", "running 2 tests\ntest tests::ok ... ok\ntest tests::bad ... FAILED\n", []string{"tests::bad"}},
		{"jest", "\x1b[31mFAIL\x1b[39m src/a.test.js\n  ✓ works (3 ms)\n  ✕ renders the list (12 ms)\n", []string{"src/a.test.js", "renders the list"}},
		{"unparsable", "make: *** [lint] Error 1\n", nil},
	} {
		require.Equal(t, c.want, failingTests(c.out), c.name)
	}
}

func TestJudgeLiftRegression_RedAtTagComparesFailures(t *testing.T) {
	r := func(sha string) *CheckResult { return &CheckResult{Exit: 1, Log: "x.log", OutputSHA256: sha} }
	tagLog := "--- FAIL: TestEnvA (0.01s)\n--- FAIL: TestEnvB (0.02s)\nFAIL\tgithub.com/x/atomix\t3.1s\n"
	// Different timings, same failures: pre-existing.
	v, note, fresh := judgeLiftRegression(r("a"), r("b"), tagLog, "--- FAIL: TestEnvB (0.05s)\n--- FAIL: TestEnvA (0.03s)\nFAIL\tgithub.com/x/atomix\t2.9s\n")
	require.Equal(t, LiftRegressionRedAtTag, v)
	require.Contains(t, note, "pre-existing")
	require.Empty(t, fresh)
	// New failures: the lift's.
	v, note, fresh = judgeLiftRegression(r("a"), r("b"), tagLog, tagLog+"--- FAIL: TestKnownUnwiredList (0.00s)\n--- FAIL: TestAlertPack/the_real_pack_passes (0.00s)\n")
	require.Equal(t, LiftRegressionRedAtTagNewFailures, v)
	require.Equal(t, []string{"TestKnownUnwiredList", "TestAlertPack/the_real_pack_passes"}, fresh)
	require.Contains(t, note, "2 failing test(s) the run at apex-v0-final did not have: TestKnownUnwiredList, TestAlertPack/the_real_pack_passes")
	require.NotContains(t, note, "pre-existing failure")
	// No names to read: the same output is the tag's red; a changed one
	// is never called pre-existing.
	v, note, _ = judgeLiftRegression(r("a"), r("a"), "lint: 3 issues\n", "lint: 3 issues\n")
	require.Equal(t, LiftRegressionRedAtTag, v)
	require.Contains(t, note, "pre-existing")
	v, note, fresh = judgeLiftRegression(r("a"), r("b"), "lint: 3 issues\n", "lint: 5 issues\n")
	require.Equal(t, LiftRegressionRedAtTag, v)
	require.Contains(t, note, "red at the tag; the output changed after the lift")
	require.NotContains(t, note, "pre-existing")
	require.Empty(t, fresh)
}

// R24: for the lift's verdict a run that exits 0 but names failing tests
// is red, by those names, at either point.
func TestJudgeLiftRegression_ExitZeroNamingFailures(t *testing.T) {
	r := func(sha string) *CheckResult { return &CheckResult{Exit: 0, Log: sha + ".log", OutputSHA256: sha} }
	failing := "--- FAIL: TestA (0.00s)\n--- FAIL: TestB (0.01s)\nok  \tgithub.com/x/y\t0.1s\n"
	v, note, fresh := judgeLiftRegression(r("t"), r("a"), "ok  \tgithub.com/x/y\t0.1s\n", failing)
	require.Equal(t, LiftRegressionRedAfterLift, v)
	require.Equal(t, []string{"TestA", "TestB"}, fresh)
	require.Contains(t, note, "red at the lift commit (exit 0, but the output names 2 failing tests: TestA, TestB), green at apex-v0-final")

	v, note, fresh = judgeLiftRegression(r("t"), r("a"), failing, failing)
	require.Equal(t, LiftRegressionRedAtTag, v, "the same names at both points: red at both")
	require.Empty(t, fresh)
	require.Contains(t, note, "red at the lift commit (exit 0, but the output names 2 failing tests) and at apex-v0-final (exit 0, but the output names 2 failing tests), failing as it did there")

	v, note, fresh = judgeLiftRegression(r("t"), r("a"), "--- FAIL: TestA (0.00s)\n", failing)
	require.Equal(t, LiftRegressionRedAtTagNewFailures, v)
	require.Equal(t, []string{"TestB"}, fresh)
	require.Contains(t, note, "(exit 0, but the output names 1 failing test)")

	v, note, _ = judgeLiftRegression(r("t"), &CheckResult{Exit: 1, Log: "a.log"}, failing, "--- FAIL: TestA (0.00s)\n--- FAIL: TestB (0.01s)\n")
	require.Equal(t, LiftRegressionRedAtTag, v, "exit 0 naming them at the tag, exit 1 naming them after: red at both")
	require.Contains(t, note, "red at the lift commit (exit 1) and at apex-v0-final (exit 0, but the output names 2 failing tests)")

	v, note, _ = judgeLiftRegression(r("t"), r("a"), failing, "ok\n")
	require.Equal(t, LiftRegressionGreen, v)
	require.Contains(t, note, "red at apex-v0-final, exit 0, but the output names 2 failing tests")

	v, note, _ = judgeLiftRegression(nil, r("a"), "", failing)
	require.Equal(t, LiftRegressionNoBaseline, v)
	require.Contains(t, note, "red at the lift commit (exit 0, but the output names 2 failing tests), with no run")
}

// R29, cortex on rc.6c: red-after-lift with exit 1 and seven failing
// tests in the output, and regression_new_failures null — the names were
// read only for an exit-0 run. They are the lift's whatever the exit.
func TestJudgeLiftRegression_NamesWhateverTheExit(t *testing.T) {
	tag := &CheckResult{Exit: 0, Log: "t.log"}
	after := &CheckResult{Exit: 1, Log: "a.log"}
	out := "--- FAIL: TestFactCheck_LiveTree (0.00s)\nFAIL\tgithub.com/x/factcheck\t0.1s\nok  \tgithub.com/x/other\t0.2s\n"
	v, note, fresh := judgeLiftRegression(tag, after, "ok  \tgithub.com/x/factcheck\t0.1s\n", out)
	require.Equal(t, LiftRegressionRedAfterLift, v)
	require.Equal(t, []string{"TestFactCheck_LiveTree", "github.com/x/factcheck"}, fresh)
	require.Contains(t, note, "LIFT FAILURE: red at the lift commit (exit 1: TestFactCheck_LiveTree, github.com/x/factcheck), green at apex-v0-final")

	many := ""
	for i := range 60 {
		many += fmt.Sprintf("--- FAIL: TestN%02d (0.00s)\n", i)
	}
	_, note, fresh = judgeLiftRegression(tag, after, "", many)
	require.Len(t, fresh, liftNewFailuresShown, "the cap holds")
	require.Contains(t, note, "(60 in all, in regression_new_failures)")

	v, _, fresh = judgeLiftRegression(tag, after, "", "make: *** [test] Error 1\n")
	require.Equal(t, LiftRegressionRedAfterLift, v)
	require.Empty(t, fresh, "no names to read: none")
}

// R30: the lift's comparison runs split a ( … ) block into its lines as
// commands of the && chain; a block splitSubshell refuses stays whole.
func TestLiftParts(t *testing.T) {
	cmds := func(ps []PlannedCheck) (out []string) {
		for _, p := range ps {
			out = append(out, p.Cmd)
		}
		return out
	}
	ps := liftParts("( true\nfalse\ntrue\nfalse )")
	require.Equal(t, []string{"true", "false", "true", "false"}, cmds(ps))
	for _, p := range ps {
		require.Equal(t, "[ape] split into 4 commands: the lift runs each line of the ( … ) block on its own, past a red one, and judges each in regression_commands", p.LogNote)
	}
	ps = liftParts("make lint && ( go test ./...\ngo vet ./... )")
	require.Equal(t, []string{"make lint", "go test ./...", "go vet ./..."}, cmds(ps))
	require.Empty(t, ps[0].LogNote)
	require.Equal(t, "[ape] split into 2 commands: the lift runs each line of the ( … ) block on its own, past a red one, and judges each in regression_commands", ps[1].LogNote)
	for _, c := range []string{"( cd x\nmake )", "( true && false )", "make test"} {
		ps = liftParts(c)
		require.Equal(t, []string{c}, cmds(ps), c)
		require.Empty(t, ps[0].LogNote, c)
	}
}

func liftWithGates(t *testing.T, gates string) string {
	t.Helper()
	return liftWithGatesAt(t, liftFixture(t, false), gates)
}

func liftWithGatesAt(t *testing.T, root, gates string) string {
	t.Helper()
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
	t.Run("red at the tag, new failures after the lift", func(t *testing.T) {
		// atomix's shape: environmental failures at the tag, plus the
		// tests that read the v0 corpus the lift deletes.
		root := liftFixture(t, false)
		script := "#!/bin/sh\necho '--- FAIL: TestNeedsPostgres (0.01s)'\n" +
			"test -f development/team-memory.md || echo '--- FAIL: TestKnownUnwiredList (0.00s)'\n" +
			"echo 'FAIL\tgithub.com/x/atomix\t0.5s'\nexit 1\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "gotest.sh"), []byte(script), 0o755))
		run(t, root, "add", "gotest.sh")
		run(t, root, "commit", "-q", "-m", "gotest")
		root = liftWithGatesAt(t, root, "sh ./gotest.sh")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, LiftRegressionRedAtTagNewFailures, res.RegressionVerdict)
		require.Equal(t, []string{"TestKnownUnwiredList"}, res.RegressionNewFailures)
		require.Contains(t, res.RegressionNote, "LIFT FAILURE")
	})
	t.Run("an && chain runs command by command", func(t *testing.T) {
		// R12, tenax on mmq4: `make lint && make test`, lint red at the
		// tag and after the lift, so make test ran at neither point —
		// and it was green at the tag and red after, the lift's.
		root := liftFixture(t, false)
		require.NoError(t, os.WriteFile(filepath.Join(root, "lint.sh"), []byte("#!/bin/sh\necho 'x.go:1: unused'\nexit 1\n"), 0o755))
		run(t, root, "add", "lint.sh")
		run(t, root, "commit", "-q", "-m", "lint")
		root = liftWithGatesAt(t, root, "sh ./lint.sh\ntest -f development/team-memory.md\ntrue")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, "sh ./lint.sh && test -f development/team-memory.md && true", res.Gates.Regression)
		require.Len(t, res.RegressionCommands, 3)
		got := make([]string, 0, len(res.RegressionCommands))
		for _, c := range res.RegressionCommands {
			got = append(got, c.Cmd+" "+c.Verdict)
			require.NotNil(t, c.RegressionAtTag, c.Cmd)
			require.NotEmpty(t, c.Note, c.Cmd)
		}
		require.Equal(t, []string{
			"sh ./lint.sh " + LiftRegressionRedAtTag,
			"test -f development/team-memory.md " + LiftRegressionRedAfterLift,
			"true " + LiftRegressionGreen,
		}, got, "each command ran at both points, past the red lint")
		require.Equal(t, LiftRegressionRedAfterLift, res.RegressionVerdict, "the worst verdict: the lift-caused red behind the lint")
		require.Equal(t, "test -f development/team-memory.md", res.Regression.Cmd)
		require.Equal(t, 0, res.RegressionAtTag.Exit)
		require.Contains(t, res.RegressionNote, "command 2 of 3, `test -f development/team-memory.md`")
		require.Contains(t, res.RegressionNote, "LIFT FAILURE")
		require.Contains(t, res.RegressionNote, "1 `sh ./lint.sh` red-at-tag, 3 `true` green")
		require.Equal(t, "_apex/state/logs/lift/regression-2.log", res.RegressionCommands[1].Regression.Log)
		require.Equal(t, "_apex/state/logs/lift/regression-2-at-tag.log", res.RegressionCommands[1].RegressionAtTag.Log)
		require.FileExists(t, filepath.Join(root, "_apex", "state", "logs", "lift", "regression-1-at-tag.log"))
		data, err := json.Marshal(res)
		require.NoError(t, err)
		require.Contains(t, string(data), `"regression_commands":[{"cmd":"sh ./lint.sh","verdict":"red-at-tag"`)
	})
	t.Run("a ( … ) block runs line by line", func(t *testing.T) {
		// R30, cortex on rc.6c: its block's lines ran joined by &&, so
		// once go test failed after the lift the lines after it ran at
		// the tag only. In the lift each line runs on its own, past a red
		// one. The same-line for … done makes the fence a block.
		root := liftWithGates(t, "true\nfalse\nfor f in a; do true; done\nfalse")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, "( true\nfalse\nfor f in a; do true; done\nfalse\n)", res.Gates.Regression)
		require.Len(t, res.RegressionCommands, 4)
		var cmds []string
		var tag, after []int
		for _, c := range res.RegressionCommands {
			cmds = append(cmds, c.Cmd)
			tag, after = append(tag, c.RegressionAtTag.Exit), append(after, c.Regression.Exit)
		}
		require.Equal(t, []string{"true", "false", "for f in a; do true; done", "false"}, cmds)
		require.Equal(t, []int{0, 1, 0, 1}, tag, "every line ran at the tag, past the red one")
		require.Equal(t, []int{0, 1, 0, 1}, after, "and after the lift")
		require.Equal(t, LiftRegressionRedAtTag, res.RegressionVerdict)
		require.Contains(t, res.RegressionNote, "each line of the ( … ) block ran on its own, past a red one")
		for _, log := range []string{"regression-3.log", "regression-3-at-tag.log"} {
			data, err := os.ReadFile(filepath.Join(root, "_apex", "state", "logs", "lift", log))
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(data), "[ape] split into 4 commands: the lift runs each line of the ( … ) block on its own, past a red one, and judges each in regression_commands\n"), log+": "+string(data))
		}
	})
	t.Run("a ( … ) block that does not split runs whole", func(t *testing.T) {
		root := liftWithGates(t, "cd .\nfalse\nfor f in a; do true; done")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, "( cd .\nfalse\nfor f in a; do true; done\n)", res.Gates.Regression)
		require.Empty(t, res.RegressionCommands, "one command")
		require.Equal(t, res.Gates.Regression, res.Regression.Cmd)
		data, err := os.ReadFile(filepath.Join(root, "_apex", "state", "logs", "lift", "regression.log"))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(string(data), "[ape] the ( … ) block runs as written, not split into commands: its line 1 changes shell state (cd)\n"), string(data))
	})
	t.Run("a command with no top-level && runs whole", func(t *testing.T) {
		root := liftWithGates(t, "( test -f nowhere.txt && true )")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Empty(t, res.RegressionCommands)
		require.Equal(t, "_apex/state/logs/lift/regression-at-tag.log", res.RegressionAtTag.Log)
		require.Equal(t, "_apex/state/logs/lift/regression.log", res.Regression.Log)
		require.Equal(t, LiftRegressionRedAtTag, res.RegressionVerdict)
	})
	t.Run("cannot run here", func(t *testing.T) {
		root := liftWithGates(t, "ape-no-such-tool-xyz build")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, 127, res.RegressionAtTag.Exit)
		require.Equal(t, LiftRegressionCannotRun, res.RegressionVerdict)
		require.NotContains(t, res.RegressionNote, "red at")
	})
	t.Run("a tool not found behind make", func(t *testing.T) {
		// R17: spirometer's `fvm flutter` targets, with fvm not on PATH.
		root := liftFixture(t, false)
		require.NoError(t, os.WriteFile(filepath.Join(root, "flutter.sh"), []byte("#!/bin/sh\necho 'fvm flutter test'\n"+
			"echo '/bin/bash: line 1: fvm: command not found' >&2\necho 'make: *** [Makefile:88: spiroapp-flutter-test] Error 127' >&2\nexit 2\n"), 0o755))
		run(t, root, "add", "flutter.sh")
		root = liftWithGatesAt(t, root, "sh ./flutter.sh")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, 2, res.RegressionAtTag.Exit)
		require.Equal(t, 2, res.Regression.Exit)
		require.Equal(t, LiftRegressionCannotRun, res.RegressionVerdict)
		require.Contains(t, res.RegressionNote, "command was not found and make reported Error 127")
	})
	t.Run("exit 0 with failing tests named", func(t *testing.T) {
		// R24, cortex: the ( … ) block's go test failed tests the lift
		// broke, the block exited 0 on its last line, and the lift said
		// green.
		root := liftFixture(t, false)
		script := "#!/bin/sh\ntest -f development/team-memory.md || { echo '--- FAIL: TestPinAgreement_LiveTree (0.00s)'; echo 'FAIL\tgithub.com/x/pinlint\t0.1s'; }\n" +
			"echo 'ok  \tgithub.com/x/other\t0.2s'\nexit 0\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "gotest.sh"), []byte(script), 0o755))
		run(t, root, "add", "gotest.sh")
		root = liftWithGatesAt(t, root, "sh ./gotest.sh")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, 0, res.RegressionAtTag.Exit)
		require.Equal(t, 0, res.Regression.Exit, "the exit is recorded as it was")
		require.Equal(t, LiftRegressionRedAfterLift, res.RegressionVerdict)
		require.Equal(t, []string{"TestPinAgreement_LiveTree", "github.com/x/pinlint"}, res.RegressionNewFailures)
		require.Contains(t, res.RegressionNote, "LIFT FAILURE: red at the lift commit (exit 0, but the output names 2 failing tests: TestPinAgreement_LiveTree, github.com/x/pinlint), green at apex-v0-final")
	})
	t.Run("exit 1 with failing tests named", func(t *testing.T) {
		// R29, cortex on rc.6c: the names reach regression_new_failures
		// when the run exits non-zero too.
		root := liftFixture(t, false)
		script := "#!/bin/sh\ntest -f development/team-memory.md && exit 0\n" +
			"echo '--- FAIL: TestPinAgreement_LiveTree (0.00s)'\necho '--- FAIL: TestLedgerLifecycle_LiveTree (0.00s)'\nexit 1\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "gotest.sh"), []byte(script), 0o755))
		run(t, root, "add", "gotest.sh")
		root = liftWithGatesAt(t, root, "sh ./gotest.sh")
		res, err := Lift(ctx(), root, LiftOptions{})
		require.NoError(t, err)
		require.Equal(t, 0, res.RegressionAtTag.Exit)
		require.Equal(t, 1, res.Regression.Exit)
		require.Equal(t, LiftRegressionRedAfterLift, res.RegressionVerdict)
		require.Equal(t, []string{"TestPinAgreement_LiveTree", "TestLedgerLifecycle_LiveTree"}, res.RegressionNewFailures)
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
	// 200 references in 85 more code files, and 30 rule candidates.
	for i := range 85 {
		var sh strings.Builder
		sh.WriteString("#!/bin/sh\n")
		for j := range 1 + i%4 {
			fmt.Fprintf(&sh, "# was: development/implementation/sprint-status.yaml (%d)\n", j)
		}
		files[fmt.Sprintf("tools/verify/check%02d.sh", i)] = sh.String()
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	var rules strings.Builder
	for i := range 30 {
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

	require.GreaterOrEqual(t, len(res.Candidates), 30)

	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	require.LessOrEqual(t, len(disp), liftDispositionCap, "summaries, not lists")
	d := string(disp)
	require.Contains(t, d, "- `development/governance/evidence/ratchet.md` — 3000\n")
	require.Contains(t, d, "- `tools/verify/check03.sh` — 4, first at line 2: `development/implementation/sprint-status.yaml`\n", "one line per code file: count and first reference")
	require.NotContains(t, d, "check03.sh:", "never a line per reference")
	require.Contains(t, d, "more file(s) (in `_apex/state/lift-disposition.json`)")
	require.Contains(t, d, "| Rule 29: never write to the shared ledger outside a reviewed change |", "the candidates are never cut to fit (R14)")
	require.NotContains(t, d, "more candidate(s)")
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

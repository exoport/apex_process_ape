package apexv1

import (
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellPipelines(t *testing.T) {
	require.Equal(t, [][][]string{
		{{"go", "test", "-v", "./..."}, {"grep", "-q", "PASS"}},
		{{"echo", "done"}},
		{{"a"}},
		{{"b"}},
		{{"c"}, {"d"}},
	}, shellPipelines(`go test -v ./... 2>&1 | grep -q PASS; echo done && a || b; c |& d`))
}

// D620: the three check shapes the v1.4 eval saw apex-change write, and the
// commands that are right.
func TestTestGrepShapes(t *testing.T) {
	const (
		exit      = "so the check's exit is the last command's"
		anchored  = "which can never match"
		unanchord = "whose test name is not anchored"
		early     = "exits 141 although the tests passed"
	)
	for cmd, want := range map[string][]string{
		// The eval's cells (ledger Q2:3, taskforge Q2:3; ledger PL:2/PL:3).
		`go test -v ./internal/ledger/ | grep -q '^--- PASS: TestRunningTotal$'`: {exit, anchored},
		`go test -v ./... -run TestImport | grep -q -- '--- PASS: TestImport'`:   {exit, unanchord},
		`go test ./... | tail -5`:                                               {exit},
		`cd app && npm test 2>&1 | grep -q "passing"`:                           {exit},
		`python -m pytest -q tests/ | tee out.txt`:                              {exit},
		`set -o pipefail; go test -v ./x | grep -q -e '--- PASS: TestX$'`:       {early, anchored},
		`set -euo pipefail; go test -v ./x | grep -q 'PASS: TestX'`:             {early, unanchord},
		`timeout 60 env CGO_ENABLED=0 go test -v ./x | grep -E 'PASS: TestX\b'`: {exit},
		// FINDING-D2-1: grep -q / head under pipefail exit 141 on a pass.
		`set -o pipefail; go test -v ./x | grep -qE -- '--- PASS: TestX '`: {early},
		`set -o pipefail; go test ./... | head -1`:                         {early},
		// Right.
		`out=$(go test -v ./x) && grep -q -- '--- PASS: TestX ' <<<"$out"`: nil,
		`go test -run '^TestX$' ./x`:                                       nil,
		`set -o pipefail; go test -v ./x | grep -- '--- PASS: TestX '`:     nil,
		`go test -v ./x 2>&1 | tee log; test ${PIPESTATUS[0]} -eq 0`:       nil,
		`grep -q 'PASS: TestX' out.txt`:                                    nil,
		`./wave.sh | grep -q wave`:                                         nil,
		`go vet ./... | grep -v vendor`:                                    nil,
		`go test ./... || echo "go test failed" | grep -q failed`:          nil,
		`set -o pipefail; cargo test 2>&1 | grep "test result: ok"`:        nil,
		`set -o pipefail; go test -v ./x | grep '--- PASS: TestX (0.00s)'`: nil,
	} {
		got := testGrepShapes(cmd)
		require.Len(t, got, len(want), "%s: %q", cmd, got)
		for i, w := range want {
			require.Contains(t, got[i], w, cmd)
		}
	}
}

// The notice reaches change check, as informational (no action), and is
// never a finding.
func TestChangeNotices_GrepTestOutput(t *testing.T) {
	p := adviseProject(t, adviseConfig, "rules\n")
	doc := strings.ReplaceAll(waveChange("add-wave"), "check: ./wave.sh", "check: go test -v ./... | grep -q '^--- PASS: TestWave$'")
	addChange(t, p, "add-wave", doc)
	commitAll(t, p, "plan")
	var ns []Diagnostic
	for _, n := range p.ChangeNotices(ctx(), "add-wave") {
		if n.Code == CodeCheckGrepTestOutput {
			ns = append(ns, n)
		}
	}
	require.Len(t, ns, 2)
	for _, n := range ns {
		require.Equal(t, SeverityNotice, n.Severity)
		require.Equal(t, ActionNone, n.Action)
		require.Equal(t, "development/changes/add-wave/change.md", n.File)
		require.Positive(t, n.Line)
		require.True(t, strings.HasPrefix(n.Message, "T1's check `go test -v ./... | grep -q '^--- PASS: TestWave$'`"), n.Message)
		require.Contains(t, n.Fix, "go test -run '^TestX$' ./pkg")
	}
	require.Empty(t, p.CheckChange("add-wave"), "a notice is not a finding")
}

// D650: no notice suggests pipefail any more (with grep -q it exits 141).
func TestTestGrepShapes_FixTextNeverSuggestsPipefail(t *testing.T) {
	for _, cmd := range []string{"go test ./... | tail -5", "go test -v ./x | grep -q 'PASS: TestX'", "set -o pipefail; go test ./... | head -1"} {
		for _, n := range testGrepShapes(cmd) {
			_, fix, _ := strings.Cut(n, " — ")
			require.NotContains(t, fix, "pipefail", cmd)
		}
	}
}

// D653: every command a notice suggests runs under ape's own shell. ape
// runs checks with `sh -c`, which is dash on Debian and Ubuntu, so a
// bash-only form (a `<<<` here-string, `[[`, `<(…)`) suggested as the fix
// would only trade one broken check for another. Each backticked command in
// a fix text must parse under /bin/sh.
func TestTestGrepShapes_SuggestedCommandsAreSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checks run under sh; no /bin/sh here")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	tick := regexp.MustCompile("`([^`]+)`")
	seen := map[string]bool{}
	for _, cmd := range []string{
		"go test ./... | tail -5",
		"go test -v ./x | grep -q '^--- PASS: TestX$'",
		"set -o pipefail; go test -v ./x | grep -q 'PASS: TestX'",
		"set -o pipefail; go test ./... | head -1",
	} {
		for _, n := range testGrepShapes(cmd) {
			_, fix, ok := strings.Cut(n, " — ")
			require.True(t, ok, n)
			for _, m := range tick.FindAllStringSubmatch(fix, -1) {
				seen[m[1]] = true
			}
		}
	}
	require.NotEmpty(t, seen)
	for snippet := range seen {
		if !strings.Contains(snippet, "go test") {
			continue // a pattern or a name (`--- PASS: TestX `), not a command
		}
		line := strings.ReplaceAll(snippet, "…", "-- '--- PASS: TestX '")
		out, err := exec.Command("sh", "-n", "-c", line).CombinedOutput()
		require.NoError(t, err, "a suggested command does not parse under /bin/sh: %s\n%s", line, out)
	}
	// The control: the here-string form D650 shipped does not.
	_, err := exec.Command("sh", "-n", "-c", `out=$(go test -v ./pkg) && grep -q x <<<"$out"`).CombinedOutput()
	if err == nil {
		t.Log("this host's /bin/sh accepts <<< (bash as sh); the control proves nothing here")
	}
}

// TestTestGrepShapes_CaptureFormSurvivesPipefail runs the capture form the
// notices suggest under `set -o pipefail` on more output than a pipe buffer
// holds: the l.96 notice fires only under pipefail, and `grep -q` there
// stops at its first match and kills printf with SIGPIPE, exit 141 on a
// pass (D653+, rc4's text). Without -q grep reads to the end.
func TestTestGrepShapes_CaptureFormSurvivesPipefail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checks run under sh; no /bin/sh here")
	}
	if err := exec.Command("sh", "-c", "set -o pipefail").Run(); err != nil {
		t.Skip("this host's /bin/sh has no pipefail")
	}
	tick := regexp.MustCompile("`([^`]+)`")
	forms := map[string]bool{}
	for _, cmd := range []string{
		"go test -v ./x | grep -q '^--- PASS: TestX$'",
		"set -o pipefail; go test -v ./x | grep -q 'PASS: TestX'",
	} {
		for _, n := range testGrepShapes(cmd) {
			for _, m := range tick.FindAllStringSubmatch(n, -1) {
				if strings.HasPrefix(m[1], "out=$(go test") {
					forms[m[1]] = true
				}
			}
		}
	}
	require.NotEmpty(t, forms)
	// seq 1 300000 is about 2 MB, far past a 64 KB pipe buffer; the match
	// sits at the start, so an early exit would leave most of it unread.
	run := func(form, pattern string) int {
		line := strings.Replace(form, "go test -v ./pkg", "seq 1 300000", 1)
		line = strings.ReplaceAll(line, "…", pattern)
		err := exec.Command("sh", "-c", "set -o pipefail; "+line).Run()
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return exit.ExitCode()
		}
		require.NoError(t, err, line)
		return 0
	}
	for form := range forms {
		assert.Equal(t, 0, run(form, "-x 1"), "a match must exit 0: %s", form)
		assert.Equal(t, 1, run(form, "-x 0"), "no match must exit 1: %s", form)
	}
	// The control: rc4's -q form exits 141 on the same match.
	assert.Equal(t, 141, run(`out=$(go test -v ./pkg) && printf '%s\n' "$out" | grep -q …`, "-x 1"),
		"grep -q under pipefail on 2 MB should die of SIGPIPE; the test proves nothing if it does not")
}

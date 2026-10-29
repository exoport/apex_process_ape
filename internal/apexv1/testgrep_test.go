package apexv1

import (
	"strings"
	"testing"

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
	)
	for cmd, want := range map[string][]string{
		// The eval's cells (ledger Q2:3, taskforge Q2:3; ledger PL:2/PL:3).
		`go test -v ./internal/ledger/ | grep -q '^--- PASS: TestRunningTotal$'`: {exit, anchored},
		`go test -v ./... -run TestImport | grep -q -- '--- PASS: TestImport'`:   {exit, unanchord},
		`go test ./... | tail -5`:                                               {exit},
		`cd app && npm test 2>&1 | grep -q "passing"`:                           {exit},
		`python -m pytest -q tests/ | tee out.txt`:                              {exit},
		`set -o pipefail; go test -v ./x | grep -q -e '--- PASS: TestX$'`:       {anchored},
		`set -euo pipefail; go test -v ./x | grep -q 'PASS: TestX'`:             {unanchord},
		`timeout 60 env CGO_ENABLED=0 go test -v ./x | grep -E 'PASS: TestX\b'`: {exit},
		// Right.
		`go test -run '^TestX$' ./x`:                                          nil,
		`set -o pipefail; go test -v ./x | grep -q -- '--- PASS: TestX '`:     nil,
		`go test -v ./x 2>&1 | tee log; test ${PIPESTATUS[0]} -eq 0`:          nil,
		`grep -q 'PASS: TestX' out.txt`:                                       nil,
		`./wave.sh | grep -q wave`:                                            nil,
		`go vet ./... | grep -v vendor`:                                       nil,
		`go test ./... || echo "go test failed" | grep -q failed`:             nil,
		`set -o pipefail; cargo test 2>&1 | grep -q "test result: ok"`:        nil,
		`set -o pipefail; go test -v ./x | grep -q '--- PASS: TestX (0.00s)'`: nil,
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

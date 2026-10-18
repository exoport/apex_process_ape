package apexv1

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// liftWithCandidates lifts a fixture whose CLAUDE.md lists n binding-rule
// candidates of about width bytes each, beside 60 code files still
// reading a removed path, and returns the disposition file and how many
// candidates the lift found (the fixture's own, and these).
func liftWithCandidates(t *testing.T, n, width int) (disposition string, candidates int) {
	t.Helper()
	root := liftFixture(t, false)
	files := map[string]string{}
	for i := range 60 {
		files[fmt.Sprintf("tools/verify/check%02d.sh", i)] = "#!/bin/sh\ncat development/implementation/sprint-status.yaml\n"
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	var rules strings.Builder
	for i := range n {
		rule := fmt.Sprintf("Rule %d: never write to the shared ledger outside a reviewed change", i)
		rule += strings.Repeat(" and say why", max(0, width-len(rule))/len(" and say why"))
		fmt.Fprintf(&rules, "- %s.\n", rule)
	}
	files["CLAUDE.md"] = string(claude) + "\n## More rules\n\n" + rules.String()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "rules")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)
	res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(res.Candidates), n)
	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	return string(disp), len(res.Candidates)
}

// R14, from cortex on rc.4: 78 candidates (about 19 KB of table), of
// which the file listed 10 — the other 68 only in the checkout-local
// JSON, out of reach of the fresh reviewer and of apex-plan anywhere else.
// Every candidate is in the file now: the summaries go down to counts
// first, and the file grows past 16 KB for the candidates, within 48 KB.
func TestLift_DispositionKeepsEveryCandidate(t *testing.T) {
	d, n := liftWithCandidates(t, 78, 220)
	require.Greater(t, len(d), liftDispositionCap, "the candidates alone overflow 16 KB")
	require.LessOrEqual(t, len(d), liftDispositionCeiling)
	for i := range 78 {
		require.Contains(t, d, fmt.Sprintf("| Rule %d: never write", i))
	}
	require.Contains(t, d, fmt.Sprintf("| %d | ", n), "the last candidate too")
	require.NotContains(t, d, "more candidate(s)")
	require.Contains(t, d, "- … and 61 more file(s) (in `_apex/state/lift-disposition.json`)", "the summaries are cut to counts first")
	require.NotContains(t, d, "check00.sh")
}

// R14: past the 48 KB ceiling, the candidates are cut, the file saying
// how many and where they are.
func TestLift_DispositionCutsCandidatesAtTheCeiling(t *testing.T) {
	d, n := liftWithCandidates(t, 400, 220)
	require.LessOrEqual(t, len(d), liftDispositionCeiling)
	require.Greater(t, len(d), liftDispositionCeiling-1024, "it fills the ceiling before it cuts")
	m := regexp.MustCompile(`… and (\d+) more candidate\(s\), numbered on from (\d+), past this file's 48 KB ceiling: in ` + "`_apex/state/lift-disposition.json`" + ` \(rule_candidates\), in the checkout that lifted`).FindStringSubmatch(d)
	require.NotNil(t, m, "the cut says how many and where")
	var more, from int
	_, err := fmt.Sscan(m[1]+" "+m[2], &more, &from)
	require.NoError(t, err)
	require.Contains(t, d, fmt.Sprintf("| %d | ", from-1), "numbered up to the cut")
	require.NotContains(t, d, fmt.Sprintf("| %d | ", from))
	require.Equal(t, from-1+more, n, "every candidate is in the file or counted")
}

// The ceiling is the caps' hard ceiling on AGENTS.md (caps.go).
func TestLiftDispositionCeiling_IsTheCapsCeiling(t *testing.T) {
	for _, c := range capDefs {
		if c.key == CapAgentsMD {
			require.Equal(t, liftDispositionCeiling, c.ceiling)
		}
	}
}

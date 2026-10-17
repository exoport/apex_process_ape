package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Backlog item 9: the spec block a MODIFIED or REMOVED delta replaces is
// fingerprinted at start; archive refuses (exit 3, nothing written) when
// another change has changed it since — the cross-branch overwrite.
func TestArchive_RefusesWhenTheBaseRequirementChanged(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "rework-hello", changeDoc("rework-hello", "greeting",
		"### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hello, kindly.\n"+
			"#### Scenario: plain run\n- check: ./greet.sh | grep -q Hello\n",
		"- T1 hello — regression: ./greet.sh | grep -q Hello\n"))
	commitAll(t, p, "plan")
	_, err := p.Start(ctx(), "rework-hello", RunOptions{})
	require.NoError(t, err)
	st := state(t, p, "rework-hello")
	require.Contains(t, st.BaseFingerprints, "greeting/Say hello")
	require.NotContains(t, st.BaseFingerprints, "greeting/Say name")

	_, err = p.Verify(ctx(), "rework-hello", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/rework-hello/review.md", "ok\n")
	_, err = p.Review(ctx(), "rework-hello", "pass", "development/changes/rework-hello/review.md")
	require.NoError(t, err)

	// Another change, merged from another branch, reworded the requirement.
	other := strings.Replace(greetingSpec, "The system SHALL print Hello.", "The system SHALL print Hello, politely.", 1)
	write(t, p, "development/specs/greeting/spec.md", other)
	commitAll(t, p, "archive: polite-hello (from another branch)")

	_, err = p.Archive(ctx(), "rework-hello", true, RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, "archive.base-changed", ce.Code)
	require.Equal(t, []string{`[greeting] Say hello changed in the spec since this change started`}, ce.Conflicts)
	require.Equal(t, other, read(t, p, "development/specs/greeting/spec.md"), "nothing written")

	// Re-start re-bases the change on the current spec (recorded); then the
	// usual path archives it.
	res, err := p.Start(ctx(), "rework-hello", RunOptions{})
	require.NoError(t, err)
	require.Empty(t, res.Baseline, "the plan is unchanged, so nothing is re-baselined")
	st = state(t, p, "rework-hello")
	require.Len(t, st.Replans, 1)
	_, err = p.Start(ctx(), "rework-hello", RunOptions{})
	require.ErrorContains(t, err, "nothing to re-baseline")
	_, err = p.Verify(ctx(), "rework-hello", RunOptions{})
	require.NoError(t, err)
	_, err = p.Review(ctx(), "rework-hello", "pass", "development/changes/rework-hello/review.md")
	require.NoError(t, err)
	_, err = p.Archive(ctx(), "rework-hello", true, RunOptions{})
	require.NoError(t, err)
	require.Contains(t, read(t, p, "development/specs/greeting/spec.md"), "kindly")
}

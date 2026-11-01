package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// commitOnTop commits path=content on top of HEAD (detached) with msg,
// returning the new HEAD: a merge group's head.
func commitOnTop(t *testing.T, p *Project, path, content, msg string) string {
	t.Helper()
	write(t, p, path, content)
	run(t, p.Root, "add", path)
	run(t, p.Root, "commit", "-q", "-m", msg)
	return run(t, p.Root, "rev-parse", "HEAD")
}

// v1.5 stage 1a step 10: the merge queue's check reads what the group
// carries from its trailers and runs what it needs.
func TestCICheck_ChangeGroup(t *testing.T) {
	p, _ := coordinatedReviewedWave(t)
	useIntegrator(t, &fakeIntegrator{states: map[int]PRState{}})
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	in := res.Integration
	run(t, p.Root, "switch", "-q", "--detach", in.Commit)

	ci, err := p.CICheck(ctx(), in.BaseSHA, in.Commit, RunOptions{})
	require.NoError(t, err, "%+v", ci)
	require.True(t, ci.Green)
	require.Equal(t, []string{"add-wave"}, ci.Changes)
	require.Positive(t, ci.SpecChecks)
	require.Empty(t, ci.Closes)

	// The same group with a broken scenario on top is red.
	head := commitOnTop(t, p, "greet.sh", "#!/bin/sh\necho Hi $1\n", "feat: break\n\nChange: add-wave")
	ci, err = p.CICheck(ctx(), in.BaseSHA, head, RunOptions{})
	require.Equal(t, CodeCIRed, failCode(t, err))
	require.False(t, ci.Green)
	require.Contains(t, strings.Join(ci.Red, "\n"), "scenario: greeting/")
}

func TestCICheck_OutsideFlowAndRecords(t *testing.T) {
	p := fixture(t, baseFiles())
	base := run(t, p.Root, "rev-parse", "HEAD")
	run(t, p.Root, "switch", "-q", "--detach")
	head := commitOnTop(t, p, "notes.txt", "x\n", "docs: a note")
	_, err := p.CICheck(ctx(), base, head, RunOptions{})
	require.Equal(t, CodeCIOutsideFlow, failCode(t, err))

	head = commitOnTop(t, p, "notes.txt", "y\n", "apex: M2 proposed\n\nMilestone: M2")
	ci, err := p.CICheck(ctx(), base, head, RunOptions{})
	require.Equal(t, []string{"apex: M2 proposed"}, ci.Records)
	require.Equal(t, len(ci.Findings) == 0, ci.Green, "records run the project check")
	if ci.Green {
		require.NoError(t, err)
	}
	require.Zero(t, ci.SpecChecks, "a record runs no scenario")
}

// A close: the milestone gate with its full run; a person's accepted red
// passes only on the red set accepted (OQ26).
func TestCICheck_CloseAcceptedRed(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	files["gate.sh"] = "#!/bin/sh\nexit 1\n"
	p := fixture(t, files)
	withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	useIntegrator(t, &fakeIntegrator{states: map[int]PRState{}})
	g, err := p.CloseMilestoneAcceptRed(ctx(), "M1", "the e2e rig is down", "ann", RunOptions{})
	require.NoError(t, err)
	closeHead := g.Integration.Commit
	base := run(t, p.Root, "rev-parse", "refs/remotes/origin/main")
	run(t, p.Root, "switch", "-q", "--detach", closeHead)

	ci, err := p.CICheck(ctx(), base, closeHead, RunOptions{})
	require.NoError(t, err, "%+v", ci.Red)
	require.Equal(t, []string{"M1"}, ci.Closes)
	require.True(t, ci.Green, "red, but the red set the person accepted")

	head := commitOnTop(t, p, "greet.sh", "#!/bin/sh\necho Hi $1\n", "fix: oops")
	ci, err = p.CICheck(ctx(), base, head, RunOptions{})
	require.Equal(t, CodeCIRed, failCode(t, err))
	require.Contains(t, strings.Join(ci.Red, "\n"), "accepted red only on gate command")
}

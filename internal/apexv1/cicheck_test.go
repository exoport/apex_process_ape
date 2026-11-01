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

// D702: main's queue takes a milestone's close (its changes ride it) and
// records; a change on its own reaching main is outside the flow.
func TestCICheck_ChangeAloneOnMainIsOutsideFlow(t *testing.T) {
	p := fixture(t, baseFiles())
	base := run(t, p.Root, "rev-parse", "HEAD")
	run(t, p.Root, "switch", "-q", "--detach")
	head := commitOnTop(t, p, "wave.sh", "#!/bin/sh\necho wave\n", "feat: wave\n\nChange: add-wave")
	ci, err := p.CICheck(ctx(), base, head, RunOptions{})
	require.Equal(t, CodeCIOutsideFlow, failCode(t, err))
	require.ErrorContains(t, err, "reach main only through the close PR")
	require.Equal(t, []string{"add-wave"}, ci.Changes)
	require.Empty(t, ci.Gates, "nothing ran")
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
	require.Empty(t, ci.Gates, "a record runs no gate")
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

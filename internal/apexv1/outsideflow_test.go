package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func outsideSubjects(r *StatusReport) []string {
	out := make([]string, 0, len(r.OutsideFlow))
	for _, o := range r.OutsideFlow {
		out = append(out, o.Subject)
	}
	return out
}

// H7: base-branch commits that touch code with no Change: trailer and no
// apex: subject are listed, since the v1 epoch and then since the last
// archive squash; planning-only commits, ape's records and attributed
// commits are not.
func TestStatus_OutsideFlow(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	commitAll(t, p, "review: add-wave")
	require.Empty(t, p.Status().OutsideFlow, "no v1 epoch commit: nothing is judged")

	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hello $1 !\n")
	commitAll(t, p, "hotfix: before the epoch") // exempt (D47 Q5)
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hello $1\n")
	commitAll(t, p, "hotfix: greet")
	write(t, p, "development/project.md", projectMD+"\nMore.\n")
	commitAll(t, p, "docs: planning only")
	write(t, p, "notes.txt", "x\n")
	commitAll(t, p, "apex: ape's own record")
	write(t, p, "notes.txt", "y\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "fix: attributed\n\nChange: add-wave")

	r := p.Status()
	require.Equal(t, []string{"hotfix: greet"}, outsideSubjects(r))
	require.Equal(t, []string{"greet.sh"}, r.OutsideFlow[0].Files)

	run(t, p.Root, "switch", "-q", "change/add-wave")
	require.Equal(t, []string{"hotfix: greet"}, outsideSubjects(p.Status()), "on a change branch: its base")

	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Empty(t, p.Status().OutsideFlow, "the archive squash is the new lower bound")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hi $1\n")
	commitAll(t, p, "quick: tweak")
	require.Equal(t, []string{"quick: tweak"}, outsideSubjects(p.Status()))
}

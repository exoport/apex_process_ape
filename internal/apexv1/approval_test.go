package apexv1

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// H2: an approval is ape's record, not the Status line. A milestone that
// says approved with no record — a hand edit, or one approved before ape
// recorded approvals — starts no change and is reported by project check,
// until a person approves it through ape.
func TestApproval_StatusLineAloneIsNotTrusted(t *testing.T) {
	files := baseFiles()
	files["_apex/state/milestones.json"] = "{}\n" // no approval record
	p := fixture(t, files)
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", "none — no spec change", "- T1 wave — check: true"))
	commitAll(t, p, "plan")

	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, CodeStatusUnrecorded, fs[0].Code)
	require.Equal(t, ActionPerson, ActionFor(fs[0].Code))
	require.Equal(t, "a person approves it: `ape milestone approve M1 --by <who>`", fs[0].Fix)

	head := HeadSHA(ctx(), p.Root)
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeStatusUnrecorded, fe.Code)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root), "a refused start leaves no branch")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "ape has no record of a person approving it")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "ape has no record of a person approving it")

	// A person re-approves: recorded, the Status line kept.
	before := p.roadmap().Find("M1").StatusText
	_, err = p.ApproveMilestone(ctx(), "M1", "ann")
	require.NoError(t, err)
	require.Equal(t, before, p.roadmap().Find("M1").StatusText)
	require.Equal(t, "apex: M1 re-approved", subjects(t, p)[0])
	require.Empty(t, p.Check())
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
}

// The gate is frozen at approval: its command and the files it names.
func TestApproval_GateChange(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Gate: ./gate.sh", "Gate: sh scripts/e2e.sh --suite=checks/m1.txt m1", 1)
	files["scripts/e2e.sh"] = "#!/bin/sh\nexit 1\n"
	files["checks/m1.txt"] = "greet\n"
	p := fixture(t, files)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	a := recs["M1"].Approval
	require.Equal(t, []string{"checks/m1.txt", "scripts/e2e.sh"}, sortedKeys(keysOf(a.GateFiles)), "sh and m1 name no file")
	require.Empty(t, p.Check())

	// A gate script weakened after approval.
	write(t, p, "scripts/e2e.sh", "#!/bin/sh\nexit 0\n")
	commitAll(t, p, "weaken")
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, CodeGateChanged, fs[0].Code)
	require.Contains(t, fs[0].Message, "scripts/e2e.sh changed")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "M1's gate changed since eval approved it: scripts/e2e.sh changed")

	// The Gate: line edited.
	write(t, p, "scripts/e2e.sh", "#!/bin/sh\nexit 1\n")
	write(t, p, "development/roadmap.md", strings.Replace(read(t, p, "development/roadmap.md"), "m1.txt m1", "m1.txt m0", 1))
	commitAll(t, p, "edit gate")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "the command was `sh scripts/e2e.sh --suite=checks/m1.txt m1`, is `sh scripts/e2e.sh --suite=checks/m1.txt m0`")

	// Start does not judge the gate: the approval record is enough.
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", "none — no spec change", "- T1 wave — check: true"))
	commitAll(t, p, "plan")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
}

// A done Status ape did not write is reported; one ape closed is not.
func TestApproval_DoneUnrecorded(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Status: approved (by eval 2026-10-15T12:00:00Z)", "Status: done (2026-10-16T00:00:00Z abcdef1)", 1)
	p := fixture(t, files)
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, CodeStatusUnrecorded, fs[0].Code)
	require.Contains(t, fs[0].Message, "M1 says done, but ape recorded no close of it")
}

func TestGateFiles(t *testing.T) {
	p := fixture(t, map[string]string{"a.sh": "a", "dir/b.yaml": "b", "c": "c"})
	got := p.gateFiles(`FOO=dir/b.yaml ./a.sh "c" --x=c -v $HOME/a.sh /etc/passwd ../a.sh *.sh dir && true; echo done | cat`)
	require.Equal(t, []string{"a.sh", "c", "dir/b.yaml"}, sortedKeys(keysOf(got)))
	require.Equal(t, gateFilesDigest(got), gateFilesDigest(p.gateFiles("./a.sh c dir/b.yaml")))
	require.NotEqual(t, gateFilesDigest(got), gateFilesDigest(p.gateFiles("./a.sh")))
}

package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pushFile commits path=content on top of the remote branch and pushes it
// there, as another member's merged work would land.
func pushFile(t *testing.T, bare, branch, path, content string) string {
	t.Helper()
	tmp := t.TempDir()
	run(t, tmp, "clone", "-q", "--branch", branch, bare, ".")
	require.NoError(t, os.WriteFile(filepath.Join(tmp, path), []byte(content), 0o644))
	run(t, tmp, "add", path)
	run(t, tmp, "commit", "-q", "-m", "elsewhere: "+path)
	run(t, tmp, "push", "-q", "origin", branch)
	return run(t, tmp, "rev-parse", "HEAD")
}

// v1.5 stage 1a step 7, reworked for ape-handled branches (D702): main is
// taken into milestone/<M> as the merge of the two, never a rebase, landed
// with the fenced fast-forward push; a conflict is laid out on
// change/take-in-<n> for apex-apply, whose committed resolution the next
// take-in lands.
func TestTakeIn(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	p := fixture(t, files)
	bare := withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)

	res, err := p.TakeIn(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, res.UpToDate, "main is already in milestone/M1")

	// Another milestone closed: main moved.
	mainTip := pushFile(t, bare, "main", "notes.txt", "from M0\n")
	baseTip := run(t, bare, "rev-parse", "milestone/M1")
	res, err = p.TakeIn(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "take-in-1", res.ID)
	require.Equal(t, res.Commit, run(t, bare, "rev-parse", "milestone/M1"), "landed on the remote")
	require.Equal(t, res.Commit, run(t, p.Root, "rev-parse", "milestone/M1"))
	parents := strings.Fields(run(t, bare, "rev-list", "--parents", "-n", "1", "milestone/M1"))
	require.Equal(t, []string{res.Commit, baseTip, mainTip}, parents, "a merge of main into the milestone, never a rebase")
	res, err = p.TakeIn(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, res.UpToDate)

	// A conflict: the milestone and main edited the same line.
	pushFile(t, bare, "milestone/M1", "greet.sh", "#!/bin/sh\necho Hi $1\n")
	pushFile(t, bare, "main", "greet.sh", "#!/bin/sh\necho Howdy $1\n")
	_, err = p.TakeIn(ctx(), "M1", RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, CodeConflicted, ce.Code)
	require.ErrorContains(t, err, "greet.sh")
	require.Equal(t, "change/take-in-2", CurrentBranch(ctx(), p.Root), "numbered from the Take-in: trailers on the branch")
	data, err := os.ReadFile(filepath.Join(p.Root, "greet.sh"))
	require.NoError(t, err)
	require.Contains(t, string(data), "<<<<<<<")
	before := run(t, bare, "rev-parse", "milestone/M1")
	_, err = p.TakeIn(ctx(), "M1", RunOptions{})
	require.ErrorAs(t, err, &ce, "unresolved: refused again")
	require.Equal(t, before, run(t, bare, "rev-parse", "milestone/M1"), "nothing landed")

	write(t, p, "greet.sh", "#!/bin/sh\necho Hi there $1\n")
	run(t, p.Root, "add", "greet.sh")
	run(t, p.Root, "commit", "-q", "--no-edit")
	resolved := run(t, p.Root, "rev-parse", "HEAD")
	res, err = p.TakeIn(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "take-in-2", res.ID)
	require.Equal(t, resolved, res.Commit, "apex-apply's resolution is what lands")
	require.Equal(t, resolved, run(t, bare, "rev-parse", "milestone/M1"))
	require.False(t, p.branchExists(ctx(), "change/take-in-2"))
	require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root))
}

// The regression runs on the take-in's merge before it lands: red, nothing
// lands and the merge is laid out for apex-apply (milestone.take-in-red);
// its committed fix is what the next take-in lands.
func TestTakeIn_RegressionRed(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig + "regression: ./reg.sh\n"
	files["reg.sh"] = "#!/bin/sh\ntest ! -f boom\n"
	p := fixture(t, files)
	bare := withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	pushFile(t, bare, "main", "boom", "main broke the milestone's regression\n")
	before := run(t, bare, "rev-parse", "milestone/M1")

	res, err := p.TakeIn(ctx(), "M1", RunOptions{})
	require.Equal(t, CodeTakeInRed, failCode(t, err))
	require.NotNil(t, res.Regression)
	require.Equal(t, before, run(t, bare, "rev-parse", "milestone/M1"), "nothing landed")
	require.Equal(t, before, run(t, p.Root, "rev-parse", "milestone/M1"), "the local branch is back")
	require.Equal(t, "change/take-in-1", CurrentBranch(ctx(), p.Root))

	run(t, p.Root, "rm", "-q", "boom")
	run(t, p.Root, "commit", "-q", "-m", "fix: the take-in passes the regression\n\nTake-in: take-in-1")
	fix := run(t, p.Root, "rev-parse", "HEAD")
	res, err = p.TakeIn(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, fix, res.Commit)
	require.Equal(t, fix, run(t, bare, "rev-parse", "milestone/M1"))
	require.Zero(t, res.Regression.Exit)
}

func TestTakeIn_UncoordinatedRefuses(t *testing.T) {
	p := fixture(t, baseFiles())
	_, err := p.TakeIn(ctx(), "M1", RunOptions{})
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
}

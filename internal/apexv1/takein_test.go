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

// v1.5 stage 1a step 7 (section 5.2): main is taken into milestone/<M> by
// a PR of their merge, merged, never rebased; a conflict is laid out on
// change/take-in-<n> for apex-apply, whose resolution the next take-in
// pushes; an ejection is redone, an attempt.
func TestTakeIn(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	p := fixture(t, files)
	bare := withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)

	res, err := p.TakeIn(ctx(), "M1")
	require.NoError(t, err)
	require.True(t, res.UpToDate, "main is already in milestone/M1")
	require.Empty(t, f.opened)

	// Another milestone closed: main moved.
	mainTip := pushFile(t, bare, "main", "notes.txt", "from M0\n")
	baseTip := run(t, bare, "rev-parse", "milestone/M1")
	res, err = p.TakeIn(ctx(), "M1")
	require.NoError(t, err)
	require.Equal(t, "take-in-1", res.ID)
	in := res.Integration
	require.Equal(t, KindTakeIn, in.Kind)
	require.Equal(t, IntegrationQueued, in.State)
	require.Equal(t, []string{"apex/take-in-1→milestone/M1"}, f.opened)
	parents := strings.Fields(run(t, bare, "rev-list", "--parents", "-n", "1", "apex/take-in-1"))
	require.Equal(t, []string{in.Commit, baseTip, mainTip}, parents, "a merge of main into the milestone, never a rebase")
	require.Equal(t, baseTip, run(t, p.Root, "rev-parse", "milestone/M1"), "the milestone moves only through its queue")

	f.states[7] = PRState{Queue: "AWAITING_CHECKS"}
	res, err = p.TakeIn(ctx(), "M1")
	require.NoError(t, err)
	require.Equal(t, IntegrationQueued, res.Integration.State)
	require.Len(t, f.opened, 1, "queued: nothing redone")

	run(t, bare, "update-ref", "refs/heads/milestone/M1", in.Commit) // the queue merged it
	f.states[7] = PRState{Merged: true, MergeCommit: in.Commit}
	res, err = p.TakeIn(ctx(), "M1")
	require.NoError(t, err)
	require.Equal(t, IntegrationMerged, res.Integration.State)
	require.Equal(t, in.Commit, run(t, p.Root, "rev-parse", "milestone/M1"))
	res, err = p.TakeIn(ctx(), "M1")
	require.NoError(t, err)
	require.True(t, res.UpToDate)

	// A conflict: the milestone and main edited the same line.
	pushFile(t, bare, "milestone/M1", "greet.sh", "#!/bin/sh\necho Hi $1\n")
	pushFile(t, bare, "main", "greet.sh", "#!/bin/sh\necho Howdy $1\n")
	f.states = map[int]PRState{}
	_, err = p.TakeIn(ctx(), "M1")
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, CodeConflicted, ce.Code)
	require.ErrorContains(t, err, "greet.sh")
	require.Equal(t, "change/take-in-2", CurrentBranch(ctx(), p.Root))
	data, err := os.ReadFile(filepath.Join(p.Root, "greet.sh"))
	require.NoError(t, err)
	require.Contains(t, string(data), "<<<<<<<")
	_, err = p.TakeIn(ctx(), "M1")
	require.ErrorAs(t, err, &ce, "unresolved: refused again")

	write(t, p, "greet.sh", "#!/bin/sh\necho Hi there $1\n")
	run(t, p.Root, "add", "greet.sh")
	run(t, p.Root, "commit", "-q", "--no-edit")
	resolved := run(t, p.Root, "rev-parse", "HEAD")
	res, err = p.TakeIn(ctx(), "M1")
	require.NoError(t, err)
	require.Equal(t, "take-in-2", res.ID)
	require.Equal(t, resolved, res.Integration.Commit, "apex-apply's resolution is what is pushed")
	require.Equal(t, resolved, run(t, bare, "rev-parse", "apex/take-in-2"))

	// Ejected: redone, an attempt.
	f.states[7] = PRState{}
	res, err = p.TakeIn(ctx(), "M1")
	require.NoError(t, err)
	require.Equal(t, 2, res.Integration.Attempts)
	require.Equal(t, "take-in-2", res.ID)
}

func TestTakeIn_UncoordinatedRefuses(t *testing.T) {
	p := fixture(t, baseFiles())
	_, err := p.TakeIn(ctx(), "M1")
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
}

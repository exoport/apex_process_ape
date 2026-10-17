package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// D52 Q18: a reviewed change waiting for a person, HEAD on main; the
// person commits the evidence on main, accepts from main, and archives
// with no conflict, no review.stale and no --allow-pending-human.
func TestAcceptOnBase_ReviewedChange(t *testing.T) {
	p := reviewedOnItsBranch(t)
	branchTip := HeadSHA(ctx(), p.Root)
	run(t, p.Root, "switch", "-q", "main")
	require.Equal(t, []string{"T2"}, p.Status().Changes[0].AwaitingHuman)

	// The evidence, committed on main: a non-apex commit, so main moved.
	write(t, p, "evidence/screen.txt", "the copy reads well\n")
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann", "evidence/screen.txt")
	require.Equal(t, "usage.evidence", usageCode(t, err), "not committed on main yet")
	commitAll(t, p, "docs: review evidence")
	evidenceCommit := HeadSHA(ctx(), p.Root)

	st, err := p.Accept(ctx(), "add-wave", "T2", "ann", "evidence/screen.txt")
	require.NoError(t, err)
	sum := sha256.Sum256([]byte("the copy reads well\n"))
	require.Equal(t, []Evidence{{Path: "evidence/screen.txt", SHA256: hex.EncodeToString(sum[:])}}, st.Accepted[0].Evidence)
	require.Empty(t, st.PendingHuman(st.Verify.PendingHuman))
	require.Equal(t, "apex: add-wave accepted T2", subjects(t, p)[0], "an apex: commit on main")
	require.Equal(t, "development/changes/add-wave/accepted.json", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))
	require.Equal(t, branchTip, run(t, p.Root, "rev-parse", ChangeBranch("add-wave")), "the change's branch is untouched")
	clean(t, p, "accept from main")
	require.Empty(t, p.Check(), "accepted.json is ape's record")
	r := p.Status()
	require.Equal(t, "add-wave", r.Changes[0].ID)
	require.Empty(t, r.Changes[0].AwaitingHuman)
	require.Empty(t, r.Changes[0].PendingHuman)

	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.False(t, res.AllowPendingHuman)
	require.Empty(t, res.PendingHuman)
	require.True(t, res.Rebased, "the evidence commit moved main: rebased, with no conflict")
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	clean(t, p, "archive")
	require.Equal(t, []string{"Add wave", "apex: add-wave accepted T2", "docs: review evidence"}, subjects(t, p)[:3])
	require.Equal(t, evidenceCommit, run(t, p.Root, "rev-parse", "HEAD~2"))

	archived, err := LoadState(p.Root + "/" + res.ArchivedTo + "/state.json")
	require.NoError(t, err)
	require.Len(t, archived.Accepted, 1)
	require.Equal(t, "T2", archived.Accepted[0].Task)
	require.Equal(t, "ann", archived.Accepted[0].By)
	require.Equal(t, "evidence/screen.txt", archived.Accepted[0].Evidence[0].Path)
	require.Equal(t, hex.EncodeToString(sum[:]), archived.Accepted[0].Evidence[0].SHA256)
	require.Empty(t, archived.Verify.PendingHuman)
	require.False(t, archived.AllowPendingHuman)
	require.NoFileExists(t, p.Root+"/"+res.ArchivedTo+"/"+acceptedFile, "folded into state.json")
	require.NoDirExists(t, p.ChangeDir("add-wave"))
	require.Empty(t, p.Check())
}

// D52 Q18: a base moved only by the acceptance (a URL as evidence) is not a
// move: the archive does not rebase the change.
func TestAcceptOnBase_RecordOnlyMoveDoesNotRebase(t *testing.T) {
	p := reviewedOnItsBranch(t)
	run(t, p.Root, "switch", "-q", "main")
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann", "https://ci.example.com/run/1")
	require.NoError(t, err)
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.False(t, res.Rebased, "only ape's record moved main")
	require.False(t, res.Reverified)
	archived, err := LoadState(p.Root + "/" + res.ArchivedTo + "/state.json")
	require.NoError(t, err)
	require.Equal(t, "https://ci.example.com/run/1", archived.Accepted[0].Evidence[0].URL)
}

// D52 Q18: evidence that exists only on the change's branch is not at the
// base's tip; with HEAD on the change's branch accept works as before.
func TestAcceptOnBase_EvidenceResolvedAtTheBase(t *testing.T) {
	p := reviewedOnItsBranch(t)
	write(t, p, "development/changes/add-wave/shot.txt", "on the branch\n")
	commitAll(t, p, "notes")
	run(t, p.Root, "switch", "-q", "main")
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann", "development/changes/add-wave/shot.txt")
	require.Equal(t, "usage.evidence", usageCode(t, err))
	require.ErrorContains(t, err, "a file committed on main")

	run(t, p.Root, "switch", "-q", ChangeBranch("add-wave"))
	st, err := p.Accept(ctx(), "add-wave", "T2", "ann", "development/changes/add-wave/shot.txt")
	require.NoError(t, err)
	require.Equal(t, "development/changes/add-wave/shot.txt", st.Accepted[0].Evidence[0].Path)
	require.Equal(t, "apex: add-wave reviewed", subjects(t, p)[0], "on the change's branch, as before")
}

// reviewedOnItsBranch is reviewedWave with its review.md committed on
// change/add-wave, as the review session leaves it.
func reviewedOnItsBranch(t *testing.T) *Project {
	t.Helper()
	p := reviewedWave(t)
	commitAll(t, p, "review: add-wave")
	clean(t, p, "the review")
	return p
}

func usageCode(t *testing.T, err error) string {
	t.Helper()
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
	return ue.Code
}

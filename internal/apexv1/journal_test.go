package apexv1

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// readyToArchive is add-wave reviewed, T2 accepted, everything committed.
func readyToArchive(t *testing.T) *Project {
	t.Helper()
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	commitAll(t, p, "review: add-wave")
	return p
}

// putJournal writes j as the journal of process pid: 0 is one that
// crashed (no process).
func putJournal(t *testing.T, p *Project, j *ArchiveJournal, pid int) {
	t.Helper()
	require.NoError(t, p.writeJournal(j))
	j.PID = pid
	data, err := json.MarshalIndent(j, "", "  ")
	require.NoError(t, err)
	write(t, p, "_apex/state/archive-journal.json", string(data))
}

func crashJournal(t *testing.T, p *Project, j *ArchiveJournal) {
	t.Helper()
	putJournal(t, p, j, 0)
}

// H28: killed between `git merge --squash` and the commit — a half squash
// staged on main, the archive folder written, HEAD on main. Status reports
// it and changes nothing; the next writing command rolls back (only the
// paths archive wrote are cleaned: a person's untracked file survives) and
// fails with archive.interrupted; archive then goes through.
func TestJournal_CrashWithAHalfSquash(t *testing.T) {
	p := readyToArchive(t)
	tip := HeadSHA(ctx(), p.Root)
	mainSHA := run(t, p.Root, "rev-parse", "main")
	run(t, p.Root, "switch", "-q", "main")
	run(t, p.Root, "merge", "--squash", "-q", "change/add-wave")
	dest := "development/archive/2026-10-16-add-wave"
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "development/archive"), 0o755))
	require.NoError(t, os.Rename(p.ChangeDir("add-wave"), filepath.Join(p.Root, dest)))
	write(t, p, "development/person-notes.md", "mine\n")
	crashJournal(t, p, &ArchiveJournal{ID: "add-wave", Base: "main", BaseSHA: mainSHA, Branch: "change/add-wave", BranchTip: tip, Step: JournalSquash, Written: []string{dest}})

	r := p.Status()
	require.NotNil(t, r.ArchiveJournal)
	require.False(t, r.ArchiveRunning)
	require.Contains(t, r.Brief(), "archive of add-wave INTERRUPTED at its squash step")
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root), "status changes nothing")

	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeArchiveInterrupted, failCode(t, err))
	require.ErrorContains(t, err, "dropped the half-made squash on main")
	require.ErrorContains(t, err, "checked out change/add-wave")
	require.Equal(t, ActionRetry, ActionFor(CodeArchiveInterrupted))
	require.Equal(t, mainSHA, run(t, p.Root, "rev-parse", "main"))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.Equal(t, tip, HeadSHA(ctx(), p.Root))
	require.NoDirExists(t, filepath.Join(p.Root, dest))
	require.FileExists(t, filepath.Join(p.Root, "development/person-notes.md"), "only what archive wrote is cleaned")
	require.Equal(t, "?? development/person-notes.md", run(t, p.Root, "status", "--porcelain"))
	require.Nil(t, p.LoadArchiveJournal())

	require.NoError(t, os.Remove(filepath.Join(p.Root, "development/person-notes.md")))
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, mainSHA, run(t, p.Root, "rev-parse", "HEAD~1"))
	require.Nil(t, p.LoadArchiveJournal(), "a finished archive leaves no journal")
}

// Killed after the squash commit and the branch deletion, before the
// journal went: the squash commit is undone, the branch restored and
// checked out, its archive ref dropped; archive again lands it once.
func TestJournal_CrashAfterTheCommit(t *testing.T) {
	p := readyToArchive(t)
	mainSHA := run(t, p.Root, "rev-parse", "main")
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	crashJournal(t, p, &ArchiveJournal{
		ID: "add-wave", Base: "main", BaseSHA: mainSHA, Branch: "change/add-wave", BranchTip: res.BranchTip, Step: JournalSquash,
		Written: []string{res.ArchivedTo},
	})

	_, err = p.NewMilestone(ctx(), "Next", "./gate.sh", nil, "")
	require.Equal(t, CodeArchiveInterrupted, failCode(t, err))
	require.ErrorContains(t, err, "undid the squash commit")
	require.ErrorContains(t, err, "restored change/add-wave")
	require.Equal(t, mainSHA, run(t, p.Root, "rev-parse", "main"))
	require.Equal(t, res.BranchTip, HeadSHA(ctx(), p.Root))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	_, err = gitRaw(ctx(), p.Root, "rev-parse", "--verify", "--quiet", "refs/apex/archive/add-wave")
	require.Error(t, err)
	clean(t, p, "a rollback")

	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, mainSHA, run(t, p.Root, "rev-parse", "HEAD~1"))
}

// Killed while the baseline ran at the base tip (HEAD detached there):
// the branch is checked out again at its recorded tip.
func TestJournal_CrashDuringTheCatchUp(t *testing.T) {
	p := readyToArchive(t)
	tip := HeadSHA(ctx(), p.Root)
	mainSHA := run(t, p.Root, "rev-parse", "main")
	run(t, p.Root, "checkout", "-q", "--detach", "main")
	crashJournal(t, p, &ArchiveJournal{ID: "add-wave", Base: "main", BaseSHA: mainSHA, Branch: "change/add-wave", BranchTip: tip, Step: JournalRebase})

	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeArchiveInterrupted, failCode(t, err))
	require.ErrorContains(t, err, "at its rebase step")
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.Equal(t, tip, HeadSHA(ctx(), p.Root))
	require.Equal(t, mainSHA, run(t, p.Root, "rev-parse", "main"))
	clean(t, p, "a rollback")
}

// A journal whose process is alive is an archive in progress, not a
// crash: nothing is rolled back.
func TestJournal_RunningIsLeftAlone(t *testing.T) {
	needSh(t)
	p := readyToArchive(t)
	sleep := exec.Command("sleep", "30")
	require.NoError(t, sleep.Start())
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	j := &ArchiveJournal{ID: "add-wave", Base: "main", BaseSHA: run(t, p.Root, "rev-parse", "main"), Branch: "change/add-wave", BranchTip: HeadSHA(ctx(), p.Root), Step: JournalRebase}
	putJournal(t, p, j, sleep.Process.Pid)
	require.True(t, p.Status().ArchiveRunning)
	require.NoError(t, p.recoverArchive(ctx()))
	require.NotNil(t, p.LoadArchiveJournal())
}

package apexv1

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Q75: one reader of both layouts — dated folders only, the newer layout
// winning for an id in both.
func TestArchivedLayout(t *testing.T) {
	got := archivedLayout(
		[]string{"2026-01-01-add-hug", "milestones", "changes", "notes", "2026-01-02-add-wave"},
		[]string{"2026-03-01-add-wave", "2026-02-01-fix-typo", "2026-1-1-short"})
	require.Equal(t, map[string]string{
		"add-hug":  "2026-01-01-add-hug",
		"add-wave": "changes/2026-03-01-add-wave",
		"fix-typo": "changes/2026-02-01-fix-typo",
	}, got)
	require.Empty(t, archivedLayout(nil, nil))
}

const awaitingTasks = "- T1 hug — check: true\n- T2 copy reads well — check: human-review\n"

// seedArchived writes an archived change's folder at rel (project-relative)
// as an ape archive left it: change.md, and a state.json recording rel.
func seedArchived(t *testing.T, p *Project, rel, id string, st *State) {
	t.Helper()
	write(t, p, rel+"/change.md", changeDoc(id, "greeting", "", awaitingTasks))
	st.ID, st.Status, st.ArchivedTo, st.BaseBranch = id, StatusArchived, rel, "main"
	require.NoError(t, SaveState(filepath.Join(p.Root, filepath.FromSlash(rel), "state.json"), st))
}

// twoLayouts is a project with two changes archived by an older ape
// (archive/<date>-<id>/) and one by this one (archive/changes/): add-hug
// awaits a person on T2 and holds the milestone's earliest regression
// baseline; add-wave's T2 was accepted with evidence inside its folder;
// fix-typo, in the newer layout, awaits a person too.
func twoLayouts(t *testing.T) *Project {
	t.Helper()
	p := fixture(t, baseFiles())
	seedArchived(t, p, "development/archive/2026-01-01-add-hug", "add-hug", &State{
		Regression: &RegressionBaseline{Cmd: "./suite.sh", SHA: "abc", At: "2000-01-01T00:00:00Z"},
	})
	write(t, p, "development/archive/2026-01-02-add-wave/assets/shot.png", "png\n")
	seedArchived(t, p, "development/archive/2026-01-02-add-wave", "add-wave", &State{
		Accepted: []Accept{{Task: "T2", By: "ann", At: "2026-01-02T00:00:00Z", Evidence: []Evidence{
			{Path: "development/archive/2026-01-02-add-wave/assets/shot.png", SHA256: strings.Repeat("a", 64)},
			{URL: "https://ci.example.com/development/archive/2026-01-02-add-wave"},
		}}},
	})
	seedArchived(t, p, "development/archive/changes/2026-02-01-fix-typo", "fix-typo", &State{
		Regression: &RegressionBaseline{Cmd: "./suite.sh", SHA: "def", At: "2001-01-01T00:00:00Z"},
	})
	write(t, p, "development/archive/milestones/M0/evidence/gate.txt", "green\n")
	commitAll(t, p, "apex: archived by ape before and after D155")
	return p
}

// readersSeeAll checks every reader of archived changes sees all three,
// wherever each lives.
func readersSeeAll(t *testing.T, p *Project, where map[string]string) {
	t.Helper()
	ids := p.ArchivedIDs()
	require.Len(t, ids, 3)
	for id, rel := range where {
		require.Equal(t, rel, p.Rel(p.archivedPath(ids[id])), id)
	}
	r := p.Status()
	require.Equal(t, 3, r.Archived, "status counts both layouts")
	archived := p.archivedAwaitingHuman()
	awaiting := make([]string, 0, len(archived))
	for i := range archived {
		awaiting = append(awaiting, archived[i].ID)
	}
	require.Equal(t, []string{"add-hug", "fix-typo"}, awaiting, "archived human-review tasks in both layouts")
	all := p.archivedChanges()
	require.Len(t, all, 3)
	for _, c := range all {
		require.NotNil(t, c.Doc, c.ID)
		require.NotNil(t, c.State, c.ID)
	}
	change, b := p.milestoneRegressionBaseline(ctx(), "M1", "./suite.sh")
	require.Equal(t, "add-hug", change, "the earliest baseline, whichever layout holds it")
	require.Equal(t, "abc", b.SHA)
	for id, rel := range where {
		_, err := p.NewChange(ctx(), id, MilestoneNone, []string{"greeting"}, nil)
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%s: %v", id, err)
		require.Equal(t, "change.id-archived", fe.Code)
		require.Contains(t, fe.Msg, rel)
		_, err = p.requireChange(ctx(), id)
		require.ErrorContains(t, err, "change "+id+" is archived ("+rel+")")
	}
	// The base branch's tree, as a change branch reads it (D51 Q17).
	onMain := p.archivedOn(ctx(), "main")
	require.Equal(t, ids, onMain)
}

// Q75 and Q76: every reader sees both layouts; archive-layout moves the
// older one in ONE commit, rewriting the recorded paths, and is then a
// no-op; --check goes from 1 to 0.
func TestArchiveLayout_MovesTheOlderLayout(t *testing.T) {
	p := twoLayouts(t)
	readersSeeAll(t, p, map[string]string{
		"add-hug":  "development/archive/2026-01-01-add-hug",
		"add-wave": "development/archive/2026-01-02-add-wave",
		"fix-typo": "development/archive/changes/2026-02-01-fix-typo",
	})

	res, err := p.ArchiveLayoutCheck()
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "archive.legacy-layout", fe.Code)
	require.Contains(t, fe.Msg, "2 archived change(s)")
	require.Contains(t, fe.Msg, "development/archive/2026-01-01-add-hug")
	require.Len(t, res.Moved, 2)

	before := HeadSHA(ctx(), p.Root)
	res, err = p.ArchiveLayout(ctx())
	require.NoError(t, err)
	require.Equal(t, []ArchiveMove{
		{ID: "add-hug", From: "development/archive/2026-01-01-add-hug", To: "development/archive/changes/2026-01-01-add-hug"},
		{ID: "add-wave", From: "development/archive/2026-01-02-add-wave", To: "development/archive/changes/2026-01-02-add-wave"},
	}, res.Moved)
	require.Equal(t, []string{
		"development/archive/changes/2026-01-01-add-hug/state.json",
		"development/archive/changes/2026-01-02-add-wave/state.json",
	}, res.Rewritten)
	require.Equal(t, res.Commit, HeadSHA(ctx(), p.Root))
	require.Equal(t, before, run(t, p.Root, "rev-parse", "HEAD~1"), "one commit")
	require.Equal(t, ArchiveLayoutSubject, subjects(t, p)[0])
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	files := strings.Split(run(t, p.Root, "show", "--name-only", "--no-renames", "--format=", "HEAD"), "\n")
	for _, f := range files {
		require.True(t, strings.HasPrefix(f, "development/archive/20") || strings.HasPrefix(f, "development/archive/changes/2026-01-0"), f)
	}
	require.Len(t, files, 10, "two folders' four files deleted and added, the png twice")
	require.DirExists(t, filepath.Join(p.ArchiveDir(), "milestones", "M0"), "milestones/ stays")
	require.NoDirExists(t, filepath.Join(p.ArchiveDir(), "2026-01-01-add-hug"))

	// The recorded paths follow the folder; everything else is as it was.
	st, err := LoadState(filepath.Join(p.ArchivedChangesDir(), "2026-01-02-add-wave", "state.json"))
	require.NoError(t, err)
	require.Equal(t, "development/archive/changes/2026-01-02-add-wave", st.ArchivedTo)
	require.Equal(t, "development/archive/changes/2026-01-02-add-wave/assets/shot.png", st.Accepted[0].Evidence[0].Path)
	require.Equal(t, strings.Repeat("a", 64), st.Accepted[0].Evidence[0].SHA256)
	require.Equal(t, "https://ci.example.com/development/archive/2026-01-02-add-wave", st.Accepted[0].Evidence[1].URL, "a URL is not a repo path")
	require.Equal(t, "png\n", read(t, p, "development/archive/changes/2026-01-02-add-wave/assets/shot.png"))
	fixTypo := read(t, p, "development/archive/changes/2026-02-01-fix-typo/state.json")

	readersSeeAll(t, p, map[string]string{
		"add-hug":  "development/archive/changes/2026-01-01-add-hug",
		"add-wave": "development/archive/changes/2026-01-02-add-wave",
		"fix-typo": "development/archive/changes/2026-02-01-fix-typo",
	})
	require.Empty(t, p.provenanceFindings(ctx()), "the move is ape's own commit")

	// Idempotent.
	res, err = p.ArchiveLayout(ctx())
	require.NoError(t, err)
	require.Empty(t, res.Moved)
	require.Empty(t, res.Commit)
	require.Equal(t, ArchiveLayoutSubject, subjects(t, p)[0])
	require.Equal(t, fixTypo, read(t, p, "development/archive/changes/2026-02-01-fix-typo/state.json"))
	res, err = p.ArchiveLayoutCheck()
	require.NoError(t, err)
	require.Empty(t, res.Moved)
}

// A legacy-layout archived change is still accepted from another branch,
// read from the base's tree — and so is a newer-layout one.
func TestArchiveLayout_AcceptArchivedFromAnotherBranch(t *testing.T) {
	p := twoLayouts(t)
	run(t, p.Root, "switch", "-q", "-c", "side")
	for _, id := range []string{"add-hug", "fix-typo"} {
		st, err := p.Accept(ctx(), id, "T2", "ann")
		require.NoError(t, err, id)
		require.Len(t, st.Accepted, 1)
		require.Equal(t, "apex: "+id+" accepted T2 (archived)", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	}
	require.Equal(t, "side", CurrentBranch(ctx(), p.Root))
}

// Archive and abandon write the newer layout: abandon here (archive's is
// in TestArchive).
func TestArchiveLayout_AbandonWritesTheNewerLayout(t *testing.T) {
	p := fixture(t, baseFiles())
	_, err := p.NewChange(ctx(), "add-hug", "M1", []string{"greeting"}, nil)
	require.NoError(t, err)
	res, err := p.Abandon(ctx(), "add-hug", "not needed")
	require.NoError(t, err)
	require.Equal(t, "development/archive/changes/"+Today()+"-add-hug", res.ArchivedTo)
	require.DirExists(t, filepath.Join(p.ArchivedChangesDir(), Today()+"-add-hug"))
	res2, err := p.ArchiveLayoutCheck()
	require.NoError(t, err, "nothing in the older layout")
	require.Empty(t, res2.Moved)
}

// F16a: inside `ape framework update` (its v1.0.0_seq-06), the tree holds
// the update's own uncommitted install — files new, changed and staged
// outside the archive. The move goes ahead, its one commit takes the
// archive alone, and the install is left exactly as it was. On v1.1.0 it
// was refused as a dirty tree, so such an update could never complete.
func TestArchiveLayout_DirtOutsideTheArchiveStays(t *testing.T) {
	p := twoLayouts(t)
	write(t, p, ".claude/skills/apex-new/SKILL.md", "# new\n")
	write(t, p, "notes.txt", "wip\n")
	write(t, p, "staged.txt", "staged\n")
	run(t, p.Root, "add", "staged.txt")
	before := HeadSHA(ctx(), p.Root)
	status := run(t, p.Root, "status", "--porcelain", "--untracked-files=all")

	res, err := p.ArchiveLayout(ctx())
	require.NoError(t, err)
	require.Len(t, res.Moved, 2)
	require.Equal(t, before, run(t, p.Root, "rev-parse", "HEAD~1"), "one commit")
	for f := range strings.SplitSeq(run(t, p.Root, "show", "--name-only", "--no-renames", "--format=", "HEAD"), "\n") {
		require.True(t, strings.HasPrefix(f, "development/archive/"), "the commit takes the archive alone: %s", f)
	}
	require.Equal(t, status, run(t, p.Root, "status", "--porcelain", "--untracked-files=all"), "the rest is as it was, staged included")
}

// archive-layout is refused while the archive has uncommitted changes, on
// a change branch, and when a folder's new place is taken — writing
// nothing each time.
func TestArchiveLayout_Refusals(t *testing.T) {
	t.Run("dirty archive", func(t *testing.T) {
		p := twoLayouts(t)
		write(t, p, "development/archive/2026-01-01-add-hug/notes.md", "wip\n")
		_, err := p.ArchiveLayout(ctx())
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, "tree.dirty", fe.Code)
		require.Contains(t, fe.Msg, "development/archive/ has uncommitted changes")
		require.DirExists(t, filepath.Join(p.ArchiveDir(), "2026-01-01-add-hug"))
	})
	t.Run("change branch", func(t *testing.T) {
		p := twoLayouts(t)
		run(t, p.Root, "switch", "-q", "-c", "change/add-x")
		_, err := p.ArchiveLayout(ctx())
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, CodeChangeBranch, fe.Code)
		require.DirExists(t, filepath.Join(p.ArchiveDir(), "2026-01-01-add-hug"))
	})
	t.Run("new place taken", func(t *testing.T) {
		p := twoLayouts(t)
		write(t, p, "development/archive/changes/2026-01-02-add-wave/change.md", "a copy\n")
		commitAll(t, p, "a copy by hand")
		head := HeadSHA(ctx(), p.Root)
		_, err := p.ArchiveLayout(ctx())
		var ce *ConflictError
		require.ErrorAs(t, err, &ce)
		require.Equal(t, CodeFolderExists, ce.Code)
		require.Equal(t, head, HeadSHA(ctx(), p.Root))
		require.Empty(t, run(t, p.Root, "status", "--porcelain"))
		require.DirExists(t, filepath.Join(p.ArchiveDir(), "2026-01-01-add-hug"), "nothing moved")
	})
	t.Run("nothing to move is no refusal", func(t *testing.T) {
		p := fixture(t, baseFiles())
		write(t, p, "notes.txt", "wip\n")
		res, err := p.ArchiveLayout(ctx())
		require.NoError(t, err)
		require.Empty(t, res.Moved)
	})
}

// A failing commit puts everything back as it was.
func TestArchiveLayout_CommitFailureUndoes(t *testing.T) {
	p := twoLayouts(t)
	head := HeadSHA(ctx(), p.Root)
	// git refuses to commit with no identity.
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")
	t.Setenv("EMAIL", "")
	run(t, p.Root, "config", "user.useConfigOnly", "true")
	_, err := p.ArchiveLayout(ctx())
	if err == nil {
		t.Skip("git found an identity on this machine")
	}
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeCommitFailed, fe.Code)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"), "the tree is as it was")
	require.DirExists(t, filepath.Join(p.ArchiveDir(), "2026-01-01-add-hug"))
	require.Equal(t, "development/archive/2026-01-02-add-wave", mustState(t, filepath.Join(p.ArchiveDir(), "2026-01-02-add-wave", "state.json")).ArchivedTo)
}

func mustState(t *testing.T, path string) *State {
	t.Helper()
	st, err := LoadState(path)
	require.NoError(t, err)
	return st
}

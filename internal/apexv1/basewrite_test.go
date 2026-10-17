package apexv1

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A record for main, written while HEAD is on another branch, lands on
// main as one commit and leaves the checkout alone.
func TestCommitOnBranch_WithoutCheckout(t *testing.T) {
	p := fixture(t, baseFiles())
	run(t, p.Root, "switch", "-q", "-c", "side")
	write(t, p, "scratch.txt", "dirty\n")
	headBefore := HeadSHA(ctx(), p.Root)

	sha, err := p.commitOnBase(ctx(), "main", map[string][]byte{
		"development/roadmap.md": []byte(roadmapApproved + "\n## Next\n- later\n"),
		"greet.sh":               []byte("#!/bin/sh\necho Hi\n"),
	}, "apex: next\n\nMilestone: M1")
	require.NoError(t, err)
	require.NotEmpty(t, sha)
	require.Equal(t, sha, run(t, p.Root, "rev-parse", "main"))
	require.Equal(t, "apex: next", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	require.Equal(t, "Milestone: M1", run(t, p.Root, "log", "-1", "--format=%b", "main"))
	require.Contains(t, run(t, p.Root, "show", "main:development/roadmap.md"), "- later")
	require.Equal(t, "100755", run(t, p.Root, "ls-tree", "main", "--format=%(objectmode)", "--", "greet.sh"), "an executable keeps its mode")
	require.Equal(t, headBefore, HeadSHA(ctx(), p.Root), "HEAD did not move")
	require.Equal(t, "side", CurrentBranch(ctx(), p.Root))
	require.Equal(t, "?? scratch.txt", run(t, p.Root, "status", "--porcelain"), "the working tree and index are untouched")

	// The same content again commits nothing.
	again, err := p.commitOnBase(ctx(), "main", map[string][]byte{"greet.sh": []byte("#!/bin/sh\necho Hi\n")}, "apex: again")
	require.NoError(t, err)
	require.Empty(t, again)
	require.Equal(t, sha, run(t, p.Root, "rev-parse", "main"))

	// A nil content deletes the file.
	_, err = p.commitOnBase(ctx(), "main", map[string][]byte{"greet.sh": nil}, "apex: drop")
	require.NoError(t, err)
	require.NotContains(t, run(t, p.Root, "ls-tree", "--name-only", "main"), "greet.sh")
}

// On the branch itself the record is an ordinary commit of exactly its
// paths, in the working tree.
func TestCommitOnBranch_CheckedOutHere(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "other.txt", "not ape's\n")
	sha, err := p.commitOnBase(ctx(), "main", map[string][]byte{"development/notes.md": []byte("n\n")}, "apex: notes")
	require.NoError(t, err)
	require.Equal(t, sha, HeadSHA(ctx(), p.Root))
	require.Equal(t, "n\n", read(t, p, "development/notes.md"))
	require.Equal(t, "development/notes.md", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))
	require.Equal(t, "?? other.txt", run(t, p.Root, "status", "--porcelain"))
}

// A project below the repository's root writes its paths under its
// prefix.
func TestCommitOnBranch_ProjectInSubdir(t *testing.T) {
	outer := fixture(t, map[string]string{"README": "r\n"})
	write(t, outer, "sub/development/roadmap.md", roadmapApproved)
	commitAll(t, outer, "sub")
	run(t, outer.Root, "switch", "-q", "-c", "side")
	p, err := Open(filepath.Join(outer.Root, "sub"))
	require.NoError(t, err)
	_, err = p.commitOnBase(ctx(), "main", map[string][]byte{"development/x.md": []byte("x\n")}, "apex: x")
	require.NoError(t, err)
	require.Equal(t, "x", run(t, outer.Root, "show", "main:sub/development/x.md"))
}

// The compare-and-swap: a commit built on a tip that has since moved
// writes nothing and says so (git.ref-moved).
func TestCommitOnBranch_CASRace(t *testing.T) {
	p := fixture(t, baseFiles())
	stale := HeadSHA(ctx(), p.Root)
	run(t, p.Root, "switch", "-q", "-c", "side")
	// Someone else moves main after the record was read at stale.
	_, err := p.commitOnBase(ctx(), "main", map[string][]byte{"a.txt": []byte("a\n")}, "theirs")
	require.NoError(t, err)
	moved := run(t, p.Root, "rev-parse", "main")

	_, err = p.commitOnBranchAt(ctx(), "main", stale, map[string][]byte{"b.txt": []byte("b\n")}, "ours")
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeRefMoved, fe.Code)
	require.Equal(t, moved, run(t, p.Root, "rev-parse", "main"), "nothing was written")

	// editOnBranch reads at the tip it commits on: it keeps their write.
	_, err = p.editOnBranch(ctx(), "main", "ours", []string{"a.txt"}, func(cur map[string][]byte) (map[string][]byte, error) {
		return map[string][]byte{"a.txt": append(cur["a.txt"], "b\n"...)}, nil
	})
	require.NoError(t, err)
	require.Equal(t, "a\nb", run(t, p.Root, "show", "main:a.txt"))
}

// A branch checked out in another worktree is refused, naming it.
func TestCommitOnBranch_OtherWorktree(t *testing.T) {
	p := fixture(t, baseFiles())
	run(t, p.Root, "switch", "-q", "-c", "side")
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, p.Root, "worktree", "add", "-q", wt, "main")
	before := run(t, p.Root, "rev-parse", "main")

	_, err := p.commitOnBase(ctx(), "main", map[string][]byte{"a.txt": []byte("a\n")}, "apex: a")
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeBaseCheckedOut, fe.Code)
	require.Contains(t, fe.Msg, "wt")
	require.Equal(t, ActionStop, ActionFor(fe.Code))
	require.Equal(t, before, run(t, p.Root, "rev-parse", "main"))
}

func TestBaseBranch(t *testing.T) {
	p := fixture(t, baseFiles())
	b, err := p.baseBranch(ctx())
	require.NoError(t, err)
	require.Equal(t, "main", b)
	run(t, p.Root, "switch", "-q", "-c", "change/add-x")
	write(t, p, "development/changes/add-x/change.md", "x\n")
	require.NoError(t, SaveState(p.StateFile("add-x"), &State{ID: "add-x", Status: StatusInProgress, BaseBranch: "main"}))
	b, err = p.baseBranch(ctx())
	require.NoError(t, err)
	require.Equal(t, "main", b)
	run(t, p.Root, "switch", "-q", "--detach")
	_, err = p.baseBranch(ctx())
	require.ErrorContains(t, err, "detached")
}

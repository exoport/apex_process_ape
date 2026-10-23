package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// F1: the working tree's digest moves with what a gate could see — an
// untracked file, an edit — and not with ape's own state, an ignored file,
// or a commit of the same content. The real index is left as it was.
func TestWorkTreeDigest(t *testing.T) {
	files := baseFiles()
	files[".gitignore"] = "build/\n"
	p := fixture(t, files)
	digest := func() string {
		t.Helper()
		d, err := p.WorkTreeDigest(ctx())
		require.NoError(t, err)
		require.Len(t, d, 40)
		return d
	}
	idx := filepath.Join(p.Root, ".git", "index")
	before, err := os.ReadFile(idx)
	require.NoError(t, err)

	d0 := digest()
	require.Equal(t, d0, digest(), "stable")
	require.Equal(t, run(t, p.Root, "rev-parse", "HEAD^{tree}"), d0, "a clean tree is HEAD's tree")
	after, err := os.ReadFile(idx)
	require.NoError(t, err)
	require.Equal(t, before, after, "the real index is untouched")

	write(t, p, "_apex/state/runs/M1.result.json", "{}\n")
	write(t, p, "_apex/state/milestones.json", "{\"x\": 1}\n")
	require.Equal(t, d0, digest(), "ape's state, tracked or not, is left out")
	write(t, p, "build/out.bin", "ignored\n")
	require.Equal(t, d0, digest(), "an ignored file is not part of it")

	write(t, p, "new.txt", "new\n")
	d1 := digest()
	require.NotEqual(t, d0, d1, "an untracked file changes it")
	require.Contains(t, run(t, p.Root, "status", "--porcelain"), "?? new.txt", "and stays untracked")

	run(t, p.Root, "add", "new.txt")
	run(t, p.Root, "commit", "-q", "-m", "new")
	require.Equal(t, d1, digest(), "a commit of the same content keeps it")

	write(t, p, "greet.sh", "#!/bin/sh\necho Hi $1\n")
	require.NotEqual(t, d1, digest(), "an edit changes it")
	run(t, p.Root, "checkout", "--", "greet.sh")
	require.Equal(t, d1, digest())
	require.NoError(t, os.Remove(filepath.Join(p.Root, "new.txt")))
	require.NotEqual(t, d1, digest(), "a deletion changes it")
}

// A record without a tree (ape v1.1), another verb's, or one whose HEAD
// moved is never recalled.
func TestGateRecall_Keys(t *testing.T) {
	p := fixture(t, baseFiles())
	head := HeadSHA(ctx(), p.Root)
	tree, err := p.WorkTreeDigest(ctx())
	require.NoError(t, err)
	save := func(r RunResult) {
		t.Helper()
		r.ID, r.Scope = "run-1", "M1"
		require.NoError(t, p.SaveRunResult(&r))
	}
	save(RunResult{Verb: VerbMilestoneGate, HeadAfter: head, Tree: tree, Reported: true})
	require.NotNil(t, p.GateRecall(ctx(), "M1"))
	save(RunResult{Verb: VerbMilestoneGate, HeadAfter: head, Reported: true})
	require.Nil(t, p.GateRecall(ctx(), "M1"), "v1.1's record has no tree")
	save(RunResult{Verb: "milestone close", HeadAfter: head, Tree: tree})
	require.Nil(t, p.GateRecall(ctx(), "M1"))
	save(RunResult{Verb: VerbMilestoneGate, HeadAfter: "0000000000000000000000000000000000000000", Tree: tree})
	require.Nil(t, p.GateRecall(ctx(), "M1"), "HEAD moved")
}

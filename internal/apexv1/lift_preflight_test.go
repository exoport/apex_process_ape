package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func preflightCodes(res *LiftResult) []string {
	var out []string
	for _, p := range res.Preflight {
		out = append(out, p.Code)
	}
	return out
}

// L14: the dry run reports every pre-flight problem, not only the first.
// The research dry runs, with no remote and no tag, said only
// lift.no-tag.
func TestLift_DryRunReportsEveryPreflightProblem(t *testing.T) {
	t.Run("no tag, no upstream, dirty", func(t *testing.T) {
		root := liftFixture(t, false)
		run(t, root, "branch", "--unset-upstream")
		require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))
		res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
		require.NoError(t, err)
		require.Equal(t, []string{"lift.dirty", "lift.no-tag", "lift.tag-not-pushed"}, preflightCodes(res))
	})
	t.Run("tag not at HEAD and not pushed", func(t *testing.T) {
		root := liftFixture(t, false)
		run(t, root, "tag", "-a", LiftTag, "-m", "final")
		require.NoError(t, os.WriteFile(filepath.Join(root, "x.txt"), []byte("x"), 0o644))
		run(t, root, "add", "x.txt")
		run(t, root, "commit", "-q", "-m", "after the tag")
		res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
		require.NoError(t, err)
		require.Equal(t, []string{"lift.tag-not-head", "lift.tag-not-pushed"}, preflightCodes(res))
		// The real run refuses with the first.
		_, err = Lift(ctx(), root, LiftOptions{})
		require.Equal(t, "lift.tag-not-head", liftCode(err))
	})
}

// L2, from mdpdf and pdfa: .claude/ was git-ignored, with the v0 skills
// on disk untracked. The dry run passed; the real lift failed at `git
// add` on .claude/settings.json and left the tree half-staged. Now the dry
// run says so, the real run refuses before writing, and once
// settings.json is un-ignored the lift removes the manifest's untracked
// v0 skills too.
func TestLift_IgnoredClaudeFolder(t *testing.T) {
	root := liftFixture(t, false)
	run(t, root, "rm", "-r", "-q", "--cached", ".claude")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/\n"), 0o644))
	run(t, root, "add", ".gitignore")
	run(t, root, "commit", "-q", "-m", "ignore .claude")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)
	head := run(t, root, "rev-parse", "HEAD")

	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, []string{CodeClaudeIgnored}, preflightCodes(res))
	require.Contains(t, res.Preflight[0].Message, ".claude/settings.json")
	require.Contains(t, res.Preflight[0].Message, "!.claude/settings.json")
	var untracked []string
	for _, d := range res.Deleted {
		if d.Untracked {
			untracked = append(untracked, d.Path)
		}
	}
	require.Equal(t, []string{".claude/skills/apex-dev-story", ".claude/skills/apex-orchestrator"}, untracked, "the manifest's, not apex-unlisted or my-own")

	_, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.Equal(t, CodeClaudeIgnored, liftCode(err))
	require.Equal(t, head, run(t, root, "rev-parse", "HEAD"))
	require.Empty(t, run(t, root, "status", "--porcelain"), "refused before anything was written")

	// The fix the message names, committed and re-tagged.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/*\n!.claude/settings.json\n"), 0o644))
	run(t, root, "add", ".gitignore")
	run(t, root, "commit", "-q", "-m", "share settings.json")
	run(t, root, "tag", "-f", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "-f", "origin", LiftTag)
	res, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	require.Empty(t, res.Preflight)
	require.Contains(t, run(t, root, "show", "--name-status", "--format=", "HEAD"), "A\t.claude/settings.json")
	require.NoDirExists(t, filepath.Join(root, ".claude", "skills", "apex-dev-story"))
	require.NoDirExists(t, filepath.Join(root, ".claude", "skills", "apex-orchestrator"))
	require.DirExists(t, filepath.Join(root, ".claude", "skills", "my-own"), "not the manifest's: kept")
	require.Empty(t, run(t, root, "status", "--porcelain"))
}

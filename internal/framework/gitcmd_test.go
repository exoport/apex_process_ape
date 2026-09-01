package framework_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// initRepo creates a fresh git repo at dir with one initial commit on
// branch main, identity configured, and returns the head SHA.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	ctx := context.Background()
	mustRun := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	mustRun("init", "-b", "main")
	mustRun("config", "user.email", "test@example.invalid")
	mustRun("config", "user.name", "Test")
	mustRun("config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644))
	mustRun("add", ".")
	mustRun("commit", "-m", "init")
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out[:len(out)-1])
}

func TestIsGitRepo(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	require.False(t, framework.IsGitRepo(ctx, repo))
	initRepo(t, repo)
	require.True(t, framework.IsGitRepo(ctx, repo))
}

func TestCurrentBranch_Main(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initRepo(t, repo)
	branch, err := framework.CurrentBranch(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, "main", branch)
}

func TestHeadSHAAndExactTag(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	sha := initRepo(t, repo)

	got, err := framework.HeadSHA(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, sha, got)

	tag, err := framework.ExactTag(ctx, repo)
	require.NoError(t, err)
	require.Empty(t, tag)

	cmd := exec.CommandContext(ctx, "git", "tag", "v0.0.1")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())

	tag, err = framework.ExactTag(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, "v0.0.1", tag)
}

func TestIsClean(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initRepo(t, repo)

	clean, err := framework.IsClean(ctx, repo)
	require.NoError(t, err)
	require.True(t, clean)

	require.NoError(t, os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644))

	clean, err = framework.IsClean(ctx, repo)
	require.NoError(t, err)
	require.False(t, clean)
}

// gitIn runs a git command in dir and fails the test if it errors.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// upstreamAndClone builds an origin repo with one commit and a clone of
// it, which is the shape every framework checkout ape updates has.
func upstreamAndClone(t *testing.T) (upstream, clone string) {
	t.Helper()
	upstream = t.TempDir()
	initRepo(t, upstream)
	// A non-bare origin refuses a push to its checked-out branch.
	gitIn(t, upstream, "config", "receive.denyCurrentBranch", "ignore")

	clone = filepath.Join(t.TempDir(), "clone")
	gitIn(t, t.TempDir(), "clone", upstream, clone)
	gitIn(t, clone, "config", "user.email", "test@example.invalid")
	gitIn(t, clone, "config", "user.name", "Test")
	return upstream, clone
}

// TestFetchAndFastForward_BringsTheTagOnTheCommitItPulls covers the defect
// that made `version_tag` empty in an installed framework.yaml.
//
// git auto-follows tags on a bare `git fetch`, but not when the command
// line names a refspec — and this one always does (`fetch origin main`).
// The release commit therefore arrived without its tag, `describe
// --exact-match` correctly found nothing at HEAD, and the install recorded
// an empty version. Nothing errored anywhere along that path, which is why
// it survived: there was no test here that fetched from a real remote at
// all, so no test could observe a tag failing to arrive.
func TestFetchAndFastForward_BringsTheTagOnTheCommitItPulls(t *testing.T) {
	ctx := context.Background()
	upstream, clone := upstreamAndClone(t)

	// Upstream cuts a release: a new commit, annotated-tagged.
	require.NoError(t, os.WriteFile(filepath.Join(upstream, "next.txt"), []byte("x\n"), 0o644))
	gitIn(t, upstream, "add", ".")
	gitIn(t, upstream, "commit", "-m", "release")
	gitIn(t, upstream, "tag", "-a", "v0.14.1", "-m", "v0.14.1")

	tag, err := framework.ExactTag(ctx, clone)
	require.NoError(t, err)
	require.Empty(t, tag, "precondition: the clone has not seen the release yet")

	require.NoError(t, framework.FetchAndFastForward(ctx, clone, "main"))

	require.Equal(t, gitIn(t, upstream, "rev-parse", "HEAD"),
		gitIn(t, clone, "rev-parse", "HEAD"), "the commit must arrive")

	tag, err = framework.ExactTag(ctx, clone)
	require.NoError(t, err)
	require.Equal(t, "v0.14.1", tag,
		"the tag on the pulled commit must arrive with it — an empty value here is "+
			"what wrote `version_tag: \"\"` into installed framework.yaml files")
}

// TestFetchAndFastForward_MovedTagDoesNotBreakTheUpdate guards the fix
// against being "simplified" into the obvious one-flag version.
//
// Folding --tags into the branch fetch looks smaller and passes the test
// above. But when upstream moves a tag, `git fetch --tags` exits 1 with
// "would clobber existing tag", and FetchAndFastForward would return that
// error before reaching the ff-merge — so one retagged release upstream
// would break `ape framework update` outright. Mirroring tags separately,
// best-effort and --force, keeps the update working AND lands the moved
// tag; anything that fails this test has put tag trouble back on the
// update's critical path.
func TestFetchAndFastForward_MovedTagDoesNotBreakTheUpdate(t *testing.T) {
	ctx := context.Background()
	upstream, clone := upstreamAndClone(t)

	require.NoError(t, os.WriteFile(filepath.Join(upstream, "a.txt"), []byte("a\n"), 0o644))
	gitIn(t, upstream, "add", ".")
	gitIn(t, upstream, "commit", "-m", "release")
	gitIn(t, upstream, "tag", "-a", "v0.14.1", "-m", "first cut")
	require.NoError(t, framework.FetchAndFastForward(ctx, clone, "main"))

	// Upstream re-cuts the same release onto a new commit.
	require.NoError(t, os.WriteFile(filepath.Join(upstream, "b.txt"), []byte("b\n"), 0o644))
	gitIn(t, upstream, "add", ".")
	gitIn(t, upstream, "commit", "-m", "re-cut release")
	gitIn(t, upstream, "tag", "-f", "-a", "v0.14.1", "-m", "moved")

	require.NoError(t, framework.FetchAndFastForward(ctx, clone, "main"),
		"a moved upstream tag must not fail the update")

	require.Equal(t, gitIn(t, upstream, "rev-parse", "HEAD"),
		gitIn(t, clone, "rev-parse", "HEAD"))

	tag, err := framework.ExactTag(ctx, clone)
	require.NoError(t, err)
	require.Equal(t, "v0.14.1", tag,
		"the moved tag must be mirrored, or version_tag goes stale instead of empty")
}

func TestParsePorcelain(t *testing.T) {
	out := "?? new.txt\n M tracked.txt\nA  staged.txt"
	entries := framework.ParsePorcelain(out)
	require.Len(t, entries, 3)
	require.Equal(t, "??", entries[0].Status)
	require.Equal(t, "new.txt", entries[0].Path)
	require.True(t, entries[0].IsUntracked())
	require.Equal(t, " M", entries[1].Status)
	require.False(t, entries[1].IsUntracked())
	require.Equal(t, "A ", entries[2].Status)
}

func TestParsePorcelain_Empty(t *testing.T) {
	require.Nil(t, framework.ParsePorcelain(""))
	require.Nil(t, framework.ParsePorcelain("\n  \n"))
}

func TestSkillsPorcelain_DistinguishesUntrackedFromModified(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initRepo(t, repo)

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude", "skills", "apex-untracked"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".claude", "skills", "apex-untracked", "SKILL.md"), []byte("u"), 0o644))

	tracked := filepath.Join(repo, ".claude", "skills", "apex-tracked", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(tracked), 0o755))
	require.NoError(t, os.WriteFile(tracked, []byte("v1"), 0o644))
	cmd := exec.CommandContext(ctx, "git", "add", ".")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())
	cmd = exec.CommandContext(ctx, "git", "commit", "-m", "add tracked")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude", "skills", "apex-fresh"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".claude", "skills", "apex-fresh", "SKILL.md"), []byte("u"), 0o644))
	require.NoError(t, os.WriteFile(tracked, []byte("v2"), 0o644))

	entries, err := framework.SkillsPorcelain(ctx, repo)
	require.NoError(t, err)

	var untracked, modified int
	for _, e := range entries {
		if e.IsUntracked() {
			untracked++
		} else {
			modified++
		}
	}
	require.GreaterOrEqual(t, untracked, 1, "expected at least one untracked apex-* path")
	require.GreaterOrEqual(t, modified, 1, "expected at least one modified apex-* path")
}

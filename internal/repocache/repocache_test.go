package repocache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestURLKey(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/exoar/apex_process_framework.git": "github.com/exoar/apex_process_framework",
		"https://GitHub.com/exoar/apex_process_framework/":    "github.com/exoar/apex_process_framework",
		"git@github.com:exoar/apex_process_governance.git":    "github.com/exoar/apex_process_governance",
		"ssh://git@github.com:22/exoar/r":                     "github.com/exoar/r",
		"file:///srv/mirror/r.git":                            "file/srv/mirror/r",
	} {
		got, err := URLKey(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "/srv/repo", "https://h/../x", "https://h/a b"} {
		_, err := URLKey(bad)
		require.Error(t, err, bad)
	}
}

func withCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := userCacheDir
	userCacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userCacheDir = old })
	return dir
}

func TestResolveFramework_Order(t *testing.T) {
	cache := withCache(t)
	t.Setenv(EnvFrameworkRepo, "")
	t.Setenv(EnvFrameworkURL, "")

	c, err := ResolveFramework("/from/flag")
	require.NoError(t, err)
	require.Equal(t, Clone{Kind: KindFramework, Path: "/from/flag", Source: SourceFlag}, c)

	t.Setenv(EnvFrameworkRepo, "/from/env")
	c, err = ResolveFramework("")
	require.NoError(t, err)
	require.Equal(t, SourceEnv, c.Source)
	require.False(t, c.Owned())

	t.Setenv(EnvFrameworkRepo, "")
	c, err = ResolveFramework("")
	require.NoError(t, err)
	require.Equal(t, SourceCache, c.Source)
	require.True(t, c.Owned())
	require.Equal(t, DefaultFrameworkURL, c.URL)
	require.Equal(t, filepath.Join(cache, "ape", "framework", "github.com", "exoar", "apex_process_framework"), c.Path)

	t.Setenv(EnvFrameworkURL, "git@example.org:fork/fw.git")
	c, err = ResolveFramework("")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cache, "ape", "framework", "example.org", "fork", "fw"), c.Path)
}

func TestResolveGovernance_Order(t *testing.T) {
	cache := withCache(t)
	t.Setenv(EnvGovernanceRepo, "")

	c, err := ResolveGovernance("", "")
	require.NoError(t, err)
	require.True(t, c.None(), "no path, no env, no URL: no governance repo")

	c, err = ResolveGovernance("", "https://github.com/exoar/gov.git")
	require.NoError(t, err)
	require.Equal(t, SourceCache, c.Source)
	require.Equal(t, filepath.Join(cache, "ape", "governance", "github.com", "exoar", "gov"), c.Path)

	t.Setenv(EnvGovernanceRepo, "/from/env")
	c, err = ResolveGovernance("", "https://github.com/exoar/gov.git")
	require.NoError(t, err)
	require.Equal(t, SourceEnv, c.Source)

	c, err = ResolveGovernance("/from/config", "")
	require.NoError(t, err)
	require.Equal(t, SourceConfig, c.Source, "the project's own path wins over the machine's")

	t.Setenv(EnvGovernanceRepo, "")
	_, err = ResolveGovernance("", "not a url")
	require.Error(t, err, "a URL typo is an error, not 'no governance repo'")
}

// --- sync -----------------------------------------------------------------

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// origin builds a repo with releases v0.1.0, v0.2.0, a candidate
// v0.3.0-rc.1 on top, and main beyond them all.
func origin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for _, tag := range []string{"v0.1.0", "v0.2.0", "v0.3.0-rc.1", ""} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "VERSION"), []byte(tag+"\n"), 0o644))
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "at "+tag)
		if tag != "" {
			git(t, dir, "tag", tag)
		}
	}
	return dir
}

func userClone(t *testing.T, from string) Clone {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "-q", from, dir)
	return Clone{Kind: KindGovernance, Path: dir, Source: SourceEnv}
}

func version(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	require.NoError(t, err)
	return strings.TrimSpace(string(b))
}

// behind puts a user clone's main back at ref: a stale checkout, the one
// case a user's clone is moved in.
func behind(t *testing.T, c Clone, ref string) {
	t.Helper()
	git(t, c.Path, "reset", "-q", "--hard", ref)
}

func TestSync_FastForwardsAStaleUserCloneOnItsBranch(t *testing.T) {
	c := userClone(t, origin(t))
	behind(t, c, "v0.1.0")
	res, err := Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.Equal(t, "v0.2.0", res.Tag, "the newest FINAL release, never the candidate")
	require.True(t, res.Moved)
	require.Equal(t, "v0.2.0", version(t, c.Path))
	require.Equal(t, "main", git(t, c.Path, "rev-parse", "--abbrev-ref", "HEAD"),
		"fast-forwarded, so 'clean, on main' stays true for whoever commits there")
	require.Equal(t, "main at v0.2.0", State(context.Background(), c))
}

func TestSync_DetachedStaleUserCloneIsDetachedAtTheRelease(t *testing.T) {
	c := userClone(t, origin(t))
	git(t, c.Path, "-c", "advice.detachedHead=false", "checkout", "-q", "v0.1.0")
	res, err := Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.True(t, res.Moved)
	require.Equal(t, "v0.2.0", version(t, c.Path))
	require.Equal(t, "HEAD", git(t, c.Path, "rev-parse", "--abbrev-ref", "HEAD"))
}

// The framework's ship repo holds promotion commits past its last tag, and
// a developer's build repo holds work past an rc: committed, clean, and not
// ape's to leave behind.
func TestSync_UserCloneAheadOfTheReleaseIsKept(t *testing.T) {
	c := userClone(t, origin(t)) // main is one commit past the rc, two past v0.2.0
	tip := git(t, c.Path, "rev-parse", "HEAD")
	res, err := Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.False(t, res.Moved)
	require.Equal(t, "main is 2 commit(s) past v0.2.0", res.Kept)
	require.Equal(t, tip, git(t, c.Path, "rev-parse", "HEAD"))
	require.Equal(t, "main", git(t, c.Path, "rev-parse", "--abbrev-ref", "HEAD"))
	require.Equal(t, "main, 2 commit(s) past v0.2.0", State(context.Background(), c))
}

func TestSync_DivergedUserCloneIsKept(t *testing.T) {
	c := userClone(t, origin(t))
	behind(t, c, "v0.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(c.Path, "LOCAL"), []byte("x"), 0o644))
	git(t, c.Path, "add", ".")
	git(t, c.Path, "commit", "-q", "-m", "local work")
	res, err := Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.False(t, res.Moved)
	require.Contains(t, res.Kept, "main has diverged from v0.2.0")
	require.FileExists(t, filepath.Join(c.Path, "LOCAL"))
}

func TestSync_VersionNeverMovesAUserCloneBackwards(t *testing.T) {
	c := userClone(t, origin(t))
	behind(t, c, "v0.2.0")
	res, err := Sync(context.Background(), c, SyncOptions{Version: "v0.1.0"})
	require.NoError(t, err)
	require.False(t, res.Moved)
	require.NotEmpty(t, res.Kept)
	require.Equal(t, "v0.2.0", version(t, c.Path))

	res, err = Sync(context.Background(), c, SyncOptions{Version: "v0.3.0-rc.1"})
	require.NoError(t, err)
	require.True(t, res.Moved, "forward to a candidate named by --version")
	require.Equal(t, "v0.3.0-rc.1", version(t, c.Path))
}

func TestSync_AlreadyThereKeepsTheBranch(t *testing.T) {
	c := userClone(t, origin(t))
	behind(t, c, "v0.2.0")
	res, err := Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.False(t, res.Moved)
	require.Equal(t, "main", git(t, c.Path, "rev-parse", "--abbrev-ref", "HEAD"))

	// At the release with local edits: nothing to move, nothing overwritten.
	require.NoError(t, os.WriteFile(filepath.Join(c.Path, "NOTES"), []byte("mine"), 0o644))
	res, err = Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.False(t, res.Moved)
	require.FileExists(t, filepath.Join(c.Path, "NOTES"))
}

func TestSync_DirtyStaleUserCloneRefusedUnlessForced(t *testing.T) {
	c := userClone(t, origin(t))
	behind(t, c, "v0.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(c.Path, "VERSION"), []byte("edited\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(c.Path, "NEW"), []byte("x"), 0o644))

	_, err := Sync(context.Background(), c, SyncOptions{})
	var dirty *DirtyError
	require.ErrorAs(t, err, &dirty)
	require.Equal(t, "governance_clone_dirty", dirty.Code())
	require.Contains(t, err.Error(), "--force-clone")
	require.Equal(t, "edited", version(t, c.Path), "a refusal touches nothing")

	res, err := Sync(context.Background(), c, SyncOptions{Force: true})
	require.NoError(t, err)
	require.True(t, res.Moved)
	require.Equal(t, "v0.2.0", version(t, c.Path))
	require.NoFileExists(t, filepath.Join(c.Path, "NEW"))
	require.Equal(t, "main", git(t, c.Path, "rev-parse", "--abbrev-ref", "HEAD"), "still on its branch")
}

func TestSync_OwnedCloneIsClonedAndOverwritten(t *testing.T) {
	src := origin(t)
	c := Clone{Kind: KindFramework, Path: filepath.Join(t.TempDir(), "cache", "fw"), Source: SourceCache, URL: src}
	res, err := Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.True(t, res.Cloned)
	require.Equal(t, "v0.2.0", version(t, c.Path))

	require.NoError(t, os.WriteFile(filepath.Join(c.Path, "VERSION"), []byte("edited\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(c.Path, ".ignored-junk"), []byte("x"), 0o644))
	_, err = Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err, "ape's own clone is overwritten without --force")
	require.Equal(t, "v0.2.0", version(t, c.Path))
	require.NoFileExists(t, filepath.Join(c.Path, ".ignored-junk"))
}

func TestSync_FetchesNewReleasesUnlessNoFetch(t *testing.T) {
	src := origin(t)
	c := userClone(t, src)
	behind(t, c, "v0.1.0")
	git(t, src, "tag", "v0.4.0", "main")

	res, err := Sync(context.Background(), c, SyncOptions{NoFetch: true})
	require.NoError(t, err)
	require.Equal(t, "v0.2.0", res.Tag, "--no-fetch sees only the tags already there")

	res, err = Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.Equal(t, "v0.4.0", res.Tag)
	require.Empty(t, version(t, c.Path), "main's tip, now tagged")
}

// A refused install must leave the clone where it was: Prepare resolves
// without moving anything.
func TestPrepare_MovesNothing(t *testing.T) {
	c := userClone(t, origin(t))
	behind(t, c, "v0.1.0")
	tg, err := Prepare(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.Equal(t, "v0.2.0", tg.Tag)
	require.Equal(t, "v0.1.0", version(t, c.Path))
}

func TestSync_NoReleaseLeavesTheCloneAlone(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "untagged")
	c := userClone(t, dir)
	_, err := Sync(context.Background(), c, SyncOptions{})
	require.ErrorIs(t, err, ErrNoRelease)
	require.Equal(t, "main", git(t, c.Path, "rev-parse", "--abbrev-ref", "HEAD"))
}

func TestSync_ReadOnlyCloneIsPinned(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced on this user")
	}
	c := userClone(t, origin(t))
	git(t, c.Path, "-c", "advice.detachedHead=false", "checkout", "-q", "v0.1.0")
	gitDir := filepath.Join(c.Path, ".git")
	require.NoError(t, os.Chmod(gitDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

	res, err := Sync(context.Background(), c, SyncOptions{})
	require.NoError(t, err)
	require.True(t, res.Pinned)
	require.False(t, res.Moved)
	require.Equal(t, "v0.1.0", res.Tag, "the release checked out there, not the newest the clone holds")
	require.Equal(t, "v0.1.0", version(t, c.Path))

	require.NoError(t, os.Chmod(gitDir, 0o755))
	git(t, c.Path, "-c", "advice.detachedHead=false", "checkout", "-q", "main")
	require.NoError(t, os.Chmod(gitDir, 0o555))
	_, err = Sync(context.Background(), c, SyncOptions{})
	require.ErrorIs(t, err, ErrNoRelease, "a pinned clone off any release has nothing to install")
}

func TestSync_ConcurrentRunsShareOneClone(t *testing.T) {
	c := userClone(t, origin(t))
	behind(t, c, "v0.1.0")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = Sync(context.Background(), c, SyncOptions{})
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, "v0.2.0", version(t, c.Path))
}

// With no clone named, State must not describe whatever repo the process
// happens to run in (an empty path is the working directory to git).
func TestState_NoCloneIsEmpty(t *testing.T) {
	require.Empty(t, State(context.Background(), Clone{Kind: KindGovernance}))
}

// The recorded origin is the URL the clone is configured with, never the
// machine's insteadOf rewrite of it: it lands in committed history, where a
// private ssh alias must not, and where two machines' rules would flip it.
func TestPrepare_OriginIsTheConfiguredURLNotTheRewrite(t *testing.T) {
	c := userClone(t, origin(t))
	git(t, c.Path, "remote", "set-url", "origin", "https://example.invalid/o/r.git")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.git@private.alias:.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://example.invalid/")
	tg, err := Prepare(context.Background(), c, SyncOptions{NoFetch: true})
	require.NoError(t, err)
	require.Equal(t, "https://example.invalid/o/r.git", tg.Origin)
}

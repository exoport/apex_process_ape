package apecmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// governanceOrigin is a governance repo with releases v0.1.0 and v0.2.0 and
// unreleased work on main beyond them.
func governanceOrigin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir)
	for _, tag := range []string{"v0.1.0", "v0.2.0", ""} {
		p := filepath.Join(dir, "governance", "patterns", "index.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("# "+tag+"\n"), 0o644))
		gitCommitAll(t, dir, "at "+tag)
		if tag != "" {
			gitRun(t, dir, "tag", tag)
		}
	}
	return dir
}

func governanceIndex(t *testing.T, clone string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(clone, "governance", "patterns", "index.yaml"))
	require.NoError(t, err)
	// A checkout follows the machine's core.autocrlf (true on the Windows
	// CI runners), unlike the install's export, which pins it off.
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// The update moves both clones to their newest release before installing,
// and records the governance release it synced.
func TestFrameworkUpdate_SyncsBothClonesToTheirRelease(t *testing.T) {
	withApeVersion(t, "0.4.0")
	// A stale framework clone: main at v1.2.0, a newer release tagged
	// past it.
	fw := releasedFramework(t, "v1.2.0")
	require.NoError(t, os.WriteFile(filepath.Join(fw, "RELEASED"), []byte("x"), 0o644))
	gitCommitAll(t, fw, "next release")
	gitRun(t, fw, "tag", "v1.3.0")
	gitRun(t, fw, "reset", "-q", "--hard", "v1.2.0")

	gov := filepath.Join(t.TempDir(), "gov")
	gitRun(t, "", "clone", "-q", governanceOrigin(t), gov)
	gitRun(t, gov, "reset", "-q", "--hard", "v0.1.0")
	root := committedProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.local.yaml"),
		[]byte("governance_repository_path: "+gov+"\n"), 0o644))

	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.NoError(t, err, out)
	require.Contains(t, out, "framework repo: "+fw+" (from flag) at v1.3.0 (checked out)")
	require.Contains(t, out, "governance repo: "+gov+" (from config) at v0.2.0 (checked out)")

	require.Equal(t, gitRun(t, fw, "rev-parse", "v1.3.0^{commit}"), gitRun(t, fw, "rev-parse", "HEAD"))
	require.Equal(t, "main", gitRun(t, fw, "rev-parse", "--abbrev-ref", "HEAD"), "fast-forwarded on its branch")
	require.Equal(t, "# v0.2.0\n", governanceIndex(t, gov), "the newest release, not main's unreleased work")

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.NotNil(t, meta.Governance)
	require.Equal(t, "v0.2.0", meta.Governance.VersionTag)
	require.Equal(t, gitRun(t, gov, "rev-parse", "HEAD"), meta.Governance.GitHash)

	// Already there: a second run moves nothing.
	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Contains(t, out, "governance repo: "+gov+" (from config) at v0.2.0 (already there)")
}

// A dirty user clone stops the update before anything is written, and
// --force overwrites it.
func TestFrameworkUpdate_DirtyGovernanceCloneRefusedUnlessForced(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	gov := filepath.Join(t.TempDir(), "gov")
	gitRun(t, "", "clone", "-q", governanceOrigin(t), gov)
	gitRun(t, gov, "reset", "-q", "--hard", "v0.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(gov, "governance", "patterns", "index.yaml"), []byte("mine\n"), 0o644))
	root := committedProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.local.yaml"),
		[]byte("governance_repository_path: "+gov+"\n"), 0o644))
	head := gitRun(t, root, "rev-parse", "HEAD")

	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.Error(t, err, out)
	code, _ := ExitCode(err)
	require.Equal(t, exitCodeFrameworkValidation, code)
	require.Contains(t, err.Error(), "governance_clone_dirty")
	require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"), "nothing installed, nothing committed")
	require.NoFileExists(t, framework.MetadataPath(root))
	require.Equal(t, "mine\n", governanceIndex(t, gov), "a refusal touches nothing")

	out, err = runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap", "--force-clone")
	require.NoError(t, err, out)
	require.Equal(t, "# v0.2.0\n", governanceIndex(t, gov))
}

// With nothing set, ape clones its own copies into the user cache and keeps
// them at the release; `config resolve` then hands skills the cache path.
func TestFrameworkUpdate_ClonesIntoTheCacheWhenNothingIsSet(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the cache directory is pointed at a temp dir through XDG_CACHE_HOME, which only Linux reads")
	}
	withApeVersion(t, "0.4.0")
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("APEX_FRAMEWORK_REPO", "")
	fw := releasedFramework(t, "v1.0.0")
	t.Setenv("APEX_FRAMEWORK_URL", "file://"+fw)
	govOrigin := governanceOrigin(t)
	root := committedProject(t)
	cfg, err := os.ReadFile(filepath.Join(root, "_apex", "config.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"),
		append(cfg, []byte("governance_repository_url: file://"+govOrigin+"\n")...), 0o644))
	gitCommitAll(t, root, "governance url")

	out, err := runFramework(t, "setup", "", root, "--no-bootstrap")
	require.NoError(t, err, out)
	fwCache := filepath.Join(cache, "ape", "framework", "file", filepath.FromSlash(fw[1:]))
	govCache := filepath.Join(cache, "ape", "governance", "file", filepath.FromSlash(govOrigin[1:]))
	require.Contains(t, out, "framework repo: "+fwCache+" (ape's cache) at v1.0.0")
	require.Equal(t, "# v0.2.0\n", governanceIndex(t, govCache))

	resolved, err := resolveMonotonic(root)
	require.NoError(t, err)
	require.Equal(t, govCache, resolved.GovernanceRepositoryPath)
	require.Equal(t, "cache", resolved.GovernanceRepositorySource)
}

// The framework's ship repo holds promotion commits past its last tag, and
// whoever releases needs it "clean, on main": a clone of the user's that is
// ahead of the release is never moved, only reported.
func TestFrameworkUpdate_UserCloneAheadOfTheReleaseIsNotMoved(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(fw, "PROMOTION"), []byte("x"), 0o644))
	gitCommitAll(t, fw, "promotion, not tagged yet")
	tip := gitRun(t, fw, "rev-parse", "HEAD")
	root := committedProject(t)

	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.NoError(t, err, out)
	require.Contains(t, out, "(not moved: main is 1 commit(s) past v1.0.0)")
	require.Equal(t, tip, gitRun(t, fw, "rev-parse", "HEAD"))
	require.Equal(t, "main", gitRun(t, fw, "rev-parse", "--abbrev-ref", "HEAD"))
	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, "v1.0.0", meta.Framework.VersionTag, "the release is installed all the same")
}

// A refused install leaves the clone where it was: the move is the last
// step before the install writes.
func TestFrameworkInstall_RefusedInstallMovesNoClone(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeApeCommands), []byte("min_ape_version: 9.0.0\n"), 0o644))
	gitCommitAll(t, fw, "needs a newer ape")
	gitRun(t, fw, "tag", "v1.1.0")
	gitRun(t, fw, "reset", "-q", "--hard", "v1.0.0")
	before := gitRun(t, fw, "rev-parse", "HEAD")
	root := committedProject(t)

	_, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.Error(t, err)
	code, _ := ExitCode(err)
	require.Equal(t, exitCodeApeBelowMinimum, code)
	require.Equal(t, before, gitRun(t, fw, "rev-parse", "HEAD"), "refused, so not moved")
}

// An update that keeps a newer rc installs nothing, so it moves nothing: the
// clone and the install must not tell two different stories.
func TestFrameworkUpdate_KeptCandidateMovesNoClone(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeSkills, "apex-foo", "SKILL.md"), []byte("# rc\n"), 0o644))
	gitCommitAll(t, fw, "rc")
	gitRun(t, fw, "tag", "v1.1.0-rc.1")
	root := committedProject(t)
	_, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap", "--version", "v1.1.0-rc.1")
	require.NoError(t, err)
	at := gitRun(t, fw, "rev-parse", "HEAD")

	out, err := runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.NotContains(t, out, "framework repo:")
	require.Equal(t, at, gitRun(t, fw, "rev-parse", "HEAD"))
}

// The eval's real setup: governance_repository_path is a `git archive`
// export of a tag, with no .git. It is read as it is — no clone (not even
// of a configured URL), no fetch, no record, exit 0 — and a second update
// still changes nothing.
func TestFrameworkUpdate_GovernanceDirThatIsNotAGitCloneIsReadAsIs(t *testing.T) {
	withApeVersion(t, "0.4.0")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fw := releasedFramework(t, "v1.0.0")
	export := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(export, "governance", "patterns"), 0o755))
	root := committedProject(t)
	cfg, err := os.ReadFile(filepath.Join(root, "_apex", "config.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"),
		append(cfg, []byte("governance_repository_url: https://example.invalid/never/cloned.git\n")...), 0o644))
	gitCommitAll(t, root, "url")
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.local.yaml"),
		[]byte("governance_repository_path: "+export+"\n"), 0o644))

	out, err := runFramework(t, "setup", fw, root, "--no-bootstrap")
	require.NoError(t, err, out)
	require.Contains(t, out, "governance repo: "+export+" (not a git clone: read as-is)")
	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Nil(t, meta.Governance)
	require.NoDirExists(t, filepath.Join(export, ".git"))

	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Contains(t, out, "commit: nothing changed, nothing committed")
}

package apecmd

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

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// releasedFramework is fakeFrameworkForContract tagged as a release.
func releasedFramework(t *testing.T, tag string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fw := t.TempDir()
	fakeFrameworkForContract(t, fw)
	gitRun(t, fw, "tag", "-a", tag, "-m", tag)
	return fw
}

// committedProject is a git project with a committed baseline and one
// untracked file of the user's that no install may commit.
func committedProject(t *testing.T) string {
	t.Helper()
	root := newTestProject(t, realProjectConfig)
	gitInit(t, root)
	gitCommitAll(t, root, "project baseline")
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("mine"), 0o644))
	return root
}

func runFramework(t *testing.T, sub, repo, root string, args ...string) (string, error) {
	t.Helper()
	r, cwd := repo, root
	cmd := newFrameworkUpdateCmd(&r, &cwd)
	if sub == "setup" {
		cmd = newFrameworkSetupCmd(&r, &cwd)
	}
	cmd.SetArgs(args)
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return buf.String(), err
}

func withApeVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

// TestContract_FrameworkUpdateNoCommitWritesNoCommits keeps the v0.3.x
// property available: with --no-commit, the whole result sits in the
// working tree for one `git diff`, and the project's history is untouched.
func TestContract_FrameworkUpdateNoCommitWritesNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fwRepo := t.TempDir()
	fakeFrameworkForContract(t, fwRepo)
	root := newTestProject(t, realProjectConfig)
	gitInit(t, root)
	writeLegacyLedger(t, root, legacyLedgerFixture)
	gitCommitAll(t, root, "project baseline")
	before := gitLog(t, root)

	_, err := runFramework(t, "setup", fwRepo, root, "--from-worktree", "--no-commit", "--no-fetch", "--no-bootstrap")
	require.NoError(t, err)
	out, err := runFramework(t, "update", fwRepo, root, "--from-worktree", "--no-commit", "--no-fetch")
	require.NoError(t, err, out)

	require.Equal(t, before, gitLog(t, root), "--no-commit wrote a commit into the project:\n%s", out)
	require.False(t, pendingMigrations(root)[0].Pending,
		"the migration must have run for the no-commit assertion to mean anything")
	require.NotEmpty(t, gitRun(t, root, "status", "--porcelain"), "the result is left in the working tree")
}

// The default install commits the release it installed — one commit, with
// the trailers the framework's release record reads — and never a file of
// the user's that was already untracked.
func TestFrameworkInstall_CommitsTheRelease(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	root := committedProject(t)
	baseline := gitRun(t, root, "rev-parse", "HEAD")

	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.NoError(t, err, out)
	require.Equal(t, baseline, gitRun(t, root, "rev-parse", "HEAD~1"), "exactly one commit")
	msg := gitRun(t, root, "log", "-1", "--format=%B")
	require.Contains(t, msg, "chore(framework): install APEX framework v1.0.0")
	require.Contains(t, msg, "Framework-Version: v1.0.0")
	require.Contains(t, msg, "Framework-Commit: "+gitRun(t, fw, "rev-parse", "v1.0.0^{commit}"))
	require.Contains(t, msg, "Generator: ape framework update")
	require.Equal(t, "?? notes.txt", gitRun(t, root, "status", "--porcelain"),
		"only the user's own untracked file is left, uncommitted")

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, framework.SourceTag, meta.Framework.Source)

	// Nothing new: no commit.
	head := gitRun(t, root, "rev-parse", "HEAD")
	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"), "an update that changed nothing commits nothing:\n%s", out)

	// A newer release: one update commit. Unreleased work on the
	// framework's main is never installed.
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeSkills, "apex-foo", "SKILL.md"), []byte("# v1.1.0\n"), 0o644))
	gitCommitAll(t, fw, "next")
	gitRun(t, fw, "tag", "v1.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeSkills, "apex-foo", "SKILL.md"), []byte("# unreleased\n"), 0o644))
	gitCommitAll(t, fw, "unreleased")

	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Contains(t, gitRun(t, root, "log", "-1", "--format=%s"), "chore(framework): update APEX framework to v1.1.0")
	b, err := os.ReadFile(filepath.Join(root, framework.ProjectSkillsDir, "apex-foo", "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, "# v1.1.0\n", string(b))
}

func TestFrameworkInstall_RefusesADirtyTreeBeforeWriting(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	root := committedProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte("edited: true\n"), 0o644))

	_, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.Equal(t, exitCodeProjectSkillsModified, exitCodeOf(t, err))
	_, statErr := os.Stat(framework.MetadataPath(root))
	require.True(t, os.IsNotExist(statErr), "nothing is written")
}

// Below the release's min_ape_version, without a terminal: no prompt, no
// update, nothing written, exit 11 and the command to run.
func TestFrameworkInstall_BelowMinimumWithoutATerminal(t *testing.T) {
	withApeVersion(t, "0.3.1")
	fw := t.TempDir()
	fakeFrameworkForContract(t, fw)
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.ProjectApeCommands), []byte("min_ape_version: 99.0.0\n"), 0o644))
	gitCommitAll(t, fw, "needs a future ape")
	gitRun(t, fw, "tag", "v1.0.0")
	root := committedProject(t)

	_, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.Equal(t, exitCodeApeBelowMinimum, exitCodeOf(t, err))
	_, statErr := os.Stat(framework.MetadataPath(root))
	require.True(t, os.IsNotExist(statErr), "nothing is written")

	// An rc of exactly the minimum passes it.
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.ProjectApeCommands), []byte("min_ape_version: 0.4.0\n"), 0o644))
	gitCommitAll(t, fw, "needs 0.4.0")
	gitRun(t, fw, "tag", "v1.1.0")
	withApeVersion(t, "0.4.0-rc.1")
	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.NoError(t, err, out)
}

func TestFrameworkInstall_UsageErrors(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"--version", "v1.0.0", "--from-worktree"},
		{"--version", "v1.0.0-beta"},
	} {
		_, err := runFramework(t, "update", t.TempDir(), root, args...)
		require.Equal(t, ExitUsage, exitCodeOf(t, err), "%v", args)
	}
}

func TestDoctor_FrameworkApeVersion(t *testing.T) {
	cases := []struct {
		ape, manifest string
		want          DoctorStatus
	}{
		{"0.4.0", "min_ape_version: 0.4.0\n", StatusOK},
		{"0.4.0-rc.1", "min_ape_version: 0.4.0\n", StatusOK},
		{"0.3.1", "min_ape_version: 0.4.0\n", StatusFail},
		{"dev", "min_ape_version: 0.4.0\n", StatusWarn},
		{"0.3.1", "required_commands: []\n", StatusInfo},
	}
	for _, c := range cases {
		withApeVersion(t, c.ape)
		root := commandSurfaceProject(t, c.manifest)
		res := checkFrameworkApeVersion(context.Background(), doctorEnv{ProjectRoot: root})
		require.Equal(t, c.want, res.Status, "ape %s, %q: %s", c.ape, c.manifest, res.Message)
	}
}

// A default update never goes backwards from a candidate installed by name.
func TestFrameworkUpdate_KeepsACandidateAheadOfTheNewestRelease(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeSkills, "apex-foo", "SKILL.md"), []byte("# rc\n"), 0o644))
	gitCommitAll(t, fw, "rc")
	gitRun(t, fw, "tag", "v1.1.0-rc.1")
	root := committedProject(t)
	_, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap", "--version", "v1.1.0-rc.1")
	require.NoError(t, err)
	head := gitRun(t, root, "rev-parse", "HEAD")

	out, err := runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"), "nothing reinstalled, nothing committed")
	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, "v1.1.0-rc.1", meta.Framework.VersionTag)

	_, err = runFramework(t, "update", fw, root, "--no-fetch", "--version", "v1.0.0")
	require.NoError(t, err)
	meta, err = framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, "v1.0.0", meta.Framework.VersionTag, "--version goes back when asked")
}

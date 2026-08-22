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

func readGitignore(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, framework.ProjectGitignore))
	require.NoError(t, err)
	return string(data)
}

func TestEnsureLockIgnored_CreatesTheFileWhenAbsent(t *testing.T) {
	root := t.TempDir()

	added, err := framework.EnsureLockIgnored(context.Background(), root)
	require.NoError(t, err)
	require.True(t, added)

	body := readGitignore(t, root)
	require.Contains(t, body, framework.GitignoreLockPattern)
	require.Contains(t, body, "ape sprint reconcile",
		"a bare pattern is a line someone eventually deletes; the reason is what stops that")
}

// TestEnsureLockIgnored_AppendsAndPreserves: .gitignore is hand-curated and
// ape does not own it, so every existing byte survives and the entry lands
// at the end.
func TestEnsureLockIgnored_AppendsAndPreserves(t *testing.T) {
	root := t.TempDir()
	existing := "# my rules\n/dist/\n*.tmp\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(root, framework.ProjectGitignore), []byte(existing), 0o644))

	added, err := framework.EnsureLockIgnored(context.Background(), root)
	require.NoError(t, err)
	require.True(t, added)

	body := readGitignore(t, root)
	require.True(t, strings.HasPrefix(body, existing), "existing content is preserved byte-for-byte")
	require.Contains(t, body, framework.GitignoreLockPattern)
}

func TestEnsureLockIgnored_AddsTheMissingNewlineFirst(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, framework.ProjectGitignore), []byte("*.tmp"), 0o644))

	_, err := framework.EnsureLockIgnored(context.Background(), root)
	require.NoError(t, err)

	body := readGitignore(t, root)
	require.NotContains(t, body, "*.tmp#", "a file with no trailing newline must not gain a joined line")
	require.Contains(t, body, "*.tmp\n")
}

// TestEnsureLockIgnored_IsIdempotent is the property `ape framework update`
// depends on: it runs this on every update, and a project must not collect
// an entry per run.
func TestEnsureLockIgnored_IsIdempotent(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	added, err := framework.EnsureLockIgnored(ctx, root)
	require.NoError(t, err)
	require.True(t, added)
	first := readGitignore(t, root)

	added, err = framework.EnsureLockIgnored(ctx, root)
	require.NoError(t, err)
	require.False(t, added, "the second run reports it wrote nothing")
	require.Equal(t, first, readGitignore(t, root), "and really wrote nothing")
}

// TestEnsureLockIgnored_RespectsAProjectsOwnBroaderRule: a project that
// already wrote `*.lock` has solved this, and must not collect a redundant
// second entry. Only git can answer that, which is why it is asked.
func TestEnsureLockIgnored_RespectsAProjectsOwnBroaderRule(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	gitInitForTest(t, root)
	require.NoError(t, os.WriteFile(
		filepath.Join(root, framework.ProjectGitignore), []byte("*.lock\n"), 0o644))

	added, err := framework.EnsureLockIgnored(context.Background(), root)
	require.NoError(t, err)
	require.False(t, added, "the sidecar is already ignored by the project's own rule")
	require.Equal(t, "*.lock\n", readGitignore(t, root))
}

// TestEnsureLockIgnored_RespectsAnExcludeFile covers the reason this asks
// git rather than reading .gitignore: the rule can live somewhere else
// entirely, and a hand-rolled pattern match would never find it.
func TestEnsureLockIgnored_RespectsAnExcludeFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	gitInitForTest(t, root)
	info := filepath.Join(root, ".git", "info")
	require.NoError(t, os.MkdirAll(info, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(info, "exclude"),
		[]byte(framework.GitignoreLockPattern+"\n"), 0o644))

	added, err := framework.EnsureLockIgnored(context.Background(), root)
	require.NoError(t, err)
	require.False(t, added, ".git/info/exclude already covers it")
	require.NoFileExists(t, filepath.Join(root, framework.ProjectGitignore),
		"and no .gitignore was invented to say so")
}

// TestEnsureLockIgnored_PatternIsNarrowerThanStarLock is the safety
// property. `*.lock` is fine in ape's own repo and destructive in a user's:
// Cargo.lock, flake.lock, Gemfile.lock, poetry.lock and composer.lock all
// match it and all belong in history. Ape writing a pattern broader than
// the problem would be ape's fault, weeks later, as an unreproducible build.
func TestEnsureLockIgnored_PatternIsNarrowerThanStarLock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	require.NotEqual(t, "*.lock", framework.GitignoreLockPattern)

	root := t.TempDir()
	gitInitForTest(t, root)
	_, err := framework.EnsureLockIgnored(context.Background(), root)
	require.NoError(t, err)

	ignored := func(rel string) bool {
		cmd := exec.CommandContext(context.Background(), "git", "check-ignore", "-q", "--", rel)
		cmd.Dir = root
		return cmd.Run() == nil
	}
	for _, keep := range []string{"Cargo.lock", "flake.lock", "Gemfile.lock", "poetry.lock", "composer.lock"} {
		require.False(t, ignored(keep), "%s is a dependency lockfile and belongs in history", keep)
	}
	// And the one it is for is ignored at whatever depth it appears, since
	// implementation_folder is configurable.
	require.True(t, ignored("development/implementation/sprint-status.yaml.lock"))
	require.True(t, ignored("elsewhere/sprint-status.yaml.lock"))
}

func gitInitForTest(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

// TestSetupAndUpdate_EnsureTheIgnoreEntry ties the helper to the two verbs
// that must run it. Update is the "verify and fix" pass — a project
// installed before this existed gets the entry on its next update, without
// having to know it was missing.
func TestSetupAndUpdate_EnsureTheIgnoreEntry(t *testing.T) {
	fw := t.TempDir()
	fakeFramework(t, fw, "v1.0.0")
	proj := newProject(t)
	ctx := context.Background()

	res, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		Bootstrapper: staticBootstrap("proj"), Now: fixedNow,
	})
	require.NoError(t, err)
	require.True(t, res.Summary.GitignoreLockAdded, "setup writes it")
	require.Contains(t, readGitignore(t, proj), framework.GitignoreLockPattern)

	res, err = framework.Update(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true, Now: fixedNow,
	})
	require.NoError(t, err)
	require.False(t, res.Summary.GitignoreLockAdded,
		"update is idempotent: already ignored, nothing written")

	// The verify-and-fix path: a project that predates the entry.
	require.NoError(t, os.Remove(filepath.Join(proj, framework.ProjectGitignore)))
	res, err = framework.Update(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true, Now: fixedNow,
	})
	require.NoError(t, err)
	require.True(t, res.Summary.GitignoreLockAdded, "update repairs a missing entry")
	require.Contains(t, readGitignore(t, proj), framework.GitignoreLockPattern)
}

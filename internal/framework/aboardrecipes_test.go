package framework_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// A recipe is a markdown file with frontmatter whose `name` equals the file
// stem. The body is not exercised here — aboard parses it, and its own suite
// owns that — so this is the minimum that is a recipe rather than a file.
const aboardRecipeBody = `---
name: human-checklist
description: "Steps only a person can carry out."
when_to_use: "When handing over work only a human can do."
---

Put the steps in a ui checklist and read back which ones they ticked.
`

// seedRecipes writes a library into the framework repo and commits it. The
// install refuses a dirty framework repo, so every fixture has to land in a
// commit.
func seedRecipes(t *testing.T, fw string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(fw, framework.SubtreeAboardRecipes)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	if len(files) == 0 {
		// git does not track an empty directory, so there is nothing to
		// commit and `commitAll` would fail on a clean tree. The directory
		// still exists on disk, which is the whole point of that case.
		return
	}
	commitAll(t, fw, "add aboard recipe library")
}

func TestSetup_InstallsAboardRecipes(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	seedRecipes(t, fw, map[string]string{"human-checklist.md": aboardRecipeBody})

	res, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Summary.AboardRecipesInstalled)
	require.Equal(t,
		[]string{filepath.Join(framework.ProjectAboardRecipesDir, "human-checklist.md")},
		res.Summary.AboardRecipePaths)

	got, err := os.ReadFile(filepath.Join(proj, framework.ProjectAboardRecipesDir, "human-checklist.md"))
	require.NoError(t, err)
	require.Equal(t, aboardRecipeBody, string(got), "copied byte-for-byte")
}

// The destination is two directories deep and nothing else in a project
// creates either of them, so the copy has to make the path itself. CopyFile
// does not.
func TestSetup_CreatesTheRecipeDirectory(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	seedRecipes(t, fw, map[string]string{"human-checklist.md": aboardRecipeBody})

	_, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(proj, "_apex", "aboard", "recipes"))
}

// Version skew, not a fault: a framework shipping no library installs none,
// and every BUILT-IN recipe still reaches the project inside the ape binary.
func TestSetup_NoRecipeLibraryIsNotAFailure(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.11.0")

	res, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.Zero(t, res.Summary.AboardRecipesInstalled)
	require.NoDirExists(t, filepath.Join(proj, framework.ProjectAboardRecipesDir))
}

// An empty library must leave no directory behind. aboard reports a recipe's
// SCOPE by the directory it came from, so an `_apex/aboard/recipes/` that
// exists and holds nothing reads as a library somebody emptied.
func TestSetup_AnEmptyLibraryLeavesNoDirectory(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	seedRecipes(t, fw, map[string]string{})

	res, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.Zero(t, res.Summary.AboardRecipesInstalled)
	require.NoDirExists(t, filepath.Join(proj, framework.ProjectAboardRecipesDir))
}

// Only .md, and only the top level. A recipe is one flat markdown file with
// frontmatter; the directory is a library, not a tree.
func TestSetup_CopiesOnlyMarkdownAndDoesNotRecurse(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	seedRecipes(t, fw, map[string]string{
		"human-checklist.md": aboardRecipeBody,
		"notes.txt":          "not a recipe",
		"template.json":      `{"tabs":[]}`,
	})
	nested := filepath.Join(fw, framework.SubtreeAboardRecipes, "drafts")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "wip.md"), []byte(aboardRecipeBody), 0o644))
	commitAll(t, fw, "add non-recipes beside the library")

	res, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Summary.AboardRecipesInstalled)

	dst := filepath.Join(proj, framework.ProjectAboardRecipesDir)
	require.FileExists(t, filepath.Join(dst, "human-checklist.md"))
	require.NoFileExists(t, filepath.Join(dst, "notes.txt"))
	require.NoFileExists(t, filepath.Join(dst, "template.json"))
	require.NoDirExists(t, filepath.Join(dst, "drafts"))
}

// A project set up against an older framework gains the library on its next
// update, with no ape release in between — the same reason the ape-commands
// manifest is installed ahead of the check that reads it.
func TestUpdate_PicksUpARecipeLibraryAddedLater(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.11.0")

	_, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(proj, framework.ProjectAboardRecipesDir))

	seedRecipes(t, fw, map[string]string{"human-checklist.md": aboardRecipeBody})

	res, err := framework.Update(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Summary.AboardRecipesInstalled)
	require.FileExists(t, filepath.Join(proj, framework.ProjectAboardRecipesDir, "human-checklist.md"))
}

// Refreshed, not synced — and this is the case that decides it. The directory
// is also where a WORKSPACE keeps its own recipes (aboard documents
// `_apex/aboard/recipes/` as "for every project in a workspace"), so a sync
// would delete work ape never installed. A framework-owned name is
// overwritten; everything else is the project's and survives.
func TestUpdate_RefreshesOwnRecipesAndKeepsTheProjectsOwn(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	seedRecipes(t, fw, map[string]string{"human-checklist.md": aboardRecipeBody})

	_, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)

	dst := filepath.Join(proj, framework.ProjectAboardRecipesDir)
	// The project writes one of its own, and edits the framework's.
	mine := filepath.Join(dst, "our-own-move.md")
	require.NoError(t, os.WriteFile(mine, []byte("---\nname: our-own-move\n---\nours\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "human-checklist.md"), []byte("locally hacked"), 0o644))

	res, err := framework.Update(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Summary.AboardRecipesInstalled)

	refreshed, err := os.ReadFile(filepath.Join(dst, "human-checklist.md"))
	require.NoError(t, err)
	require.Equal(t, aboardRecipeBody, string(refreshed), "the framework's own copy is restored")

	require.FileExists(t, mine, "update deleted a recipe the project owns")
}

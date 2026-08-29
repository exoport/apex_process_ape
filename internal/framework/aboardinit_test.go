package framework_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

func setupInto(t *testing.T, fw, proj string) *framework.UpdateResult {
	t.Helper()
	res, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	return res
}

func TestSetup_CreatesTheBoard(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")

	res := setupInto(t, fw, proj)
	require.True(t, res.Summary.AboardCreated)
	require.True(t, res.Summary.AboardGitignoreSeeded)
	require.Empty(t, res.Summary.AboardParentRoot)

	require.FileExists(t, aboard.Root(proj).StateFile(""), "no board document")
	require.DirExists(t, filepath.Join(proj, framework.ProjectAboardDir))
}

// The whole point of the inner ignore file: the DIRECTORY is committed and
// nothing in it is. Ignoring `.aboard/` from the repo root instead would hide
// this file too, so the folder would be missing on a fresh clone.
func TestSetup_TheBoardGitignoreIgnoresEverythingButItself(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	setupInto(t, fw, proj)

	got, err := os.ReadFile(filepath.Join(proj, framework.ProjectAboardGitignore))
	require.NoError(t, err)
	require.Equal(t, framework.AboardGitignore, string(got))
	require.Contains(t, string(got), "*")
	require.Contains(t, string(got), "!.gitignore")
}

// Destroying a board is the one mistake here with no undo, so an existing
// document is never overwritten — aboard.Init refuses, and the install must
// treat that as "already done" rather than raise it.
func TestUpdate_NeverOverwritesAnExistingBoard(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	setupInto(t, fw, proj)

	// Put something in the board that a re-init would destroy.
	state := aboard.Root(proj).StateFile("")
	marker := `{"version":1,"rev":9,"nextId":3,"tabs":[{"id":"ab2","name":"mine","type":"notes","state":{}}]}`
	require.NoError(t, os.WriteFile(state, []byte(marker), 0o644))

	res, err := framework.Update(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.False(t, res.Summary.AboardCreated, "update claimed to create a board that existed")

	after, err := os.ReadFile(state)
	require.NoError(t, err)
	require.Equal(t, marker, string(after), "update overwrote the project's board")
}

// The verify-and-fix pass: a project set up before the board existed gains it
// on the next update, and a project whose ignore file was deleted gets it back.
func TestUpdate_AddsTheBoardToAProjectThatHasNone(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	setupInto(t, fw, proj)

	require.NoError(t, os.RemoveAll(filepath.Join(proj, framework.ProjectAboardDir)))

	res, err := framework.Update(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.True(t, res.Summary.AboardCreated)
	require.True(t, res.Summary.AboardGitignoreSeeded)
	require.FileExists(t, filepath.Join(proj, framework.ProjectAboardGitignore))
}

// Seeded, not refreshed — the same terms as _apex/config.yaml. A project that
// edited it meant to.
func TestUpdate_DoesNotRewriteAnEditedBoardGitignore(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	setupInto(t, fw, proj)

	ignore := filepath.Join(proj, framework.ProjectAboardGitignore)
	mine := framework.AboardGitignore + "\n!theme.json\n"
	require.NoError(t, os.WriteFile(ignore, []byte(mine), 0o644))

	res, err := framework.Update(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.False(t, res.Summary.AboardGitignoreSeeded)

	after, err := os.ReadFile(ignore)
	require.NoError(t, err)
	require.Equal(t, mine, string(after), "update discarded the project's own ignore rules")
}

// A board root ABOVE the project owns it. aboard refuses to nest one root
// inside another — the inner board would be invisible to every command run
// from the outer root — so this is reported, not raised as an error that would
// fail the whole install.
func TestSetup_DoesNotNestABoardUnderAParentThatHasOne(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	_, err := aboard.Init(aboard.InitConfig{Dir: parent}, framework.AboardInvocation)
	require.NoError(t, err)

	proj := filepath.Join(parent, "nested")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.12.0")

	res := setupInto(t, fw, proj)
	require.False(t, res.Summary.AboardCreated)
	require.NotEmpty(t, res.Summary.AboardParentRoot, "the parent's board was not reported")
	require.NoDirExists(t, filepath.Join(proj, framework.ProjectAboardDir))
}

// The board ape creates must be one the board itself accepts: same root, same
// derived port, discoverable with no arguments.
func TestSetup_TheBoardItCreatesIsFoundByAboard(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	setupInto(t, fw, proj)

	found, err := aboard.FindRoot(proj)
	require.NoError(t, err, "aboard cannot find the root ape just created")

	resolved := proj
	if r, evalErr := filepath.EvalSymlinks(proj); evalErr == nil {
		resolved = r
	}
	require.Equal(t, resolved, found.String())
}

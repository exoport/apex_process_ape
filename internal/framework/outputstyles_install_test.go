package framework_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/outputstyles"
	"github.com/stretchr/testify/require"
)

const outputStylesBody = `skill,style
apex-adr-survey,Concise
apex-story-batch-dev,Default
`

func seedOutputStyles(t *testing.T, fw, body string) {
	t.Helper()
	path := filepath.Join(fw, framework.SubtreeOutputStyles)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	commitAll(t, fw, "add output-style table")
}

// The integration point that fails open on its own: the framework can
// ship the table and `ape task` can read it, and the feature still does
// nothing unless the installer puts the file in the project.
func TestSetup_InstallsOutputStyles(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	seedOutputStyles(t, fw, outputStylesBody)

	res, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.True(t, res.Summary.OutputStylesInstalled)

	got, err := os.ReadFile(filepath.Join(proj, framework.ProjectOutputStyles))
	require.NoError(t, err)
	require.Equal(t, outputStylesBody, string(got), "copied byte-for-byte")
}

// The installed file has to be the one the reader looks for. These are
// two constants in two packages, and a divergence between them would
// install a table nothing ever opens.
func TestSetup_InstalledPathIsTheOneApeReads(t *testing.T) {
	t.Parallel()
	require.Equal(t, outputstyles.TableFile, framework.ProjectOutputStyles)

	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")
	seedOutputStyles(t, fw, outputStylesBody)

	_, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)

	tbl, err := outputstyles.Load(proj)
	require.NoError(t, err)
	require.Equal(t, 2, tbl.Len())
	style, ok := tbl.Style("apex-adr-survey")
	require.True(t, ok)
	require.Equal(t, "Concise", style)
}

// Version skew in the other direction: an ape that reads the table
// against a framework that predates it must install nothing and say so,
// not fail the update.
func TestSetup_NoOutputStyleTableIsNotAFailure(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFramework(t, fw, "v0.12.0")

	res, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)
	require.False(t, res.Summary.OutputStylesInstalled)

	_, err = os.Stat(filepath.Join(proj, framework.ProjectOutputStyles))
	require.True(t, os.IsNotExist(err))

	tbl, loadErr := outputstyles.Load(proj)
	require.NoError(t, loadErr)
	require.Equal(t, 0, tbl.Len())
}

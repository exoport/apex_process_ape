package apecmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// v1FloorProject is a v1 project (development/project.md, _apex/state/)
// with the framework's one-key _apex/ape-commands.yaml (D47 Q1).
func v1FloorProject(t *testing.T, manifest string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"development", "_apex/state"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "development", "project.md"), []byte("# P\n"), 0o644))
	if manifest != "" {
		require.NoError(t, os.WriteFile(filepath.Join(root, framework.ProjectApeCommands), []byte(manifest), 0o644))
	}
	return root
}

// H1: the floor is judged as `ape framework update` judges it — an rc of
// exactly the minimum meets it, a dev build is warned about and runs.
func TestV1ApeFloor(t *testing.T) {
	cases := []struct {
		ape, manifest string
		refused       bool
		warned        bool
	}{
		{"1.0.0", "min_ape_version: 1.0.0-rc.1\n", false, false},
		{"1.0.0-rc.1", "min_ape_version: 1.0.0-rc.1\n", false, false},
		{"1.0.0-rc.1", "min_ape_version: 1.0.0\n", false, false},
		{"0.10.0", "min_ape_version: 1.0.0-rc.1\n", true, false},
		{"1.0.0-rc.1", "min_ape_version: 1.0.0-rc.2\n", true, false},
		{"dev", "min_ape_version: 1.0.0-rc.1\n", false, true},
		{"0.10.1-0.20261016120000-abcdef123456", "min_ape_version: 1.0.0\n", false, true},
		{"0.10.0", "", false, false},
		{"0.10.0", "required_commands: []\n", false, false},
	}
	for _, c := range cases {
		withApeVersion(t, c.ape)
		root := v1FloorProject(t, c.manifest)
		var warn bytes.Buffer
		err := v1ApeFloor(root, &warn)
		if c.refused {
			fe, ok := errorsAsFail(err)
			require.True(t, ok, "ape %s, %q: %v", c.ape, c.manifest, err)
			require.Equal(t, "ape.below-floor", fe.Code)
			require.Contains(t, fe.Msg, "update ape first")
			require.Equal(t, ExitRunFailed, taskPreflightExit(&taskPreflightError{err: err}))
			require.Equal(t, apexv1.ActionPerson, apexv1.ActionFor(fe.Code))
		} else {
			require.NoError(t, err, "ape %s, %q", c.ape, c.manifest)
		}
		require.Equal(t, c.warned, warn.Len() > 0, "ape %s, %q: %s", c.ape, c.manifest, warn.String())
	}

	withApeVersion(t, "1.0.0")
	root := v1FloorProject(t, "min_ape_version: [\n")
	require.Error(t, v1ApeFloor(root, &bytes.Buffer{}), "an unparseable file refuses")
}

// ape task in a v1 project refuses below the floor before anything
// spawns, and returns the refusal rather than exiting.
func TestDispatchTask_V1RefusesBelowTheFloor(t *testing.T) {
	withApeVersion(t, "0.10.0")
	root := v1FloorProject(t, "min_ape_version: 1.0.0-rc.1\n")
	res, err := dispatchTask(context.Background(), taskOptions{skill: "apex-orchestrator", projectRoot: root})
	var preflight *taskPreflightError
	require.ErrorAs(t, err, &preflight)
	require.Contains(t, err.Error(), "older than the installed framework's min_ape_version 1.0.0-rc.1")
	require.Equal(t, ExitRunFailed, taskPreflightExit(err))
	require.Equal(t, taskRun{}, res)
}

// ape doctor's existing min_ape_version check reads the one-key file of a
// v1 project as it reads a v0 framework's manifest.
func TestDoctor_FrameworkApeVersion_V1Project(t *testing.T) {
	withApeVersion(t, "0.10.0")
	root := v1FloorProject(t, "min_ape_version: 1.0.0-rc.1\n")
	res := checkFrameworkApeVersion(context.Background(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusFail, res.Status, res.Message)
	withApeVersion(t, "1.0.0-rc.1")
	res = checkFrameworkApeVersion(context.Background(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusOK, res.Status, res.Message)
}

func errorsAsFail(err error) (*apexv1.FailError, bool) {
	return errors.AsType[*apexv1.FailError](err)
}

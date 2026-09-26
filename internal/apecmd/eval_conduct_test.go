package apecmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/effort"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/stretchr/testify/require"
)

// The conductor's session must carry NO APE_SESSION: the orchestrator HALTs
// on activation when `printenv APE_SESSION` prints anything, so a marker
// would make the eval's halt stage pass for the wrong reason. Every other
// prompt-path session carries one.
func TestConductor_NeverCarriesTheSessionMarker(t *testing.T) {
	plans := []effort.Plan{
		effort.Decide("", "", nil, ""),
		effort.Decide("high", effort.SourceFlag, nil, ""),
		effort.Decide("", "", &effort.Defaults{Version: 1, Fallback: "high"}, "opus"),
	}
	for _, plan := range plans {
		for _, kv := range promptSpawnEnv(plan, true, "20260926-x") {
			require.False(t, strings.HasPrefix(kv, repl.EnvApeSession+"="), "the conductor got %q", kv)
		}
		require.Contains(t, promptSpawnEnv(plan, false, "20260926-x"), "APE_SESSION=prompt/20260926-x",
			"an ordinary prompt session is marked")
	}
}

func runConduct(t *testing.T, args ...string) (code int, msg string) {
	t.Helper()
	cmd := newEvalConductCmd()
	var out bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		return 0, out.String()
	}
	return exitCodeOf(t, err), err.Error()
}

// Refused unless APE_EVAL_HOST=1: nothing an operator runs daily carries
// this path.
func TestConduct_RefusedWithoutTheEvalGate(t *testing.T) {
	t.Setenv(envEvalHost, "")
	t.Setenv(repl.EnvApeSession, "")
	code, msg := runConduct(t, "--request-file", "-")
	require.Equal(t, exitConductNotEvalHost, code)
	require.Contains(t, msg, "APE_EVAL_HOST=1")
}

// Never nests: inside an ape session the host refuses, with its own code.
func TestConduct_RefusedInsideAnApeSession(t *testing.T) {
	t.Setenv(envEvalHost, "1")
	t.Setenv(repl.EnvApeSession, "task/20260926-x")
	code, msg := runConduct(t, "--request-file", "-")
	require.Equal(t, exitConductNested, code)
	require.Contains(t, msg, "task/20260926-x")
}

func TestConduct_RequestFile(t *testing.T) {
	_, err := readConductRequest("", strings.NewReader(""))
	require.ErrorContains(t, err, "required")

	got, err := readConductRequest("-", strings.NewReader("fix the\ntypo in the README\n"))
	require.NoError(t, err)
	require.Equal(t, "fix the typo in the README", got, "one line: a newline would submit early")

	p := filepath.Join(t.TempDir(), "req.txt")
	require.NoError(t, os.WriteFile(p, []byte("  \n"), 0o644))
	_, err = readConductRequest(p, nil)
	require.ErrorContains(t, err, "empty")
}

// The backstops get their own codes; every other outcome keeps prompt's.
func TestConductExitCode(t *testing.T) {
	require.Equal(t, exitConductIdleTimeout, conductExitCode(promptStatusIdleTimeout, ExitRunFailed))
	require.Equal(t, exitConductMaxDuration, conductExitCode(promptStatusMaxDuration, ExitRunFailed))
	require.Equal(t, ExitOK, conductExitCode(promptStatusCompleted, ExitOK))
	require.Equal(t, ExitClaudeDied, conductExitCode(promptStatusClaudeDied, ExitClaudeDied))
}

// Hidden: `ape --help` must not advertise an eval-only host.
func TestEvalGroup_IsHidden(t *testing.T) {
	require.True(t, newEvalCmd().Hidden)
}

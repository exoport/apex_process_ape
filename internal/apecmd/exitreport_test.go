package apecmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Report is the decision main makes about every error, and the one that was
// wrong: every *exitError was treated as already printed, so an error that
// carried an exit code and no printed message exited silently.
func TestReport_PrintsWhatNoCommandPrinted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		err    error
		code   int
		stderr string
	}{
		{"success", nil, ExitOK, ""},
		{"a usage error nobody printed", usageErr(errors.New("--x is required")), ExitUsage, "Error: --x is required\n"},
		{"a failure nobody printed", failErr(errors.New("publish failed")), ExitRunFailed, "Error: publish failed\n"},
		{"a gate verdict with a message", gateErr(5, errors.New("ordinal 9 is out of range")), 5, "Error: ordinal 9 is out of range\n"},
		{"a gate verdict the command printed", gateErr(1, nil), 1, ""},
		{"an error the command printed", reportedErr(ExitUsage, errors.New("already said")), ExitUsage, ""},
		{"a plain error", errors.New("plain"), ExitRunFailed, "Error: plain\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			require.Equal(t, tc.code, Report(tc.err, &stderr))
			require.Equal(t, tc.stderr, stderr.String())
		})
	}
}

// The paths measured silent on the pre-fix binary — exit 2, empty stderr —
// through the real commands and Report, the same route main takes.
func TestReport_TheMeasuredSilentPathsNowSayWhy(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "testdata", "apexproject"))))

	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		want string
	}{
		{"memory show, an ordinal the file does not have", newMemoryShowCmd, []string{"999", "--cwd", root}, "Error: "},
		{"release status, an unknown slice", newReleaseStatusCmd, []string{"--slice", "nope", "--cwd", root}, "Error: "},
		{"story fields, no --select", newStoryFieldsCmd, []string{"--cwd", root}, "Error: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.cmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			require.Error(t, err)

			var stderr bytes.Buffer
			code := Report(err, &stderr)
			require.NotEqual(t, ExitOK, code)
			require.Contains(t, stderr.String(), tc.want,
				"exit %d with nothing on stderr is the defect: %v", code, err)
		})
	}
}

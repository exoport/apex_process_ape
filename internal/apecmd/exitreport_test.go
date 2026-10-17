package apecmd

import (
	"bytes"
	"errors"
	"testing"

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

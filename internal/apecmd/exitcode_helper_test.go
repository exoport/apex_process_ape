package apecmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// exitCodeOf reads the code a command's error carries.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	require.ErrorAs(t, err, &ee, "a verdict travels as an exit code")
	return ee.code
}

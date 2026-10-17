package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelSession(t *testing.T) {
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
	require.Empty(t, ModelSession())
	require.NoError(t, requirePerson("--accept-red"))

	t.Setenv("CLAUDECODE", "1")
	require.Equal(t, "CLAUDECODE", ModelSession())
	t.Setenv("APE_SESSION", "task:abc")
	require.Equal(t, "APE_SESSION", ModelSession())
	err := requirePerson("--accept-red")
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, CodePersonOnly, ue.Code)
	require.Contains(t, ue.Msg, "--accept-red is for a person")
}

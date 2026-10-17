package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "team-memory.md")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestCheckSize_States(t *testing.T) {
	cases := []struct {
		name  string
		size  int
		soft  int64
		hard  int64
		state State
	}{
		{"under soft", 100, 1000, 2000, StateOK},
		{"exactly soft is still ok", 1000, 1000, 2000, StateOK},
		{"over soft", 1001, 1000, 2000, StateOverSoft},
		{"exactly hard is still over-soft", 2000, 1000, 2000, StateOverSoft},
		{"over hard", 2001, 1000, 2000, StateOverHard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, strings.Repeat("x", tc.size))
			c := CheckSize(path, tc.soft, tc.hard)
			require.Equal(t, tc.state, c.State)
			require.True(t, c.Exists)
			require.Equal(t, int64(tc.size), c.Bytes)
			require.Equal(t, int64(tc.size)*10/29, c.EstimatedTokens)
		})
	}
}

func TestCheckSize_AbsentFile(t *testing.T) {
	c := CheckSize(filepath.Join(t.TempDir(), "nope.md"), 0, 0)
	require.Equal(t, StateAbsent, c.State)
	require.False(t, c.Exists)
	require.Zero(t, c.Bytes)
	require.Equal(t, int64(DefaultSoftBudget), c.SoftBudget, "zero means take the default")
	require.Equal(t, int64(DefaultHardCeiling), c.HardCeiling)
}

// TestCheckSize_DoesNotReadTheFile is what makes the check cheap enough
// to run on every retrospective: a file far past the Read cap is
// classified from its stat alone.
func TestCheckSize_DoesNotReadTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "team-memory.md")
	f, err := os.Create(path)
	require.NoError(t, err)
	// A sparse file: 8 MiB of logical size, ~no blocks. Reading it would
	// be obvious; stat is instant.
	require.NoError(t, f.Truncate(8<<20))
	require.NoError(t, f.Close())

	c := CheckSize(path, 0, 0)
	require.Equal(t, StateOverHard, c.State)
	require.Equal(t, int64(8<<20), c.Bytes)
	require.Greater(t, c.Bytes, int64(ReadCap), "well past the Read cap and still classified")
}

// TestCheckSize_ReferenceProjectSize is the acceptance case: the real
// 431,950-byte file reports over-hard.
func TestCheckSize_ReferenceProjectSize(t *testing.T) {
	path := write(t, strings.Repeat("x", 431_950))
	c := CheckSize(path, 0, 0)
	require.Equal(t, StateOverHard, c.State)
	require.Equal(t, int64(431_950), c.Bytes)
}

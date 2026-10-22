//go:build linux

package procstart

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartTicks(t *testing.T) {
	n, ok := startTicks("4242 (a (weird) name) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 0 0")
	require.True(t, ok)
	require.Equal(t, uint64(987654), n)
	_, ok = startTicks("4242 (short) S 1 2")
	require.False(t, ok)
	_, ok = startTicks("no parenthesis")
	require.False(t, ok)
}

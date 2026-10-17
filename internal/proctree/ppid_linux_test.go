//go:build linux

package proctree

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStat_NameWithSpacesAndParens(t *testing.T) {
	pp, ok := parseStat("1234 (my (odd) name) S 987 1234 1234 0 -1")
	require.True(t, ok)
	require.Equal(t, 987, pp)
	_, ok = parseStat("garbage")
	require.False(t, ok)
}

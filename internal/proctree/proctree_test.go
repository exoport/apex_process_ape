package proctree

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAncestors_WalksToInitAndStopsOnCycles(t *testing.T) {
	table := map[int]int{40: 30, 30: 20, 20: 1}
	parent := func(pid int) (int, bool) { pp, ok := table[pid]; return pp, ok }
	require.Equal(t, []int{30, 20}, Ancestors(40, parent))

	table = map[int]int{40: 30, 30: 40}
	require.Equal(t, []int{30}, Ancestors(40, parent), "a cycle ends the walk")

	require.Empty(t, Ancestors(99, parent), "an unreadable parent ends it")
}

func TestParent_OfThisProcess(t *testing.T) {
	pp, ok := Parent(os.Getpid())
	require.True(t, ok)
	require.Equal(t, os.Getppid(), pp)
	require.True(t, Alive(os.Getpid()))
	require.False(t, Alive(0))
}

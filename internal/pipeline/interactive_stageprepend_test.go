package pipeline

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A caller that varies nothing must produce exactly the flags it did
// before StagePrependFlags existed — this is what keeps every pipeline
// that declares no output style byte-identical at spawn.
func TestPrependForStage_NilMapFallsBackToRunLevel(t *testing.T) {
	opts := RunOptions{PrependFlags: []string{"--settings", "{run}"}}
	require.Equal(t, []string{"--settings", "{run}"}, PrependForStage(opts, "any-stage"))
}

func TestPrependForStage_NamedStageWins(t *testing.T) {
	opts := RunOptions{
		PrependFlags: []string{"--settings", "{run}"},
		StagePrependFlags: map[string][]string{
			"design": {"--settings", "{design}"},
		},
	}
	require.Equal(t, []string{"--settings", "{design}"}, PrependForStage(opts, "design"))
	require.Equal(t, []string{"--settings", "{run}"}, PrependForStage(opts, "other"))
}

// An entry that maps to an empty slice is a deliberate "this stage gets
// nothing", not a miss — treating it as absent would silently reinstate
// the run-level flags on a stage the caller meant to strip.
func TestPrependForStage_EmptyEntryIsHonoured(t *testing.T) {
	opts := RunOptions{
		PrependFlags:      []string{"--settings", "{run}"},
		StagePrependFlags: map[string][]string{"bare": {}},
	}
	require.Empty(t, PrependForStage(opts, "bare"))
}

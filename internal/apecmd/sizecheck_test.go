package apecmd

import (
	"bytes"
	"testing"

	"github.com/exoport/apex_process_ape/internal/memory"
	"github.com/stretchr/testify/require"
)

// TestSizeCheckShouldFail is the --fail-at policy table: three values
// crossed with all four states. One table, because `ape memory check` and
// `ape context check` share the policy.
func TestSizeCheckShouldFail(t *testing.T) {
	states := []memory.State{
		memory.StateAbsent, memory.StateOK, memory.StateOverSoft, memory.StateOverHard,
	}
	want := map[string][]bool{
		failAtNever: {false, false, false, false},
		failAtSoft:  {false, false, true, true},
		failAtHard:  {false, false, false, true},
	}
	for policy, expected := range want {
		for i, state := range states {
			require.Equal(t, expected[i], sizeCheckShouldFail(state, policy),
				"--fail-at %s with state %s", policy, state)
		}
	}
}

func TestValidateFailAt(t *testing.T) {
	for _, ok := range []string{failAtNever, failAtSoft, failAtHard} {
		require.NoError(t, validateFailAt(ok))
	}
	err := validateFailAt("always")
	require.Error(t, err)
	require.Contains(t, err.Error(), "never|soft|hard")
}

// TestEmitSizeCheckLine_AbsentNamesThePath: `absent` is a verdict about
// one location, so the line has to say which one it stat'd.
func TestEmitSizeCheckLine_AbsentNamesThePath(t *testing.T) {
	var b bytes.Buffer
	emitSizeCheckLine(&b, "project-context", memory.Check{
		Path: "/p/development/project-context.md", State: memory.StateAbsent,
	})
	require.Equal(t, "project-context: absent (/p/development/project-context.md)\n", b.String())
}

// TestEmitSizeCheckLine_LabelIsTheOnlyDifference locks the shape both
// commands lead with, in the band a skill greps.
func TestEmitSizeCheckLine_LabelIsTheOnlyDifference(t *testing.T) {
	c := memory.Check{
		Path: "/p/x.md", Exists: true, Bytes: 100 << 10,
		SoftBudget: memory.DefaultSoftBudget, HardCeiling: memory.DefaultHardCeiling,
		State: memory.StateOverSoft, EstimatedTokens: (100 << 10) / 4,
	}
	var mem, ctx bytes.Buffer
	emitSizeCheckLine(&mem, "memory", c)
	emitSizeCheckLine(&ctx, "project-context", c)
	require.Equal(t, "memory: 100.0 KiB / 40.0 KiB — over-soft (~25,600 tokens, estimated)\n", mem.String())
	require.Equal(t,
		"project-context: 100.0 KiB / 40.0 KiB — over-soft (~25,600 tokens, estimated)\n", ctx.String())
}

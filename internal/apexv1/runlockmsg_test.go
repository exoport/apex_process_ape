package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// B2 (D755 (b)): a run's messages name the command as it was typed. A
// take-in of M1 locks the scope take-in-M1, and `ape milestone take-in
// take-in-M1` is a command nobody ran (1b's banner read so). The control: a
// verb whose scope is its argument reads as before.
func TestRunLockMessages_NameTheArgument(t *testing.T) {
	takeIn := &RunLock{Verb: "milestone take-in", Scope: "take-in-M1", Arg: "M1", Started: "now", PID: 1, Log: "x.log"}
	for _, err := range []error{RunBusy(takeIn), RunGone(takeIn), RunBusyPersonAct(takeIn, PersonCall{Line: "l"})} {
		require.Contains(t, err.Error(), "`ape milestone take-in M1`")
		require.NotContains(t, err.Error(), "take-in take-in-M1")
	}
	verify := &RunLock{Verb: "change verify", Scope: "add-wave", Started: "now", PID: 1, Log: "x.log"}
	require.Contains(t, RunBusy(verify).Error(), "`ape change verify add-wave`", "control")
	require.Equal(t, "`ape change verify add-wave`", verify.Command())
}

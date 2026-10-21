package apecmd

import (
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// AE-9: change tool and accept --delegate refuse a --wait past the Bash
// cap as the other long verbs do (usage.wait), before any session starts.
func TestWait_ToolAndDelegateRefuseAWaitPastTheCap(t *testing.T) {
	outsideEverySession(t)
	root := v1Repo(t)
	for name, args := range map[string][]string{
		"change tool":            {"tool", "add-wave", "lint", "--wait", "10m"},
		"accept --delegate":      {"accept", "add-wave", "--task", "T1", "--delegate", "--wait", "10m"},
		"change tool, negative":  {"tool", "add-wave", "lint", "--wait", "-1s"},
		"accept --delegate, -1s": {"accept", "add-wave", "--task", "T1", "--delegate", "--wait", "-1s"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := v1Exec(t, newChangeCmd, append(args, "--json", "--cwd", root)...)
			var ue *apexv1.UsageError
			require.ErrorAs(t, err, &ue)
			require.Equal(t, "usage.wait", ue.Code)
		})
	}
}

package repl

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// The verdict rules. The distinction that matters is BROKEN versus
// UNDETERMINED: a broken claude stops the run and a probe that could not
// tell does not — so "claude drew nothing" (offline, not logged in) must
// never be reported as a claude ape cannot drive, and a screen ape cannot
// get past must never be waved through as "could not tell".
func TestJudgeNotReady(t *testing.T) {
	t.Parallel()
	timeout := func(pane string) error {
		return &NotReadyError{Name: "p", Pane: pane, Err: context.DeadlineExceeded}
	}
	for _, tc := range []struct {
		name        string
		interrupted error
		alive       bool
		pane        string
		err         error
		want        ProbeVerdict
		detail      string
	}{
		{
			"a trust walk that could not finish", nil, true, "❯ No, exit",
			&NotReadyError{Err: errors.New("repl: dismiss trust-folder modal: could not reach a trust-granting option")},
			ProbeBroken, "could not reach a trust-granting option",
		},
		{"nothing drawn before the deadline", nil, true, "  \n ", timeout(""), ProbeUndetermined, "drew nothing"},
		{"claude exited on a screen", nil, false, "Invalid API key", timeout("Invalid API key"), ProbeBroken, "exited before"},
		{
			"an unknown screen that never became a REPL", nil, true, "Choose a theme", timeout("Choose a theme"), ProbeBroken,
			"not a screen ape knows how to get past",
		},
		{"the probe was cancelled", context.Canceled, true, "Choose a theme", timeout("Choose a theme"), ProbeUndetermined, "interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdict, detail := judgeNotReady(tc.interrupted, tc.alive, tc.pane, tc.err)
			require.Equal(t, tc.want, verdict)
			require.Contains(t, detail, tc.detail)
		})
	}
}

//go:build unix

package apecmd

import (
	"context"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// B1 (D755 (a)), end to end with a detached worker: two runs of one scope
// (verify of add-wave, twice) each keep their own log — the first one's,
// with its "ended" line, survives the second — and each run's lock and
// result name its own file.
func TestLongRun_EachRunKeepsItsOwnLog(t *testing.T) {
	root, p := detachedRepo(t, "echo wave")
	logs := make([]string, 0, 2)
	for range 2 {
		_, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--json", "--wait", "60s", "--cwd", root)
		require.NoError(t, err)
		r := p.LoadRunResult("add-wave")
		require.NotNil(t, r)
		require.Equal(t, p.Rel(p.RunLogPathFor("add-wave", r.ID)), r.Log, "the result names its run's log")
		logs = append(logs, p.RunLogPathFor("add-wave", r.ID))
	}
	require.NotEqual(t, logs[0], logs[1])
	for _, l := range logs {
		data, err := os.ReadFile(l)
		require.NoError(t, err, "%s survives", l)
		require.Contains(t, string(data), " ended, exit 0 — result:")
	}
	require.Equal(t, logs[1], p.RunLogPath("add-wave"), "the scope's newest log")
}

// B1: the fence lines a run's push wrote (member.FenceMilestone notes each)
// are in its result.json as "fences"; the control, the next run with no
// push, records none.
func TestRunHere_RecordsTheRunsFenceLines(t *testing.T) {
	root := v1Repo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	run := func(note bool) *apexv1.RunResult {
		t.Helper()
		lock, held, err := p.TakeRunLock(context.Background(), "change verify", "add-wave")
		require.NoError(t, err)
		require.Nil(t, held)
		cmd := newChangeCmd()
		cmd.SetContext(context.Background())
		compute := func(context.Context, apexv1.RunOptions) renderFn {
			if note {
				apexv1.NoteFence("2026-11-02T00:00:00Z ape: fence: milestone/M1: pushing under milestone.p.M1")
			}
			return func(*cobra.Command, *v1Flags) error { return nil }
		}
		r, err := runHere(cmd, &v1Flags{}, p, lock, compute, nil, nil)
		require.NoError(t, err)
		return r
	}
	r := run(true)
	require.Equal(t, []string{"2026-11-02T00:00:00Z ape: fence: milestone/M1: pushing under milestone.p.M1"}, r.Fences)
	require.Equal(t, r.Fences, p.LoadRunResult("add-wave").Fences, "in result.json")
	require.Empty(t, run(false).Fences, "control: a run with no push records none")
}

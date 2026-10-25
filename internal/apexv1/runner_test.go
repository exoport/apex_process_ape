package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunCheck(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "logs", "T1.log")

	r := RunCheck(ctx(), dir, "echo one; echo two >&2; exit 3", log, 0)
	require.Equal(t, 3, r.Exit)
	require.Equal(t, []string{"one", "two"}, r.Tail)
	require.Len(t, r.OutputSHA256, 64)
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "one\ntwo\n", string(data))

	r = RunCheck(ctx(), dir, "./does-not-exist", "", 0)
	require.Equal(t, 127, r.Exit)

	again := RunCheck(ctx(), dir, "echo one; echo two >&2; exit 3", "", 0)
	require.Equal(t, RunCheck(ctx(), dir, "echo one; echo two >&2; exit 0", "", 0).OutputSHA256, again.OutputSHA256,
		"the hash is of the output, not the exit")
}

// v1.4 (D561 Q4): every check ape runs has APE_GATE=1 in its environment.
func TestRunCheck_ApeGateSet(t *testing.T) {
	needSh(t)
	t.Setenv(EnvApeGate, "")
	r := RunCheck(ctx(), t.TempDir(), `test "$APE_GATE" = 1 && echo gate=$APE_GATE`, "", 0)
	require.Equal(t, 0, r.Exit, r.Tail)
	require.Equal(t, []string{"gate=1"}, r.Tail)
}

// A timed-out check records 124, and its whole process group is killed:
// a background child holding the output pipe open must not keep the
// check alive past its timeout.
func TestRunCheck_TimeoutKillsTheGroup(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "T1.log")
	start := time.Now()
	r := RunCheck(ctx(), dir, "sleep 30 & sleep 30", log, 300*time.Millisecond)
	require.Equal(t, ExitTimeout, r.Exit)
	require.Less(t, time.Since(start), 10*time.Second)
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Contains(t, string(data), "check timed out", "the note reaches the log, which is closed only after it")
}

func TestRunCheck_TailKeepsTheEnd(t *testing.T) {
	needSh(t)
	r := RunCheck(ctx(), t.TempDir(), "seq 1 100", "", 0)
	require.Len(t, r.Tail, tailLines)
	require.Equal(t, "100", r.Tail[len(r.Tail)-1])
	require.Equal(t, "61", r.Tail[0])
	require.False(t, strings.HasPrefix(r.Tail[0], "1\n"))
}

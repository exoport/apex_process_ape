package procstart

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A child process reports its own start, which is the moment the parent
// started it — not the later moment it got round to asking.
func TestTime_IsWhenTheProcessStarted(t *testing.T) {
	if os.Getenv("PROCSTART_CHILD") == "1" {
		t.Skip("the child half")
	}
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
	default:
		t.Skip("no OS record of a process's start here")
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^TestHelperReportStart$", "-test.v")
	cmd.Env = append(os.Environ(), "PROCSTART_CHILD=1")
	before := time.Now()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	var started, initAt time.Time
	for line := range strings.SplitSeq(string(out), "\n") {
		if _, v, ok := strings.Cut(line, "start="); ok {
			started, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
			require.NoError(t, err)
		}
		if _, v, ok := strings.Cut(line, "init="); ok {
			initAt, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
			require.NoError(t, err)
		}
	}
	require.False(t, started.IsZero(), string(out))
	require.False(t, started.After(initAt), "never after the package's own init")
	// /proc counts in hundredths of a second; Windows' clock ticks every
	// ~15.6 ms: the start is the spawn, to within those.
	require.WithinRange(t, started, before.Add(-50*time.Millisecond), before.Add(2*time.Second))
}

func TestHelperReportStart(t *testing.T) {
	if os.Getenv("PROCSTART_CHILD") != "1" {
		t.Skip("run by TestTime_IsWhenTheProcessStarted")
	}
	_, ok := osStart()
	require.True(t, ok, "the OS record was read")
	t.Log("start=" + Time().Format(time.RFC3339Nano))
	t.Log("init=" + initStamp.Format(time.RFC3339Nano))
}

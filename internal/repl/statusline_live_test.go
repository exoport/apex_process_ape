package repl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/usage"
)

// liveStatuslineUsageRecorded checks W4's coupling: a session spawned the
// way `ape task` builds its --settings, statusLine `<ape> usage record
// --run <key>` included, runs that command, and Claude Code hands it the
// plan's rate_limits — five_hour and seven_day, used_percentage 0–100,
// resets_at in epoch seconds (measured on 2.1.306 outside ape). If it
// fails, ape's usage guard never has a reading: every launch is "usage
// unknown" and nothing is held or stopped. One Haiku turn.
//
// The recorder is this checkout's ape, built into a temp dir, and its
// cache is a temp dir too (XDG_CACHE_HOME, set on the command itself so a
// claude that did not pass its environment on could not reach the real
// one): the developer's <user cache>/ape is never read or written.
func liveStatuslineUsageRecorded(t *testing.T, claudeBin string) {
	t.Helper()
	if os.Getenv("APE_CLAUDE_LIVE_TOKENS") == "0" {
		t.Skip("APE_CLAUDE_LIVE_TOKENS=0 — the status-line check submits a turn")
	}
	if runtime.GOOS != "linux" {
		t.Skip("points os.UserCacheDir at a temp dir through XDG_CACHE_HOME, which only Linux reads")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "ape")
	build := exec.Command("go", "build", "-o", bin, "github.com/exoport/apex_process_ape/cmd/ape")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build ape: %s", out)
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache) // so usage.Path() below reads the recorder's file

	key := usage.NewKey()
	settings, err := config.BuildSettings(config.SettingsOptions{
		Mode: config.ModeTUI, InjectHooks: true, APEBin: bin, BridgePort: 1, // no bridge listens: the hooks fail, harmlessly
		Env:               map[string]string{EnvFileReadMaxOutputTokens: "30000"},
		StatusLineCommand: "env XDG_CACHE_HOME=" + cache + " " + bin + " usage record --run " + key,
	})
	require.NoError(t, err)

	dir, token := uniqueWorkdir(t)
	name := sessionName(t, "statusline")
	t.Cleanup(func() { removeScratchTranscripts(t, home, token) })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	argv := []string{claudeBin, "--dangerously-skip-permissions", "--settings", string(settings), "--model", cost.ResolveFamilyAlias("haiku")}
	require.NoError(t, NewSessionWithEnv(ctx, name, dir, argv, EffortEnv("low")))
	t.Cleanup(func() { _ = KillSession(context.Background(), name) })
	require.NoError(t, WaitForReady(ctx, name))
	require.NoError(t, SendCommand(ctx, name, "Reply with the single word OK and nothing else."))

	var r *usage.Reading
	for r == nil && ctx.Err() == nil {
		time.Sleep(time.Second)
		r = usage.Load()
	}
	require.NotNil(t, r, "no usage reading after a turn: Claude Code did not run ape's statusLine command in an ape "+
		"PTY session, or handed it no rate_limits (the plan has none, or the field moved).\nlast pane:\n%s", paneOf(ctx, name))
	for label, v := range map[string]*float64{"five_hour": r.FiveHour, "seven_day": r.SevenDay} {
		require.NotNil(t, v, "%s missing from the reading", label)
		require.True(t, *v >= 0 && *v <= 1, "%s = %v is not a fraction 0..1 — used_percentage changed scale", label, *v)
	}
	for label, at := range map[string]*time.Time{"five_hour": r.FiveHourResetsAt, "seven_day": r.SevenDayResetsAt} {
		require.NotNil(t, at, "%s resets_at missing", label)
		require.True(t, at.After(time.Now().Add(-time.Minute)) && at.Before(time.Now().Add(8*24*time.Hour)),
			"%s resets_at = %v is not a near-future instant — resets_at is no longer epoch seconds", label, at)
	}
	first, last := usage.EndRun(key)
	require.NotNil(t, first, "the reading was not kept under the run's launch key (--run)")
	require.NotNil(t, last)
	t.Logf("claude %s ran the status line: %s (5h resets %s, 7d resets %s)", r.ClaudeVersion, r.Short(),
		r.FiveHourResetsAt.Format(time.RFC3339), r.SevenDayResetsAt.Format(time.RFC3339))
}

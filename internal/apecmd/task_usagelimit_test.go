package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// limitPane is a synthetic pane holding Claude Code 2.1.302's limit line
// (unmeasured live: no captured screen of a real limit).
const limitPane = "⏺ Running the suite…\n\n  ⎿  You've hit your usage limit · resets 11pm (Europe/Madrid)\n\n> \n  ⏵⏵ bypass permissions on\n"

func fastUsageLimitPoll(t *testing.T, pane func() string) {
	t.Helper()
	poll, read := usageLimitPoll, usageLimitPane
	usageLimitPoll = 5 * time.Millisecond
	usageLimitPane = func(context.Context, string) (string, error) { return pane(), nil }
	t.Cleanup(func() { usageLimitPoll, usageLimitPane = poll, read })
}

// H20: the limit line on two reads with no hook between ends the run as a
// usage-limit stop, with the reset parsed.
func TestUsageLimitWatch_StopsOnTheLimitLine(t *testing.T) {
	fastUsageLimitPoll(t, func() string { return limitPane })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan error, 1)
	w := &usageLimitWatch{}
	go w.run(ctx, func() string { return "s" }, func() time.Time { return time.Time{} }, func(err error) {
		select {
		case got <- err:
		default:
		}
	})
	var err error
	select {
	case err = <-got:
	case <-ctx.Done():
		t.Fatal("no stop")
	}
	cancel()
	lse, ok := errors.AsType[*pipeline.LimitStopError](err)
	require.True(t, ok)
	require.Equal(t, pipeline.TerminationUsageLimit, lse.Kind)
	require.Equal(t, "11pm (Europe/Madrid)", lse.Resets)
	require.False(t, lse.ResetsAt.IsZero())
	require.Equal(t, 23, lse.ResetsAt.In(lse.ResetsAt.Location()).Hour())
	require.Contains(t, lse.Message, "run it again after the limit resets")
	require.Equal(t, ExitUsageLimit, taskExitCode(fmt.Errorf("stage x: %w", err)))
	require.NotNil(t, w.stopped())
}

// A session still sending hooks is not stopped, whatever its pane shows;
// nor is one whose pane never shows the line.
func TestUsageLimitWatch_NoStopWhileHooksArrive(t *testing.T) {
	var hooks atomic.Int64
	fastUsageLimitPoll(t, func() string { hooks.Store(time.Now().UnixNano()); return limitPane })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var stops atomic.Int32
	w := &usageLimitWatch{}
	w.run(ctx, func() string { return "s" }, func() time.Time { return time.Unix(0, hooks.Load()).Add(time.Millisecond) },
		func(error) { stops.Add(1) })
	require.Zero(t, stops.Load())
	require.Nil(t, w.stopped())

	fastUsageLimitPoll(t, func() string { return "⏺ All green.\n> \n" })
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	w.run(ctx2, func() string { return "s" }, func() time.Time { return time.Time{} }, func(error) { stops.Add(1) })
	require.Zero(t, stops.Load())
}

// A usage-limit stop in the newest run's manifest is what ape status shows
// as the latest run.
func TestUsageLimit_RecordedAndShownInStatus(t *testing.T) {
	at := time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)
	root := v1Repo(t)
	runDir := filepath.Join(runlog.TasksRoot(root), "apex-orchestrator", "20261016-220000-abc1234")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "manifest.yaml"), []byte(
		"run_id: 20261016-220000-abc1234\nstatus: failed\ntermination:\n  kind: usage_limit\n  message: usage limit\n"+
			"  resets: 11pm (UTC)\n  resets_at: 2026-10-16T23:00:00Z\n"), 0o644))
	older := filepath.Join(runlog.TasksRoot(root), "apex-plan", "20261015-100000-0000000")
	require.NoError(t, os.MkdirAll(older, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(older, "manifest.yaml"), []byte("run_id: 20261015-100000-0000000\nstatus: completed\n"), 0o644))

	stdout, _, err := v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		LatestRun apexv1.LatestRun `json:"latest_run"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	require.Equal(t, "apex-orchestrator", st.LatestRun.Skill)
	require.Equal(t, "usage_limit", st.LatestRun.Stopped)
	require.True(t, at.Equal(*st.LatestRun.ResetsAt))

	stdout, _, err = v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "stopped: usage-limit, resets 2026-10-16T23:00:00Z")
}

// H20: the notifier runs when a change becomes blocked, with the event,
// the change and the reason in its environment; a failing notifier changes
// nothing.
func TestNotifier_OnBlock(t *testing.T) {
	root := v1Repo(t)
	out := filepath.Join(t.TempDir(), "notified")
	cfg := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(data, []byte(`notify: 'printf "%s|%s|%s" "$APE_EVENT" "$APE_CHANGE" "$APE_REASON" > `+out+`'`+"\n")...), 0o644))
	v1Git(t, root, "commit", "-q", "-am", "notify")
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "a new change is not a block")

	_, _, err = v1Exec(t, newChangeCmd, "block", "add-wave", "--reason", "which greeting?", "--cwd", root)
	require.NoError(t, err)
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "block|add-wave|which greeting?", string(got))

	// The change an ape task dispatch names: APE_CHANGE on a stop.
	p, ok := v1ProjectAt(root)
	require.True(t, ok)
	require.Equal(t, "add-wave", p.ChangeIDIn(context.Background(), "add-wave --resume"))
	require.Empty(t, p.ChangeIDIn(context.Background(), "M1"))
	require.Empty(t, p.ChangeIDIn(context.Background(), ""))

	// Blocked again: no transition, no second notice.
	require.NoError(t, os.Remove(out))
	_, _, _ = v1Exec(t, newChangeCmd, "block", "add-wave", "--reason", "still", "--cwd", root) //nolint:dogsled // only the notifier's silence is under test
	_, statErr = os.Stat(out)
	require.True(t, os.IsNotExist(statErr))

	// A notifier that fails is a warning, not a failure.
	require.NoError(t, os.WriteFile(cfg, append(data, []byte("notify: 'exit 3'\n")...), 0o644))
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-bow", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	_, stderr, err := v1Exec(t, newChangeCmd, "block", "add-bow", "--reason", "x", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stderr, "notify (block)")
}

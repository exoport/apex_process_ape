package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessions"
	"github.com/exoport/apex_process_ape/internal/usage"
)

// fakeUsageClock runs the guard on a clock the hold's sleep advances.
func fakeUsageClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	clock := start
	now, sleep := usageNow, usageSleep
	usageNow = func() time.Time { return clock }
	usageSleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clock = clock.Add(d)
		return nil
	}
	t.Cleanup(func() { usageNow, usageSleep = now, sleep })
	return &clock
}

// seedReading writes usage.json: the two windows, read at `at`, the 5-hour
// one resetting at fiveReset.
func seedReading(t *testing.T, at time.Time, five, seven float64, fiveReset time.Time) {
	t.Helper()
	sevenReset := at.Add(72 * time.Hour)
	data, err := json.Marshal(usage.Reading{At: at, FiveHour: &five, FiveHourResetsAt: &fiveReset, SevenDay: &seven, SevenDayResetsAt: &sevenReset})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(usage.Dir(), 0o755))
	require.NoError(t, os.WriteFile(usage.Path(), data, 0o644))
}

func guardFor(cfg usage.GuardConfig, usageOK bool) *usageGuard {
	return &usageGuard{cfg: cfg, skill: "apex-apply", args: "add-wave", usageOK: usageOK}
}

var guardStart = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// W4-A2: a fresh reading under every limit launches, silently.
func TestUsageGuard_Launch(t *testing.T) {
	usageTempCache(t)
	fakeUsageClock(t, guardStart)
	seedReading(t, guardStart.Add(-time.Minute), 0.14, 0.55, guardStart.Add(time.Hour))
	g := guardFor(usage.DefaultGuard(), false)
	var out bytes.Buffer
	require.NoError(t, g.beforeSpawn(context.Background(), func(*time.Time) { t.Fatal("no hold") }, &out))
	require.Empty(t, out.String())
	rec := g.record()
	require.Equal(t, "launch", rec.Decision)
	require.InDelta(t, 0.55, *rec.Reading.SevenDay, 1e-9)
	require.Equal(t, "default", rec.ShareSource)
	require.InDelta(t, 0.05, rec.ShareFiveHour, 1e-9)
	require.Nil(t, rec.HeldUntil)
}

// five_hour 0.93 + share 0.05 holds until the window's reset + 1 min, a
// line every 10 min, held while it waits; then it launches — here with
// "usage unknown", the only reading being 31 minutes old by then.
func TestUsageGuard_HoldsOnTheFiveHourWindow(t *testing.T) {
	usageTempCache(t)
	clock := fakeUsageClock(t, guardStart)
	reset := guardStart.Add(30 * time.Minute)
	seedReading(t, guardStart.Add(-time.Minute), 0.93, 0.55, reset)
	g := guardFor(usage.DefaultGuard(), false)
	var out bytes.Buffer
	var helds []*time.Time
	require.NoError(t, g.beforeSpawn(context.Background(), func(u *time.Time) { helds = append(helds, u) }, &out))

	require.Len(t, helds, 2)
	require.True(t, reset.Add(time.Minute).Equal(*helds[0]), "held until the reset + 1 min")
	require.Nil(t, helds[1], "and no longer held after")
	require.True(t, clock.Equal(reset.Add(time.Minute)))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 5, out.String()) // 31, 21, 11, 1 min left; then the launch
	require.Contains(t, lines[0], "⏸ usage guard: the 5-hour window is at 0.93, with this launch's share 0.05 over limits.usage.five_hour 0.95")
	require.Contains(t, lines[0], "(31m0s left")
	require.Contains(t, lines[4], "usage unknown (no reading in 10 min) — launching")

	*clock = clock.Add(20 * time.Minute) // the session runs; its time is not the hold's
	rec := g.record()
	require.Equal(t, "unknown", rec.Decision)
	require.True(t, reset.Add(time.Minute).Equal(*rec.HeldUntil))
	require.InDelta(t, 31*60, rec.HeldSeconds, 0.001)
	require.Equal(t, "usage unknown (no reading in 10 min)", rec.Note)
}

// A held launch stopped while it waits ends as cancelled, not launched.
func TestUsageGuard_HoldEndsWithTheContext(t *testing.T) {
	usageTempCache(t)
	fakeUsageClock(t, guardStart)
	seedReading(t, guardStart, 0.99, 0.10, guardStart.Add(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := guardFor(usage.DefaultGuard(), false).beforeSpawn(ctx, func(*time.Time) {}, &bytes.Buffer{})
	require.ErrorIs(t, err, context.Canceled)
}

// seven_day 0.86 > 0.85 stops for a person: exit 14, termination
// usage_guard with the reset, a message naming the reading, the share, the
// reset and the override.
func TestUsageGuard_StopsOnTheWeeklyWindow(t *testing.T) {
	usageTempCache(t)
	fakeUsageClock(t, guardStart)
	seedReading(t, guardStart.Add(-time.Minute), 0.10, 0.86, guardStart.Add(time.Hour))
	g := guardFor(usage.DefaultGuard(), false)
	err := g.beforeSpawn(context.Background(), func(*time.Time) { t.Fatal("a stop is not a hold") }, &bytes.Buffer{})
	lse, ok := errors.AsType[*pipeline.LimitStopError](err)
	require.True(t, ok)
	require.Equal(t, pipeline.TerminationUsageGuard, lse.Kind)
	require.True(t, guardStart.Add(-time.Minute).Add(72*time.Hour).Equal(lse.ResetsAt))
	for _, want := range []string{
		"the 7-day window is at 0.86, over limits.usage.seven_day 0.85", "(share default)", "; it resets ",
		"reading 5h 0.10 · 7d 0.86, 1 min ago", "`ape task apex-apply --args add-wave --usage-ok` (a person only)",
	} {
		require.Contains(t, lse.Message, want)
	}
	require.Equal(t, ExitUsageLimit, taskExitCode(err))
	require.Equal(t, "stop", g.record().Decision)
	require.Equal(t, pipeline.TerminationUsageGuard, pipeline.TerminationKind(err))
}

// No reading, a stale one, off and --usage-ok all launch; the first two
// with the note, the last recorded with its reading.
func TestUsageGuard_UnknownOffAndUsageOK(t *testing.T) {
	usageTempCache(t)
	fakeUsageClock(t, guardStart)
	var out bytes.Buffer
	g := guardFor(usage.DefaultGuard(), false)
	require.NoError(t, g.beforeSpawn(context.Background(), func(*time.Time) {}, &out))
	require.Contains(t, out.String(), "⚠ usage unknown (no reading yet) — launching")

	seedReading(t, guardStart.Add(-11*time.Minute), 0.99, 0.99, guardStart.Add(time.Hour))
	out.Reset()
	g = guardFor(usage.DefaultGuard(), false)
	require.NoError(t, g.beforeSpawn(context.Background(), func(*time.Time) {}, &out))
	require.Contains(t, out.String(), "usage unknown (no reading in 10 min) — launching")
	require.Equal(t, "usage unknown (no reading in 10 min)", g.record().Note)

	seedReading(t, guardStart, 0.99, 0.99, guardStart.Add(time.Hour))
	off := usage.DefaultGuard()
	off.Off = true
	g = guardFor(off, false)
	out.Reset()
	require.NoError(t, g.beforeSpawn(context.Background(), func(*time.Time) {}, &out))
	require.Empty(t, out.String())
	require.Equal(t, "off", g.record().Decision)

	g = guardFor(usage.DefaultGuard(), true)
	require.NoError(t, g.beforeSpawn(context.Background(), func(*time.Time) {}, &out))
	rec := g.record()
	require.Equal(t, "usage_ok", rec.Decision)
	require.True(t, rec.UsageOK)
	require.InDelta(t, 0.99, *rec.Reading.SevenDay, 1e-9)
	require.Contains(t, rec.Note, "--usage-ok")
}

// share: auto learns from three recorded runs of the skill: a launch the
// default share lets through holds once the skill's runs say it uses more.
func TestUsageGuard_ShareAutoLearns(t *testing.T) {
	usageTempCache(t)
	fakeUsageClock(t, guardStart)
	seedReading(t, guardStart, 0.88, 0.40, guardStart.Add(time.Hour))
	g := guardFor(usage.DefaultGuard(), false)
	require.Equal(t, usage.Launch, usage.Decide(g.cfg, usage.Load(), g.share(), guardStart, false).Outcome)

	for i := range 3 {
		b := usage.Reading{At: guardStart, FiveHour: new(0.1), SevenDay: new(0.3)}
		a := usage.Reading{At: guardStart.Add(time.Hour), FiveHour: new(0.18 + float64(i)/100), SevenDay: new(0.32)}
		require.NoError(t, usage.AppendHistory(usage.HistoryEntry{Skill: "apex-apply", Run: "r", At: guardStart, Before: &b, After: &a}))
	}
	s := g.share()
	require.Equal(t, "auto", s.Source)
	require.InDelta(t, 0.09, s.FiveHour, 1e-9)
	require.Equal(t, usage.Hold, usage.Decide(g.cfg, usage.Load(), s, guardStart, false).Outcome)

	// A configured share is the share, for both windows.
	cfgShare := usage.DefaultGuard()
	v := 0.01
	cfgShare.Share = &v
	require.Equal(t, usage.Share{FiveHour: 0.01, SevenDay: 0.01, Source: "config"}, guardFor(cfgShare, false).share())
}

// --usage-ok is a person's: refused in a model session (APE_SESSION, and
// any Claude Code session) before anything starts, exit 2.
func TestTask_UsageOKRefusedInAModelSession(t *testing.T) {
	root := v1Repo(t)
	for _, env := range []string{"APE_SESSION", "CLAUDECODE"} {
		t.Setenv("APE_SESSION", "")
		t.Setenv("CLAUDECODE", "")
		t.Setenv(env, "task/20261021-120000-abc")
		if env == "CLAUDECODE" {
			t.Setenv(env, "1")
		}
		_, _, err := v1Exec(t, newTaskCmd, "apex-apply", "--args", "add-wave", "--usage-ok", "--cwd", root)
		require.Error(t, err, env)
		code, _ := ExitCode(err)
		require.Equal(t, ExitUsage, code)
		require.Contains(t, err.Error(), "--usage-ok is for a person, and this runs in a model session ("+env+" is set)")
		var ue *apexv1.UsageError
		require.ErrorAs(t, err, &ue)
		require.Equal(t, apexv1.CodePersonOnly, ue.Code)
	}
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
	require.NoError(t, apexv1.RequirePersonFlag("--usage-ok"), "a person at a terminal")
}

// The decision lands in the run manifest as usage.guard, beside the
// readings.
func TestRecordUsageGuard_Manifest(t *testing.T) {
	usageTempCache(t)
	fakeUsageClock(t, guardStart)
	root := t.TempDir()
	runDir := filepath.Join(runlog.TasksRoot(root), "apex-apply", "20261021-120000-abc1234")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "manifest.yaml"), []byte("run_id: 20261021-120000-abc1234\nstatus: completed\n"), 0o644))
	seedReading(t, guardStart, 0.99, 0.99, guardStart.Add(time.Hour))
	g := guardFor(usage.DefaultGuard(), true)
	require.NoError(t, g.beforeSpawn(context.Background(), func(*time.Time) {}, &bytes.Buffer{}))
	recordUsageGuard(runDir, g)
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.Equal(t, "usage_ok", m.Usage.Guard.Decision)
	require.True(t, m.Usage.Guard.UsageOK)
	require.InDelta(t, 0.99, *m.Usage.Guard.Reading.FiveHour, 1e-9)
	require.Contains(t, string(data), "usage:\n    guard:\n        decision: usage_ok\n")
}

// The stop's events (W3): stop with reason usage-guard, then person with
// the override as its action.
func TestUsageGuard_StopEvents(t *testing.T) {
	root := v1Repo(t)
	usageTempCache(t)
	fakeUsageClock(t, guardStart)
	seedReading(t, guardStart, 0.10, 0.90, guardStart.Add(time.Hour))
	p, ok := v1ProjectAt(root)
	require.True(t, ok)
	g := guardFor(usage.DefaultGuard(), false)
	g.args = "M1"
	err := g.beforeSpawn(context.Background(), func(*time.Time) {}, &bytes.Buffer{})
	lse, ok := errors.AsType[*pipeline.LimitStopError](err)
	require.True(t, ok)
	emitUsageGuardStop(context.Background(), p, g, "20261021-120000-abc1234", lse)
	evs := p.EventsSince(0)
	require.Len(t, evs, 2)
	require.Equal(t, apexv1.EventStop, evs[0].Event)
	require.Equal(t, "usage-guard", evs[0].Reason)
	require.Equal(t, "20261021-120000-abc1234", evs[0].Run)
	require.Equal(t, "apex-apply", evs[0].Skill)
	require.Equal(t, apexv1.EventPerson, evs[1].Event)
	require.Equal(t, "ape task apex-apply --args M1 --usage-ok", evs[1].Action)
	require.Contains(t, evs[1].Reason, "the 7-day window is at 0.90")
}

// W4-A4: a held detached launch is state held in ape run wait --json, with
// held_until, and the wait exits 75 as for running; ape status shows a held
// session.
func TestUsageGuard_HeldIsVisible(t *testing.T) {
	root := v1Repo(t)
	until := guardStart.Add(31 * time.Minute)
	h := &detachedHandle{ID: "20261021-120000-abc123", Argv: []string{"task", "apex-apply"}, Dir: root, Root: root, StartedAt: guardStart, HeldUntil: &until}
	require.NoError(t, os.MkdirAll(runlog.DetachedRoot(root), 0o755))
	require.NoError(t, writeHandle(handlePath(root, h.ID), h))
	stdout, _, err := v1Exec(t, newRunCmd, "wait", h.ID, "--wait", "10ms", "--json", "--cwd", root)
	code, _ := ExitCode(err)
	require.Equal(t, exitCodeStillRunning, code)
	var rep map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &rep), stdout)
	require.Equal(t, "held", rep["state"])
	require.Equal(t, until.Format(time.RFC3339), rep["held_until"])
	stdout, _, err = v1Exec(t, newRunCmd, "wait", h.ID, "--wait", "10ms", "--cwd", root)
	code, _ = ExitCode(err)
	require.Equal(t, exitCodeStillRunning, code)
	require.Contains(t, stdout, "is held: the usage guard holds its launch on the 5-hour window until")

	// noteDetachedHold is how the run sets and clears it.
	t.Setenv(envDetachedID, h.ID)
	noteDetachedHold(root, nil)
	got, err := readHandle(handlePath(root, h.ID))
	require.NoError(t, err)
	require.Nil(t, got.HeldUntil)
	stdout, _, _ = v1Exec(t, newRunCmd, "wait", h.ID, "--wait", "10ms", "--json", "--cwd", root)
	require.Contains(t, stdout, `"state": "running"`)

	// ape status: the session's row says held.
	abs, err := filepath.Abs(root)
	require.NoError(t, err)
	live := registerSession("task", abs, "20261021-120000-abc1234", nil)
	require.NotNil(t, live)
	t.Cleanup(live.end)
	live.held(&until)
	stdout, _, err = v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "held: 5-hour window, launches "+until.Format(time.RFC3339))
	stdout, _, err = v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Sessions []apexv1.LiveSession `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st))
	require.Len(t, st.Sessions, 1)
	require.True(t, until.Equal(*st.Sessions[0].HeldUntil))
	live.held(nil)
	rows, err := sessions.Alive(sessions.Path())
	require.NoError(t, err)
	for _, r := range rows {
		require.Nil(t, r.HeldUntil)
	}
}

// A usage-guard stop is ape status's latest run, with the window's reset.
func TestUsageGuard_StopShownInStatus(t *testing.T) {
	root := v1Repo(t)
	runDir := filepath.Join(runlog.TasksRoot(root), "apex-apply", "20261021-120000-abc1234")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "manifest.yaml"), []byte(
		"run_id: 20261021-120000-abc1234\nstatus: failed\ntermination:\n  kind: usage_guard\n  message: usage guard\n"+
			"  resets_at: 2026-10-24T12:00:00Z\n"), 0o644))
	stdout, _, err := v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "stopped: usage-guard (not started: the 7-day window), resets 2026-10-24T12:00:00Z")
}

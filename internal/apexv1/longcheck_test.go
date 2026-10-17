package apexv1

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// D58 2.4 / D69: a tier is a scalar (its max) or {idle, max}, with the
// D69 defaults, no floor, idle never above max, max never above ape's
// hard ceiling — and --timeout is every tier's max, capped by its ceiling.
func TestConfig_TimeoutTiers(t *testing.T) {
	load := func(t *testing.T, yaml string) (Config, error) {
		t.Helper()
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(root+"/_apex", 0o755))
		require.NoError(t, os.WriteFile(ConfigPath(root), []byte(yaml), 0o644))
		return LoadConfig(root)
	}
	var none Timeouts
	require.Equal(t, TierTimeout{Idle: 15 * time.Minute, Max: 2 * time.Hour}, strip(none.Limits(KindTask, 0)))
	require.Equal(t, TierTimeout{Idle: 15 * time.Minute, Max: 2 * time.Hour}, strip(none.Limits(KindScenario, 0)))
	require.Equal(t, TierTimeout{Idle: 30 * time.Minute, Max: 2 * time.Hour}, strip(none.Limits(KindRegression, 0)))
	require.Equal(t, TierTimeout{Idle: 30 * time.Minute, Max: 4 * time.Hour}, strip(none.Limits(KindGate, 0)))

	cfg, err := load(t, "timeouts:\n  check: 5m\n  regression: {idle: 10m, max: 1h}\n  gate: {idle: 3m}\n")
	require.NoError(t, err)
	require.Equal(t, TierTimeout{Idle: 5 * time.Minute, Max: 5 * time.Minute}, strip(cfg.Timeouts.Limits(KindTask, 0)),
		"a scalar is the max; the default idle is clamped to it")
	require.Equal(t, TierTimeout{Idle: 10 * time.Minute, Max: time.Hour}, strip(cfg.Timeouts.Limits(KindRegression, 0)))
	require.Equal(t, TierTimeout{Idle: 3 * time.Minute, Max: 4 * time.Hour}, strip(cfg.Timeouts.Limits(KindGate, 0)),
		"idle alone keeps the default max")
	require.Equal(t, TierTimeout{Idle: 3 * time.Minute, Max: 6 * time.Hour}, strip(cfg.Timeouts.Limits(KindGate, 6*time.Hour)), "the flag is the max")
	require.Equal(t, CeilingCheck, cfg.Timeouts.For(KindTask, 6*time.Hour), "the flag never passes the tier's ceiling")
	require.Equal(t, time.Second, cfg.Timeouts.For(KindTask, time.Second))

	cfg, err = load(t, "timeouts: {check: {idle: 1s, max: 4h}, regression: 8h, gate: {max: 12h}}\n")
	require.NoError(t, err, "no floor, and the ceilings themselves are allowed")
	require.Equal(t, time.Second, cfg.Timeouts.Limits(KindTask, 0).Idle)

	for yaml, want := range map[string]string{
		"timeouts: {check: 4h1m}\n":                  "timeouts.check: max 4h1m0s is above ape's hard ceiling for check checks (4h0m0s)",
		"timeouts: {regression: {max: 9h}}\n":        "hard ceiling for regression checks (8h0m0s)",
		"timeouts: {gate: 13h}\n":                    "hard ceiling for gate checks (12h0m0s)",
		"timeouts: {check: {idle: 20m, max: 10m}}\n": "timeouts.check: idle 20m0s is above its max 10m0s",
		"timeouts: {check: {idle: 3h}}\n":            "idle 3h0m0s is above its max 2h0m0s",
		"timeouts: {check: {idel: 3m}}\n":            "timeouts.check.idel: not a key",
		"timeouts: {gate: {idle: 0s}}\n":             "timeouts.gate.idle",
		"timeouts: {gate: {idle: soon}}\n":           "not a positive Go duration",
		"timeouts: {regression: [1m]}\n":             "timeouts.regression: is not a mapping",
	} {
		_, err := load(t, yaml)
		require.ErrorContains(t, err, want, yaml)
	}
}

func strip(t TierTimeout) TierTimeout { return TierTimeout{Idle: t.Idle, Max: t.Max} }

// fastSampling samples a running check's progress every 50ms.
func fastSampling(t *testing.T) {
	t.Helper()
	old := progressSample
	progressSample = 50 * time.Millisecond
	t.Cleanup(func() { progressSample = old })
}

// D58 2.4: a silent check that uses no CPU is killed at its idle window,
// with reason idle and exit 124; a silent check that burns CPU is busy,
// not idle; a check that prints is busy; a check that never goes idle
// is killed at its ceiling.
func TestRunCheck_IdleAndCeiling(t *testing.T) {
	needSh(t)
	fastSampling(t)
	dir := t.TempDir()
	lim := TierTimeout{Idle: 700 * time.Millisecond, Max: 20 * time.Second}

	start := time.Now()
	r := runCheck(ctx(), dir, "echo starting; sleep 30", filepath.Join(dir, "idle.log"), lim, nil)
	require.Equal(t, ExitTimeout, r.Exit)
	require.Equal(t, ReasonIdle, r.Reason)
	require.Contains(t, r.Detail, "no output and no CPU time for 700ms (timeouts.check.idle)")
	require.Less(t, time.Since(start), 10*time.Second)
	require.Contains(t, r.Tail, "starting", "the last output lines are kept")
	data, err := os.ReadFile(filepath.Join(dir, "idle.log"))
	require.NoError(t, err)
	require.Contains(t, string(data), "[ape] check timed out: no output and no CPU time")

	// Silent, but busy: a shell loop for ~2.5s, three idle windows long.
	busy := `end=$(( $(date +%s) + 3 )); i=0; while [ "$(date +%s)" -lt "$end" ]; do i=$((i+1)); done; echo done`
	r = runCheck(ctx(), dir, busy, "", lim, nil)
	require.Equal(t, 0, r.Exit, "CPU time is progress: %+v", r)

	// Printing, but asleep between lines.
	r = runCheck(ctx(), dir, "for i in 1 2 3 4 5 6 7 8; do echo $i; sleep 0.3; done", "", lim, nil)
	require.Equal(t, 0, r.Exit, "output is progress: %+v", r)

	start = time.Now()
	r = runCheck(ctx(), dir, "while :; do echo tick; sleep 0.1; done", "", TierTimeout{Idle: time.Second, Max: 1500 * time.Millisecond, tier: "gate"}, nil)
	require.Equal(t, ExitTimeout, r.Exit)
	require.Equal(t, ReasonCeiling, r.Reason)
	require.Contains(t, r.Detail, "still running at 1.5s (timeouts.gate.max)")
	require.Less(t, time.Since(start), 10*time.Second)
	require.Equal(t, "ceiling: still running at 1.5s (timeouts.gate.max)", r.Outcome())
}

// A timed-out check is not run again under retries: a hung check hung
// again proves nothing, and holds the run for another window.
func TestRunChecks_TimedOutIsNotRetried(t *testing.T) {
	fastSampling(t)
	p := fixture(t, baseFiles())
	write(t, p, "_apex/config.yaml", "retries: 2\ntimeouts: {check: {idle: 300ms, max: 1m}}\n")
	r := p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{{Ref: "T1", Kind: KindTask, Cmd: "sleep 20"}}, RunOptions{})[0]
	require.Equal(t, ReasonIdle, r.Reason)
	require.Zero(t, r.Tries)
}

func TestParsePSTime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:00.25":     250 * time.Millisecond,
		"1:02.50":     62500 * time.Millisecond,
		"01:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"2-01:00:00":  49 * time.Hour,
		"00:00:00.00": 0,
	} {
		got, ok := parsePSTime(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "12", "a:b", "1-x"} {
		_, ok := parsePSTime(bad)
		require.False(t, ok, bad)
	}
}

package usage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func reading(at time.Time, five, seven float64) *Reading {
	fr, sr := at.Add(2*time.Hour), at.Add(72*time.Hour)
	return &Reading{At: at, FiveHour: &five, FiveHourResetsAt: &fr, SevenDay: &seven, SevenDayResetsAt: &sr}
}

// W4-A2: every row of the guard's table, on a fake clock.
func TestDecide_TheTable(t *testing.T) {
	cfg := DefaultGuard()
	share := Share{FiveHour: DefaultShare, SevenDay: DefaultShare, Source: "default"}
	fresh := now.Add(-2 * time.Minute)

	d := Decide(cfg, reading(fresh, 0.14, 0.55), share, now, false)
	require.Equal(t, Launch, d.Outcome)
	require.Empty(t, d.Why)

	// five_hour 0.93 + share 0.05 > 0.95: held until its reset + 1 min.
	r := reading(fresh, 0.93, 0.55)
	d = Decide(cfg, r, share, now, false)
	require.Equal(t, Hold, d.Outcome)
	require.Equal(t, r.FiveHourResetsAt.Add(time.Minute), d.HeldUntil)
	require.Contains(t, d.Why, "the 5-hour window is at 0.93, with this launch's share 0.05 over limits.usage.five_hour 0.95")

	// seven_day 0.86 > 0.85: stop, with the window's reset.
	r = reading(fresh, 0.10, 0.86)
	d = Decide(cfg, r, share, now, false)
	require.Equal(t, Stop, d.Outcome)
	require.Equal(t, *r.SevenDayResetsAt, d.ResetsAt)
	require.Contains(t, d.Why, "the 7-day window is at 0.86, over limits.usage.seven_day 0.85")

	// seven_day under its limit, but + share over the ceiling: stop.
	d = Decide(cfg, reading(fresh, 0.10, 0.80), Share{FiveHour: 0.05, SevenDay: 0.20, Source: "auto"}, now, false)
	require.Equal(t, Stop, d.Outcome)
	require.Contains(t, d.Why, "with this launch's share 0.20 over limits.usage.ceiling 0.95")

	// A stop outranks a hold.
	require.Equal(t, Stop, Decide(cfg, reading(fresh, 0.99, 0.90), share, now, false).Outcome)

	// No reading, or one older than max_age: unknown, which launches.
	d = Decide(cfg, nil, share, now, false)
	require.Equal(t, Unknown, d.Outcome)
	require.Equal(t, "usage unknown (no reading yet)", d.Why)
	d = Decide(cfg, reading(now.Add(-11*time.Minute), 0.99, 0.99), share, now, false)
	require.Equal(t, Unknown, d.Outcome)
	require.Equal(t, "usage unknown (no reading in 10 min)", d.Why)

	// off: nothing checked; --usage-ok: launched, the reading kept.
	off := cfg
	off.Off = true
	require.Equal(t, Off, Decide(off, reading(fresh, 0.99, 0.99), share, now, false).Outcome)
	d = Decide(cfg, reading(fresh, 0.99, 0.99), share, now, true)
	require.Equal(t, UsageOK, d.Outcome)
	require.NotNil(t, d.Reading)
	require.Contains(t, d.Why, "--usage-ok")
}

// A window whose reset has passed since the reading counts as 0: the
// launch a hold waited for goes ahead on the same reading.
func TestDecide_ResetWindowCountsAsZero(t *testing.T) {
	cfg := DefaultGuard()
	share := Share{FiveHour: 0.05, SevenDay: 0.05}
	r := reading(now.Add(-3*time.Minute), 0.97, 0.20)
	d := Decide(cfg, r, share, now, false)
	require.Equal(t, Hold, d.Outcome)
	cfg.MaxAge = 3 * time.Hour
	after := d.HeldUntil
	d = Decide(cfg, r, share, after, false)
	require.Equal(t, Launch, d.Outcome)
	require.InDelta(t, 0, *d.FiveHour, 1e-9)

	// No reset named for the 5-hour window: held for its length.
	r.FiveHourResetsAt = nil
	d = Decide(DefaultGuard(), r, share, now, false)
	require.Equal(t, Hold, d.Outcome)
	require.Equal(t, now.Add(5*time.Hour), d.HeldUntil)
}

// share: auto is the skill's median over its last 10 measured runs, per
// window; below 3 runs, 0.05. A run whose window rolled over measures
// nothing for that window.
func TestAutoShare(t *testing.T) {
	TempCache(t)
	s := AutoShare("apex-apply")
	require.Equal(t, Share{FiveHour: DefaultShare, SevenDay: DefaultShare, Source: "default"}, s)

	add := func(skill string, five0, five1, seven0, seven1 float64) {
		t.Helper()
		b, a := reading(now, five0, seven0), reading(now.Add(time.Hour), five1, seven1)
		a.FiveHourResetsAt, a.SevenDayResetsAt = b.FiveHourResetsAt, b.SevenDayResetsAt
		require.NoError(t, AppendHistory(HistoryEntry{Skill: skill, Run: "r", At: now, Before: b, After: a}))
	}
	add("apex-apply", 0.10, 0.14, 0.50, 0.51) // 0.04, 0.01
	add("apex-apply", 0.20, 0.30, 0.51, 0.53) // 0.10, 0.02
	require.Equal(t, "default", AutoShare("apex-apply").Source, "two runs are not enough")
	add("apex-plan", 0.00, 0.50, 0.00, 0.50)
	add("apex-apply", 0.30, 0.36, 0.53, 0.56) // 0.06, 0.03
	s = AutoShare("apex-apply")
	require.Equal(t, "auto", s.Source)
	require.Equal(t, 3, s.FiveHourRuns)
	require.InDelta(t, 0.06, s.FiveHour, 1e-9)
	require.InDelta(t, 0.02, s.SevenDay, 1e-9)

	// A window that reset between the two readings is not a sample.
	b, a := reading(now, 0.90, 0.60), reading(now.Add(time.Hour), 0.02, 0.61)
	a.SevenDayResetsAt = b.SevenDayResetsAt
	require.NoError(t, AppendHistory(HistoryEntry{Skill: "apex-apply", Run: "r", At: now, Before: b, After: a}))
	s = AutoShare("apex-apply")
	require.Equal(t, 3, s.FiveHourRuns)
	require.Equal(t, 4, s.SevenDayRuns)
	require.InDelta(t, 0.015, s.SevenDay, 1e-9) // 0.01, 0.01, 0.02, 0.03

	// Only the last 10 runs count.
	for range 12 {
		add("apex-apply", 0.10, 0.30, 0.10, 0.30)
	}
	s = AutoShare("apex-apply")
	require.Equal(t, 10, s.FiveHourRuns)
	require.InDelta(t, 0.20, s.FiveHour, 1e-9)
}

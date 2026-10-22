package usage

import (
	"fmt"
	"time"
)

// The guard (W4-A2): before ape starts a session (every `ape task` of an
// APEX v1 project, the loop's children included), it reads the newest
// reading and decides —
//
//	fresh, under every limit                        launch
//	five_hour + share > limits.usage.five_hour      hold until five_hour_resets_at + 1 min, then decide again
//	seven_day > limits.usage.seven_day, or
//	seven_day + share > limits.usage.ceiling        stop: the session is not started (exit 14), a person's call
//	no reading, or one older than max_age           launch, with "usage unknown (no reading in 10 min)"
//	limits.usage.off: true                          launch, nothing checked
//
// A window whose resets_at has passed counts as 0: it reset since the
// reading. The 5-hour window holds, because it resolves itself within
// hours; the weekly one stops, because waiting days is a person's call.

// The defaults of limits.usage.
const (
	DefaultSevenDay = 0.85
	DefaultFiveHour = 0.95
	DefaultCeiling  = 0.95
	DefaultMaxAge   = 10 * time.Minute
)

// HoldMargin is how long after the 5-hour window's reset a held launch
// starts.
const HoldMargin = time.Minute

// fiveHourWindow is how long a launch is held when the reading names no reset
// for the 5-hour window: the window's whole length, at most.
const fiveHourWindow = 5 * time.Hour

// GuardConfig is limits.usage with its defaults filled in.
type GuardConfig struct {
	SevenDay, FiveHour, Ceiling float64
	// Share is a configured share (nil: share: auto).
	Share  *float64
	MaxAge time.Duration
	Off    bool
}

// DefaultGuard is limits.usage when the config sets none.
func DefaultGuard() GuardConfig {
	return GuardConfig{SevenDay: DefaultSevenDay, FiveHour: DefaultFiveHour, Ceiling: DefaultCeiling, MaxAge: DefaultMaxAge}
}

// Outcome is the guard's decision.
type Outcome string

// The outcomes, as the run manifest's usage.guard.decision records them.
const (
	Launch  Outcome = "launch"
	Hold    Outcome = "hold"
	Stop    Outcome = "stop"
	Unknown Outcome = "unknown"
	Off     Outcome = "off"
	UsageOK Outcome = "usage_ok"
)

// Decision is the guard's decision on one launch.
type Decision struct {
	Outcome Outcome
	// Reading is the reading decided on (nil: none).
	Reading *Reading
	// FiveHour / SevenDay are the windows as decided on: the reading's,
	// 0 for a window that reset since it, nil for one it does not carry.
	FiveHour, SevenDay *float64
	Share              Share
	// HeldUntil is when a held launch decides again (Hold).
	HeldUntil time.Time
	// ResetsAt is when the window that stopped the launch resets (Stop;
	// zero when the reading names none).
	ResetsAt time.Time
	// Why is the decision in one line, for a hold, a stop or an unknown
	// reading ("" for a plain launch).
	Why string
}

// Decide is the guard's decision at now, on reading r (nil: none) with the
// launch's estimated share. usageOK is a person's --usage-ok: the launch
// goes ahead whatever the reading says.
func Decide(cfg GuardConfig, r *Reading, share Share, now time.Time, usageOK bool) Decision {
	d := Decision{Reading: r, Share: share}
	switch {
	case cfg.Off:
		d.Outcome = Off
		return d
	case usageOK:
		d.Outcome = UsageOK
		d.Why = "usage guard overridden by a person (--usage-ok)"
		if r != nil {
			d.Why += ", on the reading " + r.Short() + " (" + Ago(r.Age(now)) + ")"
		}
		return d
	case r == nil:
		d.Outcome, d.Why = Unknown, "usage unknown (no reading yet)"
		return d
	case r.Age(now) > cfg.MaxAge:
		d.Outcome, d.Why = Unknown, "usage unknown (no reading in "+Minutes(cfg.MaxAge)+")"
		return d
	}
	d.FiveHour = current(r.FiveHour, r.FiveHourResetsAt, now)
	d.SevenDay = current(r.SevenDay, r.SevenDayResetsAt, now)
	if sd := d.SevenDay; sd != nil {
		over := ""
		switch {
		case *sd > cfg.SevenDay:
			over = "over limits.usage.seven_day " + frac(cfg.SevenDay)
		case *sd+share.SevenDay > cfg.Ceiling:
			over = fmt.Sprintf("with this launch's share %s over limits.usage.ceiling %s", frac(share.SevenDay), frac(cfg.Ceiling))
		}
		if over != "" {
			d.Outcome = Stop
			if r.SevenDayResetsAt != nil {
				d.ResetsAt = *r.SevenDayResetsAt
			}
			d.Why = fmt.Sprintf("the 7-day window is at %s, %s", frac(*sd), over)
			return d
		}
	}
	if fh := d.FiveHour; fh != nil && *fh+share.FiveHour > cfg.FiveHour {
		d.Outcome = Hold
		if r.FiveHourResetsAt != nil {
			d.HeldUntil = r.FiveHourResetsAt.Add(HoldMargin)
		} else {
			d.HeldUntil = now.Add(fiveHourWindow)
		}
		d.Why = fmt.Sprintf("the 5-hour window is at %s, with this launch's share %s over limits.usage.five_hour %s",
			frac(*fh), frac(share.FiveHour), frac(cfg.FiveHour))
		return d
	}
	d.Outcome = Launch
	return d
}

// current is a window at now: 0 once its reset has passed.
func current(v *float64, resetsAt *time.Time, now time.Time) *float64 {
	if v == nil {
		return nil
	}
	if resetsAt != nil && !now.Before(*resetsAt) {
		zero := 0.0
		return &zero
	}
	return v
}

func frac(v float64) string { return fmt.Sprintf("%.2f", v) }

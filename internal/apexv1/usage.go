package apexv1

import (
	"time"

	"github.com/exoport/apex_process_ape/internal/usage"
)

// UsageStatus is `ape status`'s usage: the newest reading of the plan's
// windows (W4, internal/usage) and how old it is.
type UsageStatus struct {
	usage.Reading
	AgeSeconds float64 `json:"age_seconds"`
}

// usageStatus is the newest reading at now, nil when there is none.
func usageStatus(now time.Time) *UsageStatus {
	r := usage.Load()
	if r == nil {
		return nil
	}
	return &UsageStatus{Reading: *r, AgeSeconds: r.Age(now).Seconds()}
}

// Describe is the reading as `ape status` shows it: "usage: 5h 0.14 · 7d
// 0.55 (read 2 min ago)", or "usage: unknown".
func (u *UsageStatus) Describe() string {
	if u == nil {
		return "usage: unknown"
	}
	return "usage: " + u.Short() + " (read " + usage.Ago(time.Duration(u.AgeSeconds*float64(time.Second))) + ")"
}

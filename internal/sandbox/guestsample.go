package sandbox

import "sort"

// The pure half of the in-guest sampler (PLAN-24 D6): the types and the
// arithmetic that turns two /proc readings into a busy verdict. The readings
// themselves are Linux-only (guestsample_linux.go); this file is what the tests
// and the Windows build see.

// GuestSample is one raw reading of the guest's CPU accounting. Two of them,
// taken a window apart, produce a utilisation figure.
type GuestSample struct {
	// TotalJiffies is the sum of every field on /proc/stat's aggregate cpu line.
	TotalJiffies uint64
	// IdleJiffies is that line's idle + iowait.
	IdleJiffies uint64
	// PerProcess maps a process name to its consumed jiffies (utime + stime).
	// Keyed by name rather than pid because the interesting answer is "a go
	// build", not "pid 4711" — and a build spawns hundreds of short-lived pids
	// whose individual deltas say nothing.
	PerProcess map[string]uint64
}

// CPUPercentBetween returns guest CPU utilisation across the window between two
// samples, and whether it could be determined at all.
//
// The false return is load-bearing, not defensive noise: an unreadable /proc, or
// two samples taken so close together that no jiffy ticked, yields NO answer —
// and the agent must publish no verdict rather than a 0% that the reaper would
// read as a workspace sitting idle.
func CPUPercentBetween(prev, cur GuestSample) (percent float64, ok bool) {
	if cur.TotalJiffies <= prev.TotalJiffies {
		return 0, false
	}
	total := cur.TotalJiffies - prev.TotalJiffies
	var idle uint64
	if cur.IdleJiffies > prev.IdleJiffies {
		idle = cur.IdleJiffies - prev.IdleJiffies
	}
	if idle > total {
		idle = total // a counter that went backwards; clamp rather than go negative
	}
	return float64(total-idle) / float64(total) * 100, true
}

// TopProcesses returns the busiest process names between two samples, most
// active first, capped at n. Only processes that actually consumed CPU appear —
// a list of idle daemons is not evidence of work.
func TopProcesses(prev, cur GuestSample, n int) []string {
	if n <= 0 {
		return nil
	}
	type entry struct {
		name string
		d    uint64
	}
	deltas := make([]entry, 0, len(cur.PerProcess))
	for name, now := range cur.PerProcess {
		before := prev.PerProcess[name]
		if now > before {
			deltas = append(deltas, entry{name: name, d: now - before})
		}
	}
	sort.Slice(deltas, func(i, j int) bool {
		if deltas[i].d != deltas[j].d {
			return deltas[i].d > deltas[j].d
		}
		return deltas[i].name < deltas[j].name // stable output for equal deltas
	})
	if len(deltas) > n {
		deltas = deltas[:n]
	}
	out := make([]string, 0, len(deltas))
	for _, e := range deltas {
		out = append(out, e.name)
	}
	return out
}

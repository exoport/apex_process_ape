package slots

import (
	"os"
	"strconv"
	"strings"
)

// HeavyGuard is the host-load guard on `heavy` slots (OQ13): a unit is
// granted only while the host has room for one more run. v1.4 ran three
// cells at once per host at about 1 GiB each, with no loss traced to load
// (D617, D618, D628); those are the defaults.
type HeavyGuard struct {
	Capacity    int     // units per host (3)
	PerRunBytes int64   // memory one run needs at peak (1 GiB)
	MaxPSI      float64 // memory pressure, some avg10, above which nothing more starts (10)
}

// DefaultHeavyGuard is OQ13's starting point.
var DefaultHeavyGuard = HeavyGuard{Capacity: 3, PerRunBytes: 1 << 30, MaxPSI: 10}

// Room is how many more heavy units the host can take now: the capacity
// less those held, and none at all when available memory is short of one
// run or memory pressure is high. availBytes < 0 or psi < 0 is "unknown",
// which does not block (a host without PSI still runs).
func (g HeavyGuard) Room(held int, availBytes int64, psiAvg10 float64) int {
	room := g.Capacity - held
	if room <= 0 {
		return 0
	}
	if availBytes >= 0 && availBytes < g.PerRunBytes {
		return 0
	}
	if psiAvg10 >= 0 && psiAvg10 > g.MaxPSI {
		return 0
	}
	if availBytes >= 0 {
		room = min(room, int(availBytes/g.PerRunBytes))
	}
	return room
}

// MemoryPSI reads /proc/pressure/memory's "some avg10", -1 when the host
// has no PSI (not Linux, or an old kernel).
func MemoryPSI() float64 {
	data, err := os.ReadFile("/proc/pressure/memory")
	if err != nil {
		return -1
	}
	return parsePSI(string(data))
}

func parsePSI(body string) float64 {
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for f := range strings.FieldsSeq(line) {
			if v, ok := strings.CutPrefix(f, "avg10="); ok {
				if x, err := strconv.ParseFloat(v, 64); err == nil {
					return x
				}
			}
		}
	}
	return -1
}

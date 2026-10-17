package apexv1

import (
	"strconv"
	"strings"
	"time"
)

// parsePSTime reads ps's cumulative CPU time, [[dd-]hh:]mm:ss[.cc].
func parsePSTime(s string) (time.Duration, bool) {
	var days int64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, false
	}
	total := time.Duration(days)*24*time.Hour + time.Duration(secs*float64(time.Second))
	for i, unit := range []time.Duration{time.Minute, time.Hour} {
		idx := len(parts) - 2 - i
		if idx < 0 {
			break
		}
		n, err := strconv.ParseInt(parts[idx], 10, 64)
		if err != nil {
			return 0, false
		}
		total += time.Duration(n) * unit
	}
	return total, true
}

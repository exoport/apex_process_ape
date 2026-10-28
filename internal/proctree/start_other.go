//go:build !linux

package proctree

import "time"

// StartTime is unknown off Linux: there is no /proc to read it from, so
// SameProcess falls back to the pid existing.
func StartTime(int) (time.Time, bool) { return time.Time{}, false }

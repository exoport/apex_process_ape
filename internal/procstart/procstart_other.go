//go:build !linux && !darwin && !windows

package procstart

import "time"

// osStart has no record to read here: Time falls back to initStamp.
func osStart() (time.Time, bool) { return time.Time{}, false }

//go:build unix && !linux

package apecmd

import (
	"testing"
	"time"
)

// childOpened is the Linux readiness wait's stand-in where there is no
// inotify: a fixed wait, long enough for a race-built child to reach its
// SIGTERM handler (RK-1, Q143).
func childOpened(*testing.T, string) func(time.Duration) bool {
	return func(time.Duration) bool {
		time.Sleep(2 * time.Second)
		return true
	}
}

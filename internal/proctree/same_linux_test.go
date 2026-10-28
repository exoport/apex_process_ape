//go:build linux

package proctree

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// D620 (FINDING-C3-6): a record names a process by its pid AND its start;
// a pid that merely exists may be a reused one.
func TestSameProcess_AReusedPidIsNotTheRecordsProcess(t *testing.T) {
	self := os.Getpid()
	start, ok := StartTime(self)
	require.True(t, ok)
	require.WithinDuration(t, time.Now(), start, time.Hour, "this test binary started moments ago")

	require.True(t, SameProcess(self, start, time.Time{}), "its own start")
	require.False(t, SameProcess(self, start.Add(-time.Hour), time.Time{}), "a record of an earlier process with this pid")
	require.True(t, SameProcess(self, time.Time{}, time.Now()), "a record written after it started")
	require.False(t, SameProcess(self, time.Time{}, start.Add(-time.Hour)), "a record written an hour before it started: written by another process")
	require.True(t, SameProcess(self, time.Time{}, time.Time{}), "nothing recorded: the pid existing is all there is")
	require.False(t, SameProcess(0x7FFFFFFE, time.Time{}, time.Time{}), "no such pid")
}

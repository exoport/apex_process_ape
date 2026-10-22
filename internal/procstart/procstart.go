// Package procstart answers when the running process started.
//
// The v1 stop gate pairs ape's own Stop notification with the project
// stop hook's verdict by time (apexv1.WaitStopVerdict): the verdict's run
// must not have started long before the Stop. Stamping the Stop when the
// notification ARRIVED made any delay inside `ape notify` — a slow start
// on a loaded machine, a terminal query at init (D365) — look like a
// later Stop, and past the gate's slack it took the next turn's verdict.
// The notifying process's own start is the moment Claude Code ran the
// hook, which is what the pairing means to compare.
//
// A wall-clock time of this process only. apexv1's processStart is the
// other reader of the same OS records, for any pid, as an opaque identity
// a run lock compares; neither shape serves the other.
package procstart

import "time"

// initStamp is the fallback: the moment this package was initialised,
// before main and after every package it does not depend on.
var initStamp = time.Now()

// Time is when this process started: the operating system's record where
// ape can read it (Linux /proc, macOS sysctl, Windows GetProcessTimes),
// otherwise the moment this package was initialised. It is never after
// that moment: a record that says so is not believed.
func Time() time.Time {
	if t, ok := osStart(); ok && !t.After(initStamp) {
		return t
	}
	return initStamp
}

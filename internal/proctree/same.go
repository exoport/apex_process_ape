package proctree

import "time"

// startSlack is how far a start time read now may differ from one read
// earlier for the same process: /proc's boot time is whole seconds, and
// both are converted through it.
const startSlack = 2 * time.Second

// SameProcess reports whether pid is still the process a record names
// (D620, FINDING-C3-6): a pid that merely exists may be a reused one. start
// is the process's own start time as recorded (StartTime when the record
// was written): the pid's start must match it. A record without one may
// give notAfter, when the record was written by that process: a process
// that started after it is another one. With neither, or where no start
// time can be read (off Linux), the pid existing is all there is to go on.
func SameProcess(pid int, start, notAfter time.Time) bool {
	if !Alive(pid) {
		return false
	}
	now, ok := StartTime(pid)
	switch {
	case !ok:
		return true
	case !start.IsZero():
		d := now.Sub(start)
		return d <= startSlack && d >= -startSlack
	case !notAfter.IsZero():
		return !now.After(notAfter.Add(startSlack))
	}
	return true
}

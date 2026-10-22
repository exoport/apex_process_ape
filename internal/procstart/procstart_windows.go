//go:build windows

package procstart

import (
	"time"

	"golang.org/x/sys/windows"
)

// osStart is the process's creation time.
func osStart() (time.Time, bool) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, creation.Nanoseconds()), true
}

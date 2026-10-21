package apexv1

import (
	"strconv"

	"golang.org/x/sys/windows"
)

// processStart is an opaque identity of when pid started (Q144); on
// Windows the process's creation time.
func processStart(pid int) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // a pid ape recorded
	if err != nil {
		return "", false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", false
	}
	return strconv.FormatInt(created.Nanoseconds(), 10), true
}

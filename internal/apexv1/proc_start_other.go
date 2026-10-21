//go:build !linux && !darwin && !windows

package apexv1

// processStart cannot be read here: a lock is then judged by its pid
// alone, as before Q144.
func processStart(int) (string, bool) { return "", false }

//go:build !linux

package apexv1

// The /proc readings of checkinfra.go are Linux's; elsewhere nothing
// outside a check is attributed to it, and a taken port names no holder.

func endTmpHolders(string, func(int) bool) []string { return nil }
func listenerPID(int) int                           { return 0 }
func procCgroupMemoryPeak(int) int64                { return 0 }

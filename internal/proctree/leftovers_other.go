//go:build !linux

package proctree

import (
	"context"
	"time"
)

// findLeftovers finds nothing off Linux: there is no /proc to read another
// process's environment from (macOS would need sysctl KERN_PROCARGS2 or
// `ps -E`, which ape does not read yet), so no leftover is looked for or
// recorded.
func findLeftovers(string) []procInfo { return nil }

func stopLeftover(context.Context, procInfo, time.Duration) string { return LeftoverKept }

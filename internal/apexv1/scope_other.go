//go:build !linux

package apexv1

import "time"

// Check scopes are Linux's (scope_linux.go); elsewhere the process group
// (or, on Windows, the job object) is the reaping.

var checkScopes = "group"

func newScope() (unit, dir, runner string)                  { return "", "", "" }
func scopeArgv(runner, unit string, argv []string) []string { return argv }
func scopePIDs(string) []int                                { return nil }
func scopeMemoryPeak(string) int64                          { return 0 }
func scopeCPU(string) (time.Duration, bool)                 { return 0, false }
func scopeKill(string) bool                                 { return false }
func pidArgv(int) []string                                  { return nil }

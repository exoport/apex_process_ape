//go:build !linux

package sandbox

// The non-Linux stubs for the in-guest sampler (PLAN-24 D6).
//
// A workspace guest is always Linux, so these are never the real path. They
// exist because `ape` is one binary that must build and cross-compile for
// darwin and windows — the CI job that catches portability breaks compiles
// everything — and because a developer running the agent off a node should get
// an honest "no reading" rather than a fabricated one.
//
// SampleGuest returning zeroed jiffies is what makes that honest:
// CPUPercentBetween reports ok=false on it, and the agent publishes no busy
// verdict rather than a 0% the reaper would read as idle.

// SampleGuest returns an empty sample: there is no /proc to read here.
func SampleGuest() GuestSample { return GuestSample{PerProcess: map[string]uint64{}} }

// GuestLoad1 returns 0: no load average is available off Linux.
func GuestLoad1() float64 { return 0 }

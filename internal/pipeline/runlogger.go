package pipeline

import "time"

// RunLogger is the runner's view of the PLAN-5 / C6 run-log writer.
// Defined as an interface here so the pipeline package can stay free
// of an internal/runlog import (which would create a coupling that
// makes pipeline tests harder to reason about).
//
// The CLI hands the runner a concrete *runlog.Writer that satisfies
// this interface. Nil is a valid value — every method returns
// silently when called on a nil RunLogger.
type RunLogger interface {
	// CheckpointKindStep matches *runlog.Writer.CheckpointKindStep —
	// the adapter shape that lets pipeline write to runlog without
	// importing it. Implementations must be safe for concurrent use.
	CheckpointKindStep(kind, step string, payload any, at time.Time)
}

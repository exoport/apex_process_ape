package change

// The vocabulary of apex-maintenance's terminal contract, which the v0
// maintenance lane composed commits from. Kept for the framework contract
// gate (TestContract_LiveMaintenanceContract), which holds the framework's
// template to it; the lane itself went with APEX v0.

// Outcome values for maintenance_status.
const (
	StatusLanded    = "landed"
	StatusRefused   = "refused"
	StatusEscalated = "escalated"
	StatusHalted    = "halted"
)

// Per-goal status values.
const (
	GoalLanded     = "landed"
	GoalHalted     = "halted"
	GoalNotStarted = "not-started"
)

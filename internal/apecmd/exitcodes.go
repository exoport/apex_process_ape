package apecmd

// Exit codes returned via os.Exit across ape commands. Single source of
// truth (PLAN-9 F3.4) so the meaning of a code is uniform command to
// command; each command's Long help documents which codes it can
// produce. The table starts from the shipped `ape task` convention
// (PLAN-11); PLAN-17 registers its reporting codes here.
//
//	0  success
//	1  the operation ran but failed — a skill exited non-zero, the
//	   Stop-hook wait errored, or the idle-without-Stop timeout fired
//	2  usage or preflight error — bad flags, unknown skill/pipeline, or
//	   the dirty-tree gate; detected before any claude process spawns
//	3  the claude REPL never became ready inside the PTY — the
//	   trust-dialog dismissal failed or an unknown modal blocked input;
//	   the last pane snapshot is written to stderr for diagnosis
const (
	ExitOK           = 0
	ExitRunFailed    = 1
	ExitUsage        = 2
	ExitREPLNotReady = 3
)

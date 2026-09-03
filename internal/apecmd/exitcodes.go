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
//	4  claude exited before the Stop hook fired (PLAN-12 `ape prompt`) —
//	   the process died mid-session, so the run neither completed nor
//	   idled out
//	5  the session's own turn failed against the API and nothing followed
//	   it — a 529/522/… carried verbatim in the envelope. Distinct from 1
//	   because it is upstream and retryable: the skill did not misbehave,
//	   and a caller that knows its budget can decide to re-run. Without it
//	   the run would sit until the idle ceiling and then report 1, which
//	   says only "nothing happened"
//	6  the dispatch violated the project's declared commit ownership
//	   (`_apex/commit-owners.csv`) — a non-committer moved HEAD, staged
//	   content or touched the stash, or a declared committer produced no
//	   commit or one whose subject matches none of its declared formats.
//	   Distinct from 1 because the skill's own work may have SUCCEEDED:
//	   the run completed and the repository is not in the state the
//	   framework declared it would be, which is a different thing to
//	   report and a different thing to fix
const (
	ExitOK           = 0
	ExitRunFailed    = 1
	ExitUsage        = 2
	ExitREPLNotReady = 3
	ExitClaudeDied   = 4
	// ExitUpstreamAPI is sessiondriver.TerminalAPIError reaching a command.
	ExitUpstreamAPI = 5
	// ExitCommitContract is a failed per-dispatch commit-ownership
	// assertion (PLAN-26 D2).
	ExitCommitContract = 6
)

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
//
//	14 a claude.ai plan usage limit stopped the session (H20): ape read
//	   Claude Code's usage-limit line in the PTY pane and ended the run
//	   (termination usage_limit, with the reset when the line names it).
//	   Distinct from 1 and 5: nothing failed, and re-running before the
//	   reset fails the same way, so a caller waits for the reset
//
//	15 the session lost its Claude Code login (D603, termination
//	   auth_lost): its last turn failed with authentication_failed and
//	   nothing followed for the API-error grace window. Distinct from 5:
//	   re-running fails the same way until a person logs in
//
// Other codes above 6 are command-local and are NOT declared here, because no
// two commands mean the same thing by them. `ape framework` uses 3..7 for
// its own install failures (framework.go), and 8, 9 and 10 are reserved
// for `ape change`'s three outcomes — escalated, refused, halted
// (PLAN-68 B.3), which land later and declare them in their own file.
// This note is that reservation: a command taking one of those numbers
// for an unrelated meaning makes the range unreadable, and 7 is gone.
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
	// ExitUsageLimit is a usage-limit stop (H20): the next code no
	// command uses (11–13 are framework's and the toolchain's).
	ExitUsageLimit = 14
	// ExitAuthLost is sessiondriver.AuthLostError reaching a command
	// (D603): the next free code.
	ExitAuthLost = 15
)

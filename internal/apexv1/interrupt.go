package apexv1

import (
	"context"
	"errors"
)

// Interrupted is not red (D58 2.2). A start, verify, archive, gate or
// close whose ape gets SIGTERM or SIGINT mid-check — Claude Code ending
// the session that ran it, a person's Ctrl-C — has its checks' process
// groups killed (the context cancels them) and then records NOTHING: no
// red attempt, no last-verify, no gate run, no state commit. A check
// killed by a signal says nothing about the code. The command fails with
// run.interrupted (action retry): run it again.

// CodeRunInterrupted: the run was interrupted before it finished, or a
// run lock's process is gone without a result (runlock.go).
const CodeRunInterrupted = "run.interrupted"

// interrupted returns run.interrupted when ctx was cancelled: what is the
// run it ends ("the verify of add-x").
func interrupted(ctx context.Context, what string) error {
	if ctx.Err() == nil {
		return nil
	}
	why := "a signal (SIGTERM or SIGINT)"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		why = "its deadline"
	}
	return &FailError{Code: CodeRunInterrupted, Msg: what + " was interrupted by " + why +
		" before it finished: its checks were stopped, nothing was recorded and no attempt counted — run the same command again"}
}

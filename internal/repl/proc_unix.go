//go:build linux || darwin

package repl

import (
	"context"
	"time"

	"github.com/exoport/apex_process_ape/internal/proctree"
)

// procGroupKillGrace is how long a SIGTERMed child group has to shut
// down cleanly before we escalate to SIGKILL. Mirrors the
// programmatic runner's `internal/pipeline/proc_unix.go` constant of
// the same name — 500ms is enough for a well-behaved REPL + bridge
// child to flush state, short enough that an unresponsive group
// doesn't hang the runner.
const procGroupKillGrace = 500 * time.Millisecond

// terminateGroup stops the claude child AND everything it started.
//
// go-pty's Unix backend sets SysProcAttr.Setsid on the child, so its pgid
// equals its pid, and a negative-pid kill addresses that group. That was
// once believed to be all of it. It is not: claude's Bash tool runs each
// command outside claude's process group, so a skill's `go test` or build
// kept running — and kept writing into the project — after ape stopped
// the run. Measured on claude 2.1.285: a `sleep 90` survived both SIGTERM
// and SIGKILL of the group.
//
// So the descendants are snapshotted by parent pid BEFORE anything is
// signalled (once claude dies they are re-parented and the tree is lost),
// then each is signalled by pid as well as the group, and the SIGKILL
// escalation is synchronous: it used to run in a goroutine 500 ms later,
// and ape, which exits about 0.1 s after a SIGTERM, was gone before it
// fired. proctree.KillTree does all of it; ape service stops a job's tree
// the same way.
func terminateGroup(ctx context.Context, pid int) {
	proctree.KillTree(ctx, pid, procGroupKillGrace)
}

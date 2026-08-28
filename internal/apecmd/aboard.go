package apecmd

import (
	"github.com/exoport/aboard/pkg/aboard"
	aboardcli "github.com/exoport/aboard/pkg/aboard/cli"
	"github.com/spf13/cobra"
)

// aboardArgv0 is the invocation aboard names in its own output. It is the
// command the user actually typed, not ape's binary name — the board records
// it in `.aboard/run/instance.json` and reports it from `/health`, so a
// reader can be told the command they have rather than the one the library
// was written for.
const aboardArgv0 = "ape aboard"

// newAboardCmd mounts aboard's command tree under `ape aboard`.
//
// aboard is a library by construction — no package-level cobra state, no
// init() that registers commands, no reads of os.Args, and no os.Exit outside
// its own Execute (which a host never calls). That is what makes this a
// single AddCommand rather than a port.
//
// Both hosts resolve the SAME `.aboard/` per project, derive the same port
// from it, and write the same state file: a board started by `ape aboard
// serve` is the board a bare `aboard status` reports, and either binary can
// drive it. Exactly one string differs — `app`/`host` becomes `ape-aboard`,
// so an error message can name the command the reader has. The capability
// manifest must NOT differ: aboard.AppName stays "aboard" in both hosts
// because it describes the board, not the process serving it, so capsHash is
// identical either way. A change here that moves capsHash is a bug in the
// change — an agent reading the manifest must not be able to tell which host
// is serving.
//
// There is deliberately no version passed in. ape's buildident resolves
// identity from ldflags/info.Main, which is right for ape and wrong for a
// dependency: read unchanged it would report ape's tag as the board's
// version. aboard resolves its own from info.Deps when it is not the main
// module, so `ape aboard version` reports aboard's.
// aboardOptions is the configuration ape mounts the board with. Split out so
// a test can assert against the real thing rather than a copy that could
// drift from it.
func aboardOptions() aboardcli.Options {
	return aboardcli.Options{
		Host:  aboard.HostApe,
		Argv0: aboardArgv0,
	}
}

func newAboardCmd() *cobra.Command {
	cmd := aboardcli.NewRootCmd(aboardOptions())

	// Shadow ape's root PersistentPreRun for this subtree.
	//
	// Cobra runs the CLOSEST PersistentPreRun walking up from the executed
	// command, and aboard's tree defines none — so without this, every board
	// command would inherit ape's background update check and could print
	// "update available: … run 'ape update'" onto stderr mid-board-output.
	// Because that check is fired as a goroutine, whether it appears at all
	// depends on it winning the race against process exit, which makes the
	// stderr of a board command nondeterministic.
	//
	// The board is reachable from the standalone binary too and its output
	// must not differ by host; a notice about ape is not the board's to
	// carry. An empty function, not a name check on the subtree, because a
	// string match would silently stop matching if the command is renamed.
	cmd.PersistentPreRun = func(*cobra.Command, []string) {}

	return cmd
}

// aboardExitCode maps an error from the mounted tree onto its own exit table.
//
// aboard declares four statuses — 0 ok, 1 failed, 2 usage, 3 `wait` timed out
// — and ape's ExitCode recognises none of them: it matches ape's own
// *exitError and otherwise returns ExitRunFailed. Left alone, `ape aboard
// export` (a usage error) and `ape aboard wait` (a timeout) would both exit 1,
// and exit 3 in particular is documented and scripted against.
//
// aboardcli.ExitCode returns ExitFailed for an error it does not own, which
// is also ape's fallback — so "prefer aboard's answer when it is not 1" is
// correct in both directions: an aboard error keeps its status, and anything
// else falls through to ape's own mapping unchanged.
func aboardExitCode(err error) (code int, silent, handled bool) {
	if err == nil {
		return ExitOK, false, false
	}
	if c, s := aboardcli.ExitCode(err); c != aboard.ExitFailed {
		return c, s, true
	}
	return ExitRunFailed, false, false
}

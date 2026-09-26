package apecmd

import (
	"fmt"
	"os"

	"github.com/exoport/apex_process_ape/internal/repl"
)

// refuseNested stops a session-starting command inside an ape-spawned
// session. The rule: ape never runs inside an ape session.
//
// Every claude session ape spawns carries APE_SESSION=<kind>/<run-id>
// (repl.EnvApeSession), and claude hands its environment to every tool
// subprocess — so a skill that shells out to `ape task`, `ape pipeline` or
// `ape change` inside one reaches here with the marker set. Nesting used to
// be supported by design, and it put an outer run's Stop hook, idle anchor
// and cost record around work they could not see.
//
// Project-data commands (story, sprint, config, registry, …) never call
// this: skills run them inside sessions all the time, and they start
// nothing. There is deliberately no escape hatch: a variable that disables
// the rule is a variable a session can set.
func refuseNested(command string) error {
	owner := os.Getenv(repl.EnvApeSession)
	if owner == "" {
		return nil
	}
	return usageErrExit(ExitUsage, fmt.Errorf(
		"%s refused: this process is inside the ape session %s (%s is set), and ape never runs "+
			"inside an ape-spawned session. Start it from a plain shell or a plain Claude Code session",
		command, owner, repl.EnvApeSession))
}

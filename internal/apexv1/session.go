package apexv1

import "os"

// CodePersonOnly: a flag only a person may pass was given from a model
// session (BRIEF D45: --allow-pending-human, --accept-red).
const CodePersonOnly = "session.person-only"

// envApeSession is repl.EnvApeSession: every session ape spawns carries it.
const envApeSession = "APE_SESSION"

// ModelSession names the variable that marks this process as running
// under a model's tool call, or "" for a person's terminal. ape's own
// sessions carry APE_SESSION; Claude Code sets CLAUDECODE=1 in every Bash
// tool call, ape's or not — including a person's `!` line inside Claude
// Code, which is why a person-only flag is run from a plain terminal.
func ModelSession() string {
	for _, k := range []string{envApeSession, "CLAUDECODE"} {
		if os.Getenv(k) != "" {
			return k
		}
	}
	return ""
}

// requirePerson refuses a person-only flag in a model session.
func requirePerson(flag string) error {
	if k := ModelSession(); k != "" {
		return &UsageError{Code: CodePersonOnly, Msg: flag + " is for a person, and this runs in a model session (" + k + " is set)" +
			" — a person runs the command from a terminal outside Claude Code"}
	}
	return nil
}

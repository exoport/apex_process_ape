package apexv1

import (
	"context"
	"os"
)

// CodePersonOnly: a flag or command only a person may use was given from a
// model session (BRIEF D45: --allow-pending-human, --accept-red; D47 Q6:
// ape milestone approve, ape change approve, ape change answer; OI-4:
// ape change accept --by, ape decision accept) with no grant from the
// person's own words (E2, personact.go).
const CodePersonOnly = "session.person-only"

// envApeSession is repl.EnvApeSession: every session ape spawns carries it.
const envApeSession = "APE_SESSION"

// ModelSession names the variable that marks this process as running
// under a model's tool call, or "" for a person's terminal. ape's own
// sessions carry APE_SESSION; Claude Code sets CLAUDECODE=1 in every Bash
// tool call, ape's or not — including a person's `!` line inside Claude
// Code, which fires no hook (Q102) and so can never carry a grant.
func ModelSession() string {
	for _, k := range []string{envApeSession, "CLAUDECODE"} {
		if os.Getenv(k) != "" {
			return k
		}
	}
	return ""
}

// requirePerson refuses a person-only flag or command (act, one of the
// Act* constants) in a model session, unless — in a person's own Claude
// Code session, never under APE_SESSION (T7) — ctx carries that act
// granted by the person's own words (E2: ConsumeGrant, LockPersonAct).
func requirePerson(ctx context.Context, act, flag string) error {
	k := ModelSession()
	if k == "" {
		return nil
	}
	if a := PersonActFrom(ctx); k == "CLAUDECODE" && a != nil && a.granted && a.Act == act {
		return nil
	}
	msg := flag + " is for a person, and this runs in a model session (" + k + " is set)"
	if k == "CLAUDECODE" {
		msg += " with no grant from the person's own words — in Claude Code the person types the act's phrase (`ape hook pre-tool --help`)" +
			" and the model runs the command in that turn; a `!` line fires no hook and cannot grant it; or a person runs the command from a terminal outside Claude Code"
	} else {
		msg += " — a person runs the command from a terminal outside Claude Code"
	}
	return &UsageError{Code: CodePersonOnly, Msg: msg}
}

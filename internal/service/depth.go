package service

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// The nesting rule for submitted jobs (D62, D83).
//
// A task or prompt job is a model session like any other `ape task` or
// `ape prompt`, but the daemon that starts it is depth 0: it cannot see the
// session a request was published from. So the client stamps the caller's
// place in the rule (CallerStamp, from ape's own record of the session it
// runs in), and the daemon decides from the stamp:
//
//   - no stamp: refused (fail closed, Q43): a pre-rc.4 client, or a raw
//     publish that did not say where it comes from;
//   - depth 2 or more: refused, nothing spawned;
//   - depth 1: allowed only from a running apex-loop run in the
//     job's project, for a skill on its allow-list, one child at a time —
//     the SessionGate (apecmd) checks it with the functions a direct
//     `ape task` uses, and the job's `ape task` runs under the caller's
//     session marker, so it applies the same rule again itself, takes the
//     child lock, records the orchestrator as its parent and dies with it;
//   - depth 0: as before.
//
// A script job is a conductor (`ape script` refuses inside any ape
// session), so it is refused from depth 1 too.
//
// The residual, documented beside D62's double fork: the stamp is the
// client's word. A raw NATS publish can claim depth 0, so the NATS
// credentials and subject permissions remain the boundary (anyone who can
// publish on the service subjects can run skills on the daemon's machine).

// SessionGate decides a depth-1 caller's job (the daemon has refused every
// other non-zero depth): nil error and the environment the job's ape
// process runs under, or the refusal — an apexv1.UsageError or
// ConflictError, whose code and action the reply carries.
type SessionGate func(kind Kind, req RunRequest) (env []string, err error)

// RefusalError is a job the nesting rule refuses: Code is the apexv1 code
// (session.depth, session.not-allowed, session.child-running,
// session.orchestrator-running), Action its action.
type RefusalError struct {
	Code, Action, Msg string
}

// refusalBody is the data of a refused *.run reply: the refusal's code,
// action and message, and the wire version.
type refusalBody struct {
	Action  string `json:"action"`
	Code    string `json:"code"`
	Message string `json:"message"`
	V       int    `json:"v"`
}

func (r *RefusalError) Error() string {
	return fmt.Sprintf("%s (code %s, action %s)", r.Msg, r.Code, r.Action)
}

// runIDRe is the shape of a run id a stamp may name (it becomes a path
// segment and a session marker).
var runIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// admitCaller applies the nesting rule to a *.run request: the environment
// its job's ape process gets on top of the daemon's (nil at depth 0), or
// the refusal.
func admitCaller(kind Kind, req RunRequest, gate SessionGate) ([]string, *RefusalError) {
	verb := "ape service " + string(kind) + ".run"
	depth := func(msg string, a ...any) *RefusalError {
		return &RefusalError{Code: apexv1.CodeSessionDepth, Action: apexv1.ActionStop, Msg: verb + " refused: " + fmt.Sprintf(msg, a...)}
	}
	c := req.Caller
	switch {
	case c == nil:
		return nil, depth("a request without the caller's depth — submit it with an rc.4+ ape (`ape service submit`), which stamps it")
	case c.Depth < 0:
		return nil, depth("the caller's depth %d is not a depth — submit it with `ape service submit`", c.Depth)
	case c.Depth == 0:
		return nil, nil
	case c.Depth >= apexv1.MaxDepth:
		return nil, depth("the caller runs inside an ape session an apex-loop run started (depth %d, run %s), and a session at that depth starts no model session"+
			" — finish this session's own work and report back; the loop starts the next session", c.Depth, c.RunID)
	case kind == KindScript:
		return nil, depth("the caller runs inside an ape session (depth 1, run %s), and a script is a conductor: it runs from a terminal, outside every ape session", c.RunID)
	case !runIDRe.MatchString(c.RunID):
		return nil, depth("the caller is at depth 1 but names no run it runs in (run_id %q) — submit it with `ape service submit`", c.RunID)
	case gate == nil:
		return nil, depth("this daemon cannot check a depth-1 caller (run %s) — run the job from a terminal (depth 0)", c.RunID)
	}
	env, err := gate(kind, req)
	if err == nil {
		return env, nil
	}
	var ue *apexv1.UsageError
	var ce *apexv1.ConflictError
	switch {
	case errors.As(err, &ue):
		return nil, &RefusalError{Code: ue.Code, Action: ue.Action, Msg: ue.Msg}
	case errors.As(err, &ce):
		return nil, &RefusalError{Code: ce.Code, Action: ce.Action, Msg: ce.Msg}
	}
	return nil, depth("the caller's session could not be checked (%v) — run the job from a terminal (depth 0)", err)
}

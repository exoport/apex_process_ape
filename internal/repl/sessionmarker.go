package repl

import "strings"

// A long verb's detached worker (`ape __run`, apecmd/v1_longrun.go) started
// from inside a session does not carry the session's EnvApeSession value: it
// carries `<kind>/<run-id>/worker/<worker-run-id>` — still an ape session's
// marker, naming the same session, and naming the worker's run besides
// (D447). Everything the worker starts inherits it.
//
// Why: the session's leftover sweep (proctree.SessionLeftovers) stops every
// process carrying the session's exact entry once the session ends. A gate
// script that double-forks a server (`( setsid server & )`) leaves the
// worker's tree and session, so nothing but the marker ties it to anyone; with
// the session's own marker the sweep took it for the session's leftover and
// killed it mid-gate, and the detached gate went red for a reason the project
// did not cause. The worker's run outlives the session by design, and so may
// what it started.
//
// What does not change: the value is non-empty (a model session, for the
// person-only refusals and the hooks), and SessionOfMarker reads the session's
// kind and run out of either form, so the nesting rule gives a worker's
// processes exactly the answer it gives the session.

// workerMarkerInfix separates the session's marker from the worker's run.
const workerMarkerInfix = "/worker/"

// SessionOfMarker is the kind and run id of the session an EnvApeSession value
// names: `<kind>/<run-id>`, or a worker's `<kind>/<run-id>/worker/<id>`.
func SessionOfMarker(v string) (kind, run string) {
	kind, rest, _ := strings.Cut(v, "/")
	run, _, _ = strings.Cut(rest, "/")
	return kind, run
}

// WorkerMarker is the EnvApeSession value of a worker with run id workerRun,
// started under the session marker session (itself a worker's, for a worker a
// worker started: the session part is kept, the worker part replaced).
func WorkerMarker(session, workerRun string) string {
	kind, run := SessionOfMarker(session)
	return kind + "/" + run + workerMarkerInfix + workerRun
}

// WorkerEnv is env with its EnvApeSession entry rewritten to WorkerMarker. An
// absent or empty entry is left as it is: outside every session a worker
// stays outside one.
func WorkerEnv(env []string, workerRun string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, EnvApeSession+"="); ok && v != "" {
			e = EnvApeSession + "=" + WorkerMarker(v, workerRun)
		}
		out = append(out, e)
	}
	return out
}

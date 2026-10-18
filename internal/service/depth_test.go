package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// D83: the daemon decides a job from its caller's stamp — refused without
// one (fail closed), refused from depth 2 and for a script from depth 1,
// as before from depth 0, and through the gate from depth 1, whose
// refusals keep their code and action.
func TestAdmitCaller(t *testing.T) {
	okEnv := []string{"APE_SESSION=task/orch-run", "APE_DEPTH=1"}
	gate := func(err error) SessionGate {
		return func(Kind, RunRequest) ([]string, error) {
			if err != nil {
				return nil, err
			}
			return okEnv, nil
		}
	}
	at := func(depth int, run string) RunRequest {
		return RunRequest{ProjectRoot: "/p", Skill: "apex-apply", Caller: &CallerStamp{Depth: depth, RunID: run}}
	}
	cases := []struct {
		name       string
		kind       Kind
		req        RunRequest
		gate       SessionGate
		code, msg  string
		wantEnvSet bool
	}{
		{name: "unstamped", kind: KindTask, req: RunRequest{ProjectRoot: "/p", Skill: "s"}, gate: gate(nil), code: apexv1.CodeSessionDepth, msg: "without the caller's depth"},
		{name: "negative", kind: KindTask, req: at(-1, ""), gate: gate(nil), code: apexv1.CodeSessionDepth},
		{name: "terminal", kind: KindTask, req: at(0, ""), gate: gate(errors.New("never asked"))},
		{name: "terminal script", kind: KindScript, req: at(0, "")},
		{name: "depth 2", kind: KindTask, req: at(2, "child-run"), gate: gate(nil), code: apexv1.CodeSessionDepth, msg: "starts no model session"},
		{name: "depth 3", kind: KindPrompt, req: at(3, "x"), gate: gate(nil), code: apexv1.CodeSessionDepth},
		{name: "depth-1 script", kind: KindScript, req: at(1, "orch-run"), gate: gate(nil), code: apexv1.CodeSessionDepth, msg: "a script is a conductor"},
		{name: "depth 1, no run", kind: KindTask, req: at(1, ""), gate: gate(nil), code: apexv1.CodeSessionDepth},
		{name: "depth 1, a glob for a run", kind: KindTask, req: at(1, "*"), gate: gate(nil), code: apexv1.CodeSessionDepth},
		{name: "depth 1, a path for a run", kind: KindTask, req: at(1, "../x"), gate: gate(nil), code: apexv1.CodeSessionDepth},
		{name: "depth 1, no gate", kind: KindTask, req: at(1, "orch-run"), code: apexv1.CodeSessionDepth, msg: "cannot check"},
		{name: "depth 1, not allowed", kind: KindTask, req: at(1, "orch-run"), gate: gate(&apexv1.UsageError{Code: apexv1.CodeSessionNotAllowed, Action: apexv1.ActionStop, Msg: "no"}), code: apexv1.CodeSessionNotAllowed},
		{name: "depth 1, busy", kind: KindTask, req: at(1, "orch-run"), gate: gate(&apexv1.ConflictError{Code: apexv1.CodeChildRunning, Action: apexv1.ActionRetry, Msg: "busy"}), code: apexv1.CodeChildRunning},
		{name: "depth 1, gate broke", kind: KindTask, req: at(1, "orch-run"), gate: gate(errors.New("disk")), code: apexv1.CodeSessionDepth, msg: "could not be checked"},
		{name: "depth 1, allowed", kind: KindTask, req: at(1, "orch-run"), gate: gate(nil), wantEnvSet: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, r := admitCaller(c.kind, c.req, c.gate)
			if c.code == "" {
				if r != nil {
					t.Fatalf("refused: %v", r)
				}
				if c.wantEnvSet != (env != nil) {
					t.Fatalf("env = %v", env)
				}
				return
			}
			if r == nil {
				t.Fatalf("admitted, want %s", c.code)
			}
			if r.Code != c.code {
				t.Fatalf("code = %s, want %s (%v)", r.Code, c.code, r)
			}
			if r.Action == "" {
				t.Fatalf("no action: %v", r)
			}
			if c.msg != "" && !strings.Contains(r.Msg, c.msg) {
				t.Fatalf("message %q lacks %q", r.Msg, c.msg)
			}
		})
	}
}

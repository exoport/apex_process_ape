//go:build linux || darwin

package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// D83 over the wire (eval rc.3 probe N1-service: a depth-2 session's job
// ran as a parentless depth-1 session): an unstamped request and a depth-2
// caller are refused with session.depth, the reply carrying code and action,
// and nothing is spawned; a depth-1 caller the gate admits runs its job's
// ape under the caller's session marker, which is what makes that ape apply
// the rule, take the child lock and record its parent.
func TestDepthOverNATS(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	bin := fakeApe(t, `echo "SESSION=$APE_SESSION DEPTH=$APE_DEPTH" >> `+marker+"\n")
	gate := func(_ Kind, req RunRequest) ([]string, error) {
		if req.Skill != "apex-apply" {
			return nil, &apexv1.UsageError{Code: apexv1.CodeSessionNotAllowed, Action: apexv1.ActionStop, Msg: "not on the allow-list"}
		}
		return []string{"APE_SESSION=task/" + req.Caller.RunID, "APE_DEPTH=1"}, nil
	}
	r := startRigGate(t, bin, nil, gate)
	root := r.cfg.ProjectRoot

	unstamped, err := r.cli.Request(r.base+".task.run", mustJSON(t, map[string]any{"project_root": root, "skill": "apex-apply"}), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := errCode(unstamped); got != apexv1.CodeSessionDepth {
		t.Fatalf("unstamped: code %q, want session.depth", got)
	}
	var body struct{ Code, Action, Message string }
	if err := json.Unmarshal(unstamped.Data, &body); err != nil || body.Action != apexv1.ActionStop || body.Code != apexv1.CodeSessionDepth {
		t.Fatalf("reply body %s (%v)", unstamped.Data, err)
	}
	for _, depth := range []int{2, 3} {
		m := r.req(t, "task.run", RunRequest{ProjectRoot: root, Skill: "apex-apply", Caller: &CallerStamp{Depth: depth, RunID: "child-run", ParentRun: "orch-run"}})
		if got := errCode(m); got != apexv1.CodeSessionDepth {
			t.Fatalf("depth %d: code %q, want session.depth", depth, got)
		}
	}
	if m := r.req(t, "task.run", RunRequest{ProjectRoot: root, Skill: "apex-handoff", Caller: &CallerStamp{Depth: 1, RunID: "orch-run"}}); errCode(m) != apexv1.CodeSessionNotAllowed {
		t.Fatalf("not allowed: code %q", errCode(m))
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a refused job spawned its ape (%v)", err)
	}
	if n := len(r.d.reg.List()); n != 0 {
		t.Fatalf("refused jobs registered: %d", n)
	}
	if held := r.d.adm.HeldKeys(); len(held) != 0 {
		t.Fatalf("a refused job holds a slot: %v", held)
	}

	jobID := acceptedJobID(t, r, "task.run", RunRequest{ProjectRoot: root, Skill: "apex-apply", Caller: &CallerStamp{Depth: 1, RunID: "orch-run", PID: 42}})
	pollJobTerminal(t, r, jobID)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "SESSION=task/orch-run DEPTH=1") {
		t.Fatalf("the job's ape ran without the caller's marker: %s", data)
	}
	info, _ := r.d.reg.Get(jobID)
	if info.Caller == nil || info.Caller.Depth != 1 || info.Caller.RunID != "orch-run" || info.Caller.PID != 42 {
		t.Fatalf("job.status caller = %+v", info.Caller)
	}

	// A terminal's job runs as before: no marker added.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APE_SESSION", "")
	t.Setenv("APE_DEPTH", "")
	jobID = acceptedJobID(t, r, "task.run", RunRequest{ProjectRoot: root, Skill: "anything", Caller: terminal, Nonexclusive: true})
	pollJobTerminal(t, r, jobID)
	data, _ = os.ReadFile(marker)
	if !strings.Contains(string(data), "SESSION= DEPTH=") {
		t.Fatalf("a terminal's job got a marker: %s", data)
	}
}

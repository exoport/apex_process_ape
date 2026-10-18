package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// D88 on the `ape task` side: a Stop the project hook blocked for
// background work keeps the step alive like any other blocked Stop, and
// each Stop still meets its own verdict — also a Stop ape task's own
// background gate deferred (a running sub-agent), whose verdict is taken
// and ignored. The three Stops come within the matcher's 5 s slack, so a
// verdict left unconsumed would be read as the next Stop's.
func TestStopGate_BackgroundBlockedStopsKeepTheirOrder(t *testing.T) {
	root := v1Repo(t)
	t.Setenv("APE_SESSION", "task/test") // after v1Repo, which runs as a person
	r := newStopGateRecorder(root)
	d := sessiondriver.NewDriver(func() *runlog.Writer { return nil }, time.Minute)
	d.SetStopGate(r.bounded(t.Context()))

	stop := func(active bool, tasks string) {
		t.Helper()
		payload := `{"session_id":"s1","hook_event_name":"Stop","cwd":"` + root + `","stop_hook_active":` +
			map[bool]string{true: "true", false: "false"}[active] + `,"background_tasks":` + tasks + `}`
		d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop, Payload: json.RawMessage(payload)})
		_, _, err := v1ExecIn(t, newHookCmd, payload, "stop")
		require.NoError(t, err)
	}
	notDone := func(msg string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, d.WaitStepDone(ctx), context.DeadlineExceeded, msg)
	}

	// A backgrounded shell: benign to ape task's own gate, blocked by the hook.
	stop(false, `[{"id":"b1","type":"shell","status":"running","description":"go test ./..."}]`)
	notDone("a Stop blocked for background work continues the turn")
	// A backgrounded sub-agent: deferred by ape task's gate, blocked by the hook.
	stop(true, `[{"id":"a1","type":"subagent","status":"running"}]`)
	notDone("a deferred Stop never completes the step")
	// The work is done: the hook allows, and this Stop takes that verdict.
	stop(true, `[]`)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.NoError(t, d.WaitStepDone(ctx), "the third Stop met its own verdict, not the second's block")

	rec := r.record()
	require.NotNil(t, rec)
	require.Len(t, rec.Events, 3)
	for i, want := range []string{apexv1.StopBlock, apexv1.StopBlock, apexv1.StopAllow} {
		require.Equal(t, want, rec.Events[i].Verdict, "event %d", i)
	}
	require.True(t, strings.HasPrefix(rec.Events[0].Reason, apexv1.BackgroundStopPrefix), rec.Events[0].Reason)
	require.Contains(t, rec.Events[1].Reason, "(stop blocked 2/3)")
	require.Equal(t, "HEAD is not on a change branch", rec.Events[2].Reason)
	n, _ := d.DeferredStops()
	require.Equal(t, 2, n)
}

// D88: `ape change review --run` and `accept --delegate` read the child's
// last main-session Stop through its run's manifest.
func TestChildStopBackground(t *testing.T) {
	root := v1Repo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	require.Nil(t, childStopBackground(p, nil))
	require.Nil(t, childStopBackground(p, &detachedHandle{}))

	runDir := filepath.Join(runlog.TasksRoot(root), "apex-review", "20261017-120000-abcd")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	m := pipeline.Manifest{Stages: []pipeline.StageRecord{{Steps: []pipeline.StepRecord{{Sessions: []pipeline.SessionUsageRecord{
		{SessionID: "main-1"}, {SessionID: "sub-1", ParentSessionID: "main-1"},
	}}}}}}
	data, err := yaml.Marshal(&m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "manifest.yaml"), data, 0o644))
	require.Equal(t, []string{"main-1"}, runMainSessions(runDir))
	h := &detachedHandle{RunDir: runDir}
	require.Nil(t, childStopBackground(p, h), "no Stop recorded")

	t.Setenv("APE_SESSION", "task/test")
	payload := `{"session_id":"main-1","hook_event_name":"Stop","cwd":"` + root + `","background_tasks":[{"id":"b1","type":"shell","status":"running","description":"go test ./..."},{"id":"m2","type":"monitor","status":"running"}]}`
	_, _, err = v1ExecIn(t, newHookCmd, payload, "stop")
	require.NoError(t, err)
	got := childStopBackground(p, h)
	require.Equal(t, []string{`shell b1 — "go test ./..." [running]`, "monitor m2 [running]"}, got)
	require.Equal(t, `ended with live background work: shell b1 — "go test ./..." [running]; monitor m2 [running]`, apexv1.LiveBackgroundReason(got))
}

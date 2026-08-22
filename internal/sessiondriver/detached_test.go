package sessiondriver

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/stretchr/testify/require"
)

// fixtureDir is the repo-root corpus of real Claude Code hook envelopes.
// See its README for what each file proves and which gate it serves.
const fixtureDir = "../../testdata/hookpayloads"

// hookLine mirrors one runlog hook-events.jsonl row.
type hookLine struct {
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

// loadFixture reads a captured hook-events fixture, returning every row.
func loadFixture(t *testing.T, name string) []hookLine {
	t.Helper()
	f, err := os.Open(filepath.Join(fixtureDir, name))
	require.NoError(t, err)
	defer f.Close()
	var out []hookLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var h hookLine
		require.NoError(t, json.Unmarshal(sc.Bytes(), &h))
		out = append(out, h)
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, out, "fixture %s is empty", name)
	return out
}

// firstEvent returns the payload of the first row with the given event.
func firstEvent(t *testing.T, rows []hookLine, event string) json.RawMessage {
	t.Helper()
	for _, r := range rows {
		if r.Event == event {
			return r.Payload
		}
	}
	t.Fatalf("no %s event in fixture", event)
	return nil
}

// tasks builds the pointer-to-slice shape classifyStop takes, so a test
// can express "field absent" (nil) distinctly from "field present but
// empty" (a pointer to an empty slice).
func tasks(ts ...BackgroundTask) *[]BackgroundTask {
	list := ts
	return &list
}

// TestClassifyStop covers the verdict table: what a Stop means given the
// background work the harness reported alongside it.
func TestClassifyStop(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    *[]BackgroundTask
		want  stopVerdict
		block int
	}{
		// The backwards-compatibility lock. An older Claude Code that
		// sends no background_tasks must behave exactly as ape did before
		// this gate existed — absence of the field is absence of evidence,
		// never evidence of a problem.
		{"absent field completes", nil, stopComplete, 0},
		{"empty list completes", tasks(), stopComplete, 0},
		{"benign shell ignored", tasks(BackgroundTask{Type: taskShell, Status: "running"}), stopComplete, 0},
		{"benign monitor+dream ignored", tasks(
			BackgroundTask{Type: taskMonitor}, BackgroundTask{Type: taskDream},
		), stopComplete, 0},
		{"workflow ignored", tasks(BackgroundTask{Type: taskWorkflow}), stopComplete, 0},
		{"running subagent defers", tasks(
			BackgroundTask{Type: taskSubagent, Status: "running"},
		), stopDefer, 1},
		{"cloud session defers", tasks(BackgroundTask{Type: taskCloud}), stopDefer, 1},
		{"teammate is fatal", tasks(BackgroundTask{Type: taskTeammate}), stopFatal, 1},
		// A teammate outranks a resolvable sub-agent: waiting cannot fix it.
		{"teammate outranks subagent", tasks(
			BackgroundTask{Type: taskSubagent}, BackgroundTask{Type: taskTeammate},
		), stopFatal, 2},
		// An unmapped future task kind arrives under its raw internal name.
		// Defer rather than ignore — ignoring silently re-opens the hole.
		{"unknown type defers", tasks(BackgroundTask{Type: "some_future_kind"}), stopDefer, 1},
		{"benign alongside blocking still defers", tasks(
			BackgroundTask{Type: taskShell}, BackgroundTask{Type: taskSubagent},
		), stopDefer, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, blocking := classifyStop(tc.in)
			require.Equal(t, tc.want, got)
			require.Len(t, blocking, tc.block)
		})
	}
}

// TestClassifyAgentSpawn: only an Agent-tool result explicitly reporting
// teammate_spawned trips the spawn gate. Every other shape is inert.
func TestClassifyAgentSpawn(t *testing.T) {
	t.Parallel()
	env := func(tool, resp string) hookEnvelope {
		return hookEnvelope{ToolName: tool, ToolResponse: json.RawMessage(resp)}
	}
	t.Run("teammate spawn is fatal", func(t *testing.T) {
		t.Parallel()
		err := classifyAgentSpawn(env("Agent",
			`{"status":"teammate_spawned","teammate_id":"t123","name":"reviewer"}`))
		require.NotNil(t, err)
		require.Equal(t, DetectedAtSpawn, err.Source)
		require.Equal(t, "t123", err.AgentID)
		require.Contains(t, err.Error(), "@reviewer")
		require.Contains(t, err.Error(), "t123")
	})
	for _, tc := range []struct{ name, tool, resp string }{
		{"completed spawn is fine", "Agent", `{"status":"completed","agentId":"a1"}`},
		// A backgrounded agent is real and working — it must be allowed to
		// run. It is what Gate A's defer branch exists to protect.
		{"async_launched is fine", "Agent", `{"status":"async_launched","output_file":"/tmp/x"}`},
		{"failed spawn is not this defect", "Agent", `{"status":"failed"}`},
		{"other tool ignored", "Bash", `{"status":"teammate_spawned"}`},
		{"no status ignored", "Agent", `{"agentId":"a1"}`},
		{"string response ignored", "Agent", `"plain text result"`},
		{"malformed response ignored", "Agent", `{not json`},
		{"empty response ignored", "Agent", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Nil(t, classifyAgentSpawn(env(tc.tool, tc.resp)))
		})
	}
}

// TestDriver_StopWithoutBackgroundTasksCompletes is the regression lock
// for older Claude Code: a Stop carrying no payload at all must complete
// the step exactly as it always has.
func TestDriver_StopWithoutBackgroundTasksCompletes(t *testing.T) {
	t.Parallel()
	d := newTestDriver(time.Minute)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})
	require.NoError(t, d.WaitStepDone(t.Context()))
}

// TestDriver_DetachedSubagentDoesNotComplete: the §3.4 defect. A Stop
// whose payload reports a still-running sub-agent must NOT be treated as
// step completion. It is bounded by the idle backstop, not completed.
func TestDriver_DetachedSubagentDoesNotComplete(t *testing.T) {
	t.Parallel()
	rows := loadFixture(t, "stop-detached-subagent.jsonl")
	payload := firstEvent(t, rows, ipc.HookStop)

	d := newTestDriver(50 * time.Millisecond)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop, Payload: payload})

	err := d.WaitStepDone(t.Context())
	require.Error(t, err, "a Stop with a running sub-agent must not complete the step")
	var idle *IdleTimeoutError
	require.ErrorAs(t, err, &idle, "the deferred stop is bounded by the idle backstop")

	n, blocking := d.DeferredStops()
	require.Equal(t, 1, n)
	require.Len(t, blocking, 1)
	require.Equal(t, taskSubagent, blocking[0].Type)
	require.Contains(t, blocking[0].Description, "Create Story 1.2")
}

// TestDriver_CleanStopCompletes: the healthy path stays untouched — a
// real captured Stop with an empty background_tasks completes at once.
func TestDriver_CleanStopCompletes(t *testing.T) {
	t.Parallel()
	rows := loadFixture(t, "stop-clean-with-contract.jsonl")
	payload := firstEvent(t, rows, ipc.HookStop)

	d := newTestDriver(time.Minute)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop, Payload: payload})
	require.NoError(t, d.WaitStepDone(t.Context()))

	n, _ := d.DeferredStops()
	require.Zero(t, n)
}

// TestDriver_TeammateStopIsFatal: a teammate outstanding at the turn
// boundary fails the step immediately — no timer, no idle window.
func TestDriver_TeammateStopIsFatal(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(
		`{"background_tasks":[{"id":"t9","type":"teammate","status":"running","description":"review story 1-0"}]}`,
	)

	d := newTestDriver(time.Hour) // a long idle window must not be reached
	d.SetStepSkill("apex-story-batch-review")
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop, Payload: payload})

	start := time.Now()
	err := d.WaitStepDone(t.Context())
	require.Less(t, time.Since(start), 5*time.Second, "must fail fast, not wait out a timer")

	var det *DetachedAgentError
	require.ErrorAs(t, err, &det)
	require.Equal(t, DetectedAtStop, det.Source)
	require.Equal(t, "apex-story-batch-review", det.Skill)
	require.Contains(t, err.Error(), "review story 1-0")
	// Never a timeout — those stay reserved for a genuinely wedged session.
	var idle *IdleTimeoutError
	require.NotErrorAs(t, err, &idle)
}

// TestDriver_TeammateSpawnIsFatal: the same defect caught one step
// earlier, at the PostToolUse that created it.
func TestDriver_TeammateSpawnIsFatal(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(
		`{"tool_name":"Agent","tool_response":{"status":"teammate_spawned","teammate_id":"t42","name":"story-1-0"}}`,
	)

	d := newTestDriver(time.Hour)
	d.SetStepSkill("apex-story-batch-review")
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookPostToolUse, Payload: payload})

	start := time.Now()
	err := d.WaitStepDone(t.Context())
	require.Less(t, time.Since(start), 5*time.Second)

	var det *DetachedAgentError
	require.ErrorAs(t, err, &det)
	require.Equal(t, DetectedAtSpawn, det.Source)
	require.Equal(t, "t42", det.AgentID)
}

// TestDriver_HealthyAgentSpawnDoesNotTrip: the captured `completed`
// Agent result must not be mistaken for a detached one.
func TestDriver_HealthyAgentSpawnDoesNotTrip(t *testing.T) {
	t.Parallel()
	rows := loadFixture(t, "agent-post-completed.jsonl")
	payload := firstEvent(t, rows, ipc.HookPostToolUse)

	d := newTestDriver(time.Minute)
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookPostToolUse, Payload: payload})
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})
	require.NoError(t, d.WaitStepDone(t.Context()))
}

// TestDriver_SubagentPairsDoNotBlock: real SubagentStart/Stop traffic
// carries background_tasks too; none of it may block the final Stop.
func TestDriver_SubagentPairsDoNotBlock(t *testing.T) {
	t.Parallel()
	d := newTestDriver(time.Minute)
	for _, r := range loadFixture(t, "subagent-pairs.jsonl") {
		d.FeedHook(orchestrator.HookEvent{Event: r.Event, Payload: r.Payload})
	}
	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})
	require.NoError(t, d.WaitStepDone(t.Context()))
}

// TestDriver_DrainClearsFatal: one step's detached agent must not fail
// the next. OnStepStart drains the fatal alongside stale step-done
// signals.
func TestDriver_DrainClearsFatal(t *testing.T) {
	t.Parallel()
	d := newTestDriver(time.Minute)
	d.FeedHook(orchestrator.HookEvent{
		Event:   ipc.HookStop,
		Payload: json.RawMessage(`{"background_tasks":[{"type":"teammate","id":"t1"}]}`),
	})
	d.DrainStepDone()

	n, blocking := d.DeferredStops()
	require.Zero(t, n)
	require.Nil(t, blocking)

	d.FeedHook(orchestrator.HookEvent{Event: ipc.HookStop})
	require.NoError(t, d.WaitStepDone(t.Context()))
}

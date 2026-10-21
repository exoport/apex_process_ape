package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// eventsAt is the project's event log, oldest first.
func eventsAt(t *testing.T, root string) []apexv1.Event {
	t.Helper()
	p, ok := v1ProjectAt(root)
	require.True(t, ok)
	return p.EventsSince(0)
}

// eventsNamed is the logged events called name.
func eventsNamed(t *testing.T, root, name string) []apexv1.Event {
	t.Helper()
	var out []apexv1.Event
	for _, ev := range eventsAt(t, root) {
		if ev.Event == name {
			out = append(out, ev)
		}
	}
	return out
}

const proposedM2 = "\n## M2 — Farewells\nStatus: proposed\nGate: ./gate2.sh\nGoal: says goodbye\n"

// W3-R2: a model-style roadmap commit proposing M2 — no ape verb, only
// git's pre-commit hook — logs `person` with action "approve M2" exactly
// once; the next commit logs nothing.
func TestPerson_RoadmapCommitLogsApproveOnce(t *testing.T) {
	root := v1Repo(t)
	apeOnPath(t)
	out := filepath.Join(t.TempDir(), "notified")
	cfg := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(data, []byte(`notify: 'printf "%s|%s\n" "$APE_EVENT" "$APE_ACTION" >> `+out+`'`+"\n")...), 0o644))
	v1Git(t, root, "commit", "-q", "-am", "notify")
	require.Empty(t, eventsNamed(t, root, apexv1.EventPerson), "M1 is approved: nothing waits on a person")

	roadmap := filepath.Join(root, "development", "roadmap.md")
	rm, err := os.ReadFile(roadmap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(roadmap, append(rm, proposedM2...), 0o644))
	v1Git(t, root, "add", "-A")
	msg, err := gitCommit(t, root, "-m", "plan: M2")
	require.NoError(t, err, msg)
	person := eventsNamed(t, root, apexv1.EventPerson)
	require.Len(t, person, 1)
	require.Equal(t, "approve M2", person[0].Action)
	require.Equal(t, "M2", person[0].Milestone)
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "person|approve M2\n", string(got), "APE_ACTION is the phrase the person types")

	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x\n"), 0o644))
	v1Git(t, root, "add", "notes.txt")
	msg, err = gitCommit(t, root, "-m", "docs: notes")
	require.NoError(t, err, msg)
	require.Len(t, eventsNamed(t, root, apexv1.EventPerson), 1, "a second commit logs nothing")
}

// Every v1 verb that changes what status reports logs the person acts the
// new state waits on: a block logs `block`, then `person` with the answer
// phrase; blocking again logs neither.
func TestPerson_AfterAVerb(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newRootCmd, "change", "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	_, _, err = v1Exec(t, newRootCmd, "change", "block", "add-wave", "--reason", "which greeting?", "--cwd", root)
	require.NoError(t, err)
	evs := eventsAt(t, root)
	var names []string
	for _, ev := range evs {
		names = append(names, ev.Event)
	}
	require.Equal(t, []string{"block", "person"}, names)
	require.Equal(t, "M1", evs[0].Milestone, "block names the change's milestone")
	require.Equal(t, "answer add-wave: <answer>", evs[1].Action)
	require.Equal(t, "add-wave is blocked: which greeting?", evs[1].Reason)
	require.EqualValues(t, 2, evs[1].Seq)

	_, _, _ = v1Exec(t, newRootCmd, "change", "block", "add-wave", "--reason", "still", "--cwd", root) //nolint:dogsled // only the log is under test
	require.Len(t, eventsAt(t, root), 2)
}

// run-end for an attached ape task run: its exit, run id, skill and
// scope; a run a supervisor started leaves it to the supervisor.
func TestRunEnd_AttachedRun(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	t.Setenv(envDetachedID, "")
	stop := &pipeline.LimitStopError{Kind: pipeline.TerminationUsageLimit, Message: "usage limit"}
	emitTaskRunEnd(context.Background(), root, "apex-apply", "add-wave", "20261021-120000-abc", ExitUsageLimit, stop)
	ends := eventsNamed(t, root, apexv1.EventRunEnd)
	require.Len(t, ends, 1)
	require.Equal(t, apexv1.Event{
		Seq: ends[0].Seq, At: ends[0].At, Event: "run-end", Milestone: "M1", Change: "add-wave", Run: "20261021-120000-abc",
		Exit: ends[0].Exit, Reason: "usage_limit", Skill: "apex-apply",
	}, ends[0])
	require.Equal(t, ExitUsageLimit, *ends[0].Exit)

	emitTaskRunEnd(context.Background(), root, "apex-plan", "M1", "r2", 0, nil)
	ends = eventsNamed(t, root, apexv1.EventRunEnd)
	require.Len(t, ends, 2)
	require.Equal(t, "M1", ends[1].Milestone)
	require.Empty(t, ends[1].Change)
	require.Zero(t, *ends[1].Exit)

	t.Setenv(envDetachedID, "d1")
	emitTaskRunEnd(context.Background(), root, "apex-plan", "M1", "r3", 0, nil)
	require.Len(t, eventsNamed(t, root, apexv1.EventRunEnd), 2, "the supervisor's to log")
}

// W3-R6: a detached run's supervisor logs run-end with the exit it
// records, once the handle has it.
func TestRunEnd_DetachedSupervisorLogsTheExit(t *testing.T) {
	outsideEverySession(t)
	t.Setenv(asApeBinary, "1")
	root := v1Repo(t)
	require.NoError(t, os.MkdirAll(runlog.DetachedRoot(root), 0o755))
	three := 3
	require.NoError(t, writeHandle(handlePath(root, "done"), &detachedHandle{ID: "done", ExitCode: &three}))
	h := &detachedHandle{
		ID: "d1", Argv: []string{"run", "wait", "done", "--cwd", root}, Dir: t.TempDir(), Root: root,
		Log: filepath.Join(runlog.DetachedRoot(root), "d1.log"), Stdout: filepath.Join(runlog.DetachedRoot(root), "d1.out"), StartedAt: time.Now().UTC(),
	}
	path := handlePath(root, "d1")
	require.NoError(t, writeHandle(path, h))
	err := supervise(context.Background(), path)
	require.Equal(t, 3, exitCodeOf(t, err))

	ends := eventsNamed(t, root, apexv1.EventRunEnd)
	require.Len(t, ends, 1)
	require.Equal(t, "d1", ends[0].Run, "no run record: the handle's id")
	require.Equal(t, 3, *ends[0].Exit)
	got, err := readHandle(path)
	require.NoError(t, err)
	require.NotNil(t, got.ExitCode, "recorded before the event")

	require.Equal(t, "apex-apply", func() string {
		s, _ := handleTask([]string{"task", "apex-apply", "--args=add-wave", "--detach"})
		return s
	}())
	_, args := handleTask([]string{"task", "apex-apply", "--args", "add-wave --resume"})
	require.Equal(t, "add-wave --resume", args)
}

// W3-R5: a killed supervisor is logged as run-lost by the first command
// that sees it, once, however many look.
func TestRunLost_LoggedOnce(t *testing.T) {
	root := v1Repo(t)
	require.NoError(t, os.MkdirAll(runlog.DetachedRoot(root), 0o755))
	require.NoError(t, writeHandle(handlePath(root, "lost"), &detachedHandle{
		ID: "lost", Argv: []string{"task", "apex-plan", "--args", "M1"}, SupervisorPID: deadPID, StartedAt: time.Now(), RunID: "20261021-120000-abc",
	}))
	_, code := runRunCmd(t, "status", "lost", "--cwd", root, "--json")
	require.Zero(t, code)
	_, code = runRunCmd(t, "wait", "lost", "--cwd", root)
	require.Equal(t, exitCodeRunLost, code)
	require.Empty(t, detachedRuns(context.Background(), root), "a lost run is not a live one")

	lost := eventsNamed(t, root, apexv1.EventRunLost)
	require.Len(t, lost, 1)
	require.Equal(t, "20261021-120000-abc", lost[0].Run)
	require.Equal(t, "apex-plan", lost[0].Skill)
	require.Equal(t, "M1", lost[0].Milestone)
	require.True(t, strings.HasPrefix(lost[0].Reason, "its supervisor (pid "))
	require.FileExists(t, lostMarkerPath(root, "lost"))
}

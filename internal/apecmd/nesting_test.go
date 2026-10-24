package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/proctree"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessions"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// deadPID is a pid no process has: past every platform's pid range.
const deadPID = 2147483000

// outsideEverySession clears the markers and the registry, and gives this
// process no ape ancestor: a terminal.
func outsideEverySession(t *testing.T) {
	t.Helper()
	for _, k := range []string{repl.EnvApeSession, repl.EnvDepth, repl.EnvParentRun, repl.EnvParentPID, envDetachedID} {
		t.Setenv(k, "")
	}
	reg := filepath.Join(t.TempDir(), "registry.json")
	old := sessions.Path
	sessions.Path = func() string { return reg }
	t.Cleanup(func() { sessions.Path = old })
	oldParent := parentOf
	parentOf = func(int) (int, bool) { return 0, false }
	t.Cleanup(func() { parentOf = oldParent })
}

// inSession marks this process as running inside an ape session, by its
// environment only.
func inSession(t *testing.T, marker string, depth int) {
	t.Helper()
	t.Setenv(repl.EnvApeSession, marker)
	t.Setenv(repl.EnvDepth, strconv.Itoa(depth))
}

// registerRow adds a live-session row; pid must be alive.
func registerRow(t *testing.T, row sessions.Session) {
	t.Helper()
	require.NoError(t, sessions.Register(sessions.Path(), row))
}

// withAncestors makes the given pids this process's ancestors, nearest
// first, through an injected process table.
func withAncestors(t *testing.T, pids ...int) {
	t.Helper()
	table := map[int]int{}
	prev := os.Getpid()
	for _, p := range pids {
		table[prev] = p
		prev = p
	}
	table[prev] = 1
	parentOf = func(pid int) (int, bool) { pp, ok := table[pid]; return pp, ok }
}

// A loop run, orch-run: a live registry row for ape task apex-loop.
func orchestratorRow() sessions.Session {
	return sessions.Session{PID: os.Getppid(), ClaudePID: 7000, Command: "task", RunID: "orch-run", Skill: apexv1.OrchestratorSkill, Depth: 1, StartedAt: time.Now()}
}

// Every session-starting command refuses from a depth-1 session that is
// not an orchestrator run, with exit 2, code session.depth and the owning
// run named — before it reads a flag's meaning or spawns anything. The args
// would each start real work if the gate were missing, which is what makes
// this a test of the gate.
func TestNesting_EveryStarterRefusesInsideANonOrchestratorSession(t *testing.T) {
	outsideEverySession(t)
	inSession(t, "task/20260926-120146-556be67", 1)
	root := projectFor(t, allExtensionsConfig)
	for name, run := range map[string]struct {
		cmd  func() *cobra.Command
		args []string
	}{
		"task":   {newTaskCmd, []string{"apex-change", "--cwd", root}},
		"prompt": {newPromptCmd, []string{"hello", "--cwd", root}},
		"chat":   {newChatCmd, []string{"--cwd", root}},
		"script": {newScriptCmd, []string{"run.star", "--cwd", root}},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := run.cmd()
			cmd.SetArgs(run.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			err := cmd.Execute()
			require.Equal(t, ExitUsage, exitCodeOf(t, err))
			require.Contains(t, err.Error(), "20260926-120146-556be67", "the refusal names the owning run")
			require.Contains(t, err.Error(), "code session.depth, action stop")
		})
	}
}

// Project-data commands and the v1 unit-of-change commands are what
// skills run inside sessions all day: no depth refuses them.
func TestNesting_LeavesDeterministicCommandsAlone(t *testing.T) {
	outsideEverySession(t)
	inSession(t, "task/fx", 2)
	root := projectFor(t, allExtensionsConfig)

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"effort", "--cwd", root})
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	for _, run := range []struct {
		cmd  func() *cobra.Command
		args []string
	}{
		{newStatusCmd, []string{"--cwd", root}},
		{newProjectCmd, []string{"check", "--cwd", root}},
		{newChangeCmd, []string{"check", "no-such-change", "--cwd", root}},
	} {
		cmd = run.cmd()
		cmd.SetArgs(run.args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err != nil {
			require.NotContains(t, err.Error(), "session.depth", "%v is not refused for nesting", run.args)
		}
	}
}

// The refusal matrix (D62): depth × orchestrator × allow-list × starter.
func TestNesting_RefusalMatrix(t *testing.T) {
	root := t.TempDir()
	type want struct {
		depth int
		code  string
	}
	starters := []struct{ verb, skill string }{
		{"ape task", "apex-change"},
		{"ape change review --run", apexv1.ReviewSkill},
		{"ape task", "apex-handoff"},
		{"ape task", apexv1.OrchestratorSkill},
		{"ape prompt", ""},
		{"ape chat", ""},
	}
	cases := []struct {
		name  string
		setup func(t *testing.T)
		want  func(skill string) want
	}{
		{"depth 0: a terminal starts anything", func(t *testing.T) { t.Helper() }, func(string) want { return want{depth: 1} }},
		{"depth 1, not an orchestrator: nothing", func(t *testing.T) {
			t.Helper()
			inSession(t, "task/apply-run", 1)
			registerRow(t, sessions.Session{PID: os.Getppid(), Command: "task", RunID: "apply-run", Skill: "apex-apply", Depth: 1})
		}, func(string) want { return want{code: apexv1.CodeSessionDepth} }},
		{"depth 1, a prompt session: nothing", func(t *testing.T) {
			t.Helper()
			inSession(t, "prompt/p1", 1)
		}, func(string) want { return want{code: apexv1.CodeSessionDepth} }},
		{"depth 1, the orchestrator: allow-listed skills only", func(t *testing.T) {
			t.Helper()
			inSession(t, "task/orch-run", 1)
			registerRow(t, orchestratorRow())
		}, func(skill string) want {
			if skill == "apex-change" || skill == apexv1.ReviewSkill {
				return want{depth: 2}
			}
			return want{code: apexv1.CodeSessionNotAllowed}
		}},
		{"depth 2: nothing, allow-listed or not", func(t *testing.T) {
			t.Helper()
			inSession(t, "task/child-run", 2)
		}, func(string) want { return want{code: apexv1.CodeSessionDepth} }},
	}
	for _, c := range cases {
		for _, s := range starters {
			t.Run(c.name+"/"+s.verb+" "+s.skill, func(t *testing.T) {
				outsideEverySession(t)
				c.setup(t)
				w := c.want(s.skill)
				l, err := gateSession(s.verb, s.skill, root)
				if w.code != "" {
					require.Error(t, err)
					var ue *apexv1.UsageError
					require.ErrorAs(t, err, &ue, "%T", err)
					require.Equal(t, w.code, ue.Code)
					require.Equal(t, apexv1.ActionStop, ue.Action)
					require.Contains(t, err.Error(), s.verb)
					return
				}
				require.NoError(t, err)
				require.Equal(t, w.depth, l.depth)
				if w.depth == 2 {
					require.Equal(t, "orch-run", l.parentRun, "a child records its parent run")
					require.Equal(t, os.Getppid(), l.parentPID, "and the ape process it dies with")
				} else {
					require.Empty(t, l.parentRun)
					require.Zero(t, l.parentPID)
				}
			})
		}
	}
}

// D447: a long verb's worker carries the session's marker with its own run
// appended (repl.WorkerMarker), and a session started from the worker's
// script gets exactly the session's answer — the same launch, or the same
// refusal word for word — the loop's allow-listed child included.
func TestNesting_AWorkersMarkerAnswersAsItsSession(t *testing.T) {
	root := t.TempDir()
	sessionsAt := []struct {
		name   string
		marker string
		depth  int
		row    *sessions.Session
	}{
		{"a skill session", "task/apply-run", 1, &sessions.Session{PID: os.Getppid(), Command: "task", RunID: "apply-run", Skill: "apex-apply", Depth: 1}},
		{"a prompt session", "prompt/p1", 1, nil},
		{"the loop", "task/orch-run", 1, func() *sessions.Session { r := orchestratorRow(); return &r }()},
		{"a loop's child", "task/child-run", 2, nil},
	}
	starters := []struct{ verb, skill string }{
		{"ape task", "apex-change"},
		{"ape task", "apex-nope"},
		{"ape prompt", ""},
	}
	type answer struct {
		depth     int
		parentRun string
		parentPID int
		err       string
	}
	ask := func(t *testing.T, marker string, depth int, row *sessions.Session, verb, skill string) answer {
		t.Helper()
		outsideEverySession(t)
		inSession(t, marker, depth)
		if row != nil {
			registerRow(t, *row)
		}
		l, err := gateSession(verb, skill, root)
		if err != nil {
			return answer{err: err.Error()}
		}
		return answer{depth: l.depth, parentRun: l.parentRun, parentPID: l.parentPID}
	}
	for _, at := range sessionsAt {
		for _, s := range starters {
			t.Run(at.name+"/"+s.verb+" "+s.skill, func(t *testing.T) {
				want := ask(t, at.marker, at.depth, at.row, s.verb, s.skill)
				if at.name == "the loop" && s.skill == "apex-change" {
					require.Equal(t, 2, want.depth, "the loop starts an allow-listed child: %+v", want)
				} else {
					require.NotEmpty(t, want.err, "refused from the session itself")
				}
				got := ask(t, repl.WorkerMarker(at.marker, "20261023-101010-42-a1b2c3"), at.depth, at.row, s.verb, s.skill)
				require.Equal(t, want, got)
			})
		}
	}
}

// Q2: the depth comes from the process ancestry as well as the
// environment, and is the larger of the two. A model that scrubs the marker
// still runs under the claude ape spawned, matched by pid, never by name.
func TestNesting_AncestryOutranksAScrubbedEnvironment(t *testing.T) {
	root := t.TempDir()

	t.Run("scrubbed env inside a depth-2 session", func(t *testing.T) {
		outsideEverySession(t)
		registerRow(t, sessions.Session{PID: os.Getppid(), ClaudePID: 7000, Command: "task", RunID: "child-run", Skill: "apex-apply", Depth: 2, ParentRun: "orch-run"})
		withAncestors(t, 7001, 7000, os.Getppid())
		_, err := gateSession("ape task", "apex-change", root)
		requireSessionDepth(t, err)
	})

	t.Run("a forged marker naming the orchestrator", func(t *testing.T) {
		outsideEverySession(t)
		registerRow(t, orchestratorRow())
		registerRow(t, sessions.Session{PID: os.Getppid(), ClaudePID: 8000, Command: "task", RunID: "apply-run", Skill: "apex-apply", Depth: 1})
		inSession(t, "task/orch-run", 1)
		withAncestors(t, 8001, 8000, os.Getppid())
		_, err := gateSession("ape task", "apex-change", root)
		requireSessionDepth(t, err)
	})

	t.Run("an env claiming less depth than ape recorded", func(t *testing.T) {
		outsideEverySession(t)
		registerRow(t, sessions.Session{PID: os.Getppid(), ClaudePID: 7000, Command: "task", RunID: "child-run", Skill: "apex-apply", Depth: 2})
		inSession(t, "task/child-run", 1)
		withAncestors(t, 7000)
		at := currentSession(root)
		require.Equal(t, 2, at.Depth)
		require.Equal(t, "ancestry", at.Source)
	})

	t.Run("the orchestrator found by its claude pid, env scrubbed", func(t *testing.T) {
		outsideEverySession(t)
		registerRow(t, orchestratorRow())
		withAncestors(t, 7001, 7000)
		l, err := gateSession("ape task", "apex-plan", root)
		require.NoError(t, err)
		require.Equal(t, 2, l.depth)
		require.Equal(t, "orch-run", l.parentRun)
	})

	t.Run("found by the ape process running it, before the claude pid is recorded", func(t *testing.T) {
		outsideEverySession(t)
		row := orchestratorRow()
		row.ClaudePID = 0
		registerRow(t, row)
		withAncestors(t, 7001, 7000, os.Getppid())
		at := currentSession(root)
		require.True(t, at.Orchestrator())
		require.Equal(t, os.Getppid(), at.RunnerPID)
	})

	t.Run("a dead row is no session", func(t *testing.T) {
		outsideEverySession(t)
		registerRow(t, sessions.Session{PID: deadPID, ClaudePID: 7000, Command: "task", RunID: "gone", Skill: "apex-apply", Depth: 2})
		withAncestors(t, 7000)
		require.Zero(t, currentSession(root).Depth)
	})
}

// A marker whose registry row is gone is completed from ape's own run
// record: the orchestrator's manifest, still running.
func TestNesting_OrchestratorKnownFromItsManifest(t *testing.T) {
	outsideEverySession(t)
	root := t.TempDir()
	dir := filepath.Join(runlog.TasksRoot(root), apexv1.OrchestratorSkill, "orch-run")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("run_id: orch-run\nstatus: running\n"), 0o644))
	inSession(t, "task/orch-run", 1)
	l, err := gateSession("ape task", "apex-apply", root)
	require.NoError(t, err)
	require.Equal(t, 2, l.depth)
	require.Equal(t, "orch-run", l.parentRun)
	require.Zero(t, l.parentPID, "no registry row: no pid to die with")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("run_id: orch-run\nstatus: completed\n"), 0o644))
	_, err = gateSession("ape task", "apex-apply", root)
	requireSessionDepth(t, err)
}

// Q6: a script is a depth-0 conductor; it refuses at any depth, the
// orchestrator's included.
func TestNesting_ScriptRefusesAtAnyDepth(t *testing.T) {
	outsideEverySession(t)
	require.NoError(t, refuseInSession("ape script"))
	inSession(t, "task/orch-run", 1)
	registerRow(t, orchestratorRow())
	requireSessionDepth(t, refuseInSession("ape script"))
}

// One child at a time per orchestrator run; one orchestrator per project.
// A lock whose holder is dead is cleared; the same holder (the detached
// run that took it at --detach) takes it over; release is by holder.
func TestNesting_SessionLocks(t *testing.T) {
	outsideEverySession(t)
	root := t.TempDir()
	child := func() *sessionLaunch { return &sessionLaunch{depth: 2, skill: "apex-apply", parentRun: "orch-run"} }

	first := child()
	require.NoError(t, first.acquire(root, "detached-1"))
	require.Len(t, first.locks, 1)
	err := child().acquire(root, "pid-2")
	var ce *apexv1.ConflictError
	require.ErrorAs(t, err, &ce, "%v", err)
	require.Equal(t, apexv1.CodeChildRunning, ce.Code)
	require.Equal(t, apexv1.ActionRetry, ce.Action)
	require.Contains(t, err.Error(), "detached run detached-1")

	again := child()
	require.NoError(t, again.acquire(root, "detached-1"), "the run the lock was taken for takes it over")
	other := &sessionLaunch{depth: 2, skill: "apex-apply", parentRun: "another-orch"}
	require.NoError(t, other.acquire(root, "pid-3"), "the lock is per orchestrator run")
	other.release()

	retargetSessionLocks(first.locks, "detached-1", deadPID)
	require.NoError(t, child().acquire(root, "pid-4"), "a dead holder's lock is cleared")
	first.release()
	_, err = os.Stat(first.locks[0])
	require.NoError(t, err, "release is by holder: pid-4 holds it now")

	orch := &sessionLaunch{depth: 1, skill: apexv1.OrchestratorSkill}
	require.NoError(t, orch.acquire(root, "pid-5"))
	err = (&sessionLaunch{depth: 1, skill: apexv1.OrchestratorSkill}).acquire(root, "pid-6")
	require.ErrorAs(t, err, &ce, "%v", err)
	require.Equal(t, apexv1.CodeOrchestratorRunning, ce.Code)
	orch.release()
	require.NoError(t, (&sessionLaunch{depth: 1, skill: apexv1.OrchestratorSkill}).acquire(root, "pid-6"))

	require.Equal(t, filepath.Join(runlog.SessionLocksRoot(root), "orchestrator.json"), orch.locks[0])
	require.NoError(t, (&sessionLaunch{depth: 1, skill: "apex-apply"}).acquire(root, "pid-7"), "a depth-1 skill session takes no lock")
}

// A refusal from a lock another run holds is exit 3 from a session-starting
// command, with its code and action.
func TestNesting_ARefusalCarriesItsCode(t *testing.T) {
	err := sessionRefusal(apexv1.SessionBusy(true, "apex-apply, pid 1"))
	require.Equal(t, exitV1Conflict, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "code session.child-running, action retry")
}

// requireSessionDepth requires err to be the session.depth refusal.
func requireSessionDepth(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var ue *apexv1.UsageError
	require.ErrorAs(t, err, &ue, "%T: %v", err, err)
	require.Equal(t, apexv1.CodeSessionDepth, ue.Code)
}

// The loop conducts ape runs as `ape task apex-loop`, where ape knows it as
// one; as an `ape prompt` agent it would not be. Refused at preflight,
// before any spawn — and so is the operator's apex-orchestrator (Q95),
// which opens only in a person's own session.
func TestPrompt_RefusesTheOrchestratorAgent(t *testing.T) {
	_, code, err := runPromptCore(context.Background(), promptOptions{
		agent: "apex-loop", text: "build it", projectRoot: t.TempDir(),
	})
	require.Equal(t, ExitUsage, code)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ape task apex-loop")
	require.Contains(t, err.Error(), "/apex-loop <M>")
	require.NotContains(t, err.Error(), "--autonomous", "the loop has no unattended prompt mode")

	_, code, err = runPromptCore(context.Background(), promptOptions{
		agent: "apex-orchestrator", text: "where are we?", projectRoot: t.TempDir(),
	})
	require.Equal(t, ExitUsage, code)
	require.Error(t, err)
	require.Contains(t, err.Error(), "type `/apex-orchestrator`")
	require.Contains(t, err.Error(), "`ape task apex-loop --args <M> --detach`")
}

// Every session carries its marker and its lineage, and never passes on the
// detached run id of the process that hosts it.
func TestChatSpawnEnv_StampsTheSessionMarkerAndLineage(t *testing.T) {
	env, unpin, _ := chatSpawnEnv([]string{"HOME=/h", "APE_PARENT_RUN=inherited", envDetachedID + "=host"}, "", "20260926T120000Z",
		&sessionLaunch{depth: 1})
	defer unpin()
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v // the last one wins, as in exec
	}
	require.Equal(t, "chat/20260926T120000Z", got[repl.EnvApeSession])
	require.Equal(t, "1", got[repl.EnvDepth])
	require.Empty(t, got[repl.EnvParentRun], "an inherited parent never reaches a session ape did not start for it")
	require.Empty(t, got[repl.EnvParentPID])
	require.Empty(t, got[envDetachedID])

	require.Equal(t, []string{"APE_DEPTH=2", "APE_PARENT_RUN=orch", "APE_PARENT_PID=42", envDetachedID + "="},
		(&sessionLaunch{depth: 2, parentRun: "orch", parentPID: 42}).env())
}

// The registry row a session leaves for the ancestry check: registered,
// its claude recorded, and removed by run (one process may run several).
func TestNesting_LiveSessionRow(t *testing.T) {
	outsideEverySession(t)
	root := t.TempDir()
	a := registerSession("script", root, "run-a", &sessionLaunch{depth: 1, skill: "apex-plan"})
	b := registerSession("script", root, "run-b", &sessionLaunch{depth: 1, skill: "apex-change"})
	a.spawned(4321)
	rows, err := sessions.Alive(sessions.Path())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, 4321, rows[0].ClaudePID)
	require.Equal(t, "apex-plan", rows[0].Skill)
	require.Zero(t, rows[1].ClaudePID)
	a.end()
	rows, err = sessions.Alive(sessions.Path())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "run-b", rows[0].RunID)
	b.end()

	// The v1 status lists the project's live sessions.
	registerSession("task", root, "run-c", &sessionLaunch{depth: 2, skill: "apex-apply", parentRun: "orch"})
	live := (&apexv1.Project{Root: root}).LiveSessions()
	require.Len(t, live, 1)
	require.Equal(t, apexv1.LiveSession{RunID: "run-c", Kind: "task", Skill: "apex-apply", Depth: 2, ParentRun: "orch", PID: os.Getpid(), StartedAt: live[0].StartedAt}, live[0])
	data, err := json.Marshal(live[0])
	require.NoError(t, err)
	require.Contains(t, string(data), `"parent_run":"orch"`)
}

// Children die with their parent (Q4): a session whose parent's ape
// process is gone is cancelled with errParentGone.
func TestNesting_WatchParent(t *testing.T) {
	old := parentPollInterval
	parentPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { parentPollInterval = old })

	ctx, stop := watchParent(context.Background(), deadPID)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a dead parent did not stop the session")
	}
	require.ErrorIs(t, context.Cause(ctx), errParentGone)

	ctx, stop = watchParent(context.Background(), os.Getpid())
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, ctx.Err(), "a live parent keeps it")
	stop()

	ctx, stop = watchParent(context.Background(), 0)
	defer stop()
	require.NoError(t, ctx.Err())
	require.True(t, proctree.Alive(os.Getpid()))
}

// `ape change review --run` counts as starting apex-review (Q3): from a
// depth-1 session that is not the orchestrator — apex-apply that a person
// ran through `ape task` — it is refused, with the v1 JSON diagnostic.
func TestNesting_ReviewRunFromASkillSessionIsRefused(t *testing.T) {
	root := v1Repo(t)
	outsideEverySession(t)
	inSession(t, "task/apply-run", 1)
	registerRow(t, sessions.Session{PID: os.Getppid(), Command: "task", RunID: "apply-run", Skill: "apex-apply", Depth: 1})
	stdout, _, err := v1Exec(t, newChangeCmd, "review", "add-wave", "--run", "--json", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), stdout)
	var out struct {
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	require.Equal(t, apexv1.CodeSessionDepth, out.Diagnostics[0].Code)
	require.Equal(t, apexv1.ActionStop, out.Diagnostics[0].Action)
}

// `review --run` takes the orchestrator's child lock before it reads the
// change (eval rc.3 probe: a second review on a draft change answered
// change.wrong-status, not session.child-running): a busy lock is exit 3
// whatever the change's status, and a refusal after the lock was taken
// releases it.
func TestNesting_ReviewRunTakesTheChildLockFirst(t *testing.T) {
	root := v1Repo(t)
	outsideEverySession(t)
	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err, "a draft change")
	inSession(t, "task/orch-run", 1)
	registerRow(t, orchestratorRow())

	other := &sessionLaunch{depth: 2, skill: "apex-apply", parentRun: "orch-run"}
	require.NoError(t, other.acquire(root, "detached-other"))
	stdout, _, err := v1Exec(t, newChangeCmd, "review", "add-wave", "--run", "--json", "--cwd", root)
	require.Equal(t, exitV1Conflict, exitCodeOf(t, err), stdout)
	var out struct {
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	require.Equal(t, apexv1.CodeChildRunning, out.Diagnostics[0].Code)
	other.release()

	// The lock free: the change is not verified, so the review is refused
	// on its status, and the lock it took is not left behind.
	stdout, _, err = v1Exec(t, newChangeCmd, "review", "add-wave", "--run", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, apexv1.CodeWrongStatus)
	_, statErr := os.Stat(filepath.Join(runlog.SessionLocksRoot(root), "child-orch-run.json"))
	require.ErrorIs(t, statErr, os.ErrNotExist, "the refused review released its lock")
}

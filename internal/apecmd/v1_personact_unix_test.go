//go:build unix

package apecmd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// e2DetachedRepo is e2Repo with the long verbs in a detached worker, this
// test binary acting as ape for it, and M2's gate slow and red.
func e2DetachedRepo(t *testing.T) (string, *apexv1.Project) {
	t.Helper()
	root := e2Repo(t, "sleep 2\nexit 1")
	old := v1DetachDefault
	v1DetachDefault = true
	t.Setenv(asApeBinary, "1")
	t.Cleanup(func() { v1DetachDefault = old })
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, scope := range []string{"M1", "M2", "M3"} {
			if l := p.LoadRunLock(scope); l != nil && l.PID > 0 && l.PID != os.Getpid() {
				_ = syscall.Kill(l.PID, syscall.SIGKILL)
			}
		}
	})
	return root, p
}

// OI-12 and design §4.3 (a)–(c), D179: a granted `milestone approve` is a
// detached run whose lock records the person act; the worker — in the
// person's Claude Code session, CLAUDECODE set — acts on that record; the
// same command attaches with no new grant in the same turn (a) and in a
// later turn (b), and prints the ended run's result once; after the print
// it is a new act that needs the person's words again (c).
func TestE2_ApproveDetached_AttachSameAndLaterTurn(t *testing.T) {
	root, p := e2DetachedRepo(t)
	s := newE2Session(t, root, "s1")
	cmdline := "ape milestone approve M2 --wait 0"

	s.say(t, "p1", "approve M2")
	require.Empty(t, s.preTool(t, "p1", cmdline))
	stdout, _, err := v1Exec(t, newMilestoneCmd, "approve", "M2", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err), stdout)
	lock := p.LoadRunLock("M2")
	require.NotNil(t, lock)
	require.Equal(t, "milestone approve", lock.Verb)
	require.NotNil(t, lock.PersonAct)
	require.Equal(t, apexv1.ActMilestoneApprove, lock.PersonAct.Act)
	require.Contains(t, lock.PersonAct.By, `Ann Person (prompt `)
	require.NotEmpty(t, lock.PersonAct.Grant)

	// (a) the same turn: pre-tool allows the re-run with no grant (the
	// prompt's one grant is spent), and it attaches — no second run.
	require.Empty(t, s.preTool(t, "p1", cmdline))
	_, _, err = v1Exec(t, newMilestoneCmd, "approve", "M2", "--wait", "0", "--cwd", root)
	if exitCodeOf(t, err) == exitCodeStillRunning {
		require.Equal(t, lock.ID, p.LoadRunLock("M2").ID, "it attached")
	}

	// (b) a later turn: "is it done?" — still grant-free, live or ended
	// with its result unprinted.
	s.say(t, "p2", "is it done?")
	require.Empty(t, s.preTool(t, "p2", cmdline))
	require.Eventually(t, func() bool { return p.LoadRunLock("M2") == nil }, 60*time.Second, 50*time.Millisecond)
	require.Empty(t, s.preTool(t, "p2", cmdline), "an ended run whose result is unprinted attaches")
	stdout, _, err = v1Exec(t, newMilestoneCmd, "approve", "M2", "--wait", "0", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, `M2 — Second: approved (by Ann Person (prompt `)
	r := p.LoadRunResult("M2")
	require.Equal(t, lock.ID, r.ID, "the recorded run, not a new one")
	require.True(t, r.Reported)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Contains(t, recs["M2"].Approval.By, `"approve M2"`, "the worker recorded the person's act")

	// (c) printed: the same command is a new act.
	require.Contains(t, s.preTool(t, "p2", cmdline), "does not ask for it")
	stdout, _, err = v1Exec(t, newMilestoneCmd, "approve", "M2", "--wait", "0", "--json", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "session.person-only"`)
	require.Nil(t, p.LoadRunLock("M2"), "no run started")
}

// §4.3 (d): an --accept-red close meeting a plain close on its milestone
// is run.busy — in pre-tool and in the command — never an attach that
// would drop the person's flag; nothing is granted or spent.
func TestE2_AcceptRedMeetsPlainClose(t *testing.T) {
	root, p := e2DetachedRepo(t)
	s := newE2Session(t, root, "s1")
	held, other, err := p.TakeRunLock(t.Context(), "milestone close", "M1")
	require.NoError(t, err)
	require.Nil(t, other)
	t.Cleanup(func() { p.ReleaseRunLock(held) })
	cmdline := `ape milestone close M1 --accept-red "known drift"`

	s.say(t, "p1", "accept red on M1")
	reason := s.preTool(t, "p1", cmdline)
	require.Contains(t, reason, "a run without this person act")
	require.Contains(t, reason, "wait for it to end")
	require.NoDirExists(t, filepath.Join(p.GrantsDir(), "issued"), "nothing granted")
	stdout, _, err := v1Exec(t, newMilestoneCmd, "close", "M1", "--accept-red", "known drift", "--json", "--cwd", root)
	require.Equal(t, exitV1Conflict, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "run.busy"`)
	require.Equal(t, held.ID, p.LoadRunLock("M1").ID)
}

// §4.3 (e): the allow → print race. pre-tool allows an attach while the
// run is live; the run ends and another call prints its result before
// this call starts: the command finds no run to attach to and no grant,
// and refuses — no second run on pre-tool's verdict.
func TestE2_AllowPrintRace(t *testing.T) {
	root, p := e2DetachedRepo(t)
	s := newE2Session(t, root, "s1")
	cmdline := "ape milestone approve M2 --wait 0"
	s.say(t, "p1", "approve M2")
	require.Empty(t, s.preTool(t, "p1", cmdline))
	_, _, err := v1Exec(t, newMilestoneCmd, "approve", "M2", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err))
	id := p.LoadRunLock("M2").ID

	require.Empty(t, s.preTool(t, "p1", cmdline), "the allow: an attach")
	// Meanwhile the run ends and its result is printed.
	stdout, _, err := v1Exec(t, newMilestoneCmd, "approve", "M2", "--wait", "60s", "--cwd", root)
	require.NoError(t, err, stdout)
	head := v1Git(t, root, "rev-parse", "HEAD")

	// The allowed call starts now.
	stdout, _, err = v1Exec(t, newMilestoneCmd, "approve", "M2", "--wait", "0", "--json", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "session.person-only"`)
	require.Nil(t, p.LoadRunLock("M2"), "no second run")
	require.Equal(t, id, p.LoadRunResult("M2").ID)
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"))
}

// Before OI-12 the worker's requirePerson saw CLAUDECODE and refused the
// act its caller had been granted. Now the worker acts on its lock's
// person act — and only on one that rests on a consumed grant for its own
// command line (T6: a forged lock is refused).
func TestE2_WorkerActsOnTheLocksGrant(t *testing.T) {
	root, p := e2DetachedRepo(t)
	worker := func(lock *apexv1.RunLock) (string, error) {
		require.True(t, p.UpdateRunLock(lock))
		stdout, _, err := v1Exec(t, newRunWorkerCmd, lock.ID, "milestone", "approve", "M2", "--cwd="+root)
		return stdout, err
	}
	call := personCallsIn("ape milestone approve M2")[0]
	lock, _, err := p.TakeRunLock(t.Context(), "milestone approve", "M2")
	require.NoError(t, err)
	lock.PersonAct = &apexv1.PersonAct{Act: call.Act, Target: call.Target, ArgvSHA256: call.ArgvSHA256, By: "Ann Person"}
	_, err = worker(lock)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "a lock with no consumed grant behind it")
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.True(t, recs["M2"] == nil || recs["M2"].Approval == nil, "nothing approved")

	// That refusal is a result no call printed: printed now, so the next
	// call is a new act, not an attach.
	r := p.LoadRunResult("M2")
	r.Reported = true
	require.NoError(t, p.SaveRunResult(r))
	s := newE2Session(t, root, "s1")
	s.say(t, "p1", "approve M2")
	require.Empty(t, s.preTool(t, "p1", "ape milestone approve M2"))
	a := p.ConsumeGrant(call)
	require.NotNil(t, a)
	a.By = apexv1.PersonBy("Ann Person", a.PromptTS, a.Quote)
	lock, _, err = p.TakeRunLock(t.Context(), "milestone approve", "M2")
	require.NoError(t, err)
	lock.PersonAct = a
	stdout, err := worker(lock)
	require.NoError(t, err, stdout)
	recs, err = p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, a.By, recs["M2"].Approval.By)
}

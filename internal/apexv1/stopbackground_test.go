package apexv1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/sessiondriver"
	"github.com/stretchr/testify/require"
)

func bgPayload(cwd, event string, active bool, tasks ...sessiondriver.BackgroundTask) HookPayload {
	p := stopPayload(cwd, event, active)
	if tasks != nil {
		p.BackgroundTasks = &tasks
	}
	return p
}

var (
	bgShell    = sessiondriver.BackgroundTask{ID: "b1", Type: "shell", Status: "running", Description: "go test ./..."}
	bgMonitor  = sessiondriver.BackgroundTask{ID: "m2", Type: "monitor", Status: "running", Description: "watch the suite"}
	bgSubagent = sessiondriver.BackgroundTask{ID: "a3", Type: "subagent", Status: "running", AgentType: "general-purpose"}
)

// D88: in a session ape launched, the main session's Stop is blocked while
// background work runs, whatever its type, before any change-branch
// check; at most MaxConsecutiveStopBlocks times in a row.
func TestStopHook_BackgroundWork(t *testing.T) {
	for name, task := range map[string]sessiondriver.BackgroundTask{"shell": bgShell, "monitor": bgMonitor, "subagent": bgSubagent} {
		t.Run(name+" blocks, off a change branch too", func(t *testing.T) {
			p := fixture(t, baseFiles())
			inApeSession(t)
			d := StopHook(ctx(), bgPayload(p.Root, "Stop", false, task), RunOptions{})
			require.True(t, d.Block)
			require.True(t, strings.HasPrefix(d.Reason, BackgroundStopPrefix+" (stop blocked 1/3): "+task.Label()), d.Reason)
			require.Contains(t, d.Reason, "never end the turn while it runs")
			require.Contains(t, d.Reason, "600000 ms timeout")
			require.Equal(t, StopBlock, d.Record.Verdict)
			require.Equal(t, []string{task.Label()}, d.Record.Background)
			require.Empty(t, d.Record.Change, "decided before the change branch is looked at")
			runs := p.readStopLog(p.stopLogPath("sess-1"))
			require.Len(t, runs, 1)
			require.Equal(t, []string{task.Label()}, runs[0].Background, "the log carries what review --run and --delegate read")
		})
	}
	t.Run("an empty or absent list changes nothing", func(t *testing.T) {
		p := fixture(t, baseFiles())
		inApeSession(t)
		empty := bgPayload(p.Root, "Stop", false)
		empty.BackgroundTasks = &[]sessiondriver.BackgroundTask{}
		for _, pl := range []HookPayload{empty, bgPayload(p.Root, "Stop", false)} {
			d := StopHook(ctx(), pl, RunOptions{})
			require.False(t, d.Block)
			require.Equal(t, "HEAD is not on a change branch", d.Record.Reason)
			require.Nil(t, d.Record.Background)
		}
	})
	t.Run("a sub-agent's stop is not the session's turn", func(t *testing.T) {
		p := fixture(t, baseFiles())
		inApeSession(t)
		d := StopHook(ctx(), bgPayload(p.Root, "SubagentStop", false, bgShell), RunOptions{})
		require.False(t, d.Block)
		require.Nil(t, d.Record.Background)
	})
	t.Run("three blocks, then the stop is allowed, still recording the work", func(t *testing.T) {
		p := fixture(t, baseFiles())
		inApeSession(t)
		require.True(t, StopHook(ctx(), bgPayload(p.Root, "Stop", false, bgShell, bgMonitor), RunOptions{}).Block)
		for i := 2; i <= MaxConsecutiveStopBlocks; i++ {
			d := StopHook(ctx(), bgPayload(p.Root, "Stop", true, bgShell, bgMonitor), RunOptions{})
			require.True(t, d.Block, "block %d", i)
			require.Equal(t, i, d.Record.ConsecutiveBlocks)
		}
		d := StopHook(ctx(), bgPayload(p.Root, "Stop", true, bgShell, bgMonitor), RunOptions{})
		require.False(t, d.Block)
		require.Equal(t, "allowed after 3 consecutive blocks, with background work still running", d.Record.Reason)
		require.Equal(t, []string{bgShell.Label(), bgMonitor.Label()}, d.Record.Background)
		require.Equal(t, []string{bgShell.Label(), bgMonitor.Label()}, p.LastStopBackground([]string{"sess-1"}))
	})
	t.Run("on a change branch it comes first, and shares the cap with the checks", func(t *testing.T) {
		p := startedWave(t)
		inApeSession(t)
		d := StopHook(ctx(), bgPayload(p.Root, "Stop", false, bgShell), RunOptions{})
		require.True(t, d.Block)
		require.True(t, strings.HasPrefix(d.Reason, BackgroundStopPrefix), d.Reason)
		require.NoFileExists(t, p.LogDir("add-wave")+"/stop-T1.log", "no check runs while the work runs")
		// The work is done; the red check now blocks, counting on.
		d = StopHook(ctx(), bgPayload(p.Root, "Stop", true), RunOptions{})
		require.True(t, d.Block)
		require.Contains(t, d.Reason, "(stop blocked 2/3)")
		require.Equal(t, "add-wave", d.Record.Change)
	})
	t.Run("many tasks are capped in the record", func(t *testing.T) {
		p := fixture(t, baseFiles())
		inApeSession(t)
		tasks := make([]sessiondriver.BackgroundTask, 12)
		for i := range tasks {
			tasks[i] = sessiondriver.BackgroundTask{Type: "shell", Description: strings.Repeat("x", 300)}
		}
		d := StopHook(ctx(), bgPayload(p.Root, "Stop", false, tasks...), RunOptions{})
		require.Len(t, d.Record.Background, 11)
		require.Equal(t, "… 2 more", d.Record.Background[10])
		require.LessOrEqual(t, len([]rune(d.Record.Background[0])), 200)
	})
}

// H13 for D88: in a person's own session the hook informs, never blocks,
// and the change's own notice still follows.
func TestStopHook_BackgroundWorkOutsideAnApeSession(t *testing.T) {
	t.Setenv(envApeSession, "")
	p := startedWave(t)
	d := StopHook(ctx(), bgPayload(p.Root, "Stop", false, bgShell), RunOptions{})
	require.False(t, d.Block)
	require.Equal(t, StopNotApeSession, d.Record.Reason)
	require.Equal(t, []string{bgShell.Label()}, d.Record.Background)
	require.Contains(t, d.Notice, "background work still running: "+bgShell.Label())
	require.Contains(t, d.Notice, "Not blocking")

	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	d = StopHook(ctx(), bgPayload(p.Root, "Stop", false, bgShell), RunOptions{})
	require.False(t, d.Block)
	require.Contains(t, d.Notice, "background work still running")
	require.Contains(t, d.Notice, "change add-wave is in progress")
	out, err := d.Output()
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, d.Notice, got["systemMessage"])
}

// The payload field decodes from Claude Code's wire shape, and absent
// stays distinguishable from empty.
func TestParseHookPayload_BackgroundTasks(t *testing.T) {
	p, err := ParseHookPayload([]byte(`{"hook_event_name":"Stop","background_tasks":[{"id":"b1","type":"shell","status":"running","description":"go test ./..."}]}`))
	require.NoError(t, err)
	require.NotNil(t, p.BackgroundTasks)
	require.Equal(t, []sessiondriver.BackgroundTask{bgShell}, *p.BackgroundTasks)
	p, err = ParseHookPayload([]byte(`{"hook_event_name":"Stop"}`))
	require.NoError(t, err)
	require.Nil(t, p.BackgroundTasks)
}

// LastStopBackground reads the latest main-session Stop across a run's
// sessions; a later clean Stop clears it.
func TestLastStopBackground(t *testing.T) {
	p := fixture(t, baseFiles())
	require.Nil(t, p.LastStopBackground([]string{"nobody", ""}))
	t0 := time.Now().UTC()
	p.appendStopLog(p.stopLogPath("s-a"), StopRecord{SessionID: "s-a", StartedAt: t0.Format(time.RFC3339Nano), Verdict: StopBlock, Background: []string{"shell b1"}})
	p.appendStopLog(p.stopLogPath("s-b"), StopRecord{SessionID: "s-b", StartedAt: t0.Add(-time.Minute).Format(time.RFC3339Nano), Verdict: StopAllow})
	require.Equal(t, []string{"shell b1"}, p.LastStopBackground([]string{"s-b", "s-a"}))
	p.appendStopLog(p.stopLogPath("s-a"), StopRecord{SessionID: "s-a", StartedAt: t0.Add(time.Second).Format(time.RFC3339Nano), Verdict: StopAllow})
	require.Nil(t, p.LastStopBackground([]string{"s-a", "s-b"}))
}

// D88: a review session that ended without a verdict keeps the work its
// last Stop left running; one with a verdict does not.
func TestSettleReviewRun_LiveBackground(t *testing.T) {
	p := verifiedWave(t)
	run := &ReviewRun{Change: "add-wave", StartedAt: "2099-01-01T00:00:00Z", HeadSHA: HeadSHA(ctx(), p.Root)}
	commitAll(t, p, "review: add-wave")
	out, err := p.SettleReviewRun(ctx(), run, "shell b1")
	require.NoError(t, err)
	require.Empty(t, out.Verdict)
	require.Equal(t, []string{"shell b1"}, out.Background)
	require.Equal(t, "ended with live background work: shell b1", LiveBackgroundReason(out.Background))

	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	run.StartedAt = "2000-01-01T00:00:00Z"
	out, err = p.SettleReviewRun(ctx(), run, "shell b1")
	require.NoError(t, err)
	require.Equal(t, "pass", out.Verdict)
	require.Nil(t, out.Background)
}

// D88: a delegate that ended without a verdict, with live background work,
// fails for that reason — the same fail D78 records, a better reason.
func TestDelegate_NoVerdictWithLiveBackground(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	dr := startDelegate(t, p, "run-9")
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr, "shell b1 — \"go test ./...\" [running]")
	require.NoError(t, err)
	require.Equal(t, "fail", out.Verdict)
	require.Equal(t, `ended with live background work: shell b1 — "go test ./..." [running]`, out.Reason)
	require.Equal(t, []string{`shell b1 — "go test ./..." [running]`}, out.Background)
	require.Equal(t, out.Reason, p.Status().Changes[0].Delegate["T2"].Reason)
	_, err = os.Stat(p.DelegateVerdictPath("run-9"))
	require.NoError(t, err)
}

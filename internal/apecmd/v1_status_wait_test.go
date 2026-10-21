package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// fastStatusWait polls the log every 20 ms.
func fastStatusWait(t *testing.T) {
	t.Helper()
	poll, level := statusWaitPoll, statusWaitLevelEvery
	statusWaitPoll, statusWaitLevelEvery = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { statusWaitPoll, statusWaitLevelEvery = poll, level })
}

// waitResult is the part of `ape status --until … --json` the tests read.
//
//nolint:tagliatelle // the v1 --json payloads are snake_case by contract
type waitResult struct {
	Seq  int64  `json:"seq"`
	Next string `json:"next"`
	Wait struct {
		Until         []string              `json:"until"`
		Since         *int64                `json:"since"`
		Condition     *string               `json:"condition"`
		How           string                `json:"how"`
		Event         *apexv1.Event         `json:"event"`
		LastSeq       int64                 `json:"last_seq"`
		PersonActions []apexv1.PersonAction `json:"person_actions"`
		Running       []string              `json:"running"`
	} `json:"wait"`
}

// statusWait runs `ape status` with args and --json, and returns the
// result and the exit code.
func statusWait(t *testing.T, root string, args ...string) (res waitResult, code int) {
	t.Helper()
	stdout, stderr, err := v1Exec(t, newStatusCmd, append([]string{"--json", "--cwd", root}, args...)...)
	if err != nil {
		code = exitCodeOf(t, err)
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), "stdout %s, stderr %s", stdout, stderr)
	require.Equal(t, res.Seq, res.Wait.LastSeq, "seq at the top level and in wait (the conductor's decision)")
	return res, code
}

func projectOf(t *testing.T, root string) *apexv1.Project {
	t.Helper()
	p, ok := v1ProjectAt(root)
	require.True(t, ok)
	return p
}

// W3-R3: a condition that already holds returns at once (its level): a
// proposed milestone is `person`, with its phrase.
func TestStatusWait_LevelReturnsAtOnce(t *testing.T) {
	root := v1Repo(t)
	roadmap := filepath.Join(root, "development", "roadmap.md")
	rm, err := os.ReadFile(roadmap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(roadmap, append(rm, proposedM2...), 0o644))
	v1Git(t, root, "commit", "-q", "-am", "plan: M2")

	start := time.Now()
	res, code := statusWait(t, root, "--until", "person,stopped", "--wait", "9m")
	require.Zero(t, code)
	require.Less(t, time.Since(start), 30*time.Second, "at once, not after --wait")
	require.Equal(t, "level", res.Wait.How)
	require.Equal(t, "person", *res.Wait.Condition, "both hold (no run is live): the first of --until's")
	require.Equal(t, []string{"person", "stopped"}, res.Wait.Until)
	require.Nil(t, res.Wait.Since)
	require.NotEmpty(t, res.Next, "the usual status object")
	require.Equal(t, "approve M2", res.Wait.PersonActions[0].Action)
}

// W3-R4: an event logged between two calls is returned by the next with
// --since — no gap — and the seq it hands on is that event's, so the one
// after it is the next call's. A wait that runs out exits 75 with the
// newest seq.
func TestStatusWait_SinceIsGapless(t *testing.T) {
	root := v1Repo(t)
	p := projectOf(t, root)
	base := p.LastEventSeq()
	p.Emit(t.Context(), apexv1.Event{Event: apexv1.EventGateGreen, Milestone: "M1"}, nil)
	p.Emit(t.Context(), apexv1.Event{Event: apexv1.EventPerson, Milestone: "M2", Action: "approve M2"}, nil)
	p.Emit(t.Context(), apexv1.Event{Event: apexv1.EventBlock, Change: "add-wave", Reason: "why?"}, nil)
	p.Emit(t.Context(), apexv1.Event{Event: apexv1.EventRunEnd, Run: "r1"}, nil)
	since := strconv.FormatInt(base, 10)

	res, code := statusWait(t, root, "--until", "person", "--since", since, "--wait", "0")
	require.Zero(t, code)
	require.Equal(t, "edge", res.Wait.How)
	require.Equal(t, "approve M2", res.Wait.Event.Action)
	require.Equal(t, base+2, res.Seq, "the event's seq: the block after it is the next call's")
	require.Equal(t, base, *res.Wait.Since)

	res, code = statusWait(t, root, "--until", "person", "--since", strconv.FormatInt(res.Seq, 10), "--wait", "0")
	require.Zero(t, code)
	require.Equal(t, "block", res.Wait.Event.Event, "a block waits on a person too")
	require.Equal(t, base+3, res.Seq)

	res, code = statusWait(t, root, "--until", "person", "--since", strconv.FormatInt(res.Seq, 10), "--wait", "0")
	require.Equal(t, exitCodeStillRunning, code, "nothing left for person")
	require.Equal(t, "timeout", res.Wait.How)
	require.Nil(t, res.Wait.Condition)
	require.Nil(t, res.Wait.Event)
	require.Equal(t, base+4, res.Seq, "the newest seq: every event up to it was read")

	// event: any event after --since, one at a time.
	res, code = statusWait(t, root, "--until", "event", "--since", since, "--wait", "0")
	require.Zero(t, code)
	require.Equal(t, "gate-green", res.Wait.Event.Event)
	require.Equal(t, base+1, res.Seq)
}

// The edge while waiting: an event logged during the wait returns it; a
// wait that runs out says so in text too, the seq last.
func TestStatusWait_EdgeDuringTheWaitAndTimeout(t *testing.T) {
	fastStatusWait(t)
	root := v1Repo(t)
	p := projectOf(t, root)
	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Emit(t.Context(), apexv1.Event{Event: apexv1.EventBlock, Change: "add-wave", Reason: "which?"}, nil)
	}()
	res, code := statusWait(t, root, "--until", "blocked", "--wait", "20s")
	require.Zero(t, code)
	require.Equal(t, "edge", res.Wait.How)
	require.Equal(t, "blocked", *res.Wait.Condition)
	require.Equal(t, "which?", res.Wait.Event.Reason)

	stdout, _, err := v1Exec(t, newStatusCmd, "--cwd", root, "--until", "done,blocked", "--wait", "100ms")
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err))
	require.Contains(t, stdout, "none of done,blocked held — run the same command again with --since "+strconv.FormatInt(p.LastEventSeq(), 10))
	require.Regexp(t, `\nseq: \d+\n$`, stdout, "every exit prints the seq last")
}

// W3-R5: stopped is read from the run handles: a run going holds it off; a
// run whose supervisor was killed counts as stopped, and is logged
// run-lost, once.
func TestStatusWait_StoppedReadsTheHandles(t *testing.T) {
	fastStatusWait(t)
	root := v1Repo(t)
	require.NoError(t, os.MkdirAll(runlog.DetachedRoot(root), 0o755))
	// No supervisor_pid yet: a run just started, going.
	require.NoError(t, writeHandle(handlePath(root, "busy"), &detachedHandle{ID: "busy", Argv: []string{"task", "apex-plan"}, StartedAt: time.Now()}))
	res, code := statusWait(t, root, "--until", "stopped", "--wait", "0")
	require.Equal(t, exitCodeStillRunning, code)
	require.Equal(t, []string{"busy"}, res.Wait.Running)

	// Killed: its supervisor is gone without recording an exit.
	require.NoError(t, updateHandle(handlePath(root, "busy"), func(h *detachedHandle) { h.SupervisorPID = deadPID }))
	res, code = statusWait(t, root, "--until", "stopped", "--wait", "5s")
	require.Zero(t, code)
	require.Equal(t, "stopped", *res.Wait.Condition)
	require.Equal(t, "level", res.Wait.How)
	require.Empty(t, res.Wait.Running)
	require.Equal(t, "run-lost", res.Wait.Event.Event, "the loss it logged")
	require.Equal(t, "apex-plan", res.Wait.Event.Skill)

	_, code = statusWait(t, root, "--until", "stopped", "--wait", "0")
	require.Zero(t, code)
	require.Len(t, eventsNamed(t, root, apexv1.EventRunLost), 1, "logged once")
}

// stopped on its edge: the run ends during the wait — its supervisor
// records the exit and logs run-end — and the wait returns it.
func TestStatusWait_StoppedEdge(t *testing.T) {
	fastStatusWait(t)
	root := v1Repo(t)
	require.NoError(t, os.MkdirAll(runlog.DetachedRoot(root), 0o755))
	path := handlePath(root, "busy")
	h := &detachedHandle{ID: "busy", Argv: []string{"task", "apex-apply", "--args", "M1"}, StartedAt: time.Now(), Root: root}
	require.NoError(t, writeHandle(path, h))
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = recordExitAndEmit(context.Background(), path, h, 0, "")
	}()
	res, code := statusWait(t, root, "--until", "stopped", "--wait", "20s")
	require.Zero(t, code)
	require.Equal(t, "stopped", *res.Wait.Condition)
	// The handle has the exit before the event is logged: a poll between
	// the two finds the level, and the event is the next call's.
	if res.Wait.Event == nil {
		require.Equal(t, "level", res.Wait.How)
		require.Eventually(t, func() bool { return len(eventsNamed(t, root, apexv1.EventRunEnd)) == 1 }, 10*time.Second, 20*time.Millisecond)
		res, code = statusWait(t, root, "--until", "stopped", "--since", strconv.FormatInt(res.Seq, 10), "--wait", "0")
		require.Zero(t, code)
	}
	require.Equal(t, "run-end", res.Wait.Event.Event)
	require.Equal(t, 0, *res.Wait.Event.Exit)
	require.Equal(t, "M1", res.Wait.Event.Milestone)
}

// done and blocked are levels too; and the flags are checked.
func TestStatusWait_DoneBlockedAndUsage(t *testing.T) {
	root := v1Repo(t)
	_, code := statusWait(t, root, "--until", "done", "--wait", "0")
	require.Equal(t, exitCodeStillRunning, code, "M1 is approved, not done")

	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	_, _, err = v1Exec(t, newChangeCmd, "block", "add-wave", "--reason", "which?", "--cwd", root)
	require.NoError(t, err)
	res, code := statusWait(t, root, "--until", "done,blocked", "--wait", "0")
	require.Zero(t, code)
	require.Equal(t, "blocked", *res.Wait.Condition)

	roadmap := filepath.Join(root, "development", "roadmap.md")
	require.NoError(t, os.WriteFile(roadmap, []byte("# Roadmap\n\n## M1 — Skeleton\nStatus: done (2026-10-21T12:00:00Z abc1234)\nGate: ./gate.sh\nGoal: greets\n"), 0o644))
	res, code = statusWait(t, root, "--until", "done", "--wait", "0")
	require.Zero(t, code)
	require.Equal(t, "done", *res.Wait.Condition)

	for _, args := range [][]string{
		{"--since", "3"},
		{"--wait", "1m"},
		{"--until", "people"},
		{"--until", ","},
		{"--until", "person", "--since", "-1"},
		{"--until", "person", "--board"},
	} {
		stdout, _, err := v1Exec(t, newStatusCmd, append([]string{"--json", "--cwd", root}, args...)...)
		require.Equal(t, ExitUsage, exitCodeOf(t, err), "%v", args)
		require.Contains(t, stdout, `"code": "usage.until"`, "%v", args)
	}
	stdout, _, err := v1Exec(t, newStatusCmd, "--json", "--cwd", root, "--until", "person", "--wait", "10m")
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "usage.wait"`)
}

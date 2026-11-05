//go:build unix

package apecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// githubCoordRepo is a project of an ensemble integrating through GitHub
// (a bare repository as its remote, the API faked), with M1 claimed by
// this checkout: milestone/M1 is cut and pushed, and HEAD stays on main,
// as `ape milestone claim` leaves it (stage 1b's m9 after its claim).
func githubCoordRepo(t *testing.T) string {
	t.Helper()
	return githubCoordRepoOn(t, natstest.RunJetStreamServer(t))
}

// githubCoordRepoOn is githubCoordRepo on the NATS at url.
func githubCoordRepoOn(t *testing.T, url string) string {
	t.Helper()
	root := v1Repo(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, lease.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	v1Git(t, root, "branch", "-M", "main")
	bare := t.TempDir()
	v1Git(t, bare, "init", "-q", "--bare")
	v1Git(t, root, "remote", "add", "origin", bare)
	v1Git(t, root, "push", "-q", "-u", "origin", "main")
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + url + "\n  github:\n    repo: shop/notes\n    api: http://127.0.0.1:1\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	v1Git(t, root, "commit", "-qam", "chore: coordinated")
	v1Git(t, root, "push", "-q", "origin", "main")
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "github.token"), []byte("github_pat_t\n"), 0o600))

	fake := &cliIntegrator{}
	old := apexv1.NewIntegrator
	apexv1.NewIntegrator = func(context.Context, *apexv1.Project) (apexv1.Integrator, error) { return fake, nil }
	t.Cleanup(func() { apexv1.NewIntegrator = old })
	_, _, err = v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.NoError(t, err)
	// The claim record reaches the remote's main through its record PR;
	// the fake merges it, so the remote's main is where this one is.
	v1Git(t, root, "push", "-q", "origin", "main")
	require.Equal(t, "main", v1Git(t, root, "branch", "--show-current"), "a claim leaves HEAD on main")
	return root
}

// FINDING-V15-1B-1: `ape milestone take-in M` runs as a long run whose
// lock scope is take-in-M, and the detached worker was started with the
// scope as its argument: it ran `milestone take-in take-in-M` and refused
// session.not-lease-holder on a milestone named take-in-M (stage 1b's M9,
// four times). The T1: a detached take-in of the claimed M1 answers as
// the in-process one does. The control: the same call with --no-detach,
// the path 1a's take-ins took (inside close), gives the same answer, so
// what differed was only the worker's argument.
func TestMilestoneTakeIn_DetachedWorkerTakesTheMilestone(t *testing.T) {
	root := githubCoordRepo(t)
	old := v1DetachDefault
	v1DetachDefault = true
	t.Setenv(asApeBinary, "1")
	t.Cleanup(func() { v1DetachDefault = old })
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	scope := apexv1.TakeInPrefix + "M1"
	t.Cleanup(func() {
		if l := p.LoadRunLock(scope); l != nil && l.PID > 0 && l.PID != os.Getpid() {
			_ = syscall.Kill(l.PID, syscall.SIGKILL)
		}
		awaitNoProcessUnder(t, root, 10*time.Second)
	})

	stdout, stderr, err := v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--wait", "60s", "--cwd", root)
	log, _ := os.ReadFile(p.RunLogPath(scope))
	require.NoError(t, err, "stdout %s\nstderr %s\nworker log %s", stdout, stderr, log)
	require.NotContains(t, stdout+string(log), "take-in-M1 (never claimed", "the worker gated M1, not a milestone named take-in-M1")
	require.Contains(t, string(log), "`ape milestone take-in M1` started", "B2: the banner names the argument")
	require.NotContains(t, string(log), "take-in take-in-M1", "B2: never the scope as the command")
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, true, res["up_to_date"], stdout)
	require.Equal(t, "M1", res["milestone"])

	stdout, _, err = v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--no-detach", "--cwd", root)
	require.NoError(t, err, stdout)
	var here map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &here), stdout)
	require.Equal(t, res, here, "control: in-process, the same answer")
}

// D800+ (as V8e): a long verb's run waits for NATS rather than refusing,
// so a loop's call exits 75 and its next call attaches. The T1: with every
// node down, `take-in M1 --wait 3s` exits 75 while the detached run says it
// waits for NATS; once NATS is back, the next call attaches and ends 0.
// The control: `--wait 0` refuses at once (coord.nats-unreachable, exit 1)
// and starts no run. (A pause state that cannot be read takes the same
// path in member.GateWait: TestGateWait_APauseStateThatCannotBeRead.)
func TestMilestoneTakeIn_WaitsOutANATSOutage(t *testing.T) {
	c := natstest.RunCluster(t, 3)
	root := githubCoordRepoOn(t, c.URLs())
	old := v1DetachDefault
	v1DetachDefault = true
	t.Setenv(asApeBinary, "1")
	t.Cleanup(func() { v1DetachDefault = old })
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	scope := apexv1.TakeInPrefix + "M1"
	t.Cleanup(func() {
		if l := p.LoadRunLock(scope); l != nil && l.PID > 0 && l.PID != os.Getpid() {
			_ = syscall.Kill(l.PID, syscall.SIGKILL)
		}
		awaitNoProcessUnder(t, root, 10*time.Second)
	})
	for i := range 3 {
		c.Stop(i)
	}

	start := time.Now()
	stdout, stderr, err := v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--wait", "0", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), "control: --wait 0 refuses at once")
	require.Contains(t, stdout+stderr, apexv1.CodeCoordNATSUnreachable)
	require.Less(t, time.Since(start), 10*time.Second)
	require.Nil(t, p.LoadRunLock(scope), "control: no run started")

	stdout, stderr, err = v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--wait", "3s", "--cwd", root)
	log, _ := os.ReadFile(p.RunLogPath(scope))
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err), "stdout %s\nstderr %s\nworker log %s", stdout, stderr, log)
	require.Eventually(t, func() bool {
		log, _ = os.ReadFile(p.RunLogPath(scope))
		return strings.Contains(string(log), "milestone take-in: waiting for NATS")
	}, 10*time.Second, 200*time.Millisecond, "the run says what it waits for: %s", log)

	for i := range 3 {
		c.Restart(i)
	}
	c.WaitMetaLeader(30 * time.Second)
	stdout, stderr, err = v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--wait", "60s", "--cwd", root)
	log, _ = os.ReadFile(p.RunLogPath(scope))
	require.NoError(t, err, "stdout %s\nstderr %s\nworker log %s", stdout, stderr, log)
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, true, res["up_to_date"], stdout)
}

// V8l, 1c3's live shape (outage #1, m15's archive): the outage comes
// after the run's entry, during the take-in's regression, so V8's entry
// wait never sees it and the fence before the push met it: exit 1 retry,
// the take-in undone, the regression run again. The T1: NATS goes down
// while the regression runs; the run says it waits for NATS at the push,
// and once NATS is back the take-in lands with exit 0 — the regression ran
// once. (The controls are member's: outside a run, and another's lease,
// refuse at once.)
func TestMilestoneTakeIn_WaitsForNATSAtThePushFence(t *testing.T) {
	c := natstest.RunCluster(t, 3)
	root := githubCoordRepoOn(t, c.URLs())
	ran := filepath.Join(t.TempDir(), "ran.txt")
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte("regression: ./suite.sh\n")...), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "suite.sh"), []byte("#!/bin/sh\necho run >> "+ran+"\nsleep 3\n"), 0o755))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-qm", "chore: a regression")
	v1Git(t, root, "push", "-q", "origin", "main")
	_, _, err = v1Exec(t, newProjectCmd, "accept-config", "--by", "t", "--cwd", root)
	require.NoError(t, err)
	old := v1DetachDefault
	v1DetachDefault = true
	t.Setenv(asApeBinary, "1")
	t.Cleanup(func() { v1DetachDefault = old })
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	scope := apexv1.TakeInPrefix + "M1"
	t.Cleanup(func() {
		if l := p.LoadRunLock(scope); l != nil && l.PID > 0 && l.PID != os.Getpid() {
			_ = syscall.Kill(l.PID, syscall.SIGKILL)
		}
		awaitNoProcessUnder(t, root, 10*time.Second)
	})
	// No loop renews M1 here: claim it again so its TTL covers the run.
	_, _, err = v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.NoError(t, err)
	// main moves after the claim, so M1 has a take-in to land.
	v1Git(t, root, "switch", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(root, "moved.txt"), []byte("main moved\n"), 0o644))
	v1Git(t, root, "add", "moved.txt")
	v1Git(t, root, "commit", "-qm", "chore: main moves")
	v1Git(t, root, "push", "-q", "origin", "main")

	type result struct {
		stdout, stderr string
		err            error
	}
	done := make(chan result, 1)
	go func() {
		o, e, err := v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--wait", "90s", "--cwd", root)
		done <- result{o, e, err}
	}()
	for start := time.Now(); ; {
		if _, err := os.Stat(ran); err == nil {
			break
		}
		select {
		case r := <-done:
			log, _ := os.ReadFile(p.RunLogPath(scope))
			t.Fatalf("the take-in ended before its regression: %v\nstdout %q\nstderr %s\nworker log %s", r.err, r.stdout, r.stderr, log)
		default:
		}
		require.Less(t, time.Since(start), 60*time.Second, "the regression starts")
		time.Sleep(100 * time.Millisecond)
	}
	for i := range 3 {
		c.Stop(i)
	}
	require.Eventually(t, func() bool {
		log, _ := os.ReadFile(p.RunLogPath(scope))
		return strings.Contains(string(log), "the push of milestone/M1: waiting for NATS")
	}, 20*time.Second, 200*time.Millisecond, "the run waits at the push fence")
	for i := range 3 {
		c.Restart(i)
	}
	c.WaitMetaLeader(30 * time.Second)
	var r result
	select {
	case r = <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("the take-in did not end")
	}
	log, _ := os.ReadFile(p.RunLogPath(scope))
	require.NoError(t, r.err, "stdout %s\nstderr %s\nworker log %s", r.stdout, r.stderr, log)
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &res), r.stdout)
	require.Equal(t, false, res["up_to_date"], r.stdout)
	runs, err := os.ReadFile(ran)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(runs), "run"), "the regression ran once: nothing was undone")
}

// V8m (1c3, M16): a take-in conflict is laid out on change/take-in-<n>,
// apex-apply commits its fix there, and a close called from that HEAD
// resumes: its take-in lands the fix and switches back to milestone/<M>.
// The control: HEAD on an ordinary change branch still refuses
// (change.branch).
func TestMilestoneClose_ResumesOnItsLaidOutTakeIn(t *testing.T) {
	root := githubCoordRepo(t)
	v1Git(t, root, "switch", "-q", "milestone/M1")
	require.NoError(t, os.WriteFile(filepath.Join(root, "clash.txt"), []byte("milestone\n"), 0o644))
	v1Git(t, root, "add", "clash.txt")
	v1Git(t, root, "commit", "-qm", "chore: the milestone's line")
	v1Git(t, root, "push", "-q", "origin", "milestone/M1")
	v1Git(t, root, "switch", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(root, "clash.txt"), []byte("main\n"), 0o644))
	v1Git(t, root, "add", "clash.txt")
	v1Git(t, root, "commit", "-qm", "chore: main's line")
	v1Git(t, root, "push", "-q", "origin", "main")

	_, _, err := v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--no-detach", "--cwd", root)
	require.Error(t, err, "the take-in conflicts")
	require.Equal(t, "change/take-in-1", v1Git(t, root, "branch", "--show-current"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "clash.txt"), []byte("both\n"), 0o644))
	v1Git(t, root, "add", "clash.txt")
	v1Git(t, root, "commit", "-q", "--no-edit")

	stdout, stderr, err := v1Exec(t, newMilestoneCmd, "close", "M1", "--json", "--no-detach", "--cwd", root)
	require.NotContains(t, stdout+stderr+fmt.Sprint(err), apexv1.CodeChangeBranch, "the close resumes on its own take-in")
	require.Equal(t, "milestone/M1", v1Git(t, root, "branch", "--show-current"), "its take-in landed and switched back")
	got, err := os.ReadFile(filepath.Join(root, "clash.txt"))
	require.NoError(t, err)
	require.Equal(t, "both\n", string(got), "the fix landed on milestone/M1")

	v1Git(t, root, "switch", "-q", "-c", "change/unrelated")
	stdout, stderr, err = v1Exec(t, newMilestoneCmd, "close", "M1", "--json", "--no-detach", "--cwd", root)
	require.Error(t, err)
	require.Contains(t, stdout+stderr+err.Error(), apexv1.CodeChangeBranch, "control: another change branch refuses")
}

// The worker's argv is the verb's argument, and only the lock is the
// scope: take-in gets M (its scope take-in-M); a verb whose scope is its
// argument gets it unchanged (milestone gate M1).
func TestWorkerArgv_IsTheArgumentNotTheScope(t *testing.T) {
	root := v1Repo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	for _, tc := range []struct{ verb, arg string }{{"take-in", "M1"}, {"gate", "M1"}} {
		cmd := newMilestoneCmd()
		sub, _, err := cmd.Find([]string{tc.verb})
		require.NoError(t, err)
		argv := workerArgv(sub, p, tc.arg)
		require.Equal(t, []string{"milestone", tc.verb, tc.arg}, argv[:3], "%s", tc.verb)
	}
}

// FINDING-V15-1B-2 (D747+, ruled (a)): in a project integrating through
// GitHub, after a claim HEAD is on main, and the loop's first
// `ape milestone gate M9` committed `apex: M9 gate red` on the local main,
// which the remote's main lacks (m9's e8f0ab3). The gate now switches to
// milestone/<M> on a clean tree first, as change new/start and close do:
// the record lands on milestone/M1 and main is untouched. The control: with
// the tree not clean it refuses with the route, and nothing is committed
// on either branch.
func TestMilestoneGate_CoordinatedRunsOnTheMilestoneBranch(t *testing.T) {
	root := githubCoordRepo(t)
	mainBefore := v1Git(t, root, "rev-parse", "main")
	msBefore := v1Git(t, root, "rev-parse", "milestone/M1")

	require.NoError(t, os.WriteFile(filepath.Join(root, "scratch.txt"), []byte("not committed\n"), 0o644))
	stdout, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--cwd", root)
	require.Error(t, err, "control: a tree not clean is refused")
	require.Contains(t, stdout, "scratch.txt", "it names what is in the way")
	require.Equal(t, "main", v1Git(t, root, "branch", "--show-current"))
	require.Equal(t, mainBefore, v1Git(t, root, "rev-parse", "main"), "nothing committed on main")
	require.Equal(t, msBefore, v1Git(t, root, "rev-parse", "milestone/M1"), "nor on milestone/M1")
	require.NoError(t, os.Remove(filepath.Join(root, "scratch.txt")))

	stdout, _, _ = v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--cwd", root)
	require.Equal(t, "milestone/M1", v1Git(t, root, "branch", "--show-current"), "the gate switched to the milestone's branch: %s", stdout)
	require.Equal(t, mainBefore, v1Git(t, root, "rev-parse", "main"), "main is untouched")
	subject := v1Git(t, root, "log", "-1", "--format=%s", "milestone/M1")
	require.True(t, strings.HasPrefix(subject, "apex: M1 gate "), "the gate's record is on milestone/M1: %q", subject)
	require.NotEqual(t, msBefore, v1Git(t, root, "rev-parse", "milestone/M1"))
}

// FINDING-V15-1B-2 for take-in (ruled (a)): a take-in from main switches
// to milestone/<M> on a clean tree first; with the tree not clean it
// refuses and moves nothing (the control).
func TestMilestoneTakeIn_FromMainSwitchesOrRefuses(t *testing.T) {
	root := githubCoordRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "scratch.txt"), []byte("x\n"), 0o644))
	stdout, _, err := v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--cwd", root)
	require.Error(t, err, "control: refused, not taken in from main")
	require.Contains(t, stdout, "scratch.txt")
	require.Equal(t, "main", v1Git(t, root, "branch", "--show-current"))
	require.NoError(t, os.Remove(filepath.Join(root, "scratch.txt")))

	stdout, _, err = v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Equal(t, "milestone/M1", v1Git(t, root, "branch", "--show-current"))
}

// Q-H5-4 (D808+): a take-in holds M's lease through its own run, as close
// does (FINDING-V15-1A-11). With no loop renewing it, a regression three
// TTLs long used to let it lapse, and the push fence met "held by nobody
// (it lapsed)". The T1: a 1 s TTL, a 3 s regression, run in this process
// with no loop: the lease is renewed while the regression runs (its
// revision moves, still this checkout's) and the take-in lands, exit 0.
func TestMilestoneTakeIn_HoldsTheLeaseThroughItsRun(t *testing.T) {
	oldTTL, oldEvery := member.MilestoneTTL, member.MilestoneRenewEvery
	member.MilestoneTTL, member.MilestoneRenewEvery = time.Second, 250*time.Millisecond
	t.Cleanup(func() { member.MilestoneTTL, member.MilestoneRenewEvery = oldTTL, oldEvery })
	c := natstest.RunCluster(t, 3)
	root := githubCoordRepoOn(t, c.URLs())
	ran := filepath.Join(t.TempDir(), "ran.txt")
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte("regression: ./suite.sh\n")...), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "suite.sh"), []byte("#!/bin/sh\necho run >> "+ran+"\nsleep 3\n"), 0o755))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-qm", "chore: a regression")
	v1Git(t, root, "push", "-q", "origin", "main")
	_, _, err = v1Exec(t, newProjectCmd, "accept-config", "--by", "t", "--cwd", root)
	require.NoError(t, err)
	_, _, err = v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.NoError(t, err)
	v1Git(t, root, "switch", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(root, "moved.txt"), []byte("main moved\n"), 0o644))
	v1Git(t, root, "add", "moved.txt")
	v1Git(t, root, "commit", "-qm", "chore: main moves")
	v1Git(t, root, "push", "-q", "origin", "main")
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	m, err := member.Load(p)
	require.NoError(t, err)

	nc, err := nats.Connect(c.URLs())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ls, err := lease.Open(t.Context(), js)
	require.NoError(t, err)
	key := "milestone." + m.Project + ".M1"

	type result struct {
		stdout, stderr string
		err            error
	}
	done := make(chan result, 1)
	go func() {
		o, e, err := v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--no-detach", "--wait", "90s", "--cwd", root)
		done <- result{o, e, err}
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(ran); return err == nil }, 60*time.Second, 50*time.Millisecond, "the regression starts")
	holder, first, err := ls.Holder(t.Context(), key)
	require.NoError(t, err)
	require.NotEmpty(t, holder, "held as the regression starts")
	time.Sleep(2 * time.Second) // two TTLs into the regression
	h, now, err := ls.Holder(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, holder, h, "still this checkout's, two TTLs on")
	require.Greater(t, now.Revision, first.Revision, "renewed by the take-in's own hold")
	var r result
	select {
	case r = <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("the take-in did not end")
	}
	require.NoError(t, r.err, "stdout %s\nstderr %s", r.stdout, r.stderr)
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &res), r.stdout)
	require.Equal(t, false, res["up_to_date"], r.stdout)
}

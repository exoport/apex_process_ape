//go:build unix

package apecmd

import (
	"context"
	"encoding/json"
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
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// githubCoordRepo is a project of an ensemble integrating through GitHub
// (a bare repository as its remote, the API faked), with M1 claimed by
// this checkout: milestone/M1 is cut and pushed, and HEAD stays on main,
// as `ape milestone claim` leaves it (stage 1b's m9 after its claim).
func githubCoordRepo(t *testing.T) string {
	t.Helper()
	root := v1Repo(t)
	url := natstest.RunJetStreamServer(t)
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
	apexv1.NewIntegrator = func(*apexv1.Project) (apexv1.Integrator, error) { return fake, nil }
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

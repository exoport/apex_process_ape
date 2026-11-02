package apecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// Stage 1a step 4 through the CLI: milestone claim and release need an
// ensemble; in one, claim takes M1 for this checkout, release gives it up,
// and an apex-loop for M1 without the claim is refused before any session
// starts (session.not-lease-holder).
func TestMilestoneClaim_CLI(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.ErrorContains(t, err, "in no ensemble")

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

	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + url + "\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))

	stdout, _, err := v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "M1 claimed by dana/")
	stdout, _, err = v1Exec(t, newMilestoneCmd, "claim", "M1", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var claimed struct {
		Key    string `json:"key"`
		Holder string `json:"holder"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &claimed), stdout)
	holder, _, err := ls.Holder(ctx, claimed.Key)
	require.NoError(t, err)
	require.Equal(t, claimed.Holder, holder)
	require.Contains(t, holder, "dana/")

	stdout, _, err = v1Exec(t, newMilestoneCmd, "release", "M1", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "M1 released")
	holder, _, err = ls.Holder(ctx, claimed.Key)
	require.NoError(t, err)
	require.Empty(t, holder)

	_, stderr, err := v1Exec(t, newTaskCmd, "apex-loop", "--args", "M1", "--cwd", root)
	require.Error(t, err)
	require.Contains(t, err.Error()+stderr, apexv1.CodeNotLeaseHolder)
}

// Stage 1a step 7 through the CLI: in a project integrating through GitHub
// the claim cuts milestone/M1 and pushes it, and take-in with main not
// moved is up to date (the long-verb path, without opening a PR); in a
// project integrating by local trunk, take-in is a usage error.
func TestMilestoneTakeIn_CLI(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newMilestoneCmd, "take-in", "M1", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "no coordination: block")

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

	stdout, _, err := v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "milestone/M1: cut from main and pushed")
	require.Contains(t, stdout, "claim record: https://github.example/pull/1 merged into main as merge-o")
	require.Len(t, fake.opened, 1)
	require.True(t, strings.HasPrefix(fake.opened[0], "apex/record-M1-claimed-by-dana-"), fake.opened[0])
	require.Equal(t, v1Git(t, root, "rev-parse", "main"), v1Git(t, bare, "rev-parse", "milestone/M1"))

	stdout, _, err = v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, true, res["up_to_date"])
	require.Equal(t, "milestone/M1", res["branch"])
}

// cliIntegrator is the coordinated repository's API for a CLI test.
type cliIntegrator struct{ opened []string }

func (f *cliIntegrator) OpenPR(_ context.Context, head, base, _, _ string) (apexv1.PRRef, error) {
	f.opened = append(f.opened, head+"→"+base)
	return apexv1.PRRef{Number: len(f.opened), NodeID: "PR", URL: fmt.Sprintf("https://github.example/pull/%d", len(f.opened))}, nil
}

func (f *cliIntegrator) Enqueue(context.Context, apexv1.PRRef) (string, error) { return "QUEUED", nil }

func (f *cliIntegrator) PRState(_ context.Context, n int) (apexv1.PRState, error) {
	// A record PR merges, as GitHub's queue does one (record commands
	// wait for it, FINDING-V15-1A-7 b); a close PR waits in the queue.
	if n >= 1 && n <= len(f.opened) && strings.HasPrefix(f.opened[n-1], apexv1.PRBranchPrefix+"record-") {
		return apexv1.PRState{Merged: true, MergeCommit: fmt.Sprintf("merge-of-%d", n)}, nil
	}
	return apexv1.PRState{Queue: "AWAITING_CHECKS"}, nil
}

// D702: archive and close write milestone/<M>, so in an ensemble's
// project both need this checkout's lease on M, as change new and start
// do; claimed, they pass the gate (and refuse later for their own
// reasons).
func TestCoordinatedArchiveAndCloseNeedTheLease(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err, "change new commits its draft")

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
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + url + "\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))

	for _, args := range [][]string{{"change", "archive", "add-wave"}, {"milestone", "close", "M1"}} {
		cmd := newChangeCmd
		if args[0] == "milestone" {
			cmd = newMilestoneCmd
		}
		stdout, _, err := v1Exec(t, cmd, append(args[1:], "--json", "--cwd", root)...)
		require.Error(t, err, "%v", args)
		require.Contains(t, stdout, apexv1.CodeNotLeaseHolder, "%v: no lease on M1", args)
	}
	_, _, err = v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.NoError(t, err)
	for _, args := range [][]string{{"change", "archive", "add-wave"}, {"milestone", "close", "M1"}} {
		cmd := newChangeCmd
		if args[0] == "milestone" {
			cmd = newMilestoneCmd
		}
		stdout, _, _ := v1Exec(t, cmd, append(args[1:], "--json", "--cwd", root)...)
		require.NotContains(t, stdout, apexv1.CodeNotLeaseHolder, "%v: claimed, past the gate", args)
	}
}

// FINDING-V15-1A-11 (widened, D729): a close's gate runs far past the
// milestone lease's TTL before its fenced push. close now renews M's lease
// for its whole duration: while its gate runs (3 s here, against a 1 s
// TTL) another member's claim of M1 is refused, and it may claim once the
// close has ended and the TTL passed.
func TestMilestoneClose_HoldsTheLeaseWhileItsGateRuns(t *testing.T) {
	oldTTL, oldEvery := member.MilestoneTTL, member.MilestoneRenewEvery
	member.MilestoneTTL, member.MilestoneRenewEvery = time.Second, 250*time.Millisecond
	t.Cleanup(func() { member.MilestoneTTL, member.MilestoneRenewEvery = oldTTL, oldEvery })

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
	checkoutOf := func(user string, slowGate bool) string {
		root := v1Repo(t)
		if slowGate {
			// dana's gate takes 3 s, three TTLs; approved as it is now.
			require.NoError(t, os.WriteFile(filepath.Join(root, "gate.sh"), []byte("#!/bin/sh\nsleep 3\ntest -f done.flag\n"), 0o755))
			v1Git(t, root, "commit", "-q", "-am", "slow gate")
			_, _, err := v1Exec(t, newMilestoneCmd, "approve", "M1", "--by", "eval", "--cwd", root)
			require.NoError(t, err)
		}
		cfg, err := os.ReadFile(apexv1.ConfigPath(root))
		require.NoError(t, err)
		// One project, two checkouts: the same project_name, so M1 is one
		// lease key (init named each after its directory).
		cfg = regexp.MustCompile(`(?m)^project_name:.*$`).ReplaceAll(cfg, []byte("project_name: notes"))
		block := "coordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + url + "\n"
		require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
		creds, _ := natstest.MintStandaloneCreds(t, user)
		data, err := os.ReadFile(creds)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
		return root
	}
	dana, eve := checkoutOf("dana", true), checkoutOf("eve", false)
	_, _, err = v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", dana)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		out, errOut, err := v1Exec(t, newMilestoneCmd, "close", "M1", "--cwd", dana)
		t.Logf("close: %v\n%s\n%s", err, out, errOut)
	}()
	time.Sleep(2 * time.Second) // past the TTL, inside the gate
	stdout, _, err := v1Exec(t, newMilestoneCmd, "claim", "M1", "--json", "--cwd", eve)
	<-done
	require.Error(t, err, "the close renews dana's lease while its gate runs")
	require.Contains(t, stdout, apexv1.CodeNotLeaseHolder)
	require.Contains(t, stdout, "dana/")
	time.Sleep(2 * time.Second)
	_, _, err = v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", eve)
	require.NoError(t, err, "the close ended: no keeper, the lease lapsed, and eve may claim")
}

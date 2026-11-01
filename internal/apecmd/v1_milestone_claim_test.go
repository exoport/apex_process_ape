package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
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

	stdout, _, err := v1Exec(t, newMilestoneCmd, "claim", "M1", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "milestone/M1: cut from main and pushed")
	require.Equal(t, v1Git(t, root, "rev-parse", "main"), v1Git(t, bare, "rev-parse", "milestone/M1"))

	stdout, _, err = v1Exec(t, newMilestoneCmd, "take-in", "M1", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var res map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, true, res["up_to_date"])
	require.Equal(t, "milestone/M1", res["branch"])
}

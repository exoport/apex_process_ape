package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// PLAN-V15-GHAPP §7: what ape prints for the operator is what the service
// reads — the printed config loads, and the unit carries the hardening the
// plan and research's finding 7 ask for.
func TestGhappPrintConfigAndUnit(t *testing.T) {
	stdout, _, err := v1Exec(t, newGhappCmd, "print-config", "--repo", "example/notes",
		"--warden-app-id", "11", "--warden-installation", "21", "--pusher-app-id", "12", "--pusher-installation", "22",
		"--nats-url", "nats://127.0.0.1:14222,nats://127.0.0.1:14223", "--members", "diegos, ci", "--operators", "diegos")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(stdout), 0o644))
	cfg, err := ghapp.LoadConfig(path)
	require.NoError(t, err, stdout)
	require.Equal(t, int64(12), cfg.Pusher.AppID)
	require.Equal(t, int64(22), cfg.Pusher.Installations["example/notes"])
	require.Equal(t, []string{"diegos", "ci"}, cfg.Repos["example/notes"].Members)
	require.Equal(t, []string{"diegos"}, cfg.Repos["example/notes"].Operators)
	require.Equal(t, "${CREDENTIALS_DIRECTORY}/pusher.pem", cfg.Pusher.Key)
	require.Empty(t, cfg.Repos["example/notes"].RunnerLabel)
	_, _, err = v1Exec(t, newGhappCmd, "print-config", "--repo", "x/y")
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	// D871: queue/runner's label rides in the config; a default label is refused.
	args := []string{
		"print-config", "--repo", "example/notes", "--warden-app-id", "11", "--warden-installation", "21", "--pusher-app-id", "12",
		"--pusher-installation", "22", "--nats-url", "nats://127.0.0.1:14222", "--members", "diegos", "--operators", "diegos",
	}
	stdout, _, err = v1Exec(t, newGhappCmd, append(args, "--runner-label", "apex-r1")...)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(stdout), 0o644))
	cfg, err = ghapp.LoadConfig(path)
	require.NoError(t, err, stdout)
	require.Equal(t, "apex-r1", cfg.Repos["example/notes"].RunnerLabel)
	stdout, _, err = v1Exec(t, newGhappCmd, append(args, "--watchers", "ci-watch")...)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(stdout), 0o644))
	cfg, err = ghapp.LoadConfig(path)
	require.NoError(t, err, stdout)
	require.Equal(t, []string{"ci-watch"}, cfg.Repos["example/notes"].Watchers)
	_, _, err = v1Exec(t, newGhappCmd, append(args, "--runner-label", "Linux")...)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))

	stdout, _, err = v1Exec(t, newGhappCmd, "print-unit", "--ape", "/opt/ape/bin/ape")
	require.NoError(t, err)
	for _, want := range []string{
		"User=apex-ghapp", "ExecStart=/opt/ape/bin/ape ghapp serve", "LoadCredential=pusher.pem:/etc/apex-ghapp/keys/pusher.pem",
		"LimitCORE=0", "UMask=0077", "ProtectProc=invisible", "NoNewPrivileges=yes", "CapabilityBoundingSet=\n", "ProtectHome=yes",
	} {
		require.Contains(t, stdout, want)
	}
}

// PLAN-V15-GHAPP 9.4: ape ghapp setup asks the service as this checkout's
// user, for its coordinated repository; a secret's value comes from the
// file and is never echoed.
func TestGhappSetup_CLI(t *testing.T) {
	url := natstest.RunServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	got := make(chan [2]string, 4)
	_, err = nc.Subscribe(ghapp.SetupSubject+".*", func(m *nats.Msg) {
		var r ghapp.Request
		_ = json.Unmarshal(m.Data, &r)
		got <- [2]string{m.Subject, r.Repo + "|" + r.Act + "|" + r.Name + "|" + r.Value}
		rep := ghapp.Reply{OK: true, Result: []string{"did " + r.Act}}
		if r.Act == "runner-token" {
			rep.Token, rep.ExpiresAt = "RUNNER_TOKEN", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		data, _ := json.Marshal(rep)
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex", "state"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), []byte("project_name: notes\ncoordination:\n  integration: queue\n  ensemble: shop\n  home: x\n  url: "+url+"\n  github:\n    repo: shop/notes\n    app: true\n"), 0o644))
	creds, _ := natstest.MintStandaloneCreds(t, "diegos")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	secret := filepath.Join(root, "hot", "ci-nats.creds")
	require.NoError(t, os.WriteFile(secret, []byte("CI-CREDS-TEXT\n"), 0o600))

	stdout, _, err := v1Exec(t, newGhappCmd, "setup", "secrets", "--name", "APE_NATS_CREDS", "--value-file", secret, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "did secrets")
	require.NotContains(t, stdout, "CI-CREDS-TEXT", "never echoed")
	g := <-got
	require.Equal(t, ghapp.SetupSubject+".diegos", g[0], "asked as this checkout's user")
	require.Equal(t, "shop/notes|secrets|APE_NATS_CREDS|CI-CREDS-TEXT\n", g[1])

	stdout, _, err = v1Exec(t, newGhappCmd, "setup", "runner-token", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "runner registration token (expires 2030-01-01T00:00:00Z): RUNNER_TOKEN")
	<-got

	_, _, err = v1Exec(t, newGhappCmd, "setup", "delete-everything", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	_, _, err = v1Exec(t, newGhappCmd, "setup", "secrets", "--name", "GITHUB_TOKEN", "--value-file", secret, "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
}

// landIntegrator merges every enqueued PR at once.
type landIntegrator struct{ opened, enqueued []string }

func (f *landIntegrator) OpenPR(_ context.Context, head, base, _, _ string) (apexv1.PRRef, error) {
	f.opened = append(f.opened, head+"→"+base)
	return apexv1.PRRef{Number: 5, NodeID: "PR_5", URL: "https://github.example/pull/5"}, nil
}

func (f *landIntegrator) Enqueue(_ context.Context, pr apexv1.PRRef) (string, error) {
	f.enqueued = append(f.enqueued, pr.URL)
	return "QUEUED", nil
}

func (f *landIntegrator) PRState(context.Context, int) (apexv1.PRState, error) {
	return apexv1.PRState{Merged: true, MergeCommit: "abc1234"}, nil
}

// D706: ape ghapp land pushes a local branch, opens its PR into main,
// enqueues it and waits for the merge; never milestone/<M> or main.
func TestGhappLand_CLI(t *testing.T) {
	root := v1Repo(t)
	v1Git(t, root, "branch", "-M", "main")
	bare := t.TempDir()
	v1Git(t, bare, "init", "-q", "--bare")
	v1Git(t, root, "remote", "add", "origin", bare)
	v1Git(t, root, "push", "-q", "-u", "origin", "main")
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  integration: queue\n  ensemble: shop\n  home: x\n  url: nats://127.0.0.1:1\n  github:\n    repo: example/notes\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "github.token"), []byte("t\n"), 0o600))
	v1Git(t, root, "switch", "-q", "-c", "apex/framework-update")
	v1Git(t, root, "commit", "-qam", "chore(config): coordination for stage 1a")
	tip := v1Git(t, root, "rev-parse", "HEAD")
	fake := &landIntegrator{}
	old := apexv1.NewIntegrator
	apexv1.NewIntegrator = func(context.Context, *apexv1.Project) (apexv1.Integrator, error) { return fake, nil }
	t.Cleanup(func() { apexv1.NewIntegrator = old })

	stdout, _, err := v1Exec(t, newGhappCmd, "land", "apex/framework-update", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "merged into main as abc1234")
	require.Equal(t, tip, v1Git(t, bare, "rev-parse", "apex/framework-update"), "pushed")
	require.Equal(t, []string{"apex/framework-update→main"}, fake.opened)
	require.Len(t, fake.enqueued, 1)

	v1Git(t, root, "branch", "milestone/M1")
	_, _, err = v1Exec(t, newGhappCmd, "land", "milestone/M1", "--cwd", root)
	require.Error(t, err, "milestone branches are ape's")
	_, _, err = v1Exec(t, newGhappCmd, "land", "main", "--cwd", root)
	require.Error(t, err)
}

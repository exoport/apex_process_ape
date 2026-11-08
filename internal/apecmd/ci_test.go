package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// Stage 1a step 10: the workflow ape writes is valid YAML whose jobs run
// ape ci check on merge_group and ape ci event on red and on push; --write
// is idempotent.
func TestCIWorkflow(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newCICmd, "workflow", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), "solo: the workflow is queue/runner's")
	writeAndCommit(t, root, "_apex/config.yaml", "project_name: notes\ncoordination:\n  integration: queue\n  events: runner\n  runner_label: apex-r1\n  ensemble: shop\n  home: h\n  url: nats://127.0.0.1:1\n  github:\n    repo: shop/notes\n", "chore: queue/runner")
	want, err := apexv1.QueueWorkflowFor("apex-r1")
	require.NoError(t, err)
	stdout, _, err := v1Exec(t, newCICmd, "workflow", "--cwd", root)
	require.NoError(t, err)
	require.Equal(t, want, stdout)
	var wf struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(stdout), &wf))
	require.Contains(t, wf.On, "merge_group")
	var runs []string
	for _, j := range wf.Jobs {
		for _, s := range j.Steps {
			runs = append(runs, s.Run)
		}
	}
	all := strings.Join(runs, "\n")
	require.Contains(t, all, "ape ci check --base")
	require.Contains(t, all, "ape ci event ejected")
	require.Contains(t, all, "ape ci event close-merged")
	require.NotContains(t, stdout, "milestone/**", "D702: the queue is main's only")

	stdout, _, err = v1Exec(t, newCICmd, "workflow", "--write", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "wrote .github/workflows/apex-queue.yml")
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "apex-queue.yml"))
	require.NoError(t, err)
	require.Equal(t, want, string(data))
	stdout, _, err = v1Exec(t, newCICmd, "workflow", "--write", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "is up to date")
}

// ci event publishes the project event a member's waiting ape wakes on
// (OQ10): the member's Watch fires.
func TestCIEvent_WakesTheMember(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))

	root := v1Repo(t)
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  integration: trunk\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + url + "\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	wake, stop, live := member.Watch(ctx, p)
	defer stop()
	require.True(t, live)

	ciCreds, _ := natstest.MintStandaloneCreds(t, "ci-runner")
	_, _, err = v1Exec(t, newCICmd, "event", "bogus", "--cwd", root, "--nats-url", url, "--nats-creds", ciCreds)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	stdout, _, err := v1Exec(t, newCICmd, "event", "merged", "--ref", "milestone/M2", "--sha", "abc123", "--cwd", root, "--nats-url", url, "--nats-creds", ciCreds)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "published merged")
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("the member's waiting ape did not wake on CI's event")
	}
}

func TestCICheck_CLI(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newCICmd, "check", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	head := v1Git(t, root, "rev-parse", "HEAD")
	v1Git(t, root, "commit", "-q", "--allow-empty", "-m", "docs: outside the flow")
	stdout, _, err := v1Exec(t, newCICmd, "check", "--base", head, "--head", "HEAD", "--json", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, apexv1.CodeCIOutsideFlow)
}

// V12g: `ape ci event` publishes under exactly cred.CIGrant, and the
// member's waiting ape, under exactly cred.MemberGrant, wakes on it, on an
// enforcing server that refuses nothing.
func TestCIEvent_UnderTheCIGrant(t *testing.T) {
	e := natstest.StartEnforced(t)
	nc, err := nats.Connect(e.URL, nats.UserCredentials(e.Admin))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))

	root := v1Repo(t)
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  integration: trunk\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + e.URL + "\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), []byte("x"), 0o600))
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	m, err := member.Load(p)
	require.NoError(t, err)
	g := cred.MemberGrant("dana", m.Project, m.Ensemble)
	data, err := os.ReadFile(e.Mint(t, "dana", natstest.Perms{PubAllow: g.PubAllow, SubAllow: g.SubAllow}))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	wake, stop, live := member.Watch(ctx, p)
	defer stop()
	require.True(t, live)

	c := cred.CIGrant("ci-runner", m.Project)
	ciCreds := e.Mint(t, "ci-runner", natstest.Perms{PubAllow: c.PubAllow, SubAllow: c.SubAllow})
	stdout, _, err := v1Exec(t, newCICmd, "event", "merged", "--ref", "milestone/M2", "--sha", "abc123", "--cwd", root, "--nats-url", e.URL, "--nats-creds", ciCreds)
	require.NoError(t, err, stdout)
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("the member's waiting ape did not wake on CI's event")
	}
}

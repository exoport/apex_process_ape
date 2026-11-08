package apecmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/ciwatch"
	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/ghapp"
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
	// D876 (d): `ape ci watch`'s event, under its own user and message id,
	// wakes it the same way.
	m, err := member.Load(p)
	require.NoError(t, err)
	_, err = events.PublishProjectID(ctx, js, "ci-watch", m.Project, "ci-watch-close-merged-abc123", apexv1.CIEvent("close-merged", "main", "abc123", "", time.Now()))
	require.NoError(t, err)
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("the member's waiting ape did not wake on ci watch's event")
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

// D876 (d): `ape ci watch` is queue/watch's (ci.watch-mode elsewhere); its
// --once --dry-run is one read-only look — the watch token asked for over
// NATS as the credential's user, main's tip and the groups read, nothing
// published.
func TestCIWatch_ModeAndOnceDryRun(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))
	var asked []string
	var mu sync.Mutex
	sub, err := nc.Subscribe(ghapp.TokenSubject+".*", func(m *nats.Msg) {
		var req ghapp.Request
		_ = json.Unmarshal(m.Data, &req)
		mu.Lock()
		asked = append(asked, m.Subject+" "+req.Purpose+" "+req.Repo)
		mu.Unlock()
		data, _ := json.Marshal(ghapp.Reply{OK: true, Token: "ghs_watch", ExpiresAt: time.Now().Add(time.Hour)})
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghs_watch" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/shop/notes/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"ref":"refs/heads/main","object":{"sha":"abc123"}}`))
		case "/repos/shop/notes/git/matching-refs/heads/gh-readonly-queue/main/":
			_, _ = w.Write([]byte(`[{"ref":"refs/heads/gh-readonly-queue/main/pr-4-abc123","object":{"sha":"def456"}}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(gh.Close)

	root := v1Repo(t)
	base, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	setBlock := func(extra string) {
		block := "coordination:\n  integration: queue\n" + extra + "  ensemble: shop\n  home: h\n  url: " + url + "\n  github:\n    repo: shop/notes\n    api: " + gh.URL + "\n    app: true\n"
		require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(append([]byte(nil), base...), []byte(block)...), 0o644))
	}
	watchCreds, _ := natstest.MintStandaloneCreds(t, "ci-watch")
	setBlock("")
	stdout, _, err := v1Exec(t, newCICmd, "watch", "--once", "--cwd", root, "--nats-url", url, "--nats-creds", watchCreds)
	require.Error(t, err)
	require.Equal(t, 1, exitCodeOf(t, err), "queue/poll: refused")
	require.Contains(t, stdout+err.Error(), apexv1.CodeCIWatchMode)

	setBlock("  events: watch\n")
	stdout, _, err = v1Exec(t, newCICmd, "watch", "--once", "--dry-run", "--cwd", root, "--nats-url", url, "--nats-creds", watchCreds)
	require.NoError(t, err, stdout)
	var r ciwatch.Report
	require.NoError(t, json.Unmarshal([]byte(stdout), &r), stdout)
	require.Equal(t, "abc123", r.Main)
	require.Equal(t, []ciwatch.Ref{{Ref: "refs/heads/gh-readonly-queue/main/pr-4-abc123", SHA: "def456"}}, r.Groups[4])
	require.True(t, r.Baseline)
	require.Empty(t, r.Events)
	mu.Lock()
	require.Equal(t, []string{ghapp.TokenSubject + ".ci-watch watch shop/notes"}, asked, "the watch token, as the credential's user")
	mu.Unlock()
}

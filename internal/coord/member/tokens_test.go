package member

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// PLAN-V15-GHAPP 9.3: with coordination.github.app, ape asks the App
// service for each token over NATS as this member's user — no token file
// — and pushes over https with the token in an env-only header. Without
// app, the token file and git's own credentials, as before.
func TestAppTokensAndPushVia(t *testing.T) {
	url := natstest.RunServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	var mu sync.Mutex
	var subjects []string
	var reqs []ghapp.Request
	_, err = nc.Subscribe(ghapp.TokenSubject+".*", func(m *nats.Msg) {
		var r ghapp.Request
		_ = json.Unmarshal(m.Data, &r)
		mu.Lock()
		subjects, reqs = append(subjects, m.Subject), append(reqs, r)
		mu.Unlock()
		data, _ := json.Marshal(ghapp.Reply{OK: true, Token: "ghs_" + r.Purpose})
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	app := strings.Replace(queued(block), "nats://127.0.0.1:4222", url, 1) + "  github:\n    repo: shop/notes\n    app: true\n"
	p := project(t, app, false)
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))

	m, err := Load(p)
	require.NoError(t, err, "app: no hot/github.token needed")
	require.True(t, m.GitHubApp)
	tok, err := m.GitHubTokenFor(context.Background(), "pr")
	require.NoError(t, err)
	require.Equal(t, "ghs_pr", tok)

	u, env, err := GitVia(context.Background(), p, "push")
	require.NoError(t, err)
	require.Equal(t, "https://x-access-token@github.com/shop/notes.git", u, "no insteadOf rewrite of https://github.com/ applies")
	require.Contains(t, env, "GIT_CONFIG_KEY_0=http.extraHeader")
	require.Contains(t, env, "GIT_CONFIG_VALUE_1=", "no credential helper")
	var header string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "); ok {
			header = v
		}
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	require.NoError(t, err)
	require.Equal(t, "x-access-token:ghs_push", string(raw))

	mu.Lock()
	require.Equal(t, []string{ghapp.TokenSubject + ".dana", ghapp.TokenSubject + ".dana"}, subjects, "asked as this member's user")
	require.Equal(t, "shop/notes", reqs[1].Repo)
	require.Equal(t, "push", reqs[1].Purpose)
	mu.Unlock()

	// The control: no app — git's own credentials, no service asked.
	q := project(t, queued(block)+"  github:\n    repo: shop/notes\n", true)
	require.NoError(t, os.WriteFile(filepath.Join(q.Root, "hot", "github.token"), []byte("github_pat_x\n"), 0o600))
	u, env, err = GitVia(context.Background(), q, "push")
	require.NoError(t, err)
	require.Empty(t, u)
	require.Empty(t, env)
	mq, err := Load(q)
	require.NoError(t, err)
	tok, err = mq.GitHubTokenFor(context.Background(), "push")
	require.NoError(t, err)
	require.Equal(t, "github_pat_x", tok)
}

func TestWebHost(t *testing.T) {
	require.Equal(t, "https://github.com", webHost("https://api.github.com"))
	require.Equal(t, "https://ghe.example", webHost("https://ghe.example/api/v3/"))
}

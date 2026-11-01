package ghapp

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/natstest"
)

// PLAN-V15-GHAPP 9.2: the service over NATS request-reply — a member gets
// a narrowed token (cached), anyone else is refused; set-up acts are the
// operator's; the act log never holds a token or a secret's value.
func TestService(t *testing.T) {
	key, pemBytes := appKey(t)
	f := newFake(t, &key.PublicKey)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(keyPath, pemBytes, 0o600))
	notified := filepath.Join(dir, "notified")
	cfg := &Config{
		API:    srv.URL,
		Warden: AppConfig{AppID: 42, Key: keyPath, Installations: map[string]int64{"shop/notes": 8}},
		Pusher: AppConfig{AppID: 42, Key: keyPath, Installations: map[string]int64{"shop/notes": 7}},
		Repos:  map[string]RepoPolicy{"shop/notes": {Members: []string{"dana", "ci"}, Operators: []string{"dana"}}},
		Log:    filepath.Join(dir, "acts.jsonl"), Notify: "cat >> " + notified,
	}
	s, err := NewService(cfg)
	require.NoError(t, err)

	url := natstest.RunServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Serve(ctx, nc) }()
	cli, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(cli.Close)
	ask := func(subject, user string, req Request) (Reply, error) {
		actx, acancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer acancel()
		return Ask(actx, cli, subject, user, req)
	}
	require.Eventually(t, func() bool {
		_, err := ask(TokenSubject, "dana", Request{Repo: "shop/notes", Purpose: "push"})
		return err == nil
	}, 5*time.Second, 50*time.Millisecond)

	rep, err := ask(TokenSubject, "ci", Request{Repo: "shop/notes", Purpose: "pr"})
	require.NoError(t, err)
	require.Equal(t, "ghs_test", rep.Token)
	_, err = ask(TokenSubject, "dana", Request{Repo: "shop/notes", Purpose: "push"})
	require.NoError(t, err)
	require.Len(t, f.tokenReqs, 2, "push minted once and cached; pr minted once")

	_, err = ask(TokenSubject, "mallory", Request{Repo: "shop/notes", Purpose: "push"})
	require.ErrorContains(t, err, "mallory is not a member")
	_, err = ask(TokenSubject, "dana", Request{Repo: "shop/notes", Purpose: "admin"})
	require.ErrorContains(t, err, "purpose")
	_, err = ask(TokenSubject, "dana", Request{Repo: "shop/other", Purpose: "push"})
	require.ErrorContains(t, err, "not served")

	_, err = ask(SetupSubject, "ci", Request{Repo: "shop/notes", Act: "rulesets"})
	require.ErrorContains(t, err, "not an operator", "set-up acts are the operator's")
	rep, err = ask(SetupSubject, "dana", Request{Repo: "shop/notes", Act: "rulesets"})
	require.NoError(t, err)
	require.Len(t, f.rulesets, 4)
	require.NotEmpty(t, rep.Result)
	_, err = ask(SetupSubject, "dana", Request{Repo: "shop/notes", Act: "secrets", Name: "GITHUB_TOKEN", Value: "x"})
	require.ErrorContains(t, err, "only APE_NATS_URL and APE_NATS_CREDS")
	_, err = ask(SetupSubject, "dana", Request{Repo: "shop/notes", Act: "secrets", Name: "APE_NATS_CREDS", Value: "SECRET-CREDS-TEXT"})
	require.NoError(t, err)
	require.Equal(t, "SECRET-CREDS-TEXT", f.secrets["APE_NATS_CREDS"])
	rep, err = ask(SetupSubject, "dana", Request{Repo: "shop/notes", Act: "runner-token"})
	require.NoError(t, err)
	require.Equal(t, "RUNNER_TOKEN", rep.Token)
	_, err = ask(SetupSubject, "dana", Request{Repo: "shop/notes", Act: "delete-everything"})
	require.ErrorContains(t, err, "act \"delete-everything\"")

	log, err := os.ReadFile(cfg.Log)
	require.NoError(t, err)
	for _, secret := range []string{"ghs_test", "RUNNER_TOKEN", "SECRET-CREDS-TEXT"} {
		require.NotContains(t, string(log), secret, "the act log never holds a token or a secret's value")
	}
	require.Contains(t, string(log), `"user":"mallory"`, "refusals are logged too")
	st, err := os.Stat(cfg.Log)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	n, err := os.ReadFile(notified)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(n), "\n"), "secrets and runner-token notify the operator")
	require.NotContains(t, string(n), "RUNNER_TOKEN")
}

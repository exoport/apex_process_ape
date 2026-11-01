package member

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// askTimeout bounds one request to the App service.
const askTimeout = 30 * time.Second

// GitHubTokenFor is a token for purpose (push, pr, read): from the
// coordination host's GitHub App service when the block says app: true —
// an installation token narrowed to the repository and the purpose,
// asked over NATS as this member's user — else hot/github.token.
func (m Membership) GitHubTokenFor(ctx context.Context, purpose string) (string, error) {
	if !m.GitHubApp {
		return m.ReadGitHubToken()
	}
	id, err := natsconn.DecodeIdentity(m.Creds)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, "ape-ghapp-client", nats.NoReconnect())
	if err != nil {
		return "", err
	}
	defer nc.Close()
	rep, err := ghapp.Ask(ctx, nc, ghapp.TokenSubject, id.SubjectToken, ghapp.Request{Repo: m.GitHubRepo, Purpose: purpose})
	if err != nil {
		return "", err
	}
	return rep.Token, nil
}

// webHost is the git host beside the API: github.com for api.github.com,
// the Enterprise host for …/api/v3.
func webHost(api string) string {
	api = strings.TrimRight(api, "/")
	if base, ok := strings.CutSuffix(api, "/api/v3"); ok {
		return base
	}
	return strings.Replace(api, "://api.", "://", 1)
}

// GitVia is apexv1.GitVia for a member project whose tokens come from the
// App service: the repository over https, with a token for purpose (push,
// or read for a fetch) in http.extraHeader passed only through git's
// GIT_CONFIG_* environment (never argv, a URL or disk), and no credential
// helper. The URL carries the user x-access-token (no secret), so a
// person's insteadOf rewrite of https://github.com/ to an ssh alias does
// not apply. A project with a personal token file, or in no ensemble, uses
// its remote with git's own credentials ("" url).
func GitVia(ctx context.Context, p *apexv1.Project, purpose string) (url string, env []string, err error) {
	m, err := Load(p)
	if err != nil || !m.GitHubApp {
		return "", nil, nil //nolint:nilerr // not an App project: git's own credentials
	}
	tok, err := m.GitHubTokenFor(ctx, purpose)
	if err != nil {
		return "", nil, err
	}
	return gitURL(m.GitHubAPI, m.GitHubRepo), pushEnv(tok), nil
}

// gitURL is the repository's https URL with the user x-access-token.
func gitURL(api, repo string) string {
	host := webHost(api)
	if rest, ok := strings.CutPrefix(host, "https://"); ok {
		host = "https://x-access-token@" + rest
	}
	return host + "/" + repo + ".git"
}

// pushEnv carries tok to git in an Authorization header for this command
// only.
func pushEnv(tok string) []string {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=",
	}
}

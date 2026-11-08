package member

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// gitT runs git in dir, failing the test on an error.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "core.hooksPath=" + os.DevNull}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// appRemote is the coordinated repository served over http by git's own
// http-backend, recording each request's Authorization header.
type appRemote struct {
	srv     *httptest.Server
	tip     string
	mu      sync.Mutex
	headers []string
}

func newAppRemote(t *testing.T) *appRemote {
	t.Helper()
	backend := filepath.Join(gitT(t, ".", "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not installed")
	}
	root := t.TempDir()
	seed := t.TempDir()
	gitT(t, seed, "init", "-q", "-b", "main")
	gitT(t, seed, "commit", "-q", "--allow-empty", "-m", "base")
	bare := filepath.Join(root, "shop", "notes.git")
	gitT(t, seed, "clone", "-q", "--bare", seed, bare)
	r := &appRemote{tip: gitT(t, seed, "rev-parse", "HEAD")}
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.headers = append(r.headers, req.Header.Get("Authorization"))
		r.mu.Unlock()
		h.ServeHTTP(w, req)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *appRemote) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.headers...)
}

// FINDING-V15-1A-6: in an App project every git call to the coordinated
// repository goes through the App or refuses. mmr7's person config
// rewrites https://github.com/ to ssh with their key, so a fallback to
// the remote ran as the person.
//
//   - the rewrite and no NATS credential: refused (coord.membership), and
//     nothing reached the remote or ssh;
//   - the rewrite and the credential: fetched over http(s) as the App
//     (its read token in the header) — the person's global config is not
//     read, so its rewrite of the App's URL does not apply;
//   - the control, the same checkout without the rewrite: the same two
//     verdicts, so the rewrite is what the fetch survived;
//   - a rewrite in the checkout's own .git/config: refused
//     (coord.url-rewritten), nothing sent.
func TestGitVia_AppProjectRefusesOrFetchesAsTheApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("git-http-backend over CGI and a shell ssh stand-in")
	}
	remote := newAppRemote(t)

	// The App service: a read token for each request, as dana.
	url := natstest.RunServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	_, err = nc.Subscribe(ghapp.TokenSubject+".*", func(m *nats.Msg) {
		var r ghapp.Request
		_ = json.Unmarshal(m.Data, &r)
		data, _ := json.Marshal(ghapp.Reply{OK: true, Token: "ghs_" + r.Purpose})
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	credData, err := os.ReadFile(creds)
	require.NoError(t, err)

	old := apexv1.GitVia
	apexv1.GitVia = GitVia
	t.Cleanup(func() { apexv1.GitVia = old })

	// ssh, as the person's rewrite would reach it: a stand-in that only
	// records that it ran.
	sshRan := filepath.Join(t.TempDir(), "ssh-ran")
	sshCmd := filepath.Join(t.TempDir(), "ssh")
	require.NoError(t, os.WriteFile(sshCmd, []byte("#!/bin/sh\ntouch "+sshRan+"\nexit 1\n"), 0o755))
	t.Setenv("GIT_SSH_COMMAND", sshCmd)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	// The person's global config, as mmr7's: github.com and the App's
	// host both rewritten to ssh.
	rewrite := filepath.Join(t.TempDir(), "gitconfig-rewrite")
	require.NoError(t, os.WriteFile(rewrite, []byte("[url \"ssh://git@person.invalid/\"]\n\tinsteadOf = https://github.com/\n\tinsteadOf = "+remote.srv.URL+"/\n"), 0o644))
	plain := filepath.Join(t.TempDir(), "gitconfig-plain")
	require.NoError(t, os.WriteFile(plain, nil, 0o644))

	cfg := strings.Replace(queuedBlock, "nats://127.0.0.1:4222", url, 1) + "  github:\n    repo: shop/notes\n    api: " + remote.srv.URL + "/api/v3\n    app: true\n"
	checkout := func(t *testing.T, withCreds bool) *apexv1.Project {
		t.Helper()
		p := project(t, cfg, false)
		gitT(t, p.Root, "init", "-q", "-b", "main")
		gitT(t, p.Root, "commit", "-q", "--allow-empty", "-m", "local")
		gitT(t, p.Root, "remote", "add", "origin", "https://github.com/shop/notes.git")
		if withCreds {
			require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), credData, 0o600))
		}
		return p
	}
	refused := func(t *testing.T, p *apexv1.Project, code string) {
		t.Helper()
		before := len(remote.requests())
		_, err := p.FetchAndSync(context.Background(), "main")
		var fe *apexv1.FailError
		require.ErrorAsf(t, err, &fe, "a refusal, not a fetch")
		require.Equal(t, code, fe.Code, fe.Msg)
		require.Contains(t, fe.Msg, "does not fall back", "it says why it did not fetch")
		require.Len(t, remote.requests(), before, "nothing reached the remote")
		require.NoFileExists(t, sshRan, "nothing went to ssh as the person")
	}
	fetched := func(t *testing.T, p *apexv1.Project) {
		t.Helper()
		before := len(remote.requests())
		_, err := p.FetchAndSync(context.Background(), "main")
		require.NoError(t, err)
		require.Equal(t, remote.tip, gitT(t, p.Root, "rev-parse", "refs/remotes/origin/main"))
		reqs := remote.requests()[before:]
		require.NotEmpty(t, reqs, "the fetch went to the App's URL")
		for _, h := range reqs {
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "basic "))
			require.NoErrorf(t, err, "header %q", h)
			require.Equal(t, "x-access-token:ghs_read", string(raw), "as the App, with its read token")
		}
		require.NoFileExists(t, sshRan, "nothing went to ssh as the person")
	}

	t.Run("rewrite", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", rewrite)
		refused(t, checkout(t, false), apexv1.CodeCoordMembership)
		fetched(t, checkout(t, true))
	})
	t.Run("control", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", plain)
		refused(t, checkout(t, false), apexv1.CodeCoordMembership)
		fetched(t, checkout(t, true))
	})
	t.Run("checkout-rule", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", plain)
		p := checkout(t, true)
		gitT(t, p.Root, "config", "url.ssh://git@person.invalid/.insteadOf", remote.srv.URL+"/")
		before := len(remote.requests())
		_, err := p.FetchAndSync(context.Background(), "main")
		var fe *apexv1.FailError
		require.ErrorAsf(t, err, &fe, "a refusal, not a fetch")
		require.Equal(t, apexv1.CodeCoordURLRewritten, fe.Code, fe.Msg)
		require.NotContains(t, fe.Msg, "x-access-token", "the message names hosts, not the URL's user")
		require.Len(t, remote.requests(), before, "nothing reached the remote")
		require.NoFileExists(t, sshRan)
	})
}

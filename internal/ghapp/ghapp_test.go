package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"
)

// fakeGitHub is the slice of GitHub's REST API the service calls.
type fakeGitHub struct {
	mu        sync.Mutex
	appKey    *rsa.PublicKey
	tokenReqs []map[string]any
	rulesets  map[string]Ruleset
	nextID    int64
	workflow  bool // the workflow file is on the default branch
	secretPub *[32]byte
	secretPri *[32]byte
	secrets   map[string]string
	prs       []string
	branch    bool
	// noMergeQueues: the App has not got Merge queues (FINDING-V15-1A-7 c):
	// a token asking for it is refused, as GitHub does.
	noMergeQueues bool
}

func newFake(t *testing.T, appKey *rsa.PublicKey) *fakeGitHub {
	t.Helper()
	pub, pri, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return &fakeGitHub{appKey: appKey, rulesets: map[string]Ruleset{}, nextID: 100, secretPub: pub, secretPri: pri, secrets: map[string]string{}}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/app/installations/"):
		if !f.validJWT(auth) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"bad jwt"}`))
			return
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		f.tokenReqs = append(f.tokenReqs, req)
		if perms, _ := req["permissions"].(map[string]any); f.noMergeQueues && perms["merge_queues"] != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"The permissions requested are not granted to this installation."}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"ghs_test","expires_at":"2030-01-01T00:00:00Z","permissions":{"contents":"write"}}`))
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/contents/.github/workflows/apex-queue.yml"):
		if !f.workflow {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodGet && path == "/repos/shop/notes/rulesets":
		out := []Ruleset{}
		for _, rs := range f.rulesets {
			out = append(out, Ruleset{ID: rs.ID, Name: rs.Name})
		}
		writeJSON(w, out)
	case r.Method == http.MethodPost && path == "/repos/shop/notes/rulesets":
		var rs Ruleset
		_ = json.Unmarshal(body, &rs)
		f.nextID++
		rs.ID = f.nextID
		f.rulesets[rs.Name] = rs
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/repos/shop/notes/rulesets/"):
		var rs Ruleset
		_ = json.Unmarshal(body, &rs)
		rs.ID = f.rulesets[rs.Name].ID
		f.rulesets[rs.Name] = rs
	case r.Method == http.MethodGet && path == "/repos/shop/notes/actions/secrets/public-key":
		writeJSON(w, map[string]string{"key_id": "k1", "key": base64.StdEncoding.EncodeToString(f.secretPub[:])})
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/repos/shop/notes/actions/secrets/"):
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		sealed, _ := base64.StdEncoding.DecodeString(in["encrypted_value"])
		plain, ok := box.OpenAnonymous(nil, sealed, f.secretPub, f.secretPri)
		if !ok || in["key_id"] != "k1" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		f.secrets[strings.TrimPrefix(path, "/repos/shop/notes/actions/secrets/")] = string(plain)
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPost && path == "/repos/shop/notes/actions/runners/registration-token":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"RUNNER_TOKEN","expires_at":"2030-01-01T01:00:00Z"}`))
	case r.Method == http.MethodGet && path == "/repos/shop/notes":
		_, _ = w.Write([]byte(`{"default_branch":"main"}`))
	case r.Method == http.MethodGet && path == "/repos/shop/notes/pulls":
		out := []map[string]string{}
		for _, u := range f.prs {
			out = append(out, map[string]string{"html_url": u})
		}
		writeJSON(w, out)
	case r.Method == http.MethodGet && path == "/repos/shop/notes/git/ref/heads/main":
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"}}`))
	case r.Method == http.MethodPost && path == "/repos/shop/notes/git/refs":
		f.branch = true
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && path == "/repos/shop/notes/contents/.github/workflows/apex-queue.yml":
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPost && path == "/repos/shop/notes/pulls":
		u := "https://github.example/shop/notes/pull/1"
		f.prs = append(f.prs, u)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url":"` + u + `"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no route ` + r.Method + " " + path + `"}`))
	}
}

// validJWT checks an App JWT's RS256 signature and its issuer, as GitHub does.
func (f *fakeGitHub) validJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(f.appKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	return json.Unmarshal(claims, &c) == nil && c.Iss == "42" && c.Exp > time.Now().Unix()
}

func appKey(t *testing.T) (key *rsa.PrivateKey, pemBytes []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func TestInstallationTokenIsNarrowed(t *testing.T) {
	key, pemBytes := appKey(t)
	loaded, err := LoadKey(pemBytes)
	require.NoError(t, err)
	f := newFake(t, &key.PublicKey)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	ctx := context.Background()

	jwt, err := AppJWT(42, loaded, time.Now())
	require.NoError(t, err)
	tok, err := c.InstallationToken(ctx, jwt, 7, "notes", Purposes["push"])
	require.NoError(t, err)
	require.Equal(t, "ghs_test", tok.Token)
	require.Len(t, f.tokenReqs, 1)
	require.Equal(t, []any{"notes"}, f.tokenReqs[0]["repositories"], "narrowed to one repository")
	perms, _ := f.tokenReqs[0]["permissions"].(map[string]any)
	require.Equal(t, "write", perms["contents"])
	for _, p := range Purposes {
		require.NotContains(t, p, "workflows", "G9: no member token carries workflows")
		require.NotContains(t, p, "administration")
	}

	other, _ := appKey(t)
	bad, err := AppJWT(42, other, time.Now())
	require.NoError(t, err)
	_, err = c.InstallationToken(ctx, bad, 7, "notes", Purposes["push"])
	require.ErrorContains(t, err, "HTTP 401", "the control: another key's JWT is refused")
}

func TestSetupActs(t *testing.T) {
	key, _ := appKey(t)
	f := newFake(t, &key.PublicKey)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	ctx := context.Background()
	const wf = ".github/workflows/apex-queue.yml"

	done, err := c.ApplyRulesets(ctx, "ghs_setup", "shop/notes", 99, wf)
	require.NoError(t, err)
	require.Len(t, f.rulesets, 4)
	require.Contains(t, done[0], "R4 without its required check", "no workflow on main yet: no PR could pass it")
	r1 := f.rulesets[RulesetR1]
	require.Len(t, r1.BypassActors, 1, "R1's bypass is the member App")
	require.InDelta(t, 99, r1.BypassActors[0]["actor_id"], 0)
	require.Empty(t, f.rulesets[RulesetR3].BypassActors, "D703: R3 has no bypass for 1a")
	require.Empty(t, f.rulesets[RulesetR4].BypassActors)
	require.NotContains(t, rulesJSON(f.rulesets[RulesetR4]), RequiredCheck)

	f.workflow = true
	done, err = c.ApplyRulesets(ctx, "ghs_setup", "shop/notes", 99, wf)
	require.NoError(t, err)
	require.Len(t, f.rulesets, 4, "updated by name, not duplicated")
	require.Contains(t, done[0], "updated ")
	require.Contains(t, rulesJSON(f.rulesets[RulesetR4]), RequiredCheck)
	require.Contains(t, rulesJSON(f.rulesets[RulesetR4]), `"merge_method":"MERGE"`)

	require.NoError(t, c.SetSecret(ctx, "ghs_setup", "shop/notes", "APE_NATS_CREDS", "-----BEGIN NATS USER JWT-----\nx\n"))
	require.Equal(t, "-----BEGIN NATS USER JWT-----\nx\n", f.secrets["APE_NATS_CREDS"], "sealed with the repository's key, opened by it")

	rt, err := c.RunnerToken(ctx, "ghs_setup", "shop/notes")
	require.NoError(t, err)
	require.Equal(t, "RUNNER_TOKEN", rt.Token)

	u, err := c.WorkflowPR(ctx, "ghs_setup", "shop/notes", wf, "name: apex-queue\n")
	require.NoError(t, err)
	require.Equal(t, "https://github.example/shop/notes/pull/1", u)
	require.True(t, f.branch)
	again, err := c.WorkflowPR(ctx, "ghs_setup", "shop/notes", wf, "name: apex-queue\n")
	require.NoError(t, err)
	require.Equal(t, u, again)
	require.Len(t, f.prs, 1, "an open workflow PR is reused")
}

func rulesJSON(r Ruleset) string {
	b, err := json.Marshal(r.Rules)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

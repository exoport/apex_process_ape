package ghapp

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// The key-holding service (PLAN-V15-GHAPP §3–§4): NATS request-reply,
// the caller the subject's last token (enforced by subject-scoped
// permissions on an operator-mode cluster; stated as not enforced on 1a's
// no-auth one).

// Subjects the service answers.
const (
	TokenSubject = "ape.sup.ghapp.pusher" //nolint:gosec // a NATS subject, not a credential: .<user>
	SetupSubject = "ape.sup.ghapp.warden" // .<user>
	queueGroup   = "ape-ghapp"
)

// WardenPermissions are what an apex-warden token carries for one act.
var WardenPermissions = map[string]string{
	"administration": "write", "secrets": "write", "contents": "write",
	"pull_requests": "write", "workflows": "write", "metadata": "read",
}

// AppConfig is one App.
type AppConfig struct {
	AppID int64 `yaml:"app_id"`
	// Key is the private key's path; $CREDENTIALS_DIRECTORY (systemd's
	// LoadCredential=) is expanded.
	Key string `yaml:"key"`
	// Installations maps <owner>/<repo> to the App's installation id there.
	Installations map[string]int64 `yaml:"installations"`
}

// RepoPolicy is who may ask for what on one repository.
type RepoPolicy struct {
	Members   []string `yaml:"members"`
	Operators []string `yaml:"operators"`
}

// Config is the service's config (/etc/apex-ghapp/config.yaml).
type Config struct {
	NATS struct {
		URL   string `yaml:"url"`
		Creds string `yaml:"creds"`
	} `yaml:"nats"`
	API string `yaml:"api"`
	// Warden is the set-up App (rulesets, secrets, the workflow PR, runner
	// tokens); Pusher the everyday one (pushes and PRs; R1's bypass actor).
	Warden AppConfig             `yaml:"warden"`
	Pusher AppConfig             `yaml:"pusher"`
	Repos  map[string]RepoPolicy `yaml:"repos"`
	// Log is the act log (append-only JSON lines, 0600).
	Log string `yaml:"log"`
	// Notify, when set, runs (sh -c) after the secrets and runner-token
	// acts, the act's record on stdin (research finding 6).
	Notify string `yaml:"notify"`
}

// LoadConfig reads and checks the config.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("ghapp: %s: %w", path, err)
	}
	if c.API == "" {
		c.API = "https://api.github.com"
	}
	if c.Pusher.AppID == 0 || c.Warden.AppID == 0 || len(c.Repos) == 0 || c.Log == "" {
		return nil, fmt.Errorf("ghapp: %s needs warden.app_id, pusher.app_id, repos and log", path)
	}
	return &c, nil
}

// Service answers token and set-up requests.
type Service struct {
	cfg       *Config
	gh        *Client
	wardenKey *rsa.PrivateKey
	pusherKey *rsa.PrivateKey
	now       func() time.Time

	mu      sync.Mutex
	cache   map[string]Token
	missing map[string][]string // the optional permissions each cached token lacks
	logMu   sync.Mutex
}

// NewService loads the two keys.
func NewService(cfg *Config) (*Service, error) {
	load := func(path string) (*rsa.PrivateKey, error) {
		data, err := os.ReadFile(os.ExpandEnv(path))
		if err != nil {
			return nil, fmt.Errorf("ghapp: read key: %w", err)
		}
		return LoadKey(data)
	}
	sk, err := load(cfg.Warden.Key)
	if err != nil {
		return nil, err
	}
	mk, err := load(cfg.Pusher.Key)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, gh: NewClient(cfg.API), wardenKey: sk, pusherKey: mk, now: time.Now, cache: map[string]Token{}, missing: map[string][]string{}}, nil
}

// Request is a caller's request; Reply the service's answer.
type Request struct {
	Repo    string `json:"repo"`
	Purpose string `json:"purpose,omitempty"`
	Act     string `json:"act,omitempty"`
	// Name and Value are the secrets act's (Value never logged).
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// Reply is the service's answer.
type Reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Token is a pusher token or a runner registration token.
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	Result    []string  `json:"result,omitempty"`
	// Missing are the optional permissions the token was minted without,
	// as "<key>: <level>", because the App has not got them.
	Missing []string `json:"missing,omitempty"`
}

// Serve subscribes the two subjects on nc until ctx ends.
func (s *Service) Serve(ctx context.Context, nc *nats.Conn) error {
	subs := make([]*nats.Subscription, 0, 2)
	for subj, h := range map[string]func(context.Context, string, Request) Reply{TokenSubject: s.token, SetupSubject: s.setup} {
		sub, err := nc.QueueSubscribe(subj+".*", queueGroup, func(m *nats.Msg) {
			user := m.Subject[strings.LastIndex(m.Subject, ".")+1:]
			var req Request
			rep := Reply{Error: "a request is JSON"}
			if json.Unmarshal(m.Data, &req) == nil {
				rctx, cancel := context.WithTimeout(ctx, time.Minute)
				rep = h(rctx, user, req)
				cancel()
			}
			data, _ := json.Marshal(rep)
			_ = m.Respond(data)
		})
		if err != nil {
			return err
		}
		subs = append(subs, sub)
	}
	<-ctx.Done()
	for _, sub := range subs {
		_ = sub.Unsubscribe()
	}
	return nil
}

func refuse(format string, a ...any) Reply { return Reply{Error: fmt.Sprintf(format, a...)} }

// token is a pusher token for (repo, purpose), to a member of the repo.
func (s *Service) token(ctx context.Context, user string, req Request) Reply {
	perms, ok := Purposes[req.Purpose]
	pol, known := s.cfg.Repos[req.Repo]
	inst := s.cfg.Pusher.Installations[req.Repo]
	switch {
	case !known || inst == 0:
		return s.logged(user, "pusher", req, refuse("repository %q is not served", req.Repo))
	case !slices.Contains(pol.Members, user):
		return s.logged(user, "pusher", req, refuse("%s is not a member of %s", user, req.Repo))
	case !ok:
		return s.logged(user, "pusher", req, refuse("purpose %q: push, pr or read", req.Purpose))
	}
	key := req.Repo + "|" + req.Purpose
	s.mu.Lock()
	t, cached := s.cache[key]
	missing := s.missing[key]
	s.mu.Unlock()
	if !cached || t.ExpiresAt.Before(s.now().Add(10*time.Minute)) {
		var err error
		if t, missing, err = s.mintPusher(ctx, inst, req.Repo, perms); err != nil {
			return s.logged(user, "pusher", req, refuse("%v", err))
		}
		s.mu.Lock()
		s.cache[key], s.missing[key] = t, missing
		s.mu.Unlock()
	}
	return s.logged(user, "pusher", req, Reply{OK: true, Token: t.Token, ExpiresAt: t.ExpiresAt, Missing: missing})
}

// mintPusher mints a pusher token for repo with perms. An App that lacks
// an optional permission (GitHub refuses the whole token then) gets one
// without it, and missing names it (FINDING-V15-1A-7 c).
func (s *Service) mintPusher(ctx context.Context, inst int64, repo string, perms map[string]string) (t Token, missing []string, err error) {
	jwt, err := AppJWT(s.cfg.Pusher.AppID, s.pusherKey, s.now())
	if err != nil {
		return Token{}, nil, fmt.Errorf("signing: %w", err)
	}
	_, name, _ := strings.Cut(repo, "/")
	t, err = s.gh.InstallationToken(ctx, jwt, inst, name, perms)
	if !notGranted(err) {
		return t, nil, err
	}
	fewer := maps.Clone(perms)
	for _, k := range OptionalPermissions {
		if lvl, ok := fewer[k]; ok {
			delete(fewer, k)
			missing = append(missing, k+": "+lvl)
		}
	}
	if len(missing) == 0 {
		return t, nil, err
	}
	t, err = s.gh.InstallationToken(ctx, jwt, inst, name, fewer)
	return t, missing, err
}

// notGranted is GitHub's refusal of a token asking for a permission the
// installation has not got (HTTP 422).
func notGranted(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(ae.Message), "not granted")
}

// setupToken is an apex-warden token for repo.
func (s *Service) setupToken(ctx context.Context, repo string) (string, error) {
	inst := s.cfg.Warden.Installations[repo]
	if inst == 0 {
		return "", fmt.Errorf("apex-warden is not installed on %s", repo)
	}
	jwt, err := AppJWT(s.cfg.Warden.AppID, s.wardenKey, s.now())
	if err != nil {
		return "", err
	}
	_, name, _ := strings.Cut(repo, "/")
	t, err := s.gh.InstallationToken(ctx, jwt, inst, name, WardenPermissions)
	return t.Token, err
}

// setup runs one templated set-up act, for an operator of the repo.
func (s *Service) setup(ctx context.Context, user string, req Request) Reply {
	pol, known := s.cfg.Repos[req.Repo]
	if !known {
		return s.logged(user, "warden", req, refuse("repository %q is not served", req.Repo))
	}
	if !slices.Contains(pol.Operators, user) {
		return s.logged(user, "warden", req, refuse("%s is not an operator of %s: set-up acts are the operator's", user, req.Repo))
	}
	tok, err := s.setupToken(ctx, req.Repo)
	if err != nil {
		return s.logged(user, "warden", req, refuse("%v", err))
	}
	var rep Reply
	switch req.Act {
	case "rulesets":
		done, err := s.gh.ApplyRulesets(ctx, tok, req.Repo, s.cfg.Pusher.AppID, apexv1.QueueWorkflowRel)
		rep = Reply{OK: err == nil, Result: done}
		if err != nil {
			rep.Error = err.Error()
		}
	case "workflow":
		u, err := s.gh.WorkflowPR(ctx, tok, req.Repo, apexv1.QueueWorkflowRel, apexv1.QueueWorkflow)
		rep = Reply{OK: err == nil, Result: []string{u}}
		if err != nil {
			rep.Error = err.Error()
		}
	case "secrets":
		if !SecretNames[req.Name] || req.Value == "" {
			return s.logged(user, "warden", req, refuse("secret %q: only APE_NATS_URL and APE_NATS_CREDS, with a value", req.Name))
		}
		err := s.gh.SetSecret(ctx, tok, req.Repo, req.Name, req.Value)
		rep = Reply{OK: err == nil, Result: []string{"set " + req.Name}}
		if err != nil {
			rep.Error = err.Error()
		}
		s.notify(ctx, user, req, rep)
	case "runner-token":
		t, err := s.gh.RunnerToken(ctx, tok, req.Repo)
		rep = Reply{OK: err == nil, Token: t.Token, ExpiresAt: t.ExpiresAt}
		if err != nil {
			rep.Error = err.Error()
		}
		s.notify(ctx, user, req, rep)
	default:
		return s.logged(user, "warden", req, refuse("act %q: rulesets, workflow, secrets or runner-token", req.Act))
	}
	return s.logged(user, "warden", req, rep)
}

// actRecord is one act log line: never a token or a secret's value.
type actRecord struct {
	At        string `json:"at"`
	User      string `json:"user"`
	Kind      string `json:"kind"`
	Repo      string `json:"repo"`
	Purpose   string `json:"purpose,omitempty"`
	Act       string `json:"act,omitempty"`
	Name      string `json:"name,omitempty"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

func (s *Service) record(user, kind string, req Request, rep Reply) actRecord {
	r := actRecord{At: s.now().UTC().Format(time.RFC3339), User: user, Kind: kind, Repo: req.Repo, Purpose: req.Purpose, Act: req.Act, Name: req.Name, OK: rep.OK, Error: rep.Error}
	if !rep.ExpiresAt.IsZero() {
		r.ExpiresAt = rep.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return r
}

// logged appends the act's record to the log and returns rep.
func (s *Service) logged(user, kind string, req Request, rep Reply) Reply {
	data, err := json.Marshal(s.record(user, kind, req, rep))
	if err != nil {
		return rep
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	f, err := os.OpenFile(s.cfg.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(data, '\n'))
		_ = f.Close()
	}
	return rep
}

// notify runs the operator's notify with the act's record on stdin.
func (s *Service) notify(ctx context.Context, user string, req Request, rep Reply) {
	if s.cfg.Notify == "" {
		return
	}
	data, err := json.Marshal(s.record(user, "warden", req, rep))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", s.cfg.Notify) //nolint:gosec // the operator's own command, from the root-owned config
	c.Stdin = strings.NewReader(string(data) + "\n")
	_ = c.Run()
}

// notifyTimeout bounds the operator's notify command.
const notifyTimeout = 30 * time.Second

// Ask sends req to the service as user (ape's side) and returns its reply.
func Ask(ctx context.Context, nc *nats.Conn, subject, user string, req Request) (Reply, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}
	m, err := nc.RequestWithContext(ctx, subject+"."+user, data)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return Reply{}, errors.New("ghapp: no App service answers on " + subject + " — is apex-ghapp running on the coordination host?")
		}
		return Reply{}, err
	}
	var rep Reply
	if err := json.Unmarshal(m.Data, &rep); err != nil {
		return Reply{}, err
	}
	if !rep.OK {
		return rep, errors.New("ghapp: " + rep.Error)
	}
	return rep, nil
}

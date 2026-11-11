package member

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
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
// asked over NATS as this member's user — else hot/github.token. Inside a
// gated verb's run (WithRunWait) NATS unreachable, or no App service
// answering, is waited out (V8L-1, D808+): the push, fetch and PR calls
// after a fence ask for their token here. A refusal the service answers
// with is returned at once.
func (m Membership) GitHubTokenFor(ctx context.Context, purpose string) (string, error) {
	if !m.GitHubApp {
		tok, err := m.ReadGitHubToken()
		return tok, err
	}
	id, err := natsconn.DecodeIdentity(m.Creds)
	if err != nil {
		return "", err
	}
	var tok string
	err = waitTransient(ctx, "the "+purpose+" token from the App service", func() (bool, error) {
		actx, cancel := context.WithTimeout(ctx, askTimeout)
		defer cancel()
		// A request the server refuses by permission is never answered:
		// the refusal comes back on the connection's error handler, and
		// is told from an unanswered one by it (V8LM2-1, V12).
		denied := make(chan error, 1)
		nc, err := natsconn.Connect(actx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, "ape-ghapp-client", nats.NoReconnect(),
			nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
				if errors.Is(err, nats.ErrPermissionViolation) {
					select {
					case denied <- err:
						cancel()
					default:
					}
				}
			}))
		if err != nil {
			return !natsconn.AuthRefused(err), err
		}
		defer nc.Close()
		rep, err := ghapp.Ask(actx, nc, ghapp.TokenSubject, id.SubjectToken, ghapp.Request{Repo: m.GitHubRepo, Purpose: purpose})
		select {
		case perr := <-denied:
			return false, fmt.Errorf("ghapp: this credential may not ask the App service for a token (%w)", perr)
		default:
		}
		if err != nil {
			return askUnanswered(err), err
		}
		tok = rep.Token
		return false, nil
	})
	return tok, err
}

// askUnanswered is an App service request nobody answered: no responders,
// or no reply in time. A reply that refuses is not.
func askUnanswered(err error) bool {
	return errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, nats.ErrConnectionClosed)
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
//
// An App project never falls back to the remote (FINDING-V15-1A-6): a
// person's git config may rewrite it to ssh with their key, and with an
// agent the call would run as the person, silently. A membership that
// cannot be used is coord.membership, a token the service did not give
// coord.nats-unreachable; both say the route.
func GitVia(ctx context.Context, p *apexv1.Project, purpose string) (url string, env []string, err error) {
	m, err := Load(p)
	if err != nil {
		if !appConfigured(p) {
			return "", nil, nil // not an App project: git's own credentials
		}
		if fe, ok := errors.AsType[*apexv1.FailError](err); ok {
			return "", nil, fe // coord.integration: the block makes no mode
		}
		return "", nil, &apexv1.FailError{Code: apexv1.CodeCoordMembership, Action: apexv1.ActionPerson, Msg: fmt.Sprintf(
			"this project reaches GitHub only through the App service (coordination.github.app: true), and its membership cannot be used: %v"+
				" — ape does not fall back to the remote with git's own credentials (they may be a person's); a person places the ensemble credential at %s (mode 0600, in the ignored hot/) or fixes the coordination: block, then runs the command again",
			err, apexv1.NATSCredsRel)}
	}
	if !m.GitHubApp {
		return "", nil, nil
	}
	tok, err := m.GitHubTokenFor(ctx, purpose)
	if err != nil {
		return "", nil, &apexv1.FailError{Code: apexv1.CodeCoordNATSUnreachable, Action: apexv1.ActionRetry, Msg: fmt.Sprintf(
			"no %s token from the coordination host's App service (%s): %v"+
				" — ape does not fall back to the remote with git's own credentials (they may be a person's); check that the App service runs (`ape ghapp serve`, the apex-ghapp unit) and NATS answers at %s, then run the command again",
			purpose, m.GitHubRepo, err, m.URL)}
	}
	return gitURL(m.GitHubAPI, m.GitHubRepo), pushEnv(tok), nil
}

// appConfigured reports a project whose config says its GitHub access is
// the App service's, read without the membership (whose credential may be
// what is missing).
func appConfigured(p *apexv1.Project) bool {
	cfg, err := apexv1.LoadConfig(p.Root)
	return err == nil && cfg.Coordination != nil && cfg.Coordination.GitHub != nil && cfg.Coordination.GitHub.App
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
// only. The person's global and system git config are not read for it
// (FINDING-V15-1A-6): an url.*.insteadOf there could send the call to ssh
// with their key; a rule in the checkout's own config is refused by
// apexv1's gitTarget, which checks the URL git would use.
func pushEnv(tok string) []string {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=",
	}
}

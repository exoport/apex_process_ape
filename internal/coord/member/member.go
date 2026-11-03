// Package member is a project's membership in an ensemble (APEX v1.5,
// stage 0, step 9; proposal §4.5, §4.6, OQ3–OQ5): the coordination: block
// of _apex/config.yaml, the credential in hot/, the rule that NATS is
// required to start and to integrate, and the status line of an outage.
package member

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/conductor"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

var (
	// ErrNotMember: the project declares no coordination: block; it
	// coordinates with nobody, and nothing here applies.
	ErrNotMember = errors.New("member: the project declares no coordination: block")
	// ErrIncomplete: the block lacks a field it needs.
	ErrIncomplete = errors.New("member: the coordination: block is incomplete")
	// ErrNameClash: the ensemble's token equals a member project's (R8).
	ErrNameClash = errors.New("member: the ensemble's name is a member project's")
	// ErrReservedEnsemble: the ensemble's name ends in -assessor, the
	// scope of another ensemble's assessor records (step 8): its
	// conductors would be delivered them.
	ErrReservedEnsemble = errors.New("member: an ensemble's name may not end in -assessor")
	// ErrNoCreds: hot/ape-nats.creds is missing.
	ErrNoCreds = errors.New("member: no NATS credential at " + apexv1.NATSCredsRel)
	// ErrGitHub: coordination.github names no <owner>/<name> repository,
	// or hot/github.token is missing or empty (stage 1a).
	ErrGitHub = errors.New("member: coordination.github cannot be used")
)

// AssessorScopeSuffix ends the scope token of an ensemble's assessor
// records (coordinator's recordScope).
const AssessorScopeSuffix = "-assessor"

// DefaultGitHubAPI is coordination.github.api when unset.
const DefaultGitHubAPI = "https://api.github.com"

// Membership is a resolved coordination: block.
type Membership struct {
	// Ensemble and Project are subject tokens (SubjectToken, R8).
	Ensemble, Project string
	Home, URL         string
	// Creds is the credential's absolute path.
	Creds string
	// GitHubRepo (<owner>/<name>) and GitHubAPI are the coordinated
	// repository (stage 1a, section 5), "" when the block names none: the
	// project then integrates by local trunk. GitHubToken is the token's
	// absolute path.
	GitHubRepo, GitHubAPI, GitHubToken string
	// GitHubApp: tokens come from the coordination host's GitHub App
	// service over NATS (PLAN-V15-GHAPP); GitHubToken is then unused.
	GitHubApp bool
}

// Load resolves the project's membership. A block that names its own
// project as the ensemble is refused (R8); the credential must exist.
func Load(p *apexv1.Project) (Membership, error) {
	cfg, err := apexv1.LoadConfig(p.Root)
	if err != nil {
		return Membership{}, err
	}
	c := cfg.Coordination
	if c == nil {
		return Membership{}, ErrNotMember
	}
	var missing []string
	for _, f := range []struct{ name, v string }{{"ensemble", c.Ensemble}, {"home", c.Home}, {"url", c.URL}} {
		if strings.TrimSpace(f.v) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return Membership{}, fmt.Errorf("%w: no %s", ErrIncomplete, strings.Join(missing, ", "))
	}
	name := cfg.ProjectName
	if name == "" {
		name = filepath.Base(p.Root)
	}
	m := Membership{
		Ensemble: natsconn.SubjectToken(c.Ensemble),
		Project:  natsconn.SubjectToken(name),
		Home:     c.Home,
		URL:      c.URL,
		Creds:    filepath.Join(p.Root, filepath.FromSlash(apexv1.NATSCredsRel)),
	}
	if err := CheckEnsemble(m.Ensemble, []string{m.Project}); err != nil {
		return Membership{}, err
	}
	if _, err := os.Stat(m.Creds); err != nil {
		return Membership{}, fmt.Errorf("%w: %w", ErrNoCreds, err)
	}
	if g := c.GitHub; g != nil {
		owner, name, ok := strings.Cut(strings.TrimSpace(g.Repo), "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return Membership{}, fmt.Errorf("%w: repo %q is not <owner>/<name>", ErrGitHub, g.Repo)
		}
		m.GitHubRepo, m.GitHubAPI = owner+"/"+name, strings.TrimRight(strings.TrimSpace(g.API), "/")
		if m.GitHubAPI == "" {
			m.GitHubAPI = DefaultGitHubAPI
		}
		m.GitHubToken = filepath.Join(p.Root, filepath.FromSlash(apexv1.GitHubTokenRel))
		if m.GitHubApp = g.App; m.GitHubApp {
			return m, nil
		}
		if st, err := os.Stat(m.GitHubToken); err != nil || st.Size() == 0 {
			return Membership{}, fmt.Errorf("%w: no token at %s (a fine-grained token for %s, placed by a person)", ErrGitHub, apexv1.GitHubTokenRel, m.GitHubRepo)
		}
	}
	return m, nil
}

// ReadGitHubToken reads the coordinated repository's token, for ape's own
// calls only: it is never put in an environment or a log.
func (m Membership) ReadGitHubToken() (string, error) {
	if m.GitHubToken == "" {
		return "", fmt.Errorf("%w: the block names no github repository", ErrGitHub)
	}
	data, err := os.ReadFile(m.GitHubToken)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrGitHub, err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("%w: %s is empty", ErrGitHub, apexv1.GitHubTokenRel)
	}
	return tok, nil
}

// CheckEnsemble refuses an ensemble whose token equals a member project's
// token (R8): `ape.<kind>.<user>.<x>` would not say which it names. It
// refuses one ending in -assessor too: ape.evt.coord.<ens>-assessor.> is
// ensemble <ens>'s assessor records, which an ensemble of that name's
// conductors (ape.evt.coord.<name>.>) would be delivered.
func CheckEnsemble(ens string, members []string) error {
	ens = natsconn.SubjectToken(ens)
	if strings.HasSuffix(ens, AssessorScopeSuffix) {
		return fmt.Errorf("%w: %q — rename the ensemble in coordination.ensemble", ErrReservedEnsemble, ens)
	}
	for _, p := range members {
		if natsconn.SubjectToken(p) == ens {
			return fmt.Errorf("%w: %q — rename the ensemble in coordination.ensemble", ErrNameClash, ens)
		}
	}
	return nil
}

// pauseCheckTimeout bounds the read of a milestone's pause events.
const pauseCheckTimeout = 2 * time.Second

// Gate is the rule of OQ5 for one start or integration step (op): nil for
// a project in no ensemble; coord.membership for a block that cannot be
// used; coord.nats-unreachable when NATS cannot be reached with the
// project's credential. With a milestone, a milestone the conductor's ape
// paused (OQ24: a question left unanswered) refuses too, with
// coord.milestone-paused: only that milestone; and one whose lease this
// checkout does not hold refuses with session.not-lease-holder.
func Gate(ctx context.Context, p *apexv1.Project, op, milestone string, now time.Time) error {
	m, err := Load(p)
	if errors.Is(err, ErrNotMember) {
		return nil
	}
	if err != nil {
		return &apexv1.FailError{Code: apexv1.CodeCoordMembership, Msg: err.Error() +
			" — a person fixes _apex/config.yaml's coordination: block or places the ensemble credential"}
	}
	var nc *nats.Conn
	if err := Require(p.Root, op, func() error {
		var err error
		nc, err = natsconn.Connect(ctx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, "ape-member", nats.NoReconnect())
		return err
	}, now); err != nil {
		return err
	}
	defer nc.Close()
	if milestone == "" || milestone == "none" {
		return nil
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil // no stream to read: nothing is paused
	}
	pctx, cancel := context.WithTimeout(ctx, pauseCheckTimeout)
	defer cancel()
	if paused, why, err := conductor.PausedWith(pctx, js, m.Project, milestone); err == nil && paused {
		if why.Kind == conductor.PausePerson {
			return &apexv1.FailError{Code: apexv1.CodeCoordMilestonePaused, Action: apexv1.ActionPerson, Msg: fmt.Sprintf(
				"%s is paused by %s since %s: %s — so %s waits; `ape milestone resume %s`, from the checkout holding its claim, resumes it",
				milestone, why.By, why.At, why.Reason, op, milestone)}
		}
		return &apexv1.FailError{Code: apexv1.CodeCoordMilestonePaused, Action: apexv1.ActionPerson, Msg: fmt.Sprintf(
			"%s is paused: a question to the conductor went unanswered (OQ24), so %s waits — the conductor's answer resumes it, or `ape milestone resume %s` from the checkout holding its claim", milestone, op, milestone)}
	}
	// Stage 1a: a change of a milestone is this checkout's to start only
	// while it holds the milestone's lease (section 3.1).
	return requireMilestoneLease(ctx, p, op, milestone)
}

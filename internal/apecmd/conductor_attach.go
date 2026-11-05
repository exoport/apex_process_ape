package apecmd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/conductorwatch"
	"github.com/exoport/apex_process_ape/internal/coord/conductor"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

func newConductorAttachCmd() *cobra.Command {
	var projects []string
	var pane, user string
	var gitUser bool // user is git user.email's slug, not --user
	cmd := &cobra.Command{
		Use:   "attach --project <dir>… [--pane <tmux pane>] [--user <slug>]",
		Short: "APEX v1.5 (in development): deliver an ensemble's events into your conductor's tmux pane",
		Long: `Attach your conductor — your own Claude Code session, in a tmux pane — to the ensemble its
--project directories belong to (v1.5, OQ25), and run until interrupted. ape claims the
conductor lease conductor.<user>.<ensemble>, <user> being the credential's (exit 1 if another of your conductors holds
it), binds your durable consumer, and types each event — the coordinator's, the
ensemble's contracts, each member project's — into the pane, marked as ape's
(<ape-delivery …>: a prompt carrying one grants no person act) and ending with the
` + "`ape conductor ack <token>`" + ` that acknowledges it.

It types and submits by the same rules as ` + "`ape conductor watcher`" + ` (Enter is pressed
again, up to three times, until the input row empties or a turn starts): the pane runs the Claude Code
session pinned at the first look (another session — after a /clear — pauses it until
` + "`ape conductor watcher --repin`" + ` from that session), no turn running, an empty input row,
and a steady screen. An event not acknowledged is delivered again; a question left past
30 minutes is escalated and past 2 hours its milestone pauses; a conductor that
acknowledges nothing stops renewing its lease, which another of your conductors may
then take over, unacknowledged events included. A NATS outage is ridden out: the lease
is renewed again once NATS answers (claimed again if it lapsed meanwhile and nobody else
took it), and deliveries resume; only another of your conductors holding the ensemble
ends it.

A person's act, from a terminal: refused in a model session (APE_SESSION or CLAUDECODE set), and
the pre-tool hook denies it in any form — the conductor session it delivers into never
starts it (V8i).

The projects must belong to one ensemble (two checkouts of one project count once); the NATS URL and the credential
(hot/ape-nats.creds) are the first project's.

Exit codes: 0 stopped (Ctrl-C) · 1 the lease is held by another of your conductors, or
was lost · 2 usage (no pane, no member project, projects of different ensembles), or a model
session.`,
		Example: "  ape conductor attach --project ~/src/notes --project ~/src/billing --pane %3",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := apexv1.RequirePersonFlag(apexv1.PersonConductorAttach); err != nil {
				return usageErr(err)
			}
			if len(projects) == 0 {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projects = []string{wd}
			}
			var first member.Membership
			var tokens []string
			for i, d := range projects {
				abs, err := filepath.Abs(d)
				if err != nil {
					return err
				}
				p, err := apexv1.Open(apexv1.FindRoot(abs))
				if err != nil {
					return usageErr(err)
				}
				m, err := member.Load(p)
				if err != nil {
					return usageErr(fmt.Errorf("%s: %w", d, err))
				}
				if i == 0 {
					first = m
					if user == "" {
						user = apexv1.PersonSlug(apexv1.GitUserEmail(cmd.Context(), p.Root))
						gitUser = true
					}
				} else if m.Ensemble != first.Ensemble {
					return usageErr(fmt.Errorf("%s is in ensemble %s, %s in %s: one conductor attaches to one ensemble", d, m.Ensemble, projects[0], first.Ensemble))
				}
				if !slices.Contains(tokens, m.Project) {
					tokens = append(tokens, m.Project) // two checkouts of one project are one project
				}
			}
			// The conductor's name is its credential's (FINDING-2A-DRY-1): the
			// grant scopes its lease, consumer and acks to the credential's
			// user, so any other name is refused by an enforcing cluster —
			// after a 30 s wait, as it was. A credential that names nobody
			// keeps the git user.email slug.
			if id, err := natsconn.DecodeIdentity(first.Creds); err == nil && id.Name != "" {
				switch {
				case gitUser:
					user = id.SubjectToken
				case natsconn.SubjectToken(user) != id.SubjectToken:
					return usageErr(fmt.Errorf("--user %s: the credential %s is %s's, and a conductor runs as its credential's user (its lease, consumer and acks are scoped to %s): drop --user, or attach with %s's credential",
						user, first.Creds, id.SubjectToken, id.SubjectToken, user))
				}
			}
			if strings.TrimSpace(user) == "" {
				return usageErr(errors.New("--user: no git user.email to name you by"))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if pane == "" {
				pane = conductorwatch.DiscoverPane(ctx)
			}
			if pane == "" {
				return usageErr(errors.New("--pane: name your conductor's tmux pane (there is not exactly one pane running claude)"))
			}
			nc, err := natsconn.Connect(ctx, natsconn.Config{URL: first.URL, CredsFile: first.Creds}, "ape-conductor-attach")
			if err != nil {
				return failErr(err)
			}
			defer nc.Close()
			host, _ := os.Hostname()
			logf := func(format string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), format, a...) }
			fmt.Fprintf(cmd.ErrOrStderr(), "attach: %s's conductor for ensemble %s (%s), delivering into tmux pane %s\n", user, first.Ensemble, strings.Join(tokens, ", "), pane)
			err = conductor.Run(ctx, nc, conductor.Config{
				User: user, Ensemble: first.Ensemble, Projects: tokens,
				Holder: host + "-" + strconv.Itoa(os.Getpid()), Nonce: uuid.NewString()[:8], Log: logf,
				Session: &conductorwatch.PaneSession{Ctx: ctx, Gate: &conductorwatch.Gate{
					Pane: &conductorwatch.Tmux{Target: pane}, StateDir: conductorwatch.StateDir(), Log: logf,
				}},
			})
			if errors.Is(err, conductor.ErrTaken) || errors.Is(err, conductor.ErrLost) {
				return failErr(err)
			}
			return err
		},
	}
	cmd.Flags().StringArrayVar(&projects, "project", nil, "a member project of the ensemble (repeatable; default the current directory's)")
	cmd.Flags().StringVar(&pane, "pane", "", "your conductor's tmux pane (-t target); default the one pane running claude")
	cmd.Flags().StringVar(&user, "user", "", "your conductor's name in the lease and consumer (default: the credential's user, else the slug of git user.email); one that is not the credential's is refused")
	return cmd
}

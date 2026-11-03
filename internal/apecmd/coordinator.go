package apecmd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/usage"
)

func newCoordinatorCmd() *cobra.Command {
	var f coordFlags
	var ensemble, id string
	var projects, assessFrom []string
	var assess bool
	var assessBudget string
	cmd := &cobra.Command{
		Use:   "coordinator --ensemble <name> [--id <id>] [--project <slug>]...",
		Short: "APEX v1.5 (in development): run an ensemble's coordinator until stopped",
		Long: `Run the coordinator of one ensemble (stage 0; proposal §3.1): it claims the lease
coord.<ensemble> (exit 1 if another coordinator holds it), serves slot requests on
ape.coord.<ensemble>.<verb> as a NATS-micro service, records slots it granted that
expired (slot-reclaimed), and publishes lease-expired for the ensemble's leases,
those of the --project members' milestones included. The supervisor starts it; a
person may too. With --assess it also runs the assessor: the members' plan-approved,
change-archived and gate-red triggers are batched (60 s quiet, 5 min ceiling), each batch
is assessed by ` + "`ape task apex-assess`" + ` in a batch folder under
$XDG_STATE_HOME/ape/coord/<ensemble>/assess (else ~/.local/state/…), and what its
notice.json says is checked and published as impact-notice / hold-requested, or the
batch as assessor-failed; each batch's record is kept there under kept/. SIGINT/SIGTERM
releases the lease and exits 0.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			nc, m, err := f.connect(ctx, "ape-coordinator")
			if err != nil {
				return err
			}
			defer nc.Close()
			if ensemble == "" && m != nil {
				ensemble = m.Ensemble
			}
			if ensemble == "" {
				return usageErr(errors.New("--ensemble is required outside a project with a coordination: block"))
			}
			if id == "" {
				id = defaultCoordID("coord")
			}
			cfg := coordinator.Config{Ensemble: ensemble, ID: id, Projects: projects}
			if assess {
				if cfg.Assessor, err = f.assessorConfig(cmd, ensemble, assessFrom, assessBudget); err != nil {
					return err
				}
			}
			return coordinator.Run(ctx, nc, cfg)
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&ensemble, "ensemble", "", "the ensemble (default: the project's)")
	cmd.Flags().StringVar(&id, "id", "", "this coordinator's id, the lease holder (default: <host>-<pid>)")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "a member project whose milestone leases it watches (repeatable)")
	cmd.Flags().BoolVar(&assess, "assess", false, "run the assessor (v1.5 §3.2): batch the --project members' triggers and run apex-assess on each batch")
	cmd.Flags().StringVar(&assessBudget, "assess-budget", apexv1.FormatTokens(assessBudgetDefault), "the assessor's tokens for this stage (tokens_total, kept in the state dir's spent.json): once spent, each batch is skipped (assessor-skipped); 0 skips every batch, off sets no budget")
	cmd.Flags().StringArrayVar(&assessFrom, "assess-checkout", nil, "<project>=<path>: the checkout of a member the assessor reads it from, and runs its installed apex-assess (repeatable; default: the project of --cwd)")
	return cmd
}

// assessorConfig is --assess's: the checkouts, the state dir and the
// runner, with the production batching window (Q2).
func (f *coordFlags) assessorConfig(cmd *cobra.Command, ens string, specs []string, budgetFlag string) (*coordinator.AssessorConfig, error) {
	checkouts, err := assessCheckouts(specs)
	if err != nil {
		return nil, usageErr(err)
	}
	budget, err := assessBudget(budgetFlag)
	if err != nil {
		return nil, usageErr(err)
	}
	if len(checkouts) == 0 {
		p, err := f.project()
		if err != nil {
			return nil, err
		}
		m, err := member.Load(p)
		if err != nil {
			return nil, usageErr(fmt.Errorf("--assess needs --assess-checkout <project>=<path>, or a member project in --cwd: %w", err))
		}
		checkouts[natsconn.SubjectToken(m.Project)] = p.Root
	}
	dir, err := assessStateDir(ens)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	r := &assessRunner{dir: dir, checkouts: checkouts, session: taskSession(self), budget: budget, usageSkip: assessUsageSkip, logf: func(format string, args ...any) {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
	}}
	return &coordinator.AssessorConfig{Quiet: assessQuiet, Ceiling: assessCeiling, OnBatch: r.onBatch}, nil
}

// The assessor's batching window (Q2, D756).
const (
	assessQuiet   = time.Minute
	assessCeiling = 5 * time.Minute
)

// assessUsageSkip is the usage guard's word on an assessor launch from
// checkout, without its hold: a stop or a hold skips the batch (D2).
func assessUsageSkip(checkout string) (string, bool) {
	p, err := apexv1.Open(checkout)
	if err != nil {
		return "", false
	}
	g := newUsageGuard(p, apexv1.AssessSkill, "", false)
	d := usage.Decide(g.cfg, usage.Load(), g.share(), usageNow(), false)
	if d.Outcome == usage.Stop || d.Outcome == usage.Hold {
		return "usage guard: " + d.Why, true
	}
	return "", false
}

// assessBudget is --assess-budget: tokens, 0 (every batch skipped), or
// off (-1: no budget).
func assessBudget(v string) (int64, error) {
	switch v {
	case "off":
		return -1, nil
	case "0":
		return 0, nil
	}
	n, err := apexv1.ParseTokens(v)
	if err != nil {
		return 0, fmt.Errorf("--assess-budget: %w (tokens such as 20M, 0, or off)", err)
	}
	return n, nil
}

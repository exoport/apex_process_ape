package apecmd

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
)

func newCoordinatorCmd() *cobra.Command {
	var f coordFlags
	var ensemble, id string
	var projects []string
	cmd := &cobra.Command{
		Use:   "coordinator --ensemble <name> [--id <id>] [--project <slug>]...",
		Short: "APEX v1.5 (in development): run an ensemble's coordinator until stopped",
		Long: `Run the coordinator of one ensemble (stage 0; proposal §3.1): it claims the lease
coord.<ensemble> (exit 1 if another coordinator holds it), serves slot requests on
ape.coord.<ensemble>.<verb> as a NATS-micro service, records slots it granted that
expired (slot-reclaimed), and publishes lease-expired for the ensemble's leases,
those of the --project members' milestones included. The supervisor starts it; a
person may too. SIGINT/SIGTERM releases the lease and exits 0.`,
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
			return coordinator.Run(ctx, nc, coordinator.Config{Ensemble: ensemble, ID: id, Projects: projects})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&ensemble, "ensemble", "", "the ensemble (default: the project's)")
	cmd.Flags().StringVar(&id, "id", "", "this coordinator's id, the lease holder (default: <host>-<pid>)")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "a member project whose milestone leases it watches (repeatable)")
	return cmd
}

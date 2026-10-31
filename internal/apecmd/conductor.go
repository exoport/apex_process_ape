package apecmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/coord/conductor"
)

func newConductorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conductor",
		Short: "APEX v1.5 (in development): the conductor's deliveries",
	}
	cmd.AddCommand(newConductorAckCmd())
	return cmd
}

func newConductorAckCmd() *cobra.Command {
	var f coordFlags
	cmd := &cobra.Command{
		Use:   "ack <token>",
		Short: "Acknowledge one delivered event, by the token its delivery names",
		Long: `Acknowledge an event the conductor's ape typed into the session (proposal §3.1, OQ22).
Each delivery ends "When you have handled this event, run: ape conductor ack <token>";
the token is the event's JetStream ack subject. An unacknowledged event is delivered
again; acknowledgements are what keep the conductor's lease renewed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nc, _, err := f.connect(cmd.Context(), "ape-conductor-ack")
			if err != nil {
				return err
			}
			defer nc.Close()
			if err := conductor.Ack(nc, args[0]); err != nil {
				return failErr(err)
			}
			if err := nc.Flush(); err != nil {
				return failErr(err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "acknowledged")
			return nil
		},
	}
	f.add(cmd)
	return cmd
}

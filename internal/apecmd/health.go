package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/supervisor"
)

func newHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "APEX v1.5 (in development): report a coordinator that did not answer",
	}
	cmd.AddCommand(newHealthReportCmd())
	return cmd
}

func newHealthReportCmd() *cobra.Command {
	var f coordFlags
	var ensemble, subject, what string
	cmd := &cobra.Command{
		Use:   "report [--ensemble <name>] --subject <subject> --what <text>",
		Short: "Tell the supervisor a request to the coordinator timed out, and print its verdict",
		Long: `Send ape.sup.health.report (proposal §3.3): this ape's request on --subject timed out.
The supervisor verifies before it acts and answers one of: replaced (the coordinator was
stale and is restarted), unconfirmed (the coordinator looks healthy: suspect this ape's
own link), gave-up (stale, but the restart budget is spent; a person decides). Exit 0 on
any verdict, 1 when no supervisor answered.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			nc, m, err := f.connect(ctx, "ape-health-report")
			if err != nil {
				return err
			}
			defer nc.Close()
			if ensemble == "" && m != nil {
				ensemble = m.Ensemble
			}
			if ensemble == "" || subject == "" {
				return usageErr(errors.New("--ensemble (outside a member project) and --subject are required"))
			}
			body, err := json.Marshal(supervisor.HealthReport{Ensemble: ensemble, Reporter: defaultCoordID("ape"), Subject: subject, SentAt: time.Now().UTC(), What: what})
			if err != nil {
				return err
			}
			msg, err := nc.Request("ape.sup.health.report", body, 3*layout.HealthDeadline)
			if err != nil {
				return failErr(fmt.Errorf("no supervisor answered ape.sup.health.report: %w", err))
			}
			var v supervisor.Verdict
			if err := json.Unmarshal(msg.Data, &v); err != nil {
				return failErr(fmt.Errorf("the supervisor's answer is not a verdict: %w", err))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", v.Action, v.Message)
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&ensemble, "ensemble", "", "the ensemble (default: the project's)")
	cmd.Flags().StringVar(&subject, "subject", "", "the subject whose request timed out")
	cmd.Flags().StringVar(&what, "what", "", "what this ape was doing")
	return cmd
}

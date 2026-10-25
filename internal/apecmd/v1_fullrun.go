package apecmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// newMilestoneFullCmd is `ape milestone full <M>` (v1.4, D508, D563): the
// milestone's full run, on demand.
func newMilestoneFullCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "full <M>",
		Short: "Run the milestone's full checks and regression command, and record them for the gate",
		Long: `Run the milestone's full run on demand: every ` + "`check (full):`" + ` scenario of the spec and the
milestone's regression command (its milestone tier), judged as ` + "`ape milestone gate`" + ` judges them,
and record it in _apex/state/logs/full-<M>.json. It records no gate result and commits
nothing.

The gate runs the same full run itself once its Gate: command is green (a red Gate:
command is red already, and the full checks are skipped), and records it the same way. A
full run is current while HEAD, the working tree (ape's state aside), the milestone's
approval, its full checks and the regression command are what they were when it ended; a
gate run again, and ` + "`ape milestone close`" + `, take a current one instead of running them again
(JSON run.full_run_reused: when it ran). ` + "`ape milestone gate <M> --rerun`" + ` runs them anyway.

Same refusals as the gate (a change branch, a done milestone, an approval ape holds no
record of, or a gate changed since it). Timeouts: each scenario under timeouts.check, the
regression command under timeouts.regression. A long run (the milestone's lock: run.busy
beside its gate or close): --wait, exit 75, attach by running it again.

JSON: {"milestone", "at", "sha", "tree", "results": [<check result>], "regression",
"verdict", "secs", "by": "full"}.

Exit codes: 0 green · 1 a full check red or the regression command newly red
(milestone.full-red), or refused · 75 still running.`,
		Example: "  ape milestone full M2\n  ape milestone full M2 --json --wait 9m",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			id := args[0]
			return runLong(cmd, &f, p, id, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				fr, err := p.RunFull(ctx, id, opt)
				return func(cmd *cobra.Command, f *v1Flags) error {
					if err != nil {
						return v1Fail(cmd, f, err, fr)
					}
					return v1Emit(cmd.OutOrStdout(), f, fr, func(w io.Writer) {
						fmt.Fprintf(w, "%s full run GREEN in %.1fs at %.7s: %d full check(s)", id, fr.Secs, fr.SHA, len(fr.Results))
						if fr.Verdict != nil {
							fmt.Fprintf(w, " + the regression command (%s)", fr.Verdict.Cmd)
						}
						fmt.Fprintln(w, " — recorded; a gate at this tree takes it")
					})
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	addV1TimeoutFlag(cmd, &f)
	addV1WaitFlags(cmd, &f)
	return cmd
}

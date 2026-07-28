package apecmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
)

// newCostsRepriceCmd implements `ape costs reprice` — retroactive cost
// correction from tokens already on disk.
//
// When the price table goes stale, token counts stay correct and only the
// dollars are wrong. Every run artefact persists its full per-model token
// breakdown (PLAN-10 D1: input, output, cache-read, and the 5m/1h
// cache-creation split), so once the table is fixed the affected runs can
// be recomputed exactly instead of being written off. That turns a pricing
// gap from "we permanently lost N days of cost data" into "we lost N days
// until someone ran one command".
func newCostsRepriceCmd() *cobra.Command {
	var (
		outputFormat string
		write        bool
	)
	cmd := &cobra.Command{
		Use:   "reprice",
		Short: "Recompute stored costs from on-disk token counts using the current price table",
		Long: `Walk this project's run artefacts and recompute every cost_usd from the
per-model token counts stored alongside it.

Use this after correcting the price table (a new model id added to
internal/cost/prices.yaml, or an override persisted via
` + "`ape costs update --from`" + `) to fix runs that were recorded while the
table was stale.

Artefacts covered:
  _output/{pipelines,tasks}/<name>/<run-id>/manifest.yaml
  _output/ape/prompts/<prompt-id>/prompt.yaml

Chat session.yaml has no per-model breakdown, so there is nothing to
reprice from — those are skipped.

Dry run by default: it prints what would change and touches nothing. Pass
--write to apply, then run ` + "`ape costs roll`" + ` to refresh the rollup cache.
Only cost_usd scalars are rewritten; key order, comments, and every other
field survive the round-trip.

A model that is STILL unpriced cannot be fixed by repricing — its stored
cost is left alone and the model is listed in the report so you know the
total remains a lower bound.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			rep, err := cost.Reprice(cwd, write)
			if err != nil {
				return fmt.Errorf("ape costs reprice: %w", err)
			}
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				return output.Print(cmd.OutOrStdout(), format, rep)
			}
			printRepriceHuman(rep, write)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "human | json | yaml")
	cmd.Flags().BoolVar(&write, "write", false, "Apply the recomputed costs (default: dry run)")
	return cmd
}

func printRepriceHuman(rep cost.RepriceReport, write bool) {
	if rep.Scanned == 0 {
		fmt.Println("no run artefacts found under _output/ — nothing to reprice.")
		return
	}
	if len(rep.Files) > 0 {
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ARTEFACT\tOLD\tNEW\tDELTA\tRECORDS")
		for _, f := range rep.Files {
			fmt.Fprintf(tw, "%s\t$%.4f\t$%.4f\t%+.4f\t%d\n",
				f.Path, f.OldCostUSD, f.NewCostUSD, f.NewCostUSD-f.OldCostUSD, f.Records)
		}
		tw.Flush()
		fmt.Println()
	}
	fmt.Printf("scanned %d artefact(s); %d would change\n", rep.Scanned, rep.Changed)
	fmt.Printf("total: $%.4f → $%.4f (%+.4f)\n", rep.OldTotal, rep.NewTotal, rep.NewTotal-rep.OldTotal)

	if len(rep.StillUnpriced) > 0 {
		fmt.Println()
		fmt.Printf("⚠ still unpriced: %v\n", rep.StillUnpriced)
		fmt.Println("  those runs keep their stored cost and remain a lower bound.")
		fmt.Println("  add the exact rate(s) first — `ape costs coverage` shows the gap.")
	}
	fmt.Println()
	if !write {
		fmt.Println("dry run — nothing written. Re-run with --write to apply.")
		return
	}
	fmt.Printf("wrote %d artefact(s). Run `ape costs roll` to refresh the rollup cache.\n", rep.Written)
}

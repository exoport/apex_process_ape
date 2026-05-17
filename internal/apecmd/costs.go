package apecmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/diegosz/apex_process_ape/internal/cost"
	"github.com/spf13/cobra"
)

func newCostsCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "costs",
		Short: "Show this project's Claude cost rollup",
		Long: `Reads <project>/_output/ape/cost-rollup.json and prints
totals — today, this week, all-time — broken down per pipeline + chat.

  ape costs                          Project rollup (human / json).
  ape costs run <run-id>             Single pipeline run (reads manifest.yaml).
  ape costs chat <chat-id>           Single chat session (reads session.yaml).
  ape costs update --from <file>     Refresh the price table from a YAML file.
  ape costs roll                     Force a project rollup rebuild from all
                                     run / chat directories.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, _ := os.Getwd()
			r, err := cost.LoadRollup(cwd)
			if err != nil {
				return err
			}
			if outputFormat == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			return printCostsHuman(r)
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "human | json")
	cmd.AddCommand(newCostsUpdateCmd(), newCostsRollCmd())
	return cmd
}

func newCostsUpdateCmd() *cobra.Command {
	var fromPath string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Refresh the price table from a YAML file (overwrites in-memory map)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if fromPath == "" {
				return fmt.Errorf("ape costs update: --from <file> required")
			}
			// PLAN-5 / C7 — update path TBD; for now we only read
			// the YAML and report which models would change. A
			// follow-up commit can wire actual persistence of
			// pricing overrides under ~/.ape/.
			return fmt.Errorf("ape costs update: not implemented yet (file=%s)", fromPath)
		},
	}
	cmd.Flags().StringVar(&fromPath, "from", "", "Path to a YAML file with model price overrides")
	return cmd
}

func newCostsRollCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "roll",
		Short: "Rebuild <project>/_output/ape/cost-rollup.json from on-disk run / chat artefacts",
		RunE: func(_ *cobra.Command, _ []string) error {
			// Full rebuild walks _output/pipelines/<name>/<run-id>/manifest.yaml
			// + _output/ape/chats/<chat-id>/session.yaml. The
			// scaffolding lives in cost.LoadRollup / SaveRollup;
			// the walker is a follow-up commit because manifest.yaml's
			// PLAN-3 reader is the natural source and that integration
			// is left for the pipeline-web-mode merge.
			return fmt.Errorf("ape costs roll: not implemented yet (use `ape pipeline` / `ape chat` to seed the rollup)")
		},
	}
}

func printCostsHuman(r *cost.Rollup) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BUCKET\tRUNS\tCOST\tINPUT\tOUTPUT\tCACHE-R")
	// All-time totals per pipeline.
	for name, b := range r.Pipelines {
		fmt.Fprintf(tw, "pipeline:%s\t%d\t$%.2f\t%d\t%d\t%d\n",
			name, len(b.Runs), b.Totals.CostUSD,
			b.Totals.InputTokens, b.Totals.OutputTokens, b.Totals.CacheReadTokens)
	}
	if r.Chats.Totals.CostUSD > 0 || len(r.Chats.Runs) > 0 {
		fmt.Fprintf(tw, "chats\t%d\t$%.2f\t%d\t%d\t%d\n",
			len(r.Chats.Runs), r.Chats.Totals.CostUSD,
			r.Chats.Totals.InputTokens, r.Chats.Totals.OutputTokens, r.Chats.Totals.CacheReadTokens)
	}
	tw.Flush()

	// Recent days.
	days := r.SortedDays()
	if len(days) > 0 {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "by day:")
		td := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(td, "DAY\tCOST\tINPUT\tOUTPUT")
		// Last 7 days.
		recent := days
		if len(recent) > 7 {
			recent = recent[len(recent)-7:]
		}
		for _, d := range recent {
			t := r.ByDay[d]
			fmt.Fprintf(td, "%s\t$%.2f\t%d\t%d\n", d, t.CostUSD, t.InputTokens, t.OutputTokens)
		}
		td.Flush()
	}

	if r.UpdatedAt.IsZero() {
		fmt.Fprintln(os.Stderr, "rollup never populated. Run `ape pipeline` or `ape chat` to seed it.")
	} else {
		fmt.Fprintf(os.Stderr, "rollup updated %s\n", r.UpdatedAt.Format(time.RFC3339))
	}
	return nil
}

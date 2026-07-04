package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/diegosz/apex_process_ape/internal/cost"
	"github.com/diegosz/apex_process_ape/internal/output"
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
			if output.Format(outputFormat) == output.FormatJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			return printCostsHuman(r)
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "human | json")
	cmd.AddCommand(newCostsRunCmd(), newCostsChatCmd(), newCostsUpdateCmd(), newCostsRollCmd())
	return cmd
}

// newCostsRunCmd implements `ape costs run <run-id>` — a reader over a
// single run's manifest.yaml (PLAN-10 D5; restores the command PLAN-9
// removed from the help text).
func newCostsRunCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:     "run <run-id>",
		Short:   "Show cost for a single pipeline or task run (reads its manifest.yaml)",
		Example: "  ape costs run 20260703-120102-a1b2c3",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			m, ok := cost.FindRunManifest(cwd, args[0])
			if !ok {
				return fmt.Errorf("no run manifest found for run-id %q under _output/{pipelines,tasks}", args[0])
			}
			if output.Format(outputFormat) == output.FormatJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(m)
			}
			fmt.Printf("%s %s (run %s)\n", m.Kind, m.Name, m.RunID)
			printTotalsLine("total", m.Totals)
			printPerModel(m.PerModel)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "human | json")
	return cmd
}

// newCostsChatCmd implements `ape costs chat <chat-id>` — a reader over a
// single chat session.yaml. PLAN-10 D5.
func newCostsChatCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:     "chat <chat-id>",
		Short:   "Show cost for a single chat session (reads its session.yaml)",
		Example: "  ape costs chat 0a675bc4",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			s, ok := cost.FindChatSession(cwd, args[0])
			if !ok {
				return fmt.Errorf("no chat session found for chat-id %q under _output/ape/chats", args[0])
			}
			if output.Format(outputFormat) == output.FormatJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(s)
			}
			fmt.Printf("chat %s\n", s.ChatID)
			printTotalsLine("total", s.Totals)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "human | json")
	return cmd
}

func newCostsUpdateCmd() *cobra.Command {
	var fromPath string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Persist model price overrides from a YAML file to ~/.ape/prices.yaml",
		Long: `Reads a YAML file in the shape:

  prices:
    claude-opus-4-7:
      base_input: 5.00
      output: 25.00
    claude-sonnet-4-6:
      base_input: 3.00
      output: 15.00

and persists it to ~/.ape/prices.yaml. Subsequent ape invocations
prefer these values over the built-in price table (cost.Prices).
PLAN-5 / C7.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if fromPath == "" {
				return errors.New("ape costs update: --from <file> required")
			}
			overrides, err := cost.LoadOverridesFrom(fromPath)
			if err != nil {
				return err
			}
			if err := cost.SaveOverrides(overrides); err != nil {
				return fmt.Errorf("ape costs update: save: %w", err)
			}
			fmt.Fprintf(os.Stderr, "saved %d override(s) to ~/.ape/prices.yaml\n", len(overrides))
			for model, p := range overrides {
				fmt.Fprintf(os.Stderr, "  %s\tin=$%.2f out=$%.2f\n", model, p.BaseInput, p.Output)
			}
			return nil
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
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			r, err := cost.RebuildRollup(cwd)
			if err != nil {
				return fmt.Errorf("ape costs roll: %w", err)
			}
			fmt.Fprintf(os.Stderr, "rebuilt rollup: %d pipeline name(s), %d task skill(s), %d chat(s), %d day(s)\n",
				len(r.Pipelines), len(r.Tasks), len(r.Chats.Runs), len(r.ByDay))
			return nil
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
	// All-time totals per task skill (PLAN-11).
	for name, b := range r.Tasks {
		fmt.Fprintf(tw, "task:%s\t%d\t$%.2f\t%d\t%d\t%d\n",
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

	// Per-model breakdown (PLAN-10 D5). Only when populated — older
	// rollups predating per-model capture have an empty map.
	if len(r.PerModel) > 0 {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "by model:")
		mw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(mw, "MODEL\tCOST\tINPUT\tOUTPUT\tCACHE-R\tTURNS")
		for _, model := range sortedModelKeys(r.PerModel) {
			t := r.PerModel[model]
			fmt.Fprintf(mw, "%s\t$%.2f\t%d\t%d\t%d\t%d\n",
				model, t.CostUSD, t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.NumTurns)
		}
		mw.Flush()
	}

	if r.UpdatedAt.IsZero() {
		fmt.Fprintln(os.Stderr, "rollup never populated. Run `ape pipeline` or `ape chat` to seed it.")
	} else {
		fmt.Fprintf(os.Stderr, "rollup updated %s\n", r.UpdatedAt.Format(time.RFC3339))
	}
	return nil
}

// printTotalsLine renders one labelled totals row for the single-run /
// single-chat readers.
func printTotalsLine(label string, t cost.Totals) {
	fmt.Printf("  %s: $%.2f  in=%d out=%d cache-r=%d turns=%d\n",
		label, t.CostUSD, t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.NumTurns)
}

// printPerModel renders the per-model breakdown for the single-run
// reader, sorted by model id. No-op on empty input.
func printPerModel(perModel map[string]cost.Totals) {
	if len(perModel) == 0 {
		return
	}
	fmt.Println("  by model:")
	for _, model := range sortedModelKeys(perModel) {
		t := perModel[model]
		fmt.Printf("    %-22s $%.2f  in=%d out=%d turns=%d\n",
			model, t.CostUSD, t.InputTokens, t.OutputTokens, t.NumTurns)
	}
}

// sortedModelKeys returns the model ids of a per-model map in ascending
// order for stable rendering.
func sortedModelKeys(perModel map[string]cost.Totals) []string {
	out := make([]string, 0, len(perModel))
	for k := range perModel {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

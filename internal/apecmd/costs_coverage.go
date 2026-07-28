package apecmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
)

// newCostsCoverageCmd implements `ape costs coverage` — the drift detector
// for the built-in price table.
//
// ape's price table is hand-curated because Anthropic publishes no price
// API, and Claude Code ships on a schedule ape does not control. A model id
// can therefore change under a released ape binary at any moment, and when
// it does, tokens keep counting correctly while cost silently goes to zero.
// No release-time check can catch that on its own. This command can: it
// reads the transcripts the locally-installed Claude Code is writing right
// now and reports every model id the table does not cover exactly.
func newCostsCoverageCmd() *cobra.Command {
	var (
		outputFormat string
		strict       bool
		days         int
	)
	cmd := &cobra.Command{
		Use:   "coverage",
		Short: "Check the built-in price table against the models Claude Code is actually emitting",
		Long: `Sweep the local Claude Code transcripts (~/.claude/projects) and report
how this ape binary prices every model id it finds.

Each observed model resolves to one of:

  exact      a rate for this exact model id (built-in table, an override
             in ~/.ape/prices.yaml, or a dated promotional window)
  family     no exact row — priced from the model's family tier. Close,
             but an approximation, and flagged as one everywhere.
  unpriced   nothing matched. Those turns contribute $0.00 to every
             total, which is not the same as being free.

--strict exits 2 when any observed model is not exactly priced. A sweep
that finds no transcripts exits 0 and says so: absence of evidence is not
coverage, so CI (which has no transcripts) skips rather than passes.

Exit codes:
  0  every observed model exactly priced, or nothing observed
  2  --strict and at least one model is estimated or unpriced`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var since time.Time
			if days > 0 {
				since = time.Now().Add(-time.Duration(days) * 24 * time.Hour)
			}
			rep, err := cost.ObserveModels("", since)
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				if err := output.Print(cmd.OutOrStdout(), format, rep); err != nil {
					return err
				}
			} else {
				printCoverageHuman(rep)
			}
			if strict && rep.Observed() && !rep.OK() {
				fmt.Fprintf(os.Stderr,
					"\n❌ price table is stale: %d unpriced, %d estimated model(s), %d alias drift(s).\n"+
						"   Update internal/cost/prices.yaml (`prices:` for rates, `aliases:` for\n"+
						"   which generation a bare family word resolves to) and rebuild.\n",
					rep.UnpricedModels, rep.EstimatedModels, len(rep.AliasDrifts))
				os.Exit(ExitUsage)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "human | json | yaml")
	cmd.Flags().BoolVar(&strict, "strict", false, "Exit 2 when any observed model is not exactly priced")
	cmd.Flags().IntVar(&days, "days", 30, "Only read transcripts modified in the last N days (0 = all)")
	return cmd
}

func printCoverageHuman(rep cost.CoverageReport) {
	if !rep.Observed() {
		fmt.Println("no Claude Code transcripts found in the window.")
		fmt.Println("coverage NOT verified — this is a skip, not a pass.")
		fmt.Printf("price table updated: %s (%d model(s) known)\n", rep.TableUpdated, len(cost.KnownModels()))
		return
	}
	aliases := cost.FamilyAliases()
	names := make([]string, 0, len(aliases))
	for a := range aliases {
		names = append(names, a)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, a := range names {
		parts = append(parts, a+"→"+aliases[a])
	}
	fmt.Printf("family aliases: %s\n\n", strings.Join(parts, "  "))

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tSEEN\tPRICING\tIN/1M\tOUT/1M\tLAST SEEN")
	for _, m := range rep.Models {
		last := ""
		if !m.LastSeen.IsZero() {
			last = m.LastSeen.Format("2006-01-02")
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t$%.2f\t$%.2f\t%s\n",
			m.Model, m.Turns, m.Source, m.BaseInput, m.Output, last)
	}
	tw.Flush()

	fmt.Println()
	fmt.Println("SEEN counts every assistant line naming the model, including the duplicate")
	fmt.Println("snapshots claude writes per message — it measures usage, not billable turns,")
	fmt.Println("so it reads higher than the turn counts in `ape costs`.")
	fmt.Println()
	fmt.Println(rep.Summary())
	if rep.ClaudeVersion != "" {
		fmt.Printf("claude code version (newest turn): %s\n", rep.ClaudeVersion)
	}
	if rep.TranscriptsSkipped > 0 {
		fmt.Printf("note: %d older transcript(s) beyond the scan cap were not read\n", rep.TranscriptsSkipped)
	}
	if bad := cost.RejectedOverrides(); len(bad) > 0 {
		fmt.Println()
		fmt.Println("⚠ ignored row(s) in ~/.ape/prices.yaml — the built-in rate is used instead:")
		for _, reason := range bad {
			fmt.Printf("    %s\n", reason)
		}
	}

	if len(rep.AliasDrifts) > 0 {
		fmt.Println()
		fmt.Println("⚠ family alias drift — a bare `sonnet` / `opus` in a spec or --model")
		fmt.Println("  resolves through this table, so a stale entry selects the WRONG MODEL:")
		for _, d := range rep.AliasDrifts {
			fmt.Printf("    %-8s → %-20s superseded by: %s\n",
				d.Alias, d.Target, strings.Join(d.Newer, ", "))
		}
		fmt.Println("  fix the `aliases:` block in internal/cost/prices.yaml.")
	}

	gaps := rep.Gaps()
	if len(gaps) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("to close the gap, add the exact rate(s) to internal/cost/prices.yaml:")
	for _, m := range gaps {
		fmt.Printf("  %s:\n    base_input: %.2f\n    output: %.2f\n", m.Model, m.BaseInput, m.Output)
	}
	fmt.Println("(the values shown are this binary's current estimate — confirm each against")
	fmt.Println(" https://platform.claude.com/docs/en/about-claude/pricing before committing)")
	fmt.Println("without a rebuild: put the same rows in a file and run `ape costs update --from <file>`")
}

package apecmd

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/spf13/cobra"
)

// `ape sandbox costs` — what the work done inside workspaces cost (PLAN-24 D3).
//
// It is a separate verb from `ape costs` rather than a flag on it because they
// read different things. `ape costs` reads THIS project's rollup from
// _output/ape/cost-rollup.json on the machine you are standing on; this reads a
// NODE's workspaces, wherever they are and whatever projects they hold, from the
// composed homes only that node can open.

func newSandboxCostsCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "costs [name]",
		Short: "Show what Claude sessions inside workspaces cost",
		Long: `Report the Claude usage accumulated inside the node's workspaces, per
workspace, all-time. With a name, report only that workspace.

The numbers come from the session transcripts each workspace's composed home
already holds on the node — there is no agent, no telemetry wire, and nothing
to enable. The node does the scan because it owns those homes: they are private
to the daemon, and an operator's ape cannot read them.

A model with no rate in the price table contributes $0 and is called out, so a
total that is a LOWER BOUND is never mistaken for an exact one. The table doing
the pricing is the NODE's, embedded in its aped at build time — so a gap is
fixed by upgrading aped there, not by 'ape costs update' here.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, done, err := dialVMM(cmd)
			if err != nil {
				return err
			}
			defer done()

			var name string
			if len(args) == 1 {
				name = args[0]
			}
			reply, err := client.Costs(cmd.Context(), name)
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				return output.Print(cmd.OutOrStdout(), format, reply)
			}
			printSandboxCostsHuman(cmd, reply)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

// printSandboxCostsHuman renders the per-workspace table, the node total, and
// the pricing-health warnings — the last on STDERR, so `--output-format json`
// stays parseable and so does a piped human run.
func printSandboxCostsHuman(cmd *cobra.Command, reply workspace.CostsReply) {
	out := cmd.OutOrStdout()
	if len(reply.Workspaces) == 0 {
		fmt.Fprintln(out, "no workspaces (ape sandbox up <name>)")
		return
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKSPACE\tSESSIONS\tCOST\tINPUT\tOUTPUT\tCACHE-R\tTURNS\tLAST-TURN")
	for i := range reply.Workspaces {
		w := &reply.Workspaces[i]
		if w.Error != "" {
			fmt.Fprintf(tw, "%s\t-\t-\t-\t-\t-\t-\t%s\n", w.Name, w.Error)
			continue
		}
		t := w.Totals
		fmt.Fprintf(tw, "%s\t%d\t$%.2f\t%d\t%d\t%d\t%d\t%s\n",
			w.Name, w.Sessions, t.CostUSD, t.InputTokens, t.OutputTokens,
			t.CacheReadTokens, t.NumTurns, orDash(w.LastTurnAt))
	}
	if len(reply.Workspaces) > 1 {
		t := reply.Totals
		fmt.Fprintf(tw, "TOTAL\t\t$%.2f\t%d\t%d\t%d\t%d\t\n",
			t.CostUSD, t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.NumTurns)
	}
	_ = tw.Flush()

	warnSandboxPricingGaps(cmd, reply)
}

// warnSandboxPricingGaps names the workspaces whose totals are approximate and
// why. It is the same no-silent-zero rule `ape costs` applies to a project
// rollup: an unpriced model contributed $0 because no rate was found, not
// because it was free, and a reader must be able to tell.
func warnSandboxPricingGaps(cmd *cobra.Command, reply workspace.CostsReply) {
	unpriced := map[string]bool{}
	estimated := map[string]bool{}
	for i := range reply.Workspaces {
		for _, m := range reply.Workspaces[i].UnpricedModels {
			unpriced[m] = true
		}
		for _, m := range reply.Workspaces[i].EstimatedModels {
			estimated[m] = true
		}
	}
	if len(unpriced) == 0 && len(estimated) == 0 {
		return
	}
	errOut := cmd.ErrOrStderr()
	if len(unpriced) > 0 {
		fmt.Fprintf(errOut, "⚠ unpriced model(s): %s — their turns contributed $0.00; the totals above are a lower bound\n",
			strings.Join(sortedSet(unpriced), ", "))
	}
	if len(estimated) > 0 {
		fmt.Fprintf(errOut, "⚠ estimated model(s): %s — priced at the family rate, not an exact published rate\n",
			strings.Join(sortedSet(estimated), ", "))
	}
	// Worth being precise about WHERE the gap is: the scan runs on the node, so
	// the price table with the hole in it is aped's, not this client's. Running
	// `ape costs update` here would change nothing.
	fmt.Fprintln(errOut, "  the pricing is the NODE's — upgrade aped there (its price table is embedded at build time)")
}

// sortedSet returns a set's members in ascending order.
func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package apecmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/spf13/cobra"
)

// `ape sandbox capacity` — the node headroom report (PLAN-24 D4).
//
// It exists because every workspace is a real VM holding real memory, so the
// question "can I start another one?" has an answer the node knows and nothing
// surfaced. It is a report for a human, not scheduler input: cross-node
// placement is a fleet concern and is deliberately out of scope.

func newSandboxCapacityCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "capacity",
		Short: "Show the node's workspace headroom (cores, memory, how many more fit)",
		Long: `Report what the target aped node can still take: its cores and memory, how
many workspaces it already carries, how many of those are RUNNING (only those
hold RAM — a stopped workspace keeps its state and frees its memory), and how
many more the free memory holds.

FITS is an estimate, and the number it divides by is printed next to it: guest
memory is set by the node's Kata configuration, which aped does not own, so the
per-workspace size is an assumption rather than a measurement. Disagree with it
by redoing the division on the memory numbers above it.

This is a report, not placement. Choosing which node a workspace lands on is a
fleet concern and is not what this answers.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			backend, done, err := vmmBackend(cmd)
			if err != nil {
				return err
			}
			defer done()
			caps, err := backend.Capabilities(cmd.Context())
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				return output.Print(cmd.OutOrStdout(), format, caps)
			}
			printCapacityHuman(cmd, caps)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

// printCapacityHuman renders the capability report as an aligned table plus the
// one-line verdict an operator is actually after.
func printCapacityHuman(cmd *cobra.Command, caps workspace.Capabilities) {
	out := cmd.OutOrStdout()
	c := caps.Capacity
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "cores\t%d\n", c.Cores)
	fmt.Fprintf(tw, "memory\t%s total, %s available\n", humanBytes(caps.Mem.TotalBytes), humanBytes(caps.Mem.AvailableBytes))
	fmt.Fprintf(tw, "kvm\t%s\n", yesNo(caps.KVM))
	fmt.Fprintf(tw, "host-fs\t%s\n", yesNo(caps.HostFS))
	fmt.Fprintf(tw, "workspaces\t%d provisioned, %d running\n", c.Workspaces, c.Running)
	fmt.Fprintf(tw, "kata factory\ttemplating=%s vm-cache=%s\n", yesNo(caps.Factory.Templating), yesNo(caps.Factory.VMCache))
	if len(caps.Runtimes) > 0 {
		names := make([]string, 0, len(caps.Runtimes))
		for _, r := range caps.Runtimes {
			n := r.VMM
			if r.Default {
				n += " (default)"
			}
			names = append(names, n)
		}
		fmt.Fprintf(tw, "runtimes\t%s\n", strings.Join(names, ", "))
	}
	_ = tw.Flush()

	fmt.Fprintln(out)
	switch {
	case caps.Mem.AvailableBytes <= 0:
		// Distinguish "full" from "could not read": an operator who acts on a zero
		// needs to know which of the two it was.
		fmt.Fprintln(out, "fits: unknown — the node reported no available memory (a full box, or an unreadable /proc/meminfo)")
	case c.Fits == 0:
		fmt.Fprintf(out, "fits: 0 more workspaces at %s each — stop one to make room (ape sandbox stop <name>)\n",
			humanBytes(c.WorkspaceMemBytes))
	default:
		fmt.Fprintf(out, "fits: ~%d more workspace(s) at %s each\n", c.Fits, humanBytes(c.WorkspaceMemBytes))
	}
}

// humanBytes renders a byte count in binary units. Sizes here are memory, which
// is quoted in GiB by every tool an operator will cross-check against.
func humanBytes(n int64) string {
	if n <= 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

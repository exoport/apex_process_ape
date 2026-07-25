package apecmd

import (
	"fmt"
	"strings"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/spf13/cobra"
)

// Changing a LIVE workspace's egress allowlist (PLAN-21 follow-up).
//
// This is possible without recreating the workspace because of where the enforcement
// sits: the CONNECT proxy runs on the HOST, on a port the guest already has in its
// HTTPS_PROXY, so a new allowlist means restarting that proxy on the same port. The
// guest is not reconfigured, the network namespace is untouched (its wall only names
// the port), and nothing about the container changes.
//
// Mounts are the opposite case and cannot work this way — they live in the container's
// OCI spec, which is fixed at creation — so changing those needs `down` + `up`.

func newSandboxEgressCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Inspect and change a workspace's egress allowlist",
		Long: `Change which domains a RUNNING workspace may reach, without recreating it.

The allowlist is enforced by a host-side CONNECT proxy on a fixed port, so re-pointing
it is a proxy restart on that same port — the workspace keeps running and its
HTTPS_PROXY stays valid. The request is intersected with the node's egress policy
exactly as at create time, so this can narrow or re-shape a grant but never exceed
what the node permits.

  ape sandbox egress set dev --domain github.com --domain proxy.golang.org

Mounts cannot be changed this way (they are fixed in the container's OCI spec at
creation) — use 'down' then 'up', which is cheap because repos, caches and the
framework all live in durable host mounts.`,
	}
	cmd.AddCommand(newSandboxEgressSetCmd())
	return cmd
}

func newSandboxEgressSetCmd() *cobra.Command {
	var (
		domains      []string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Replace a running workspace's egress allowlist",
		Long: `Replace the domains a running workspace may reach. The list is REPLACED, not
added to, so it is also how you revoke access: 'set <name>' with no --domain leaves the
workspace with no egress at all.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, done, err := dialVMM(cmd)
			if err != nil {
				return err
			}
			defer done()

			reply, err := client.EgressSet(cmd.Context(), args[0], sandbox.SortedDomains(domains))
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format == output.FormatJSON || format == output.FormatYAML {
				return output.Print(cmd.OutOrStdout(), format, reply)
			}
			out := cmd.OutOrStdout()
			if len(reply.Domains) == 0 {
				fmt.Fprintf(out, "workspace %q now has NO egress\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "workspace %q egress: %s\n", args[0], strings.Join(reply.Domains, ", "))
			if reply.ProxyURL != "" {
				fmt.Fprintf(out, "proxy: %s (unchanged — the guest keeps its HTTPS_PROXY)\n", reply.ProxyURL)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&domains, "domain", nil, "Domain to allow (repeatable; replaces the current list)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

package apedcmd

import (
	"os"

	"github.com/exoport/apex_process_ape/internal/netd"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/spf13/cobra"
)

func newNetdCmd() *cobra.Command {
	var (
		socket    string
		leaseFile string
		bridge    string
		hostCIDR  string
		portLow   int
		portHigh  int
		allowUIDs []int
	)
	cmd := &cobra.Command{
		Use:   "netd",
		Short: "Run the narrow privileged network helper for workspace egress (privileged)",
		Long: `Run the sandbox network helper: wire one network namespace + veth pair per
workspace onto the host<->guest bridge so Kata can tap it, and remove it again on
teardown (PLAN-21 D3).

It exists because the root executor deliberately CANNOT do this: its unit has an
empty capability set, AF_UNIX only, RestrictNamespaces=yes and @mount denied, and
widening it to run CNI is forbidden by the ape/aped split's charter. This helper
is the separate, single-purpose privileged actor instead — CAP_NET_ADMIN +
CAP_SYS_ADMIN, two verbs, root-only socket, no containerd access and no policy of
its own. Authorization happens before the call (the executor's policy check) and
domain enforcement after it (the CONNECT proxy in aped-front).

The bridge itself is host configuration, not this helper's job: install
aped-netbr.service (deploy/systemd) or run deploy/dev-host.sh prereqs first.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			uids := make([]uint32, 0, len(allowUIDs))
			for _, uid := range allowUIDs {
				if uid >= 0 {
					uids = append(uids, uint32(uid)) //nolint:gosec // guarded non-negative
				}
			}
			return netd.Serve(cmd.Context(), netd.ServerConfig{
				Socket:      socket,
				LeaseFile:   leaseFile,
				Bridge:      bridge,
				HostCIDR:    hostCIDR,
				PortLow:     portLow,
				PortHigh:    portHigh,
				AllowedUIDs: uids,
				Stderr:      os.Stderr,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&socket, "socket", netd.DefaultSocket, "AF_UNIX socket to bind (root-only, mode 0600)")
	f.StringVar(&leaseFile, "lease-file", netd.DefaultLeaseFile, "Workspace→address lease file")
	f.StringVar(&bridge, "bridge", sandbox.DefaultEgressBridge, "Host↔guest bridge created by aped-netbr.service")
	f.StringVar(&hostCIDR, "host-cidr", sandbox.DefaultEgressHostCIDR, "Bridge host address in CIDR form (supplies the proxy IP + guest subnet)")
	f.IntVar(&portLow, "proxy-port-low", sandbox.DefaultEgressPortLow, "Lowest proxy port the wall may be opened for (must match the host nft chain)")
	f.IntVar(&portHigh, "proxy-port-high", sandbox.DefaultEgressPortHigh, "Highest proxy port the wall may be opened for")
	f.IntSliceVar(&allowUIDs, "allow-uid", nil, "Peer uids allowed to command the helper (default: 0, the root executor)")
	return cmd
}

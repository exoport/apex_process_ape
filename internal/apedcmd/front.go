package apedcmd

import (
	"os"
	"time"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/spf13/cobra"
)

// defaultMgmtPort is the default management NATS listen port (offset from the
// stock 4222 so aped-front does not collide with a user's own nats-server).
const defaultMgmtPort = 4223

func newFrontCmd() *cobra.Command {
	var (
		node        string
		socket      string
		mgmtHost    string
		mgmtPort    int
		stateDir    string
		hostHome    string
		guestNats   string
		operatorCr  string
		credsExpiry time.Duration
	)
	cmd := &cobra.Command{
		Use:   "front",
		Short: "Run the de-privileged NATS surface + vmm micro service",
		Long: `Run the front-end: embed the two-account nats-server (HOST_OPS + TELEMETRY),
run the vmm micro service on ape.vmm.<node>.>, resolve create requests
(compose + mint per-VM creds), and forward typed commands to the executor over
the priv socket. Runs de-privileged (User=aped); a compromise here is
TELEMETRY-scoped and still cannot satisfy the executor's SO_PEERCRED gate.

Management NATS binds --mgmt-host (default 127.0.0.1, guest-unreachable). Guest
telemetry reaches a bridge-IP endpoint set with --guest-nats-url, which is
injected into each VM as APE_NATS_URL alongside its minted per-VM .creds.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if node == "" {
				node, _ = os.Hostname()
			}
			if hostHome == "" {
				hostHome, _ = os.UserHomeDir()
			}
			return aped.RunFront(cmd.Context(), aped.FrontConfig{
				Node:              node,
				Socket:            socket,
				MgmtHost:          mgmtHost,
				MgmtPort:          mgmtPort,
				StateDir:          stateDir,
				HostHome:          hostHome,
				GuestNatsURL:      guestNats,
				OperatorCredsPath: operatorCr,
				CredsExpiry:       credsExpiry,
				ApeVersion:        Version,
				Stderr:            os.Stderr,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&node, "node", "", "Node token for ape.vmm.<node>.> (default: hostname)")
	f.StringVar(&socket, "socket", "/run/aped/priv.sock", "Priv socket to reach the executor")
	f.StringVar(&mgmtHost, "mgmt-host", "127.0.0.1", "Management NATS listen host (guest-unreachable)")
	f.IntVar(&mgmtPort, "mgmt-port", defaultMgmtPort, "Management NATS listen port")
	f.StringVar(&stateDir, "state-dir", "/var/lib/aped", "State dir (keys, staging homes, per-VM creds)")
	f.StringVar(&hostHome, "host-home", "", "Home to compose ~/.claude from (default: current user home)")
	f.StringVar(&guestNats, "guest-nats-url", "", "APE_NATS_URL injected into guests ('' disables per-VM creds)")
	f.StringVar(&operatorCr, "operator-creds", "/var/lib/aped/creds/operator.creds", "Where to write the host-operator .creds for the ape CLI")
	f.DurationVar(&credsExpiry, "creds-expiry", 24*time.Hour, "Per-VM credential lifetime (0 = no expiry)")
	return cmd
}

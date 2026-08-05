package apedcmd

import (
	"os"
	"time"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/sandbox"
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
		policyPath  string
		egressIP    string
		egressLow   int
		egressHigh  int
		fwRoot      string
		fwRef       string
		cacheRoot   string
		credentials string
		credSyncInt time.Duration
		apeBinary   string
		idleStop    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "front",
		Short: "Run the de-privileged NATS surface + vmm micro service",
		Long: `Run the front-end: embed the two-account nats-server (HOST_OPS + TELEMETRY),
run the vmm micro service on ape.vmm.<node>.>, resolve create requests
(compose + mint per-VM creds), and forward typed commands to the executor over
the priv socket. Runs de-privileged (User=aped); a compromise here is
TELEMETRY-scoped and still cannot satisfy the executor's SO_PEERCRED gate.

Management NATS binds --mgmt-host (default 127.0.0.1, guest-unreachable) and
STAYS guest-unreachable. Guests reach it through the CONNECT proxy they already
use: --guest-nats-url names a sentinel authority the front installs as a system
route in every workspace's proxy, whose far end is a loopback dial this process
makes to itself. No second listener, no firewall hole, and every use of it lands
in the workspace's egress audit trail with a distinguishing reason.`,
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
				ApeGitCommit:      GitCommit,
				ApeBinary:         apeBinary,
				PolicyPath:        policyPath,
				EgressBindIP:      egressIP,
				EgressPortLow:     egressLow,
				EgressPortHigh:    egressHigh,
				FrameworkRoot:     fwRoot,
				FrameworkRef:      fwRef,
				CacheRoot:         cacheRoot,
				Credentials:       credentials,
				CredSyncInterval:  credSyncInt,
				IdleStop:          idleStop,
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
	f.StringVar(&guestNats, "guest-nats-url", "",
		"APE_NATS_URL injected into guests — use the sentinel "+sandbox.AgentNatsURL+
			", which the front routes to its own listener through each workspace's CONNECT proxy "+
			"('' disables per-VM creds, so guests boot with no agent)")
	f.StringVar(&operatorCr, "operator-creds", "/var/lib/aped/creds/operator.creds", "Where to write the host-operator .creds for the ape CLI")
	f.DurationVar(&credsExpiry, "creds-expiry", 24*time.Hour, "Per-VM credential lifetime (0 = no expiry)")
	f.StringVar(&policyPath, "policy", "", "policy.yaml to read egress policy from ('' → egress disabled; normally /etc/aped/policy.yaml)")
	f.StringVar(&egressIP, "egress-bridge-ip", "", "Bridge address the per-workspace CONNECT proxies listen on (default: the aped-netbr bridge address)")
	f.IntVar(&egressLow, "egress-port-low", 0, "Lowest proxy listen port (must match the host nftables chain)")
	f.IntVar(&egressHigh, "egress-port-high", 0, "Highest proxy listen port")
	f.StringVar(&fwRoot, "framework-root", "", "Host dir holding materialized APEX framework refs, one subdir per ref ('' → no framework mount)")
	f.StringVar(&fwRef, "framework-ref", "", "Default framework ref to mount read-only at /opt/apex-framework")
	f.StringVar(&cacheRoot, "cache-root", "", "Host dir holding durable tool caches (asdf/go/cargo/...); '' → no cache mounts")
	f.StringVar(&credentials, "credentials", "", "Credential mode composed into workspaces: oauth | api-key | none (default none). oauth copies <host-home>/.claude/.credentials.json into each workspace and keeps them converged")
	f.DurationVar(&credSyncInt, "cred-sync-interval", 0, "How often to converge the shared credential across host + workspaces (default 3s)")
	// The default (the `ape` beside this aped) is what makes delivery need no configuration:
	// both binaries ship in one release archive. The flag is for installs that split them.
	f.StringVar(&apeBinary, "ape-binary", "", "The `ape` binary delivered read-only into every workspace at "+sandbox.ApeBinDest+" (default: the ape beside this aped)")
	// Off by default, deliberately: automatic lifecycle action is opt-in, so
	// upgrading aped never starts stopping a node's workspaces on its own. The
	// reaper also never touches a workspace it has no heartbeat for — silence is
	// unknown, not idle — so enabling it cannot reap a workspace whose agent is
	// absent (see PLAN-24 D7).
	f.DurationVar(&idleStop, "idle-stop", 0,
		"Stop workspaces whose in-guest agent reports them idle for this long (0 = never; suggested "+
			sandbox.DefaultIdleStop.String()+"). Stops only — state is kept and 'ape sandbox start' revives it")
	return cmd
}

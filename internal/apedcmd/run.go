package apedcmd

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var (
		socket      string
		policyPath  string
		stateDir    string
		auditLog    string
		node        string
		driver      string
		nerdctl     string
		nerdctlData string
		ctrdAddr    string
		ctrdNS      string
		allowUsers  []string
		allowUIDs   []int
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the network-less root executor (privileged)",
		Long: `Run the root executor: serve the AF_UNIX priv socket, gate every connection
on SO_PEERCRED, re-validate each fully-resolved command against policy, drive
containerd/nerdctl, and write an append-only audit record per privileged op.

Under systemd the priv socket is provided by aped-priv.socket (socket
activation, auto-detected); otherwise it is bound at --socket. Only peers whose
SO_PEERCRED uid is in the --allow-user / --allow-uid set may issue commands —
normally just the aped-front user.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if node == "" {
				node, _ = os.Hostname()
			}
			uids, err := resolveUIDs(allowUsers, allowUIDs)
			if err != nil {
				return fmt.Errorf("%w: %w", aped.ErrConfig, err)
			}
			return aped.RunExecutor(cmd.Context(), aped.ExecutorRunConfig{
				Socket:              socket,
				PolicyPath:          policyPath,
				StateDir:            stateDir,
				AuditLog:            auditLog,
				Node:                node,
				AllowedUIDs:         uids,
				Driver:              driver,
				Nerdctl:             nerdctl,
				NerdctlDataRoot:     nerdctlData,
				ContainerdAddress:   ctrdAddr,
				ContainerdNamespace: ctrdNS,
				Stderr:              os.Stderr,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&socket, "socket", "/run/aped/priv.sock", "AF_UNIX priv socket path (ignored when socket-activated)")
	f.StringVar(&policyPath, "policy", "/etc/aped/policy.yaml", "Path to policy.yaml (required; fail-closed)")
	f.StringVar(&stateDir, "state-dir", "/var/lib/aped", "State dir (workspace registry)")
	f.StringVar(&auditLog, "audit-log", "/var/log/aped/audit.jsonl", "Append-only audit log path ('' to disable the file sink)")
	f.StringVar(&node, "node", "", "Node token for audit subjects (default: hostname)")
	f.StringVar(&driver, "driver", "shell", "Workspace backend: shell (nerdctl) | containerd (Go client — barrier-3 fix, opt-in)")
	f.StringVar(&nerdctl, "nerdctl", "", "Driver binary override for --driver shell (default: nerdctl)")
	f.StringVar(&nerdctlData, "nerdctl-data-root", "", "nerdctl --data-root override (default: <state-dir>/nerdctl, under the executor's writable state)")
	f.StringVar(&ctrdAddr, "containerd-address", "", "containerd socket for --driver containerd (default: /run/containerd/containerd.sock)")
	f.StringVar(&ctrdNS, "containerd-namespace", "", "containerd namespace for --driver containerd (default: aped)")
	f.StringSliceVar(&allowUsers, "allow-user", []string{"aped"}, "Usernames whose SO_PEERCRED uid may issue commands (the aped-front user)")
	f.IntSliceVar(&allowUIDs, "allow-uid", nil, "Additional peer uids allowed over the priv socket")
	return cmd
}

// resolveUIDs turns the --allow-user names + --allow-uid numbers into a uid set.
// A username that does not resolve is skipped (the aped-front user may not exist
// on a dev box); explicit uids are always included.
func resolveUIDs(users []string, uids []int) ([]uint32, error) {
	out := make([]uint32, 0, len(users)+len(uids))
	for _, name := range users {
		u, err := user.Lookup(name)
		if err != nil {
			continue // user absent (e.g. dev box) — not fatal
		}
		n, err := strconv.Atoi(u.Uid)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad uid %q for user %q", u.Uid, name)
		}
		out = append(out, uint32(n)) //nolint:gosec // guarded non-negative above
	}
	for _, uid := range uids {
		if uid < 0 {
			return nil, fmt.Errorf("negative uid %d", uid)
		}
		out = append(out, uint32(uid)) //nolint:gosec // guarded non-negative above
	}
	return out, nil
}

// exitCodeFor maps a daemon error onto the exit-code table: config/usage → 2,
// everything else → 1.
func exitCodeFor(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, aped.ErrConfig), errors.Is(err, aped.ErrPrivUnsupported):
		return exitUsage
	default:
		return exitRunFailed
	}
}
